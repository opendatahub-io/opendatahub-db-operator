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
	"encoding/json"
	"fmt"
	"math"
	"os"

	"github.com/invopop/jsonschema"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/resources/gvk"
)

const (
	defaultImageRef        = "controller:latest"
	defaultImagePullPolicy = "Always"
	defaultLimitsCPU       = "500m"
	defaultLimitsMemory    = "128Mi"
	defaultRequestsCPU     = "10m"
	defaultRequestsMemory  = "64Mi"

	resourceKeyLimits   = "limits"
	resourceKeyRequests = "requests"
	resourceKeyCPU      = "cpu"
	resourceKeyMemory   = "memory"

	configMapKeyPlatformType    = "platformType"
	configMapKeyPlatformVersion = "platformVersion"
)

// Values defines the Helm chart values structure.
type Values struct {
	// NameOverride overrides the chart name used by the "name" template helper.
	NameOverride string `json:"nameOverride,omitempty"`

	// FullnameOverride overrides the release fullname used by the "fullname" template helper.
	FullnameOverride string `json:"fullnameOverride,omitempty"`

	// Operator configures the operator Deployment itself.
	Operator OperatorSpec `json:"operator"`

	// ServiceAccount configures the chart-managed ServiceAccount.
	ServiceAccount ServiceAccountSpec `json:"serviceAccount"`

	// ImagePullSecrets configures pod image pull secrets.
	ImagePullSecrets []ImagePullSecretRef `json:"imagePullSecrets"`

	// Platform values are written explicitly into the controller ConfigMap.
	Platform PlatformSpec `json:"platform"`

	// Config provides additional controller configuration entries that are
	// merged into the controller ConfigMap.
	Config map[string]string `json:"config"`
}

// OperatorSpec configures the operator Deployment.
type OperatorSpec struct {
	// Image configures the container image for the operator.
	Image ImageSpec `json:"image"`

	// Replicas is the number of operator pod replicas.
	Replicas int32 `json:"replicas" jsonschema:"default=1,minimum=1"`

	// Resources configures CPU and memory requests/limits for the operator.
	Resources ResourceSpec `json:"resources"`
}

// ImageSpec describes a container image.
type ImageSpec struct {
	Ref        string `json:"ref"`
	PullPolicy string `json:"pullPolicy" jsonschema:"enum=Always,enum=IfNotPresent,enum=Never"`
}

// PlatformSpec contains explicit platform handshake values written to the ConfigMap.
type PlatformSpec struct {
	Type    string `json:"type" jsonschema:"default=OpenDataHub"`
	Version string `json:"version" jsonschema:"default="`
}

// ResourceSpec mirrors corev1.ResourceRequirements but with simpler
// serialization for Helm values. Limits/Requests are a generic resource-name
// -> quantity-string map (matching corev1.ResourceList's own shape) rather
// than fixed CPU/Memory fields, so an ephemeral-storage limit, a
// nvidia.com/gpu request, or any other resource name kustomize's input
// declares survives chart generation instead of being silently dropped.
type ResourceSpec struct {
	Limits   map[string]string `json:"limits,omitempty"`
	Requests map[string]string `json:"requests,omitempty"`
}

// ServiceAccountSpec configures the operator's ServiceAccount.
type ServiceAccountSpec struct {
	// Name overrides the ServiceAccount name (defaults to release fullname).
	Name string `json:"name,omitempty"`

	// Annotations are additional annotations on the ServiceAccount.
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ImagePullSecretRef matches the pod imagePullSecrets item shape.
type ImagePullSecretRef struct {
	Name string `json:"name"`
}

// DefaultValues returns a Values instance with sensible defaults.
func DefaultValues() Values {
	return Values{
		Operator: OperatorSpec{
			Image: ImageSpec{
				Ref:        defaultImageRef,
				PullPolicy: defaultImagePullPolicy,
			},
			Replicas: 1,
			Resources: ResourceSpec{
				Limits: map[string]string{
					resourceKeyCPU:    defaultLimitsCPU,
					resourceKeyMemory: defaultLimitsMemory,
				},
				Requests: map[string]string{
					resourceKeyCPU:    defaultRequestsCPU,
					resourceKeyMemory: defaultRequestsMemory,
				},
			},
		},
		ServiceAccount:   ServiceAccountSpec{},
		ImagePullSecrets: []ImagePullSecretRef{},
		Platform: PlatformSpec{
			Type:    "OpenDataHub",
			Version: "",
		},
		Config: map[string]string{},
	}
}

// nestedInt64 reads an integer field that may have been decoded as either
// int64 or float64 -- sigs.k8s.io/yaml.Unmarshal round-trips YAML through
// encoding/json, whose numbers always land in an interface{} as float64, so
// unstructured.NestedInt64 alone would never find a Deployment's "replicas".
func nestedInt64(obj map[string]any, fields ...string) (int64, bool, error) {
	val, found, err := unstructured.NestedFieldNoCopy(obj, fields...)
	if err != nil || !found {
		return 0, found, err
	}

	switch v := val.(type) {
	case int64:
		return v, true, nil
	case float64:
		return int64(v), true, nil
	default:
		return 0, true, fmt.Errorf("field %v: unexpected type %T for integer value", fields, val)
	}
}

// findOperatorDeployment identifies the single Deployment resource that
// this chart is actually about -- the one containing a container named
// managerContainerName -- and returns it. Every other per-Deployment
// resolution (which ConfigMap it reads config from, which ServiceAccount it
// runs as, what its default image/resources/replicas are) must be derived
// from this exact same object, not from an independent scan of resources
// that could disagree about which Deployment "the operator" even is. A
// bundle with more than one manager-containing Deployment is rejected as
// ambiguous rather than silently picking one; a bundle with none is
// rejected the same way containsDeployment's absence check used to be.
func findOperatorDeployment(resources []unstructured.Unstructured) (*unstructured.Unstructured, error) {
	var found *unstructured.Unstructured

	for i := range resources {
		r := &resources[i]
		if r.GroupVersionKind() != gvk.Deployment {
			continue
		}

		containers, ok, err := unstructured.NestedSlice(r.Object, "spec", "template", "spec", "containers")
		if err != nil {
			return nil, fmt.Errorf("reading deployment %s containers: %w", r.GetName(), err)
		}
		if !ok {
			continue
		}
		if _, ok := findManagerContainer(containers); !ok {
			continue
		}

		if found != nil {
			return nil, fmt.Errorf(
				"multiple Deployments contain a %q container (%s and %s) -- can't identify a single operator Deployment",
				managerContainerName, found.GetName(), r.GetName(),
			)
		}
		found = r
	}

	if found == nil {
		return nil, fmt.Errorf("no Deployment contains a %q container", managerContainerName)
	}

	return found, nil
}

// OperatorConfigMapName returns the name of the ConfigMap backing the
// volume mounted at exactly the path the manager container's own
// moduleconfig.ConfigPathEnvVar points at -- i.e. the one specific
// ConfigMap this operator actually reads its own configuration from at
// startup, resolved the same way the running operator itself resolves it.
// Matching by "any ConfigMap-backed volume the manager container happens to
// mount" isn't precise enough: a manager mounting both a CA bundle and its
// own config would pick whichever volume happens to be listed first, which
// may well be the CA bundle. Returns "" if the manager container doesn't
// set that env var, or mounts nothing at the path it names.
func OperatorConfigMapName(deployment *unstructured.Unstructured) (string, error) {
	containers, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if err != nil {
		return "", fmt.Errorf("reading deployment %s containers: %w", deployment.GetName(), err)
	}
	if !found {
		return "", nil
	}

	managerContainer, ok := findManagerContainer(containers)
	if !ok {
		return "", nil
	}

	configPath := containerEnvValue(managerContainer, moduleconfig.ConfigPathEnvVar)
	if configPath == "" {
		return "", nil
	}

	volumeMounts, _ := managerContainer["volumeMounts"].([]any)
	var configVolumeName string
	for _, vm := range volumeMounts {
		mount, ok := vm.(map[string]any)
		if !ok {
			continue
		}
		if mountPath, _ := mount["mountPath"].(string); mountPath == configPath {
			configVolumeName, _ = mount["name"].(string)

			break
		}
	}
	if configVolumeName == "" {
		return "", nil
	}

	volumes, found, err := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "volumes")
	if err != nil {
		return "", fmt.Errorf("reading deployment %s volumes: %w", deployment.GetName(), err)
	}
	if !found {
		return "", nil
	}

	for _, v := range volumes {
		vol, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if volName, _ := vol["name"].(string); volName != configVolumeName {
			continue
		}
		cm, ok := vol["configMap"].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := cm["name"].(string); ok && name != "" {
			return name, nil
		}
	}

	return "", nil
}

// containerEnvValue returns the literal value of the named env entry in a
// decoded container map, or "" if it's unset or not a literal string value
// (e.g. a valueFrom reference, which can't be resolved statically here).
func containerEnvValue(container map[string]any, name string) string {
	env, _ := container["env"].([]any)
	for _, e := range env {
		entry, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if entryName, _ := entry["name"].(string); entryName == name {
			value, _ := entry["value"].(string)

			return value
		}
	}

	return ""
}

// OperatorServiceAccountRef returns the name and namespace of the
// ServiceAccount the operator's own Deployment actually runs as
// (spec.template.spec.serviceAccountName, in the Deployment's own
// namespace) -- the full identity transformRoleBinding treats as "this
// operator" when deciding which RoleBinding/ClusterRoleBinding subject to
// template. Matching by name alone would treat a same-named ServiceAccount
// in a completely different namespace as the operator too. Returns ""/""
// if the Deployment doesn't set serviceAccountName.
func OperatorServiceAccountRef(deployment *unstructured.Unstructured) (name string, namespace string, err error) {
	saPath := []string{"spec", "template", "spec", "serviceAccountName"}
	saName, found, nestedErr := unstructured.NestedString(deployment.Object, saPath...)
	if nestedErr != nil {
		return "", "", fmt.Errorf("reading deployment %s serviceAccountName: %w", deployment.GetName(), nestedErr)
	}
	if !found {
		return "", "", nil
	}

	return saName, deployment.GetNamespace(), nil
}

// ExtractDefaults extracts default values from the kustomize resources,
// primarily from the operator Deployment resolved by findOperatorDeployment
// -- never from independently re-scanning resources for "a" Deployment,
// which could disagree with which one rendering itself treats as the
// operator when more than one Deployment is present.
func ExtractDefaults(deployment *unstructured.Unstructured, resources []unstructured.Unstructured) (Values, error) {
	values := DefaultValues()

	operatorConfigMapName, err := OperatorConfigMapName(deployment)
	if err != nil {
		return Values{}, err
	}

	// Extract image, pull policy, and resources from the manager container
	// specifically -- not containers[0], which could be a sidecar the pod
	// happens to list first. transformDeployment templates the manager
	// container's image/resources the same way; defaults extraction must
	// agree on which container that is, or the generated values.yaml's
	// defaults would describe the sidecar while the rendered template
	// controls the manager.
	containers, found, _ := unstructured.NestedSlice(deployment.Object, "spec", "template", "spec", "containers")
	if found {
		if c, ok := findManagerContainer(containers); ok {
			if img, exists := c["image"].(string); exists {
				values.Operator.Image.Ref = img
			}

			if policy, exists := c["imagePullPolicy"].(string); exists {
				values.Operator.Image.PullPolicy = policy
			}

			if res, exists := c["resources"].(map[string]any); exists {
				values.Operator.Resources = extractResources(res)
			}
		}
	}

	// Extract replicas
	replicas, found, err := nestedInt64(deployment.Object, "spec", "replicas")
	if err != nil {
		return Values{}, fmt.Errorf("reading spec.replicas: %w", err)
	}
	if found {
		values.Operator.Replicas = int32(replicas)
	}

	for _, r := range resources {
		if r.GroupVersionKind() != gvk.ConfigMap {
			continue
		}
		// Match by name AND namespace (a ConfigMap volume always resolves
		// in the pod's own namespace, so that's the correct comparison --
		// not just any resource sharing the name), and match nothing at all
		// when operatorConfigMapName couldn't be resolved: an unqualified
		// "operatorConfigMapName != "" && ..." guard (the previous version
		// of this check) short-circuits to true when the name is empty,
		// which extracted and merged data from every ConfigMap in the
		// input, in file order, whenever there wasn't a single identifiable
		// operator ConfigMap to begin with.
		if r.GetName() != operatorConfigMapName || r.GetNamespace() != deployment.GetNamespace() {
			// Not the ConfigMap mounted into the operator's own container --
			// leave it out of Values entirely, same as transformResource
			// leaves its rendered template untouched.
			continue
		}

		// injectConfigMapValues (chart.go) replaces this ConfigMap's entire
		// rendered "data:" block with .Values.platform.*/.Values.config
		// templating, discarding whatever literal keys kustomize supplied
		// here (e.g. a platformType override, or a controller.zap.level
		// patch). Read those keys into Values now so they survive as the
		// generated chart's new defaults instead of silently reverting to
		// DefaultValues()'s compiled-in platform type/version and an empty
		// config map.
		data, found, err := unstructured.NestedStringMap(r.Object, "data")
		if err != nil {
			return Values{}, fmt.Errorf("reading ConfigMap %s data: %w", r.GetName(), err)
		}
		if !found {
			continue
		}

		for key, val := range data {
			switch key {
			case configMapKeyPlatformType:
				values.Platform.Type = val
			case configMapKeyPlatformVersion:
				values.Platform.Version = val
			default:
				values.Config[key] = val
			}
		}
	}

	return values, nil
}

func extractResources(res map[string]any) ResourceSpec {
	spec := ResourceSpec{}

	if limits, ok := res[resourceKeyLimits].(map[string]any); ok {
		spec.Limits = extractResourceList(limits)
	}
	if requests, ok := res[resourceKeyRequests].(map[string]any); ok {
		spec.Requests = extractResourceList(requests)
	}

	return spec
}

// extractResourceList converts a decoded resources.limits/requests map into
// resource-name -> quantity-string pairs, preserving every resource name
// present (cpu, memory, ephemeral-storage, a vendor device plugin name like
// nvidia.com/gpu, ...) rather than only ones this package happens to know
// the name of.
func extractResourceList(m map[string]any) map[string]string {
	if len(m) == 0 {
		return nil
	}

	out := make(map[string]string, len(m))
	for name, val := range m {
		q, ok := quantityString(val)
		if !ok {
			continue
		}
		out[name] = q
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// quantityString renders a decoded resource quantity value back to its
// string form. sigs.k8s.io/yaml.Unmarshal always decodes YAML numbers into
// an interface{} as float64 (it round-trips through encoding/json), so a
// value written as `memory: 536870912` in the source manifest -- valid,
// common input -- arrives here as a float64, not a string; treating only
// the string case as supported (the previous behavior) silently drops it.
// Whole numbers render as plain quantities (536870912, not
// 536870912000m -- milli-scaling only makes sense for a fractional value
// like a CPU request of 0.5).
func quantityString(val any) (string, bool) {
	switch v := val.(type) {
	case string:
		return v, true
	case float64:
		if v == math.Trunc(v) {
			return resource.NewQuantity(int64(v), resource.DecimalSI).String(), true
		}

		return resource.NewMilliQuantity(int64(v*1000), resource.DecimalSI).String(), true
	default:
		return "", false
	}
}

// WriteValuesYAML writes the values to a YAML file.
func WriteValuesYAML(v Values, path string) error {
	data, err := MarshalValuesYAML(v)
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}

// MarshalValuesYAML renders v as it would be written to values.yaml,
// without touching disk -- so callers (run(), in particular) can validate
// every piece of a chart generation succeeds before writing any of it.
func MarshalValuesYAML(v Values) ([]byte, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshaling values: %w", err)
	}

	return data, nil
}

// WriteValuesSchema generates a JSON Schema from the Values struct and
// writes it to the given path.
func WriteValuesSchema(path string) error {
	data, err := MarshalValuesSchema()
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}

// MarshalValuesSchema renders the values.schema.json content without
// touching disk -- see MarshalValuesYAML.
func MarshalValuesSchema() ([]byte, error) {
	reflector := &jsonschema.Reflector{}
	schema := reflector.Reflect(&Values{})

	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling schema: %w", err)
	}

	return data, nil
}
