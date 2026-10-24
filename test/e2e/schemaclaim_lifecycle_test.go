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
	"testing"
	"time"

	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	infraapi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	dbcontroller "github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
	modulemanager "github.com/opendatahub-io/opendatahub-db-operator/pkg/manager"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres"
	"github.com/opendatahub-io/opendatahub-db-operator/test/support"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Schema-scoped SQL used by admin/claim pods. connectionScript is defined in
// external_claim_test.go (same package).
const schemaClaimObjectCheckScript = connectionScript + `
case "$DB_SSLMODE" in
    verify-full)
        connection="$connection sslrootcert='/etc/db-trust/ca.crt'"
        ;;
    disable)
        ;;
    *)
        echo "unexpected sslmode: $DB_SSLMODE" >&2
        exit 1
        ;;
esac
psql -X -qAt -v ON_ERROR_STOP=1 -v schema="$CLAIM_SCHEMA" -v role="$CLAIM_ROLE" "$connection" <<'SQL'
SELECT CASE WHEN EXISTS (
  SELECT 1 FROM pg_namespace WHERE nspname = :'schema'
) THEN 'schema_ok' ELSE 'schema_missing' END;
SELECT CASE WHEN EXISTS (
  SELECT 1 FROM pg_roles WHERE rolname = :'role'
) THEN 'role_ok' ELSE 'role_missing' END;
SQL
`

const schemaClaimWriteAsClaimScript = connectionScript + `
case "$DB_SSLMODE" in
    verify-full)
        connection="$connection sslrootcert='/etc/db-trust/ca.crt'"
        ;;
    disable)
        ;;
    *)
        echo "unexpected sslmode: $DB_SSLMODE" >&2
        exit 1
        ;;
esac
psql -X -qAt -v ON_ERROR_STOP=1 -v schema="$CLAIM_SCHEMA" "$connection" <<'SQL'
CREATE TABLE IF NOT EXISTS :"schema".isolation_probe (value integer);
TRUNCATE :"schema".isolation_probe;
INSERT INTO :"schema".isolation_probe VALUES (7);
SELECT value FROM :"schema".isolation_probe;
SQL
`

const schemaClaimCrossReadScript = connectionScript + `
case "$DB_SSLMODE" in
    verify-full)
        connection="$connection sslrootcert='/etc/db-trust/ca.crt'"
        ;;
    disable)
        ;;
    *)
        echo "unexpected sslmode: $DB_SSLMODE" >&2
        exit 1
        ;;
esac
# Peer schema must not be readable. Expect psql to fail.
if psql -X -qAt -v ON_ERROR_STOP=1 -v schema="$PEER_SCHEMA" "$connection" \
  -c 'SELECT value FROM :"schema".isolation_probe'; then
  echo "cross-schema read unexpectedly succeeded" >&2
  exit 1
fi
echo "cross_read_denied"
`

const schemaClaimSchemaPresenceScript = connectionScript + `
case "$DB_SSLMODE" in
    verify-full)
        connection="$connection sslrootcert='/etc/db-trust/ca.crt'"
        ;;
    disable)
        ;;
    *)
        echo "unexpected sslmode: $DB_SSLMODE" >&2
        exit 1
        ;;
esac
psql -X -qAt -v ON_ERROR_STOP=1 -v schema="$CLAIM_SCHEMA" "$connection" <<'SQL'
SELECT CASE WHEN EXISTS (
  SELECT 1 FROM pg_namespace WHERE nspname = :'schema'
) THEN 'present' ELSE 'absent' END;
SQL
`

const schemaClaimRolePresenceScript = connectionScript + `
case "$DB_SSLMODE" in
    verify-full)
        connection="$connection sslrootcert='/etc/db-trust/ca.crt'"
        ;;
    disable)
        ;;
    *)
        echo "unexpected sslmode: $DB_SSLMODE" >&2
        exit 1
        ;;
esac
psql -X -qAt -v ON_ERROR_STOP=1 -v role="$CLAIM_ROLE" "$connection" <<'SQL'
SELECT CASE WHEN EXISTS (
  SELECT 1 FROM pg_roles WHERE rolname = :'role'
) THEN 'present' ELSE 'absent' END;
SQL
`

// TestSchemaClaimLifecycle exercises SchemaClaim acceptance criteria on Kind:
// provision, Secret contract, tenant isolation, and Retain/Delete cleanup.
func TestSchemaClaimLifecycle(t *testing.T) {
	profile := os.Getenv("ODH_E2E_CERT_MANAGER_PROFILE")
	if profile == "" {
		t.Skip("set ODH_E2E_CERT_MANAGER_PROFILE to present or absent")
	}
	if profile != certManagerProfilePresent && profile != certManagerProfileAbsent {
		t.Fatalf("unknown cert-manager profile %q", profile)
	}
	tlsEnabled := profile == certManagerProfilePresent

	g := NewWithT(t)
	restCfg, err := ctrl.GetConfig()
	g.Expect(err).NotTo(HaveOccurred())
	scheme, err := modulemanager.NewScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cli, err := client.New(restCfg, client.Options{Scheme: scheme})
	g.Expect(err).NotTo(HaveOccurred())
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	t.Cleanup(cancel)

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "odh-db-schemaclaim-"}}
	g.Expect(cli.Create(ctx, namespace)).To(Succeed())
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		if err := support.DeleteNamespace(cleanupCtx, cli, namespace.Name, namespace.UID); err != nil {
			t.Errorf("cleaning consumer namespace: %v", err)
		}
	})

	provider := &infraapi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "odh-db-schemaclaim-"},
		Spec: infraapi.DatabaseProviderSpec{
			Type: infraapi.ProviderTypeInternal,
			Internal: &infraapi.InternalProviderSpec{
				Storage: infraapi.StorageSpec{Size: resource.MustParse("1Gi")},
			},
		},
	}
	if tlsEnabled {
		provider.Spec.Internal.TLS = &infraapi.InternalProviderTLSSpec{}
	}
	g.Expect(cli.Create(ctx, provider)).To(Succeed())
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		err := cli.Delete(cleanupCtx, provider, client.Preconditions{UID: &provider.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning internal provider: %v", err)
		}
	})

	//nolint:dupl // Same Reachable wait as internal_claim_test; keep inline to avoid a shared e2e helper for one block.
	g.Eventually(func(g Gomega) {
		current := &infraapi.DatabaseProvider{}
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(provider), current)).To(Succeed())
		condition := conditions.FindStatusCondition(current, "Reachable")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Status).To(Equal(metav1.ConditionTrue), "%s", condition.Message)
		g.Expect(current.Status.Connection.Host).NotTo(BeEmpty())
		g.Expect(current.Status.TLS).NotTo(BeNil())
		g.Expect(current.Status.TLS.Enabled).To(Equal(tlsEnabled))
		g.Expect(current.Status.TLS.Ready).To(Equal(tlsEnabled))
	}).WithTimeout(5*time.Minute).Should(Succeed(), "internal provider did not become reachable")

	retainClaim := &infraapi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "schema-retain", Namespace: namespace.Name},
		Spec: infraapi.SchemaClaimSpec{
			Provider:       infraapi.ProviderRef{Name: provider.Name},
			Access:         infraapi.AccessModeReadWrite,
			DeletionPolicy: infraapi.DeletionPolicyRetain,
		},
	}
	deleteClaim := &infraapi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "schema-delete", Namespace: namespace.Name},
		Spec: infraapi.SchemaClaimSpec{
			Provider:       infraapi.ProviderRef{Name: provider.Name},
			Access:         infraapi.AccessModeReadWrite,
			DeletionPolicy: infraapi.DeletionPolicyDelete,
		},
	}
	// Anchor claim keeps this consumer namespace on the Internal NetworkPolicy
	// after retain/delete claims are removed, so post-delete admin psql pods
	// can still reach Postgres. Operator-namespace peers require the operator
	// pod label, so arbitrary SQL pods there are denied.
	anchorClaim := &infraapi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "schema-anchor", Namespace: namespace.Name},
		Spec: infraapi.SchemaClaimSpec{
			Provider:       infraapi.ProviderRef{Name: provider.Name},
			Access:         infraapi.AccessModeReadOnly,
			DeletionPolicy: infraapi.DeletionPolicyRetain,
		},
	}
	for _, claim := range []*infraapi.SchemaClaim{retainClaim, deleteClaim, anchorClaim} {
		g.Expect(cli.Create(ctx, claim)).To(Succeed())
	}

	waitSchemaClaimProvisioned(t, ctx, cli, retainClaim)
	waitSchemaClaimProvisioned(t, ctx, cli, deleteClaim)
	waitSchemaClaimProvisioned(t, ctx, cli, anchorClaim)

	expectedSSLMode := postgres.SSLModeDisable
	if tlsEnabled {
		expectedSSLMode = postgres.SSLModeVerifyFull
	}
	retainSecret := assertSchemaClaimSecret(t, ctx, cli, retainClaim, expectedSSLMode, tlsEnabled)
	deleteSecret := assertSchemaClaimSecret(t, ctx, cli, deleteClaim, expectedSSLMode, tlsEnabled)
	g.Expect(string(retainSecret.Data[postgres.SecretKeyUser])).
		NotTo(Equal(string(deleteSecret.Data[postgres.SecretKeyUser])))

	image := envOrDefault("ODH_E2E_CONSUMER_IMAGE", "postgres:16")
	operatorNamespace := os.Getenv("ODH_E2E_NAMESPACE")
	g.Expect(operatorNamespace).NotTo(BeEmpty(), "ODH_E2E_NAMESPACE must name the operator namespace")
	adminSecretCopy := copyInternalAdminSecret(t, ctx, cli, provider.Name, operatorNamespace, namespace.Name)

	// AC1: schema and role exist for both claims.
	runSchemaClaimSQL(t, ctx, cli, restCfg, namespace.Name, adminSecretCopy.Name, image, tlsEnabled,
		schemaClaimObjectCheckScript,
		map[string]string{
			"CLAIM_SCHEMA": string(retainSecret.Data[postgres.SecretKeySchema]),
			"CLAIM_ROLE":   string(retainSecret.Data[postgres.SecretKeyUser]),
			"DB_NAME":      string(retainSecret.Data[postgres.SecretKeyDatabase]),
			"DB_SSLMODE":   expectedSSLMode,
		},
		"schema_ok\nrole_ok",
	)
	runSchemaClaimSQL(t, ctx, cli, restCfg, namespace.Name, adminSecretCopy.Name, image, tlsEnabled,
		schemaClaimObjectCheckScript,
		map[string]string{
			"CLAIM_SCHEMA": string(deleteSecret.Data[postgres.SecretKeySchema]),
			"CLAIM_ROLE":   string(deleteSecret.Data[postgres.SecretKeyUser]),
			"DB_NAME":      string(deleteSecret.Data[postgres.SecretKeyDatabase]),
			"DB_SSLMODE":   expectedSSLMode,
		},
		"schema_ok\nrole_ok",
	)

	// AC2: distinct roles; claim A can write its schema; claim B cannot read it.
	retainSecretName := dbcontroller.SecretNameForSchemaClaim(retainClaim)
	deleteSecretName := dbcontroller.SecretNameForSchemaClaim(deleteClaim)
	runSchemaClaimSQL(t, ctx, cli, restCfg, namespace.Name, retainSecretName, image, tlsEnabled,
		schemaClaimWriteAsClaimScript,
		map[string]string{
			"CLAIM_SCHEMA": string(retainSecret.Data[postgres.SecretKeySchema]),
		},
		"7",
	)
	runSchemaClaimSQL(t, ctx, cli, restCfg, namespace.Name, deleteSecretName, image, tlsEnabled,
		schemaClaimCrossReadScript,
		map[string]string{
			"PEER_SCHEMA": string(retainSecret.Data[postgres.SecretKeySchema]),
		},
		"cross_read_denied",
	)

	retainSchema := string(retainSecret.Data[postgres.SecretKeySchema])
	retainRole := string(retainSecret.Data[postgres.SecretKeyUser])
	deleteSchema := string(deleteSecret.Data[postgres.SecretKeySchema])
	deleteRole := string(deleteSecret.Data[postgres.SecretKeyUser])
	sharedDB := string(retainSecret.Data[postgres.SecretKeyDatabase])

	// AC4a: Retain — drop role + Secret, keep schema.
	g.Expect(cli.Delete(ctx, retainClaim, client.Preconditions{UID: &retainClaim.UID})).To(Succeed())
	g.Eventually(func(g Gomega) {
		current := &infraapi.SchemaClaim{}
		err := cli.Get(ctx, client.ObjectKeyFromObject(retainClaim), current)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "retain claim still exists: %v", err)
	}).WithTimeout(2 * time.Minute).Should(Succeed())
	g.Eventually(func(g Gomega) {
		secret := &corev1.Secret{}
		err := cli.Get(ctx, client.ObjectKey{Namespace: namespace.Name, Name: retainSecretName}, secret)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "retain Secret still exists: %v", err)
	}).WithTimeout(2 * time.Minute).Should(Succeed())
	runSchemaClaimSQL(t, ctx, cli, restCfg, namespace.Name, adminSecretCopy.Name, image, tlsEnabled,
		schemaClaimSchemaPresenceScript,
		map[string]string{
			"CLAIM_SCHEMA": retainSchema,
			"DB_NAME":      sharedDB,
			"DB_SSLMODE":   expectedSSLMode,
		},
		"present",
	)
	runSchemaClaimSQL(t, ctx, cli, restCfg, namespace.Name, adminSecretCopy.Name, image, tlsEnabled,
		schemaClaimRolePresenceScript,
		map[string]string{
			"CLAIM_ROLE": retainRole,
			"DB_NAME":    sharedDB,
			"DB_SSLMODE": expectedSSLMode,
		},
		"absent",
	)

	// AC4b: Delete — drop role + Secret + schema.
	g.Expect(cli.Delete(ctx, deleteClaim, client.Preconditions{UID: &deleteClaim.UID})).To(Succeed())
	g.Eventually(func(g Gomega) {
		current := &infraapi.SchemaClaim{}
		err := cli.Get(ctx, client.ObjectKeyFromObject(deleteClaim), current)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "delete claim still exists: %v", err)
	}).WithTimeout(2 * time.Minute).Should(Succeed())
	g.Eventually(func(g Gomega) {
		secret := &corev1.Secret{}
		err := cli.Get(ctx, client.ObjectKey{Namespace: namespace.Name, Name: deleteSecretName}, secret)
		g.Expect(apierrors.IsNotFound(err)).To(BeTrue(), "delete Secret still exists: %v", err)
	}).WithTimeout(2 * time.Minute).Should(Succeed())
	runSchemaClaimSQL(t, ctx, cli, restCfg, namespace.Name, adminSecretCopy.Name, image, tlsEnabled,
		schemaClaimSchemaPresenceScript,
		map[string]string{
			"CLAIM_SCHEMA": deleteSchema,
			"DB_NAME":      sharedDB,
			"DB_SSLMODE":   expectedSSLMode,
		},
		"absent",
	)
	runSchemaClaimSQL(t, ctx, cli, restCfg, namespace.Name, adminSecretCopy.Name, image, tlsEnabled,
		schemaClaimRolePresenceScript,
		map[string]string{
			"CLAIM_ROLE": deleteRole,
			"DB_NAME":    sharedDB,
			"DB_SSLMODE": expectedSSLMode,
		},
		"absent",
	)
}

func waitSchemaClaimProvisioned(
	t *testing.T,
	ctx context.Context,
	cli client.Client,
	claim *infraapi.SchemaClaim,
) {
	t.Helper()
	g := NewWithT(t)
	secretName := dbcontroller.SecretNameForSchemaClaim(claim)
	g.Eventually(func(g Gomega) {
		current := &infraapi.SchemaClaim{}
		g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(claim), current)).To(Succeed())
		*claim = *current
		condition := conditions.FindStatusCondition(current, "Provisioned")
		g.Expect(condition).NotTo(BeNil())
		g.Expect(condition.Status).To(Equal(metav1.ConditionTrue), "%s", condition.Message)
		g.Expect(current.Status.Connection.SecretRef.Name).To(Equal(secretName))
		g.Expect(current.Status.Schema).NotTo(BeEmpty())
		g.Expect(current.Status.SchemaCreated).To(BeTrue())
	}).WithTimeout(3*time.Minute).Should(Succeed(), "schema claim %s/%s did not provision", claim.Namespace, claim.Name)
}

func assertSchemaClaimSecret(
	t *testing.T,
	ctx context.Context,
	cli client.Client,
	claim *infraapi.SchemaClaim,
	expectedSSLMode string,
	tlsEnabled bool,
) *corev1.Secret {
	t.Helper()
	g := NewWithT(t)
	secretName := dbcontroller.SecretNameForSchemaClaim(claim)
	credentials := &corev1.Secret{}
	g.Eventually(func(g Gomega) {
		g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: claim.Namespace, Name: secretName}, credentials)).To(Succeed())
	}).WithTimeout(time.Minute).Should(Succeed())

	for _, key := range []string{
		postgres.SecretKeyHost,
		postgres.SecretKeyPort,
		postgres.SecretKeyUser,
		postgres.SecretKeyPassword,
		postgres.SecretKeyDatabase,
		postgres.SecretKeySchema,
		postgres.SecretKeySSLMode,
	} {
		g.Expect(credentials.Data[key]).NotTo(BeEmpty(), "claim Secret needs %q", key)
	}
	g.Expect(string(credentials.Data[postgres.SecretKeySchema])).To(Equal(claim.Status.Schema))
	g.Expect(string(credentials.Data[postgres.SecretKeySSLMode])).To(Equal(expectedSSLMode))
	if tlsEnabled {
		g.Expect(credentials.Data[postgres.SecretKeyCA]).NotTo(BeEmpty())
	} else {
		g.Expect(credentials.Data).NotTo(HaveKey(postgres.SecretKeyCA))
	}
	g.Expect(credentials.Annotations[postgres.AnnotationConnectionTypeProtocol]).
		To(Equal(postgres.ConnectionTypeProtocolPostgres))

	owner := metav1.GetControllerOf(credentials)
	g.Expect(owner).NotTo(BeNil(), "credentials Secret must have a controller owner")
	g.Expect(owner.UID).To(Equal(claim.UID))
	g.Expect(owner.Kind).To(Equal(infraapi.SchemaClaimKind))
	return credentials
}

func copyInternalAdminSecret(
	t *testing.T,
	ctx context.Context,
	cli client.Client,
	providerName, operatorNamespace, consumerNamespace string,
) *corev1.Secret {
	t.Helper()
	g := NewWithT(t)
	internalAdminSecret := &corev1.Secret{}
	g.Expect(cli.Get(ctx, client.ObjectKey{
		Namespace: operatorNamespace,
		Name:      dbcontroller.InternalAdminSecretName(providerName),
	}, internalAdminSecret)).To(Succeed())
	adminSecretCopy := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "schemaclaim-admin-", Namespace: consumerNamespace},
		Type:       internalAdminSecret.Type,
		Data:       internalAdminSecret.Data,
	}
	g.Expect(cli.Create(ctx, adminSecretCopy)).To(Succeed())
	t.Cleanup(func() {
		cleanupCtx, done := context.WithTimeout(context.Background(), 30*time.Second)
		defer done()
		err := cli.Delete(cleanupCtx, adminSecretCopy, client.Preconditions{UID: &adminSecretCopy.UID})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			t.Errorf("cleaning temporary schemaclaim admin Secret: %v", err)
		}
	})
	return adminSecretCopy
}

func runSchemaClaimSQL(
	t *testing.T,
	ctx context.Context,
	cli client.Client,
	restCfg *rest.Config,
	namespace, secretName, image string,
	tlsEnabled bool,
	script string,
	envOverrides map[string]string,
	wantLogSubstring string,
) {
	t.Helper()
	g := NewWithT(t)
	pod := consumerPod(namespace, secretName, image, tlsEnabled)
	pod.Name = ""
	pod.GenerateName = "schemaclaim-sql-"
	pod.Spec.Containers[0].Command = []string{"/bin/sh", "-ec", script}
	for key, value := range envOverrides {
		setOrAppendEnv(&pod.Spec.Containers[0].Env, key, value)
	}
	runPod(t, ctx, cli, restCfg, pod)

	if wantLogSubstring == "" {
		return
	}
	logs, err := consumerLogs(ctx, restCfg, pod.Namespace, pod.Name)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(logs)).To(ContainSubstring(wantLogSubstring), "pod logs: %s", logs)
}

func setOrAppendEnv(env *[]corev1.EnvVar, name, value string) {
	for i := range *env {
		if (*env)[i].Name == name {
			(*env)[i].Value = value
			(*env)[i].ValueFrom = nil
			return
		}
	}
	*env = append(*env, corev1.EnvVar{Name: name, Value: value})
}
