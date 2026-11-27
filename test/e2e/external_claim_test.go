/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	infraapi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	modulemanager "github.com/opendatahub-io/opendatahub-db-operator/pkg/manager"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres"
	"github.com/opendatahub-io/opendatahub-db-operator/test/support"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestExternalProviderClaimLifecycle(t *testing.T) {
	adminNamespace := os.Getenv("ODH_E2E_EXTERNAL_ADMIN_SECRET_NAMESPACE")
	adminName := os.Getenv("ODH_E2E_EXTERNAL_ADMIN_SECRET_NAME")
	if adminNamespace == "" && adminName == "" {
		t.Skip("set ODH_E2E_EXTERNAL_ADMIN_SECRET_NAMESPACE and ODH_E2E_EXTERNAL_ADMIN_SECRET_NAME")
	}
	if adminNamespace == "" || adminName == "" {
		t.Fatal("both external admin Secret namespace and name are required")
	}

	g := NewWithT(t)
	restCfg, err := ctrl.GetConfig()
	g.Expect(err).NotTo(HaveOccurred())
	scheme, err := modulemanager.NewScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cli, err := client.New(restCfg, client.Options{Scheme: scheme})
	g.Expect(err).NotTo(HaveOccurred())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)

	// The operand CRDs must exist for providers and claims; when this test
	// is shuffled first, enable the module so it stays order-independent.
	ensureOperandCRDs(t, ctx, cli)

	adminSecret := &corev1.Secret{}
	g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: adminNamespace, Name: adminName}, adminSecret)).To(Succeed())
	for _, key := range []string{"host", "port", "user", "password", "dbname", "ca.crt"} {
		g.Expect(adminSecret.Data[key]).NotTo(BeEmpty(), "external admin Secret needs %q", key)
	}
	g.Expect(string(adminSecret.Data["sslmode"])).To(Equal("verify-full"),
		"the administrator-supplied target must support hostname-verified TLS")

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "odh-db-contract-"}}
	g.Expect(cli.Create(ctx, namespace)).To(Succeed())
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		if err := support.DeleteNamespace(cleanupCtx, cli, namespace.Name, namespace.UID); err != nil {
			t.Errorf("cleaning consumer namespace: %v", err)
		}
	})

	provider := &infraapi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "odh-db-e2e-external-"},
		Spec: infraapi.DatabaseProviderSpec{
			Type: infraapi.ProviderTypeExternal,
			External: &infraapi.ExternalProviderSpec{
				ConnectionSecretRef: corev1.SecretReference{Namespace: adminNamespace, Name: adminName},
			},
		},
	}
	g.Expect(cli.Create(ctx, provider)).To(Succeed())
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		err := cli.Delete(cleanupCtx, provider, client.Preconditions{UID: &provider.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning external provider: %v", err)
		}
	})

	g.Eventually(func(g Gomega) {
		current := &infraapi.DatabaseProvider{}
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(provider), current)).To(Succeed())
		condition := conditions.FindStatusCondition(current, "Reachable")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Status).To(Equal(metav1.ConditionTrue), "%s", condition.Message)
		g.Expect(conditions.FindStatusCondition(current, infraapi.ConditionOperationalLimitations)).To(BeNil())
		g.Expect(current.Status.TLS).NotTo(BeNil())
		g.Expect(current.Status.TLS.Ready).To(BeTrue())
	}).WithTimeout(2*time.Minute).Should(Succeed(), "external provider did not become reachable")

	claim := &infraapi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "connection-contract", Namespace: namespace.Name},
		Spec: infraapi.DatabaseClaimSpec{
			Provider: infraapi.ProviderRef{Name: provider.Name},
			Access:   infraapi.AccessModeReadWrite,
		},
	}
	g.Expect(cli.Create(ctx, claim)).To(Succeed())
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		err := cli.Delete(cleanupCtx, claim, client.Preconditions{UID: &claim.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning database claim: %v", err)
		}
	})

	g.Eventually(func(g Gomega) {
		current := &infraapi.DatabaseClaim{}
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(claim), current)).To(Succeed())
		condition := conditions.FindStatusCondition(current, "Provisioned")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Status).To(Equal(metav1.ConditionTrue), "%s", condition.Message)
		g.Expect(current.Status.Connection.SecretRef.Name).To(Equal(claim.Name))
	}).WithTimeout(2*time.Minute).Should(Succeed(), "database claim did not provision")

	credentials := &corev1.Secret{}
	g.Eventually(func(g Gomega) {
		g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: namespace.Name, Name: claim.Name}, credentials)).To(Succeed())
	}).WithTimeout(time.Minute).Should(Succeed())
	for _, key := range []string{"host", "port", "user", "password", "dbname", "ca.crt"} {
		g.Expect(credentials.Data[key]).NotTo(BeEmpty(), "claim Secret needs %q", key)
	}
	g.Expect(string(credentials.Data["sslmode"])).To(Equal("verify-full"))
	g.Expect(credentials.Annotations["opendatahub.io/connection-type-protocol"]).To(Equal("postgres"))

	image := envOrDefault("ODH_E2E_CONSUMER_IMAGE", "postgres:16")
	createAdminContractTable(t, ctx, cli, restCfg, adminNamespace, adminName,
		string(credentials.Data["dbname"]), string(credentials.Data["user"]),
		string(adminSecret.Data["sslmode"]), image, true)
	runConsumer(t, ctx, cli, restCfg, namespace.Name, claim.Name, image, true)
	runAccessorConsumer(t, ctx, cli, restCfg, namespace.Name, claim.Name, image, true)

	g.Expect(cli.Delete(ctx, claim, client.Preconditions{UID: &claim.UID})).To(Succeed())
	g.Eventually(func(g Gomega) {
		current := &infraapi.DatabaseClaim{}
		err := cli.Get(ctx, client.ObjectKeyFromObject(claim), current)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "claim still exists: %v", err)
	}).WithTimeout(2 * time.Minute).Should(Succeed())
}

// TLS fixture: ODH_E2E_EXTERNAL_ADMIN_SECRET_*. Plaintext fixture (no ca.crt):
// ODH_E2E_EXTERNAL_PLAINTEXT_ADMIN_SECRET_*. Skip when the pair is unset.

func TestExternalPreferAccessorUsesTLS(t *testing.T) {
	runExternalPreferAllow(t, preferAllowCase{
		nsKey: "ODH_E2E_EXTERNAL_ADMIN_SECRET_NAMESPACE", nameKey: "ODH_E2E_EXTERNAL_ADMIN_SECRET_NAME",
		sslMode: postgres.SSLModePrefer, withCA: true,
		requireSourceMode: postgres.SSLModeVerifyFull,
	})
}

func TestExternalPreferAccessorPlaintext(t *testing.T) {
	runExternalPreferAllow(t, preferAllowCase{
		nsKey: "ODH_E2E_EXTERNAL_PLAINTEXT_ADMIN_SECRET_NAMESPACE", nameKey: "ODH_E2E_EXTERNAL_PLAINTEXT_ADMIN_SECRET_NAME",
		sslMode: postgres.SSLModePrefer, withCA: false,
	})
}

func TestExternalAllowAccessorPlaintext(t *testing.T) {
	runExternalPreferAllow(t, preferAllowCase{
		nsKey: "ODH_E2E_EXTERNAL_PLAINTEXT_ADMIN_SECRET_NAMESPACE", nameKey: "ODH_E2E_EXTERNAL_PLAINTEXT_ADMIN_SECRET_NAME",
		sslMode: postgres.SSLModeAllow, withCA: false,
	})
}

type preferAllowCase struct {
	nsKey, nameKey, sslMode, requireSourceMode string
	withCA                                     bool
}

func requireExternalAdminSecret(t *testing.T, nsKey, nameKey string) (string, string) {
	t.Helper()
	namespace, name := os.Getenv(nsKey), os.Getenv(nameKey)
	if namespace == "" && name == "" {
		t.Skip("set " + nsKey + " and " + nameKey)
	}
	if namespace == "" || name == "" {
		t.Fatal("both " + nsKey + " and " + nameKey + " are required")
	}
	return namespace, name
}

func runExternalPreferAllow(t *testing.T, tc preferAllowCase) {
	t.Helper()
	sourceNS, sourceName := requireExternalAdminSecret(t, tc.nsKey, tc.nameKey)
	g := NewWithT(t)
	restCfg, err := ctrl.GetConfig()
	g.Expect(err).NotTo(HaveOccurred())
	scheme, err := modulemanager.NewScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cli, err := client.New(restCfg, client.Options{Scheme: scheme})
	g.Expect(err).NotTo(HaveOccurred())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	t.Cleanup(cancel)

	ensureOperandCRDs(t, ctx, cli)

	source := &corev1.Secret{}
	g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: sourceNS, Name: sourceName}, source)).To(Succeed())
	for _, key := range []string{
		postgres.SecretKeyHost, postgres.SecretKeyPort, postgres.SecretKeyUser,
		postgres.SecretKeyPassword, postgres.SecretKeyDatabase,
	} {
		g.Expect(source.Data[key]).NotTo(BeEmpty(), "external admin Secret needs %q", key)
	}
	if tc.withCA {
		g.Expect(source.Data[postgres.SecretKeyCA]).NotTo(BeEmpty(), "TLS External admin Secret needs ca.crt")
	} else {
		g.Expect(source.Data[postgres.SecretKeyCA]).To(BeEmpty(), "plaintext External admin Secret must omit ca.crt")
	}
	if tc.requireSourceMode != "" {
		g.Expect(string(source.Data[postgres.SecretKeySSLMode])).To(Equal(tc.requireSourceMode))
	}

	adminSecret := createPreferAllowAdminSecret(t, ctx, cli, source, tc.sslMode, tc.withCA)
	// When a claim exists, its cleanup owns provider/admin Secret deletion after
	// NotFound. These fallbacks only run if the test fails before that handoff,
	// so a failed claim wait cannot drop DropRole's dependencies early.
	claimOwnsBackendCleanup := false
	t.Cleanup(func() {
		if claimOwnsBackendCleanup {
			return
		}
		cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		err := cli.Delete(cleanupCtx, adminSecret, client.Preconditions{UID: &adminSecret.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning prefer/allow admin Secret: %v", err)
		}
	})

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "odh-db-prefer-"}}
	g.Expect(cli.Create(ctx, namespace)).To(Succeed())
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		if err := support.DeleteNamespace(cleanupCtx, cli, namespace.Name, namespace.UID); err != nil {
			t.Errorf("cleaning consumer namespace: %v", err)
		}
	})

	provider := &infraapi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "odh-db-e2e-prefer-"},
		Spec: infraapi.DatabaseProviderSpec{
			Type: infraapi.ProviderTypeExternal,
			External: &infraapi.ExternalProviderSpec{
				ConnectionSecretRef: corev1.SecretReference{
					Namespace: adminSecret.Namespace, Name: adminSecret.Name,
				},
			},
		},
	}
	g.Expect(cli.Create(ctx, provider)).To(Succeed())
	t.Cleanup(func() {
		if claimOwnsBackendCleanup {
			return
		}
		cleanupCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		err := cli.Delete(cleanupCtx, provider, client.Preconditions{UID: &provider.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning external provider: %v", err)
		}
	})
	g.Eventually(func(g Gomega) {
		current := &infraapi.DatabaseProvider{}
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(provider), current)).To(Succeed())
		condition := conditions.FindStatusCondition(current, "Reachable")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Status).To(Equal(metav1.ConditionTrue), "%s", condition.Message)
	}).WithTimeout(2*time.Minute).Should(Succeed(), "external provider did not become reachable")

	claim := &infraapi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "prefer-allow-contract", Namespace: namespace.Name},
		Spec: infraapi.DatabaseClaimSpec{
			Provider: infraapi.ProviderRef{Name: provider.Name},
			Access:   infraapi.AccessModeReadWrite,
		},
	}
	g.Expect(cli.Create(ctx, claim)).To(Succeed())
	claimOwnsBackendCleanup = true
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 3*time.Minute)
		defer done()
		err := cli.Delete(cleanupCtx, claim, client.Preconditions{UID: &claim.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning database claim: %v", err)
			return
		}
		NewWithT(t).Eventually(func(g Gomega) {
			current := &infraapi.DatabaseClaim{}
			getErr := cli.Get(cleanupCtx, client.ObjectKeyFromObject(claim), current)
			g.Expect(apierrors.IsNotFound(getErr)).To(BeTrue(), "claim still exists: %v", getErr)
		}).WithTimeout(2 * time.Minute).WithPolling(2 * time.Second).Should(Succeed())

		err = cli.Delete(cleanupCtx, provider, client.Preconditions{UID: &provider.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning external provider: %v", err)
		}
		err = cli.Delete(cleanupCtx, adminSecret, client.Preconditions{UID: &adminSecret.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning prefer/allow admin Secret: %v", err)
		}
	})
	g.Eventually(func(g Gomega) {
		current := &infraapi.DatabaseClaim{}
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(claim), current)).To(Succeed())
		condition := conditions.FindStatusCondition(current, "Provisioned")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Status).To(Equal(metav1.ConditionTrue), "%s", condition.Message)
		g.Expect(current.Status.Connection.SecretRef.Name).To(Equal(claim.Name))
	}).WithTimeout(2*time.Minute).Should(Succeed(), "database claim did not provision")

	credentials := &corev1.Secret{}
	g.Eventually(func(g Gomega) {
		g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: namespace.Name, Name: claim.Name}, credentials)).To(Succeed())
	}).WithTimeout(time.Minute).Should(Succeed())
	g.Expect(string(credentials.Data[postgres.SecretKeySSLMode])).To(Equal(tc.sslMode),
		"claim Secret must preserve prefer/allow")
	if tc.withCA {
		g.Expect(credentials.Data[postgres.SecretKeyCA]).NotTo(BeEmpty())
	} else {
		_, hasCA := credentials.Data[postgres.SecretKeyCA]
		g.Expect(hasCA).To(BeFalse(), "plaintext claim Secret must omit ca.crt")
	}

	connection, params, err := claimLibpqConnection(credentials, caFileForConsumer(tc.withCA))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(params.SSLMode).To(Equal(tc.sslMode))
	g.Expect(strings.Contains(connection, "sslmode='"+tc.sslMode+"'")).
		To(BeTrue(), "accessor Libpq string must preserve sslmode")

	image := envOrDefault("ODH_E2E_CONSUMER_IMAGE", "postgres:16")
	createAdminContractTable(t, ctx, cli, restCfg, adminSecret.Namespace, adminSecret.Name,
		string(credentials.Data[postgres.SecretKeyDatabase]), string(credentials.Data[postgres.SecretKeyUser]),
		tc.sslMode, image, tc.withCA)
	runAccessorConsumer(t, ctx, cli, restCfg, namespace.Name, claim.Name, image, tc.withCA)
}

func createPreferAllowAdminSecret(
	t *testing.T,
	ctx context.Context,
	cli client.Client,
	source *corev1.Secret,
	sslMode string,
	withCA bool,
) *corev1.Secret {
	t.Helper()
	g := NewWithT(t)
	data := map[string][]byte{
		postgres.SecretKeyHost:     append([]byte(nil), source.Data[postgres.SecretKeyHost]...),
		postgres.SecretKeyPort:     append([]byte(nil), source.Data[postgres.SecretKeyPort]...),
		postgres.SecretKeyUser:     append([]byte(nil), source.Data[postgres.SecretKeyUser]...),
		postgres.SecretKeyPassword: append([]byte(nil), source.Data[postgres.SecretKeyPassword]...),
		postgres.SecretKeyDatabase: append([]byte(nil), source.Data[postgres.SecretKeyDatabase]...),
		postgres.SecretKeySSLMode:  []byte(sslMode),
	}
	if withCA {
		data[postgres.SecretKeyCA] = append([]byte(nil), source.Data[postgres.SecretKeyCA]...)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "external-prefer-admin-",
			Namespace:    source.Namespace,
		},
		Data: data,
	}
	g.Expect(cli.Create(ctx, secret)).To(Succeed())
	return secret
}
