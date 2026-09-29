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

package chartgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/onsi/gomega"
)

const deploymentAndRoleBindingManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: manager-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: manager-role
subjects:
- kind: ServiceAccount
  name: manager
`

const deploymentOnlyManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
`

// Regenerating a chart after a resource kind is removed from the kustomize
// input must not leave that kind's template behind -- an installer reading
// the chart would still apply a stale RoleBinding/CRD/etc. that nothing in
// the current source produces anymore.
func TestRun_RemovesStaleTemplatesOnRegeneration(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()

	g.Expect(run(strings.NewReader(deploymentAndRoleBindingManifest), outputDir, "test", "0.1.0")).To(Succeed())

	templatesDir := filepath.Join(outputDir, templatesDirName)
	entries, err := os.ReadDir(templatesDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(namesOf(entries)).To(ContainElement(ContainSubstring("rolebinding")))

	// Regenerate from an input that no longer has the RoleBinding.
	g.Expect(run(strings.NewReader(deploymentOnlyManifest), outputDir, "test", "0.1.0")).To(Succeed())

	entries, err = os.ReadDir(templatesDir)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(namesOf(entries)).NotTo(ContainElement(ContainSubstring("rolebinding")))
}

// A failed upstream producer in the documented `kustomize build ... |
// manager chartgen ...` pipeline can hand chartgen empty or Deployment-less
// input; chartgen must refuse rather than overwrite an existing chart with
// one containing no resources.
func TestRun_RejectsEmptyInput(t *testing.T) {
	g := NewWithT(t)

	err := run(strings.NewReader(""), t.TempDir(), "test", "0.1.0")
	g.Expect(err).To(HaveOccurred())
}

func TestRun_RejectsInputWithoutDeployment(t *testing.T) {
	g := NewWithT(t)

	const noDeployment = `
apiVersion: v1
kind: ServiceAccount
metadata:
  name: manager
`
	err := run(strings.NewReader(noDeployment), t.TempDir(), "test", "0.1.0")
	g.Expect(err).To(HaveOccurred())
}

// Two Deployments both containing a "manager" container is ambiguous input,
// not a shape chartgen should guess at: ExtractDefaults and the render pass
// used to resolve which Deployment is "the operator" independently (the
// first one found vs. every one that has a manager container), so the
// second Deployment could silently receive the first's image, resources,
// and ServiceAccount identity. Rejecting outright is safer than a silent,
// possibly-wrong pick.
func TestRun_RejectsAmbiguousMultipleOperatorDeployments(t *testing.T) {
	g := NewWithT(t)

	const twoOperatorDeployments = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager-two
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: some-other-image:latest
`
	err := run(strings.NewReader(twoOperatorDeployments), t.TempDir(), "test", "0.1.0")
	g.Expect(err).To(HaveOccurred())
}

// An auxiliary Deployment without a manager container is not the operator
// and not ambiguous -- it should pass through untouched rather than
// aborting generation (the old per-resource dispatch called
// stampManagerContainerPlaceholders on every Deployment unconditionally,
// which errored out on exactly this shape).
func TestRun_PassesThroughAuxiliaryDeploymentWithoutManagerContainer(t *testing.T) {
	g := NewWithT(t)

	const operatorPlusAuxiliaryDeployment = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: auxiliary-worker
  namespace: system
spec:
  replicas: 2
  template:
    spec:
      containers:
      - name: worker
        image: quay.io/example/worker:v1
`
	outputDir := t.TempDir()
	err := run(strings.NewReader(operatorPlusAuxiliaryDeployment), outputDir, "test", "0.1.0")
	g.Expect(err).NotTo(HaveOccurred())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "apps_v1_deployment.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).To(ContainSubstring("name: auxiliary-worker"))
	g.Expect(rendered).To(ContainSubstring("image: quay.io/example/worker:v1"))
	g.Expect(rendered).To(ContainSubstring("replicas: 2"))
	g.Expect(rendered).To(ContainSubstring(`image: "{{ include "chart.imageRef" . }}"`))
}

// A previously-generated chart must not be destroyed by a run whose input
// is invalid -- these both fail before templatesDir is ever removed.
func TestRun_RejectsInvalidInputWithoutDeletingExistingChart(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	g.Expect(run(strings.NewReader(deploymentAndRoleBindingManifest), outputDir, "test", "0.1.0")).To(Succeed())

	err := run(strings.NewReader(""), outputDir, "test", "0.1.0")
	g.Expect(err).To(HaveOccurred())

	entries, err := os.ReadDir(filepath.Join(outputDir, templatesDirName))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(namesOf(entries)).To(ContainElement(ContainSubstring("rolebinding")))
}

// A bare prefix match on "namespace:" anywhere in the document (the old
// behavior) would rewrite this ConfigMap's own data value, not just its
// metadata.namespace -- corrupting arbitrary embedded configuration content
// that has nothing to do with the Kubernetes namespace field.
// A Deployment lacking a container named "manager" satisfies
// containsDeployment (there's a Deployment) but still fails deep inside
// renderGroup once transformDeployment can't find the container it's meant
// to template. That failure must be caught before templatesDir is ever
// touched, not discovered partway through writing it.
const deploymentWithoutManagerContainerManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: not-the-manager
        image: controller:latest
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: manager-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: manager-role
subjects:
- kind: ServiceAccount
  name: manager
`

func TestRun_LateRenderFailureDoesNotDeleteExistingChart(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	g.Expect(run(strings.NewReader(deploymentAndRoleBindingManifest), outputDir, "test", "0.1.0")).To(Succeed())

	err := run(strings.NewReader(deploymentWithoutManagerContainerManifest), outputDir, "test", "0.1.0")
	g.Expect(err).To(HaveOccurred())

	entries, err := os.ReadDir(filepath.Join(outputDir, templatesDirName))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(namesOf(entries)).To(ContainElement(ContainSubstring("rolebinding")),
		"a late failure during rendering must not have touched the previous, still-valid templates directory")
}

const deploymentWithConfigMapDataResemblingNamespaceManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
  namespace: system
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: unrelated-configmap
  namespace: system
data:
  embedded.yaml: |
    namespace: external-system
    key: value
`

// A "namespace:" key nested inside metadata.annotations/metadata.labels is
// not metadata.namespace -- only a line at exactly metadata's own child
// indent is the resource's actual namespace field.
const deploymentWithNamespaceLookingAnnotationManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
  namespace: system
  annotations:
    example.com/namespace: keep-me-untouched
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
`

func TestRun_DoesNotRewriteNamespaceLookingAnnotationKeys(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	err := run(strings.NewReader(deploymentWithNamespaceLookingAnnotationManifest), outputDir, "test", "0.1.0")
	g.Expect(err).NotTo(HaveOccurred())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "apps_v1_deployment.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).To(ContainSubstring("example.com/namespace: keep-me-untouched"))
	g.Expect(rendered).To(ContainSubstring("namespace: {{ .Release.Namespace }}"))
}

func TestRun_DoesNotRewriteNamespaceLookingDataValues(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	err := run(strings.NewReader(deploymentWithConfigMapDataResemblingNamespaceManifest), outputDir, "test", "0.1.0")
	g.Expect(err).NotTo(HaveOccurred())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "core_v1_configmap.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).To(ContainSubstring("namespace: external-system"))
	g.Expect(rendered).To(ContainSubstring("namespace: {{ .Release.Namespace }}"))
}

// A manager container can reference a ConfigMap that isn't part of this
// bundle at all (a pre-existing/externally-managed configuration) --
// config/rbac/kustomization.yaml documents the equivalent option for the
// ServiceAccount. Nothing here should be templated against a resource that
// was never actually rendered: no checksum include referencing a
// never-generated core_v1_configmap.yaml, no serviceAccountName rewritten
// to a chart-managed identity nothing creates.
const deploymentWithExternalConfigMapAndServiceAccountManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
  namespace: system
spec:
  replicas: 1
  template:
    spec:
      serviceAccountName: externally-managed-account
      containers:
      - name: manager
        image: controller:latest
        env:
        - name: ODH_MODULE_OPERATOR_CONFIGURATION_PATH
          value: /etc/controller/config
        volumeMounts:
        - name: config
          mountPath: /etc/controller/config
      volumes:
      - name: config
        configMap:
          name: externally-managed-config
`

func TestRun_DoesNotReferenceExternalConfigMapInChecksum(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	err := run(strings.NewReader(deploymentWithExternalConfigMapAndServiceAccountManifest), outputDir, "test", "0.1.0")
	g.Expect(err).NotTo(HaveOccurred())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "apps_v1_deployment.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).NotTo(ContainSubstring("config-checksum"))
	g.Expect(rendered).NotTo(ContainSubstring("core_v1_configmap.yaml"))

	// No ConfigMap resource exists in this input at all, so no
	// core_v1_configmap.yaml template should have been generated either.
	_, err = os.Stat(filepath.Join(outputDir, templatesDirName, "core_v1_configmap.yaml"))
	g.Expect(os.IsNotExist(err)).To(BeTrue())
}

// An externally-managed account's identity (not just its Deployment
// reference) still moves with the rest of the bundle to wherever the chart
// installs -- a RoleBinding subject naming that account must have its
// *namespace* tracked to .Release.Namespace even though its *name* stays
// the real, literal one (there's no chart-managed identity to rename it
// to). Losing the account's identity entirely once it's known to be
// unmanaged (the old behavior) skipped namespace tracking too, not just
// renaming -- leaving the subject pointing at the original, pre-install
// namespace once installed anywhere else.
func TestRun_TracksNamespaceForExternallyManagedServiceAccountSubject(t *testing.T) {
	g := NewWithT(t)

	const externalServiceAccountWithRoleBindingManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
  namespace: system
spec:
  replicas: 1
  template:
    spec:
      serviceAccountName: externally-managed-account
      containers:
      - name: manager
        image: controller:latest
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: example
  namespace: system
subjects:
- kind: ServiceAccount
  name: externally-managed-account
  namespace: system
`
	outputDir := t.TempDir()
	err := run(strings.NewReader(externalServiceAccountWithRoleBindingManifest), outputDir, "test", "0.1.0")
	g.Expect(err).NotTo(HaveOccurred())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "rbac.authorization.k8s.io_v1_rolebinding.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).To(ContainSubstring("name: externally-managed-account"),
		"the subject's real name must survive -- there's no chart-managed identity to rename it to")
	g.Expect(rendered).NotTo(ContainSubstring(".Values.serviceAccount.name"))
	g.Expect(rendered).To(ContainSubstring("namespace: {{ .Release.Namespace }}"),
		"the subject's namespace must still track the release namespace even though its name doesn't change")
}

func TestRun_DoesNotRenameExternallyManagedServiceAccount(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	err := run(strings.NewReader(deploymentWithExternalConfigMapAndServiceAccountManifest), outputDir, "test", "0.1.0")
	g.Expect(err).NotTo(HaveOccurred())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "apps_v1_deployment.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).To(ContainSubstring("serviceAccountName: externally-managed-account"))
	g.Expect(rendered).NotTo(ContainSubstring(".Values.serviceAccount.name"))
}

// A ServiceAccount named the same as the one the Deployment references, but
// sitting in a different namespace, is a different principal entirely --
// name-only existence matching would wrongly treat it as chart-managed and
// rewrite the Deployment/RBAC to a Values-driven identity while the real,
// same-named-but-different-namespace object is left alone, unreferenced.
func TestRun_DoesNotMatchServiceAccountInWrongNamespace(t *testing.T) {
	g := NewWithT(t)

	const crossNamespaceServiceAccountManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
  namespace: system
spec:
  replicas: 1
  template:
    spec:
      serviceAccountName: operator
      containers:
      - name: manager
        image: controller:latest
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: operator
  namespace: some-other-namespace
`
	outputDir := t.TempDir()
	err := run(strings.NewReader(crossNamespaceServiceAccountManifest), outputDir, "test", "0.1.0")
	g.Expect(err).NotTo(HaveOccurred())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "apps_v1_deployment.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).To(ContainSubstring("serviceAccountName: operator"))
	g.Expect(rendered).NotTo(ContainSubstring(".Values.serviceAccount.name"))
}

const deploymentWithTwoConfigMapsManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
        env:
        - name: ODH_MODULE_OPERATOR_CONFIGURATION_PATH
          value: /etc/controller/config
        volumeMounts:
        - name: config
          mountPath: /etc/controller/config
      volumes:
      - name: config
        configMap:
          name: config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: config
data:
  platformType: OpenDataHub
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: unrelated-configmap
data:
  foo: bar
`

// Only the ConfigMap the Deployment actually mounts is "the" operator
// configuration. A second, unrelated ConfigMap in the same input must keep
// its own literal data in the generated chart, not get overwritten with the
// first ConfigMap's Helm-templated data.
func TestRun_OnlyTemplatesOperatorConfigMap(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	g.Expect(run(strings.NewReader(deploymentWithTwoConfigMapsManifest), outputDir, "test", "0.1.0")).To(Succeed())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "core_v1_configmap.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).To(ContainSubstring("platformType: {{"))
	g.Expect(rendered).To(ContainSubstring("foo: bar"))
	g.Expect(rendered).NotTo(ContainSubstring("bar: {{"))
}

const deploymentWithAuxiliaryServiceAccountManifest = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
  namespace: system
spec:
  replicas: 1
  template:
    spec:
      serviceAccountName: operator
      containers:
      - name: manager
        image: controller:latest
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: operator
  namespace: system
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: auxiliary-account
  namespace: system
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/example
`

// Renaming every ServiceAccount to the operator's own identity (the old
// behavior) would collide an auxiliary account with the operator and lose
// its name -- and, separately, that auxiliary account's own real
// annotations must survive rather than being merged into the operator's
// override block or duplicated into a second "annotations:" key.
func TestRun_PreservesAuxiliaryServiceAccountAndItsAnnotations(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	err := run(strings.NewReader(deploymentWithAuxiliaryServiceAccountManifest), outputDir, "test", "0.1.0")
	g.Expect(err).NotTo(HaveOccurred())

	data, err := os.ReadFile(filepath.Join(outputDir, templatesDirName, "core_v1_serviceaccount.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	rendered := string(data)

	g.Expect(rendered).To(ContainSubstring(`name: {{ default (include "chart.fullname" .) .Values.serviceAccount.name }}`))
	g.Expect(rendered).To(ContainSubstring("name: auxiliary-account"))

	docs := strings.Split(rendered, "\n---\n")
	var auxDoc string
	for _, doc := range docs {
		if strings.Contains(doc, "auxiliary-account") {
			auxDoc = doc
		}
	}

	const (
		roleARN        = "eks.amazonaws.com/role-arn: arn:aws:iam::123456789012:role/example"
		notAnOverride  = "auxiliary-account must not receive the operator's annotation-merge templating"
		exactlyOneKey  = "auxiliary-account must have exactly one annotations key:\n%s"
		operatorMerge  = ".Values.serviceAccount.annotations"
		annotationsKey = "annotations:"
	)
	g.Expect(auxDoc).To(ContainSubstring(roleARN))
	g.Expect(strings.Count(auxDoc, annotationsKey)).To(Equal(1), exactlyOneKey, auxDoc)
	g.Expect(auxDoc).NotTo(ContainSubstring(operatorMerge), notAnOverride)
}

// publishPath's backup-then-swap-then-remove-backup sequence must never
// leave its ".chartgen-previous"/".chartgen-staging-*" scratch artifacts
// behind on the ordinary success path -- only outputDir's real files.
func TestRun_LeavesNoPublishingArtifactsBehind(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	g.Expect(run(strings.NewReader(deploymentAndRoleBindingManifest), outputDir, "test", "0.1.0")).To(Succeed())
	g.Expect(run(strings.NewReader(deploymentAndRoleBindingManifest), outputDir, "test", "0.1.0")).To(Succeed())

	entries, err := os.ReadDir(outputDir)
	g.Expect(err).NotTo(HaveOccurred())
	for _, e := range entries {
		g.Expect(e.Name()).NotTo(HavePrefix("."), "leftover staging/backup artifact: %s", e.Name())
	}
}

// Simulates a process killed exactly between publishPath's two renames: the
// destination is gone, but its backup (the previous, valid content) is
// still sitting there. The next publishPath call for that same name must
// restore it before doing anything else -- an earlier version of this
// function cleared the backup unconditionally first, destroying the one
// copy recovery needed.
func TestPublishPath_RecoversFromInterruptedPreviousPublish(t *testing.T) {
	g := NewWithT(t)

	outputDir := t.TempDir()
	backupPath := filepath.Join(outputDir, "values.yaml") + ".chartgen-previous"
	g.Expect(os.WriteFile(backupPath, []byte("old-content"), 0o644)).To(Succeed())
	// dst ("values.yaml") itself does not exist -- exactly the state a kill
	// between the two renames would leave behind.

	stagingDir := t.TempDir()
	g.Expect(os.WriteFile(filepath.Join(stagingDir, "values.yaml"), []byte("new-content"), 0o644)).To(Succeed())

	g.Expect(publishPath(stagingDir, outputDir, "values.yaml")).To(Succeed())

	data, err := os.ReadFile(filepath.Join(outputDir, "values.yaml"))
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(string(data)).To(Equal("new-content"))

	_, err = os.Lstat(filepath.Join(outputDir, "values.yaml") + ".chartgen-previous")
	g.Expect(os.IsNotExist(err)).To(BeTrue(), "backup must be cleaned up after a successful publish")
}

func namesOf(entries []os.DirEntry) []string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}

	return names
}
