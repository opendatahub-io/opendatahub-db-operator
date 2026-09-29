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

package chartgen_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/opendatahub-io/opendatahub-db-operator/cmd/chartgen"
)

// deploymentWithReplicas mirrors what decodeResources produces: a Deployment
// unstructured.Unstructured decoded via sigs.k8s.io/yaml, which round-trips
// through encoding/json and so represents numbers as float64, not int64.
func deploymentWithReplicas(t *testing.T, replicas int) unstructured.Unstructured {
	t.Helper()

	manifest := `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: ` + strconv.Itoa(replicas) + `
  template:
    spec:
      containers:
      - name: manager
        image: controller:latest
`
	var obj unstructured.Unstructured
	if err := yaml.Unmarshal([]byte(manifest), &obj.Object); err != nil {
		t.Fatalf("unmarshaling test manifest: %v", err)
	}

	return obj
}

func TestExtractDefaults_PreservesNonDefaultReplicaCount(t *testing.T) {
	g := NewWithT(t)

	deployment := deploymentWithReplicas(t, 3)
	values, err := chartgen.ExtractDefaults(&deployment, []unstructured.Unstructured{deployment})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values.Operator.Replicas).To(Equal(int32(3)))
}

func TestExtractDefaults_DefaultReplicaCount(t *testing.T) {
	g := NewWithT(t)

	deployment := deploymentWithReplicas(t, 1)
	values, err := chartgen.ExtractDefaults(&deployment, []unstructured.Unstructured{deployment})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values.Operator.Replicas).To(Equal(int32(1)))
}

func configMapWithData(t *testing.T, data map[string]string) unstructured.Unstructured {
	t.Helper()

	var obj unstructured.Unstructured
	obj.SetGroupVersionKind(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"})
	obj.SetName("config")
	untyped := make(map[string]any, len(data))
	for k, v := range data {
		untyped[k] = v
	}
	g := NewWithT(t)
	g.Expect(unstructured.SetNestedMap(obj.Object, untyped, "data")).To(Succeed())

	return obj
}

// deploymentWithConfigVolume builds a manager Deployment that resolves its
// operator ConfigMap to configMapName the same way OperatorConfigMapName
// requires: an env var naming the mount path, and a volumeMount/volume pair
// at that same path.
func deploymentWithConfigVolume(t *testing.T, configMapName string) unstructured.Unstructured {
	t.Helper()

	return unstructuredFromYAMLForValuesTest(t, `
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
          name: `+configMapName+`
`)
}

// chart.go's injectConfigMapValues rewrites this ConfigMap's rendered "data:"
// block entirely from .Values.platform.*/.Values.config, discarding whatever
// literal keys the kustomize input actually had. ExtractDefaults must read
// those keys first so they survive as the generated chart's new defaults.
func TestExtractDefaults_PreservesConfigMapData(t *testing.T) {
	g := NewWithT(t)

	cm := configMapWithData(t, map[string]string{
		"platformType":         "SelfManagedRhoai",
		"platformVersion":      "3.5.0",
		"controller.zap.level": "debug",
	})
	deployment := deploymentWithConfigVolume(t, "config")

	values, err := chartgen.ExtractDefaults(&deployment, []unstructured.Unstructured{deployment, cm})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values.Platform.Type).To(Equal("SelfManagedRhoai"))
	g.Expect(values.Platform.Version).To(Equal("3.5.0"))
	g.Expect(values.Config).To(HaveKeyWithValue("controller.zap.level", "debug"))
}

// ExtractDefaults reads image/pullPolicy/resources from containers[0] would
// leak a sidecar's image into values.yaml's defaults if the sidecar happens
// to be listed before the manager container -- exactly the shape
// TestTransformDeployment_OnlyTemplatesManagerContainerImage already uses to
// prove the render side is correct; this proves the defaults-extraction
// side agrees with it.
func TestExtractDefaults_IgnoresSidecarListedFirst(t *testing.T) {
	g := NewWithT(t)

	deployment := unstructuredFromYAMLForValuesTest(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: proxy
        image: quay.io/example/sidecar:v1
      - name: manager
        image: controller:latest
        imagePullPolicy: IfNotPresent
`)

	values, err := chartgen.ExtractDefaults(&deployment, []unstructured.Unstructured{deployment})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values.Operator.Image.Ref).To(Equal("controller:latest"))
	g.Expect(values.Operator.Image.PullPolicy).To(Equal("IfNotPresent"))
}

func unstructuredFromYAMLForValuesTest(t *testing.T, manifest string) unstructured.Unstructured {
	t.Helper()

	var obj unstructured.Unstructured
	if err := yaml.Unmarshal([]byte(manifest), &obj.Object); err != nil {
		t.Fatalf("unmarshaling test manifest: %v", err)
	}

	return obj
}

// A sidecar's own ConfigMap volume listed before the manager's real
// configuration volume must not be mistaken for it -- OperatorConfigMapName
// has to resolve through the manager container's own volumeMounts, not just
// return the first ConfigMap-backed volume the pod spec happens to declare.
func TestOperatorConfigMapName_IgnoresSidecarVolumeListedFirst(t *testing.T) {
	g := NewWithT(t)

	deployment := unstructuredFromYAMLForValuesTest(t, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: manager
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: proxy
        image: quay.io/example/sidecar:v1
        volumeMounts:
        - name: sidecar-config
          mountPath: /etc/sidecar
      - name: manager
        image: controller:latest
        env:
        - name: ODH_MODULE_OPERATOR_CONFIGURATION_PATH
          value: /etc/controller/config
        volumeMounts:
        - name: config
          mountPath: /etc/controller/config
      volumes:
      - name: sidecar-config
        configMap:
          name: sidecar-config-map
      - name: config
        configMap:
          name: operator-config-map
`)

	name, err := chartgen.OperatorConfigMapName(&deployment)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(name).To(Equal("operator-config-map"))
}

// Numeric memory (memory: 536870912, decoded as float64 by
// sigs.k8s.io/yaml, not a string) and resource names other than cpu/memory
// (ephemeral-storage, a vendor device plugin name) used to be silently
// dropped -- ResourceList was a fixed cpu/memory-string struct, and only
// string-typed memory was ever read at all.
func TestExtractDefaults_PreservesNumericMemoryAndOtherResourceNames(t *testing.T) {
	g := NewWithT(t)

	deployment := unstructuredFromYAMLForValuesTest(t, `
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
        resources:
          limits:
            memory: 536870912
            ephemeral-storage: 1Gi
            nvidia.com/gpu: 1
          requests:
            cpu: 0.5
`)

	values, err := chartgen.ExtractDefaults(&deployment, []unstructured.Unstructured{deployment})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values.Operator.Resources.Limits).To(HaveKeyWithValue("memory", "536870912"))
	g.Expect(values.Operator.Resources.Limits).To(HaveKeyWithValue("ephemeral-storage", "1Gi"))
	g.Expect(values.Operator.Resources.Limits).To(HaveKeyWithValue("nvidia.com/gpu", "1"))
	g.Expect(values.Operator.Resources.Requests).To(HaveKeyWithValue("cpu", "500m"))
}

// A manager container mounting both a CA bundle and its own configuration
// must resolve to the configuration ConfigMap specifically -- via the exact
// path ODH_MODULE_OPERATOR_CONFIGURATION_PATH names -- not whichever
// ConfigMap-backed volume mount happens to be listed first on the manager
// container itself.
func TestOperatorConfigMapName_ResolvesByConfigurationPathNotMountOrder(t *testing.T) {
	g := NewWithT(t)

	deployment := unstructuredFromYAMLForValuesTest(t, `
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
        - name: ca-bundle
          mountPath: /etc/ssl/ca-bundle
        - name: config
          mountPath: /etc/controller/config
      volumes:
      - name: ca-bundle
        configMap:
          name: cluster-ca-bundle
      - name: config
        configMap:
          name: operator-config-map
`)

	name, err := chartgen.OperatorConfigMapName(&deployment)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(name).To(Equal("operator-config-map"))
}

// Two ConfigMaps sharing the operator's configuration name but living in
// different namespaces must not be conflated -- only the one in the
// Deployment's own namespace (where a ConfigMap volume always resolves) may
// feed Values.Platform/Values.Config. Matching by name alone would merge
// both (in whatever order resources happens to list them), letting an
// unrelated, same-named ConfigMap in another namespace silently override or
// blend into the real operator's defaults.
func TestExtractDefaults_IgnoresSameNameConfigMapInDifferentNamespace(t *testing.T) {
	g := NewWithT(t)

	deployment := deploymentWithConfigVolume(t, "config")
	deployment.SetNamespace("system")

	correctNamespace := configMapWithData(t, map[string]string{"controller.zap.level": "debug"})
	correctNamespace.SetNamespace("system")

	wrongNamespace := configMapWithData(t, map[string]string{"controller.zap.level": "info"})
	wrongNamespace.SetNamespace("some-other-namespace")

	allResources := []unstructured.Unstructured{deployment, wrongNamespace, correctNamespace}
	values, err := chartgen.ExtractDefaults(&deployment, allResources)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values.Config).To(HaveKeyWithValue("controller.zap.level", "debug"))
}

// When no operator ConfigMap can be identified at all (no configuration
// path env var, or nothing mounted at it), ExtractDefaults must extract
// nothing from any ConfigMap in the input -- not merge every ConfigMap's
// data in, which an unqualified "operatorConfigMapName != \"\" && ..." guard
// used to do as an accidental side effect of short-circuiting to true when
// the name was empty.
func TestExtractDefaults_ExtractsNothingWhenNoOperatorConfigMapIdentified(t *testing.T) {
	g := NewWithT(t)

	deployment := unstructuredFromYAMLForValuesTest(t, `
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
`)
	unrelated := configMapWithData(t, map[string]string{"controller.zap.level": "debug"})

	values, err := chartgen.ExtractDefaults(&deployment, []unstructured.Unstructured{deployment, unrelated})
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(values.Config).NotTo(HaveKey("controller.zap.level"))
}

// The generated helpers reference .Values.nameOverride/.Values.fullnameOverride
// (standard Helm chart convention). If Values doesn't declare them, the
// default JSON-schema reflector forbids additionalProperties and Helm
// rejects any chart install/template invocation that sets either override.
func TestWriteValuesSchema_AllowsNameOverrides(t *testing.T) {
	g := NewWithT(t)

	path := filepath.Join(t.TempDir(), "values.schema.json")
	g.Expect(chartgen.WriteValuesSchema(path)).To(Succeed())

	data, err := os.ReadFile(path)
	g.Expect(err).NotTo(HaveOccurred())

	schemaJSON := string(data)
	g.Expect(schemaJSON).To(ContainSubstring("nameOverride"))
	g.Expect(schemaJSON).To(ContainSubstring("fullnameOverride"))
}
