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

// Package claimextensions verifies claim CRD Enum=vector rejects unsupported
// extensions at the apiserver for both claim kinds (no webhook).
package claimextensions

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	modulemanager "github.com/opendatahub-io/opendatahub-db-operator/pkg/manager"
)

func startClaimCRDEnv(t *testing.T) (context.Context, client.Client) {
	t.Helper()
	g := NewWithT(t)
	ctrl.SetLogger(logr.Discard())

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	restCfg, err := env.Start()
	g.Expect(err).NotTo(HaveOccurred(),
		"starting envtest -- KUBEBUILDER_ASSETS must point at a directory with etcd/kube-apiserver "+
			"(see `setup-envtest use -p path`); `make test-envtest` sets this up automatically")
	t.Cleanup(func() {
		g.Expect(env.Stop()).To(Succeed())
	})

	scheme, err := modulemanager.NewScheme()
	g.Expect(err).NotTo(HaveOccurred())
	cli, err := client.New(restCfg, client.Options{Scheme: scheme})
	g.Expect(err).NotTo(HaveOccurred())

	ctx := t.Context()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "claim-extensions-test"}}
	g.Expect(cli.Create(ctx, ns)).To(Succeed())

	return ctx, cli
}

func TestClaimExtensionsEnum_AllowsVectorOnBothKinds(t *testing.T) {
	g := NewWithT(t)
	ctx, cli := startClaimCRDEnv(t)

	dc := &infraApi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "dc-vector", Namespace: "claim-extensions-test"},
		Spec: infraApi.DatabaseClaimSpec{
			Extensions: []string{"vector"},
		},
	}
	g.Expect(cli.Create(ctx, dc)).To(Succeed())

	sc := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "sc-vector", Namespace: "claim-extensions-test"},
		Spec: infraApi.SchemaClaimSpec{
			Extensions: []string{"vector"},
		},
	}
	g.Expect(cli.Create(ctx, sc)).To(Succeed())
}

func TestClaimExtensionsEnum_DeniesUnsupportedOnBothKinds(t *testing.T) {
	g := NewWithT(t)
	ctx, cli := startClaimCRDEnv(t)

	dc := &infraApi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "dc-postgis", Namespace: "claim-extensions-test"},
		Spec: infraApi.DatabaseClaimSpec{
			Extensions: []string{"postgis"},
		},
	}
	err := cli.Create(ctx, dc)
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsInvalid(err) || apierrors.IsBadRequest(err)).To(BeTrue(),
		"expected Invalid/BadRequest from OpenAPI Enum, got: %v", err)
	g.Expect(err.Error()).To(Or(
		ContainSubstring("postgis"),
		ContainSubstring("Unsupported value"),
		ContainSubstring("enum"),
	))

	sc := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "sc-postgis", Namespace: "claim-extensions-test"},
		Spec: infraApi.SchemaClaimSpec{
			Extensions: []string{"postgis"},
		},
	}
	err = cli.Create(ctx, sc)
	g.Expect(err).To(HaveOccurred())
	g.Expect(apierrors.IsInvalid(err) || apierrors.IsBadRequest(err)).To(BeTrue(),
		"expected Invalid/BadRequest from OpenAPI Enum, got: %v", err)
	g.Expect(err.Error()).To(Or(
		ContainSubstring("postgis"),
		ContainSubstring("Unsupported value"),
		ContainSubstring("enum"),
	))
}
