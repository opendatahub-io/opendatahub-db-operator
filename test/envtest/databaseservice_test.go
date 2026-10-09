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

// Package envtest tests DatabaseService reconciliation against envtest. Run it with make test-envtest.
package envtest

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	fwapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/reconciler"
	"k8s.io/apimachinery/pkg/api/resource"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesv1alpha1 "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	dbcontroller "github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
	modulemanager "github.com/opendatahub-io/opendatahub-db-operator/pkg/manager"
	"github.com/opendatahub-io/opendatahub-db-operator/test/support"
)

func TestControllersReconcileServiceAndProviders(t *testing.T) {
	g := NewWithT(t)

	ctrl.SetLogger(logr.Discard())

	gomegaCfg := support.LoadGomegaConfig()
	SetDefaultEventuallyTimeout(gomegaCfg.EventuallyTimeout)
	SetDefaultEventuallyPollingInterval(gomegaCfg.EventuallyPollingInterval)
	SetDefaultConsistentlyPollingInterval(gomegaCfg.ConsistentlyPollingInterval)

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	restCfg, err := env.Start()
	g.Expect(err).NotTo(HaveOccurred(),
		"starting envtest -- KUBEBUILDER_ASSETS must point at a directory with etcd/kube-apiserver "+
			"(see `setup-envtest use -p path`); `make test-envtest` sets this up automatically")
	t.Cleanup(func() {
		g.Expect(env.Stop()).To(Succeed())
	})

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cfg, err := moduleconfig.Load()
	g.Expect(err).NotTo(HaveOccurred())

	// envtest is a single, short-lived process -- leader election (which
	// needs a Lease in an existing namespace) and the metrics/health/pprof
	// listeners aren't needed to exercise the reconciler.
	cfg.Controller.LeaderElection.Enabled = false
	cfg.Controller.Metrics.BindAddress = "0"
	cfg.Controller.Health.BindAddress = "0"
	cfg.Controller.Pprof.BindAddress = "0"

	mgr, err := modulemanager.New(ctx, restCfg, cfg)
	g.Expect(err).NotTo(HaveOccurred())

	managerDone := make(chan struct{})
	var managerErr error
	go func() {
		managerErr = mgr.Start(ctx)
		close(managerDone)
	}()
	t.Cleanup(func() {
		cancel()
		<-managerDone
		if managerErr != nil {
			t.Errorf("manager failed while stopping: %v", managerErr)
		}
	})

	g.Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue(), "manager cache failed to sync")

	cli := mgr.GetClient()

	g.Expect(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "database-system"}})).To(Succeed())
	t.Run("DatabaseService readiness", func(t *testing.T) {
		g := NewWithT(t)
		instance := &servicesv1alpha1.DatabaseService{
			ObjectMeta: metav1.ObjectMeta{Name: servicesv1alpha1.DatabaseServiceInstanceName},
		}
		g.Expect(cli.Create(ctx, instance)).To(Succeed())

		g.Eventually(func(g Gomega) {
			select {
			case <-managerDone:
				g.Expect(managerDone).NotTo(BeClosed(), "manager stopped unexpectedly: %v", managerErr)
			default:
			}

			got := &servicesv1alpha1.DatabaseService{}
			g.Expect(cli.Get(ctx, client.ObjectKeyFromObject(instance), got)).To(Succeed())
			g.Expect(got.Status.Phase).To(Equal(reconciler.DefaultPhaseReady))

			readyCond := conditions.FindStatusCondition(got, string(fwapi.ConditionTypeReady))
			g.Expect(readyCond).NotTo(BeNil(), "no Ready condition on status.conditions")
			g.Expect(readyCond.Status).To(Equal(metav1.ConditionTrue))
			release := got.Status.ComponentReleaseStatus.GetRelease(moduleconfig.ReleasePlatform)
			g.Expect(release).NotTo(BeNil(), "no platform release in status.releases")
			g.Expect(release.Version).To(Equal(cfg.ComponentRelease().Version))
		}).Should(Succeed())
	})

	t.Run("DatabaseProvider schema validation", func(t *testing.T) {
		invalidProviders := []struct {
			name     string
			provider *infraApi.DatabaseProvider
		}{
			{
				name: "Internal names reject dots",
				provider: &infraApi.DatabaseProvider{
					ObjectMeta: metav1.ObjectMeta{Name: "pg.primary"},
					Spec: infraApi.DatabaseProviderSpec{
						Type: infraApi.ProviderTypeInternal,
						Internal: &infraApi.InternalProviderSpec{
							Namespace: "database-system",
							Storage:   infraApi.StorageSpec{Size: resource.MustParse("1Gi")},
						},
					},
				},
			},
			{
				name: "Internal names are limited to 63 characters",
				provider: &infraApi.DatabaseProvider{
					ObjectMeta: metav1.ObjectMeta{Name: "p" + strings.Repeat("a", 63)},
					Spec: infraApi.DatabaseProviderSpec{
						Type: infraApi.ProviderTypeInternal,
						Internal: &infraApi.InternalProviderSpec{
							Namespace: "database-system",
							Storage:   infraApi.StorageSpec{Size: resource.MustParse("1Gi")},
						},
					},
				},
			},
			{
				name: "unsupported extensions are rejected",
				provider: &infraApi.DatabaseProvider{
					ObjectMeta: metav1.ObjectMeta{Name: "bad-extension"},
					Spec: infraApi.DatabaseProviderSpec{
						Type: infraApi.ProviderTypeInternal,
						Internal: &infraApi.InternalProviderSpec{
							Namespace:  "database-system",
							Storage:    infraApi.StorageSpec{Size: resource.MustParse("1Gi")},
							Extensions: []string{"uuid_ossp"},
						},
					},
				},
			},
		}
		for _, tc := range invalidProviders {
			t.Run(tc.name, func(t *testing.T) {
				NewWithT(t).Expect(cli.Create(ctx, tc.provider)).To(HaveOccurred())
			})
		}
	})

	external := &infraApi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "external"},
		Spec: infraApi.DatabaseProviderSpec{
			Type: infraApi.ProviderTypeExternal,
			External: &infraApi.ExternalProviderSpec{
				ConnectionSecretRef: corev1.SecretReference{Name: "external-admin", Namespace: "default"},
			},
		},
	}
	externalKey := client.ObjectKeyFromObject(external)
	t.Run("External provider reports a missing admin Secret", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(cli.Create(ctx, external)).To(Succeed())
		g.Eventually(func(g Gomega) {
			got := &infraApi.DatabaseProvider{}
			g.Expect(cli.Get(ctx, externalKey, got)).To(Succeed())
			reachable := conditions.FindStatusCondition(got, "Reachable")
			g.Expect(reachable).NotTo(BeNil())
			g.Expect(reachable.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(reachable.Reason).To(Equal("ConnectionSecretUnavailable"))
			g.Expect(reachable.Message).To(ContainSubstring("external-admin"))
			g.Expect(reachable.Message).To(ContainSubstring("Verify"))
			ready := conditions.FindStatusCondition(got, string(fwapi.ConditionTypeReady))
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).NotTo(Equal(metav1.ConditionTrue))
		}).Should(Succeed())
		expectStableProviderResourceVersion(t, cli, externalKey)
	})

	internal := &infraApi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "internal"},
		Spec: infraApi.DatabaseProviderSpec{
			Type: infraApi.ProviderTypeInternal,
			Internal: &infraApi.InternalProviderSpec{
				Namespace:  "database-system",
				Storage:    infraApi.StorageSpec{Size: resource.MustParse("1Gi")},
				Extensions: []string{"uuid-ossp"},
			},
		},
	}
	internalKey := client.ObjectKeyFromObject(internal)
	t.Run("Internal provider resources and storage lifecycle", func(t *testing.T) {
		g := NewWithT(t)
		g.Expect(cli.Create(ctx, internal)).To(Succeed())
		g.Eventually(func(g Gomega) {
			got := &infraApi.DatabaseProvider{}
			g.Expect(cli.Get(ctx, internalKey, got)).To(Succeed())
			reachable := conditions.FindStatusCondition(got, "Reachable")
			g.Expect(reachable).NotTo(BeNil())
			g.Expect(reachable.Status).To(Equal(metav1.ConditionFalse))
			ready := conditions.FindStatusCondition(got, string(fwapi.ConditionTypeReady))
			g.Expect(ready).NotTo(BeNil())
			g.Expect(ready.Status).NotTo(Equal(metav1.ConditionTrue))
		}).Should(Succeed())

		statefulSet := &appsv1.StatefulSet{}
		g.Eventually(func(g Gomega) {
			g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: "database-system", Name: "internal"}, statefulSet)).To(Succeed())
			g.Expect(statefulSet.Spec.Replicas).NotTo(BeNil())
			g.Expect(*statefulSet.Spec.Replicas).To(Equal(int32(1)))
		}).Should(Succeed())
		pvc := &corev1.PersistentVolumeClaim{}
		g.Eventually(func(g Gomega) {
			g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: "database-system", Name: "internal"}, pvc)).To(Succeed())
			g.Expect(pvc.Spec.Resources.Requests.Storage().String()).To(Equal("1Gi"))
		}).Should(Succeed())
		pvcUID := pvc.UID
		g.Expect(pvcUID).NotTo(BeEmpty())
		providerOwnsPVC := false
		for _, owner := range pvc.OwnerReferences {
			g.Expect(owner.Kind).NotTo(Equal("StatefulSet"))
			if owner.Kind == infraApi.DatabaseProviderKind && owner.Name == "internal" {
				providerOwnsPVC = true
			}
		}
		g.Expect(providerOwnsPVC).To(BeTrue(), "the provider, not the StatefulSet, must own persistent storage")
		pvc.Status.Phase = corev1.ClaimBound
		g.Expect(cli.Status().Update(ctx, pvc)).To(Succeed())
		service := &corev1.Service{}
		g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: "database-system", Name: "internal"}, service)).To(Succeed())
		policy := &networkingv1.NetworkPolicy{}
		g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: "database-system", Name: "internal"}, policy)).To(Succeed())
		adminSecret := &corev1.Secret{}
		g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: "database-system", Name: "internal-admin"}, adminSecret)).
			To(Succeed())

		updated := &infraApi.DatabaseProvider{}
		g.Expect(cli.Get(ctx, internalKey, updated)).To(Succeed())
		updated.Spec.Internal.Storage.Size = resource.MustParse("2Gi")
		updated.Spec.Internal.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
		}
		updated.Spec.Internal.TLS = &infraApi.InternalProviderTLSSpec{
			Certificate: infraApi.InternalProviderTLSCertificateSpec{SecretName: "internal-custom-tls"},
		}
		g.Expect(cli.Update(ctx, updated)).To(Succeed(), "storage and resources should remain mutable")

		for name, mutate := range map[string]func(*infraApi.DatabaseProvider){
			"change namespace":  func(p *infraApi.DatabaseProvider) { p.Spec.Internal.Namespace = "other-system" },
			"remove namespace":  func(p *infraApi.DatabaseProvider) { p.Spec.Internal.Namespace = "" },
			"change extensions": func(p *infraApi.DatabaseProvider) { p.Spec.Internal.Extensions = []string{"pgcrypto"} },
			"remove extensions": func(p *infraApi.DatabaseProvider) { p.Spec.Internal.Extensions = nil },
			"shrink storage":    func(p *infraApi.DatabaseProvider) { p.Spec.Internal.Storage.Size = resource.MustParse("1Gi") },
			"add storage class": func(p *infraApi.DatabaseProvider) {
				storageClass := "fast"
				p.Spec.Internal.Storage.StorageClassName = &storageClass
			},
		} {
			t.Run(name, func(t *testing.T) {
				g := NewWithT(t)
				candidate := updated.DeepCopy()
				mutate(candidate)
				g.Expect(cli.Update(ctx, candidate)).To(HaveOccurred())
			})
		}
		g.Eventually(func(g Gomega) {
			retained := &corev1.PersistentVolumeClaim{}
			g.Expect(cli.Get(ctx, client.ObjectKey{Namespace: "database-system", Name: "internal"}, retained)).To(Succeed())
			g.Expect(retained.UID).To(Equal(pvcUID))
			g.Expect(retained.Status.Phase).To(Equal(corev1.ClaimBound))
		}).Should(Succeed())
		g.Eventually(func(g Gomega) {
			got := &infraApi.DatabaseProvider{}
			g.Expect(cli.Get(ctx, internalKey, got)).To(Succeed())
			tlsCondition := conditions.FindStatusCondition(got, dbcontroller.ConditionTLSConfiguration)
			g.Expect(tlsCondition).NotTo(BeNil())
			g.Expect(tlsCondition.Reason).To(Equal("CertManagerUnavailable"))
		}).Should(Succeed())
		expectStableProviderResourceVersion(t, cli, internalKey)
	})
}

func expectStableProviderResourceVersion(t *testing.T, cli client.Client, key client.ObjectKey) {
	t.Helper()
	g := NewWithT(t)
	var resourceVersion string
	g.Eventually(func(g Gomega) {
		got := &infraApi.DatabaseProvider{}
		g.Expect(cli.Get(t.Context(), key, got)).To(Succeed())
		resourceVersion = got.ResourceVersion
	}).Should(Succeed())
	g.Consistently(func(g Gomega) {
		got := &infraApi.DatabaseProvider{}
		g.Expect(cli.Get(t.Context(), key, got)).To(Succeed())
		g.Expect(got.ResourceVersion).To(Equal(resourceVersion))
	}).Should(Succeed())
}
