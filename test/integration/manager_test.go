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

// Package integration holds this module's integration test suite, run
// against a real, connected cluster (the current kubeconfig context) rather
// than envtest. Phase 1 (RHOAIENG-96274) only has one test: proof that the
// scaffold's manager actually starts against a real cluster and reaches a
// healthy state, before any CRD exists. Reconciler-level integration tests
// land in RHOAIENG-96277 alongside the CRDs and controllers they exercise.
package integration

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	ctrl "sigs.k8s.io/controller-runtime"

	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	modulemanager "github.com/opendatahub-io/opendatahub-db-operator/pkg/manager"
	"github.com/opendatahub-io/opendatahub-db-operator/test/support"
)

const (
	testNamespacePrefix = "opendatahub-db-operator-scaffold-it"
	healthAddr          = "127.0.0.1:8998"
)

// TestManagerStartsAndBecomesHealthy proves the phase-1 scaffold actually
// runs: pkg/manager.New builds a real controller-runtime manager against the
// connected cluster, leader election succeeds, and /healthz and /readyz
// respond -- all before any CRD exists. This is the manager-startup proof
// the whole phase hinges on (see docs/plan.md phase 1, verification rung 3).
func TestManagerStartsAndBecomesHealthy(t *testing.T) {
	g := NewWithT(t)

	ctrl.SetLogger(logr.Discard())

	gomegaCfg := support.LoadGomegaConfig()
	SetDefaultEventuallyTimeout(gomegaCfg.EventuallyTimeout)
	SetDefaultEventuallyPollingInterval(gomegaCfg.EventuallyPollingInterval)
	SetDefaultConsistentlyPollingInterval(gomegaCfg.ConsistentlyPollingInterval)

	restCfg, err := ctrl.GetConfig()
	g.Expect(err).NotTo(HaveOccurred(), "no kubeconfig / current context -- this test needs a real, connected cluster")

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	// A per-run, unique namespace name -- on top of the create-ownership and
	// UID-precondition delete below -- so this test can never step on a
	// concurrent run or an unrelated namespace that happens to share a name.
	testNamespace := fmt.Sprintf("%s-%d", testNamespacePrefix, time.Now().UnixNano())

	cfg, err := moduleconfig.Load()
	g.Expect(err).NotTo(HaveOccurred())

	cfg.OperatorNamespace = testNamespace
	cfg.Controller.Metrics.BindAddress = "0"
	cfg.Controller.Health.BindAddress = healthAddr
	cfg.Controller.Pprof.BindAddress = "0"
	cfg.Controller.LeaderElection.Enabled = true
	cfg.Controller.LeaderElection.ID = "opendatahub-db-operator-scaffold-it-lock"

	mgr, err := modulemanager.New(ctx, restCfg, cfg)
	g.Expect(err).NotTo(HaveOccurred())

	// Leader election acquires a Lease in the operator namespace, so it must
	// exist before the manager starts. Only ever clean up the namespace this
	// run actually created, and only that exact object (via the UID
	// precondition) -- never a same-named namespace this run doesn't own.
	nsUID, created, err := support.EnsureNamespace(ctx, mgr.GetClient(), testNamespace)
	g.Expect(err).NotTo(HaveOccurred())
	if created {
		t.Cleanup(func() {
			_ = support.DeleteNamespace(context.Background(), mgr.GetClient(), testNamespace, nsUID)
		})
	}

	managerErrCh := make(chan error, 1)
	go func() {
		managerErrCh <- mgr.Start(ctx)
	}()

	g.Expect(mgr.GetCache().WaitForCacheSync(ctx)).To(BeTrue(), "manager cache failed to sync")

	select {
	case <-mgr.Elected():
		// Leader election succeeded.
	case err := <-managerErrCh:
		t.Fatalf("manager stopped before being elected: %v", err)
	case <-time.After(gomegaCfg.EventuallyTimeout):
		t.Fatal("timed out waiting for leader election")
	}

	g.Eventually(func(g Gomega) {
		assertOK(g, fmt.Sprintf("http://%s/healthz", healthAddr))
		assertOK(g, fmt.Sprintf("http://%s/readyz", healthAddr))
	}).Should(Succeed())
}

func assertOK(g Gomega, url string) {
	resp, err := http.Get(url) //nolint:gosec,noctx // test-only call against a loopback address we just bound ourselves.
	g.Expect(err).NotTo(HaveOccurred())
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(resp.StatusCode).To(Equal(http.StatusOK), "GET %s: %s", url, string(body))
}
