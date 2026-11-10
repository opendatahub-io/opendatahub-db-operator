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

package operands

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/odh-platform-utilities/framework/render/kustomize"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
)

// embeddedCRDs lists the CRD manifest files synced from config/crd/bases by
// the Makefile manifests target.
var embeddedCRDs = []string{
	"infrastructure.opendatahub.io_databaseclaims.yaml",
	"infrastructure.opendatahub.io_databaseproviders.yaml",
	"infrastructure.opendatahub.io_schemaclaims.yaml",
}

// embeddedOperandRBAC lists the operand RBAC manifests that moved from
// config/rbac as the source of truth for module operand installation.
var embeddedOperandRBAC = []string{
	"internal-provider-writer-clusterrole.yaml",
	"internal-provider-writer-rolebinding.yaml",
}

const sourceCRDDir = "../../../../config/crd/bases"

func TestEmbeddedManifestsMatchCRDBases(t *testing.T) {
	for _, file := range embeddedCRDs {
		t.Run(file, func(t *testing.T) {
			g := NewWithT(t)

			source, err := os.ReadFile(filepath.Join(sourceCRDDir, file))
			g.Expect(err).NotTo(HaveOccurred())
			embedded, err := manifestFS.ReadFile("manifests/" + file)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(embedded).To(Equal(source),
				"embedded operand manifests drifted from config/crd/bases; run `make manifests` to sync them")
		})
	}
}

func TestKustomizationListsExactlyTheOperandResources(t *testing.T) {
	g := NewWithT(t)

	raw, err := manifestFS.ReadFile("manifests/kustomization.yaml")
	g.Expect(err).NotTo(HaveOccurred())

	var kustomization struct {
		Resources []string `json:"resources"`
	}
	g.Expect(yaml.Unmarshal(raw, &kustomization)).To(Succeed())
	expected := append(slices.Clone(embeddedCRDs), embeddedOperandRBAC...)
	g.Expect(kustomization.Resources).To(ConsistOf(expected),
		"the operand kustomization must list exactly the synced CRDs and the operand RBAC")
}

func TestEmbeddedManifestsRender(t *testing.T) {
	g := NewWithT(t)

	fs, err := FS()
	g.Expect(err).NotTo(HaveOccurred())

	engine := kustomize.NewEngine(kustomize.WithEngineFS(fs))
	rendered, err := engine.Render(".")
	g.Expect(err).NotTo(HaveOccurred())

	crdGVK := schema.GroupVersionKind{
		Group:   "apiextensions.k8s.io",
		Version: "v1",
		Kind:    "CustomResourceDefinition",
	}
	names := make([]string, 0, len(rendered))
	for i := range rendered {
		if rendered[i].GroupVersionKind() != crdGVK {
			continue
		}
		names = append(names, rendered[i].GetName())
	}
	g.Expect(names).To(ConsistOf(
		infraApi.DatabaseProviderCRDName,
		infraApi.DatabaseClaimCRDName,
		infraApi.SchemaClaimCRDName,
	))
}

// TestEmbeddedOperandRBACRenders verifies the moved operand RBAC manifests
// render with their final names and the RoleBinding subject placeholder.
func TestEmbeddedOperandRBACRenders(t *testing.T) {
	g := NewWithT(t)

	fs, err := FS()
	g.Expect(err).NotTo(HaveOccurred())

	engine := kustomize.NewEngine(kustomize.WithEngineFS(fs))
	rendered, err := engine.Render(".")
	g.Expect(err).NotTo(HaveOccurred())

	var roleBinding *unstructured.Unstructured
	clusterRoleFound := false
	for i := range rendered {
		switch {
		case rendered[i].GetKind() == "ClusterRole" && rendered[i].GetName() == "internal-provider-writer":
			clusterRoleFound = true
		case rendered[i].GetKind() == "RoleBinding" && rendered[i].GetName() == "internal-provider-writer":
			roleBinding = &rendered[i]
		}
	}
	g.Expect(clusterRoleFound).To(BeTrue(), "internal-provider-writer ClusterRole not rendered")
	g.Expect(roleBinding).NotTo(BeNil(), "internal-provider-writer RoleBinding not rendered")

	subjects, found, err := unstructured.NestedSlice(roleBinding.Object, "subjects")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue(), "RoleBinding has no subjects")
	g.Expect(subjects).To(HaveLen(1))
	subject, ok := subjects[0].(map[string]any)
	g.Expect(ok).To(BeTrue())
	g.Expect(subject["kind"]).To(Equal("ServiceAccount"))
	// The name and namespace are placeholders the apply customizer
	// finalizes from runtime configuration.
	g.Expect(subject["name"]).To(Equal("operator"))
	g.Expect(subject["namespace"]).To(Equal("system"))

	roleRef, found, err := unstructured.NestedMap(roleBinding.Object, "roleRef")
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(found).To(BeTrue())
	g.Expect(roleRef["kind"]).To(Equal("ClusterRole"))
	g.Expect(roleRef["name"]).To(Equal("internal-provider-writer"))
}
