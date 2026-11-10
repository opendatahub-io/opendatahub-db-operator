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

// Package operandinstall verifies the operand install path end to end: a
// manager started without infrastructure CRDs installs them as module
// operands, registers the infrastructure controllers once they are
// established, and provisions the default provider — no restart. It runs
// in a separate test process because controller-runtime rejects duplicate
// controller names across managers in one process.
package operandinstall

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/reconciler"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesv1alpha1 "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	modulemanager "github.com/opendatahub-io/opendatahub-db-operator/pkg/manager"
	pginstance "github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres/instance"
)

func TestOperandsInstalledAndControllersRegistered(t *testing.T) {
	g := NewWithT(t)
	ctrl.SetLogger(logr.Discard())

	// Only the DatabaseService CRD is installed: the manager must start
	// healthy with no infrastructure CRDs, and the operator must apply
	// them itself as module operands.
	env := &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "..", "config", "crd", "bases", "services.platform.opendatahub.io_databaseservices.yaml"),
		},
		ErrorIfCRDPathMissing: true,
	}
	restCfg, err := env.Start()
	g.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(func() { g.Expect(env.Stop()).To(Succeed()) })

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	cfg, err := moduleconfig.Load()
	g.Expect(err).NotTo(HaveOccurred())
	cfg.Controller.LeaderElection.Enabled = false
	cfg.Controller.Metrics.BindAddress = "0"
	cfg.Controller.Health.BindAddress = "0"
	cfg.Controller.Pprof.BindAddress = "0"
	cfg.Controller.Webhook.Enabled = false
	// The establishment gate requeues with this interval while the operand
	// CRDs settle; the compiled default (3m) would stall the suite.
	cfg.DatabaseService.RetryInterval = time.Second

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

	cli := mgr.GetClient()

	// The internal provider controller creates the default provider's
	// resources in the operator namespace; envtest has no namespace
	// controller, so the suite creates it up front. This also makes the
	// late controller registration observable through the provider's
	// status and StatefulSet.
	g.Expect(cli.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: cfg.OperatorNamespace}})).To(Succeed())

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
		g.Expect(got.Status.Phase).To(Equal(reconciler.DefaultPhaseNotReady))

		operandsReady := conditions.FindStatusCondition(got, "OperandsReady")
		g.Expect(operandsReady).NotTo(BeNil())
		g.Expect(operandsReady.Status).To(Equal(metav1.ConditionTrue))
		g.Expect(operandsReady.Reason).To(Equal("OperandsEstablished"))

		defaultProvider := conditions.FindStatusCondition(got, "DefaultProviderReady")
		g.Expect(defaultProvider).NotTo(BeNil())
		g.Expect(defaultProvider.Status).To(Equal(metav1.ConditionFalse))
		g.Expect(defaultProvider.Reason).To(Equal("Provisioning"))
	}, 60*time.Second).Should(Succeed())

	// The provider controller was registered only after the operator
	// installed the operand CRDs; its reconciliation of the default
	// provider proves the late registration worked. envtest runs no
	// PostgreSQL, so the StatefulSet exists but never becomes ready.
	g.Eventually(func(g Gomega) {
		provider := &infraApi.DatabaseProvider{}
		g.Expect(cli.Get(ctx, client.ObjectKey{Name: cfg.DefaultProviderName}, provider)).To(Succeed())
		reachable := conditions.FindStatusCondition(provider, "Reachable")
		g.Expect(reachable).NotTo(BeNil())
		g.Expect(reachable.Status).To(Equal(metav1.ConditionFalse))
		g.Expect(reachable.Reason).To(Equal("Provisioning"))

		statefulSet := &appsv1.StatefulSet{}
		key := client.ObjectKey{Namespace: cfg.OperatorNamespace, Name: cfg.DefaultProviderName}
		g.Expect(cli.Get(ctx, key, statefulSet)).To(Succeed())
		g.Expect(*statefulSet.Spec.Replicas).To(Equal(pginstance.InternalPostgresReplicaCount))
	}, 60*time.Second).Should(Succeed())

	// The operand CRDs the operator applied are established and remain
	// after the module singleton exists.
	reader := mgr.GetAPIReader()
	for _, name := range []string{
		infraApi.DatabaseProviderCRDName,
		infraApi.DatabaseClaimCRDName,
		infraApi.SchemaClaimCRDName,
	} {
		crd := &apiextensionsv1.CustomResourceDefinition{}
		err := reader.Get(ctx, client.ObjectKey{Name: name}, crd)
		g.Expect(err).NotTo(HaveOccurred(), "operand CRD %q must exist", name)
		established := false
		for i := range crd.Status.Conditions {
			if crd.Status.Conditions[i].Type == apiextensionsv1.Established {
				established = crd.Status.Conditions[i].Status == apiextensionsv1.ConditionTrue
			}
		}
		g.Expect(established).To(BeTrue(), "operand CRD %q must be established", name)
	}
}
