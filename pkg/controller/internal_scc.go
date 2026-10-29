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

package controller

import (
	"context"
	"fmt"

	"github.com/opendatahub-io/odh-platform-utilities/framework/cluster"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres/instance"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/resources/gvk"
	securityv1 "github.com/openshift/api/security/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// InternalPostgresSCCName is the shared cluster-scoped SCC for Internal
	// Postgres pods (UID/GID 26). One SCC serves all Internal providers.
	// Install creates it (Helm chart / config/openshift); the reconciler
	// only verifies it exists.
	InternalPostgresSCCName = "odh-db-internal-postgres"

	// InternalPostgresSCCClusterRoleName grants use of InternalPostgresSCCName.
	// Install creates it; the reconciler binds provider SAs via RoleBinding.
	InternalPostgresSCCClusterRoleName = "odh-db-internal-postgres"

	// InternalPostgresSCCOwnedLabelKey marks install-time SCC/ClusterRole
	// objects owned by this module. Used to detect name collisions with
	// unrelated cluster objects that reuse the same name.
	InternalPostgresSCCOwnedLabelKey = "db.infrastructure.opendatahub.io/internal-postgres-scc"

	// InternalPostgresSCCOwnedLabelValue is the expected value for
	// InternalPostgresSCCOwnedLabelKey.
	InternalPostgresSCCOwnedLabelValue = "owned"
)

// internalPostgresSCCOwnedLabels is applied to install-time SCC and
// ClusterRole manifests. Keep in sync with config/openshift/*.yaml.
func internalPostgresSCCOwnedLabels() map[string]string {
	return map[string]string{
		InternalPostgresSCCOwnedLabelKey: InternalPostgresSCCOwnedLabelValue,
		"app.kubernetes.io/name":         InternalPostgresSCCName,
		"app.kubernetes.io/part-of":      "opendatahub-db-operator",
		"app.kubernetes.io/component":    "internal-postgres-scc",
	}
}

// SecurityContextConstraintsAvailable reports whether the OpenShift SCC API is
// present. Kind and vanilla Kubernetes return false.
func SecurityContextConstraintsAvailable(cli client.Client) (bool, error) {
	return cluster.IsAPIAvailable(cli, gvk.SecurityContextConstraints)
}

// DesiredInternalPostgresSCC returns the restricted-v2-shaped SCC limited to
// RHEL postgresql UID/GID 26. Keep in sync with
// config/openshift/internal_postgres_scc.yaml.
func DesiredInternalPostgresSCC() *securityv1.SecurityContextConstraints {
	uid := instance.InternalPostgresUID
	idRange := []securityv1.IDRange{{Min: uid, Max: uid}}
	return &securityv1.SecurityContextConstraints{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gvk.SecurityContextConstraints.GroupVersion().String(),
			Kind:       gvk.SecurityContextConstraints.Kind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   InternalPostgresSCCName,
			Labels: internalPostgresSCCOwnedLabels(),
		},
		AllowPrivilegedContainer: false,
		AllowHostDirVolumePlugin: false,
		AllowHostNetwork:         false,
		AllowHostPorts:           false,
		AllowHostPID:             false,
		AllowHostIPC:             false,
		ReadOnlyRootFilesystem:   false,
		AllowPrivilegeEscalation: new(false),
		RequiredDropCapabilities: []corev1.Capability{
			"ALL",
		},
		AllowedCapabilities:    nil,
		DefaultAddCapabilities: nil,
		Volumes: []securityv1.FSType{
			securityv1.FSTypeConfigMap,
			securityv1.FSTypeDownwardAPI,
			securityv1.FSTypeEmptyDir,
			securityv1.FSTypePersistentVolumeClaim,
			securityv1.FSProjected,
			securityv1.FSTypeSecret,
		},
		SELinuxContext: securityv1.SELinuxContextStrategyOptions{
			Type: securityv1.SELinuxStrategyMustRunAs,
		},
		RunAsUser: securityv1.RunAsUserStrategyOptions{
			Type: securityv1.RunAsUserStrategyMustRunAs,
			UID:  &uid,
		},
		SupplementalGroups: securityv1.SupplementalGroupsStrategyOptions{
			Type:   securityv1.SupplementalGroupsStrategyMustRunAs,
			Ranges: idRange,
		},
		FSGroup: securityv1.FSGroupStrategyOptions{
			Type:   securityv1.FSGroupStrategyMustRunAs,
			Ranges: idRange,
		},
		SeccompProfiles: []string{"runtime/default"},
	}
}

// DesiredInternalPostgresSCCClusterRole grants use of the shared Internal SCC.
// Keep in sync with config/openshift/internal_postgres_scc_clusterrole.yaml.
func DesiredInternalPostgresSCCClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacv1.SchemeGroupVersion.String(),
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   InternalPostgresSCCClusterRoleName,
			Labels: internalPostgresSCCOwnedLabels(),
		},
		Rules: []rbacv1.PolicyRule{{
			APIGroups:     []string{gvk.SecurityContextConstraints.Group},
			Resources:     []string{"securitycontextconstraints"},
			ResourceNames: []string{InternalPostgresSCCName},
			Verbs:         []string{"use"},
		}},
	}
}

// DesiredInternalPostgresSCCRoleBinding binds the Internal Postgres SA to the
// shared SCC-use ClusterRole in the pod's namespace.
func DesiredInternalPostgresSCCRoleBinding(namespace, serviceAccountName, providerName string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: rbacv1.SchemeGroupVersion.String(),
			Kind:       "RoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      InternalSCCRoleBindingName(providerName),
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/name":                    providerName,
				"app.kubernetes.io/component":               "databaseprovider",
				"db.infrastructure.opendatahub.io/provider": providerName,
			},
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     InternalPostgresSCCClusterRoleName,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      serviceAccountName,
			Namespace: namespace,
		}},
	}
}

// EnsureInternalPostgresSCC verifies the install-time SCC exists when the
// OpenShift SCC API is available. The chart (or config/openshift) creates the
// SCC and use ClusterRole; the reconciler must not create them. A same-named
// SCC without the ownership label is treated as a collision and fails clearly.
func EnsureInternalPostgresSCC(ctx context.Context, cli client.Client, available bool) error {
	if !available {
		return nil
	}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(gvk.SecurityContextConstraints)
	err := cli.Get(ctx, client.ObjectKey{Name: InternalPostgresSCCName}, live)
	if apierrors.IsNotFound(err) {
		return fmt.Errorf(
			"SecurityContextConstraints %q not found; install the OpenShift chart resources or apply config/openshift",
			InternalPostgresSCCName,
		)
	}
	if err != nil {
		return fmt.Errorf("getting SecurityContextConstraints %s: %w", InternalPostgresSCCName, err)
	}
	if live.GetLabels()[InternalPostgresSCCOwnedLabelKey] != InternalPostgresSCCOwnedLabelValue {
		return fmt.Errorf(
			"SecurityContextConstraints %q exists but is not owned by opendatahub-db-operator (missing label %s=%s); resolve the name collision before continuing",
			InternalPostgresSCCName,
			InternalPostgresSCCOwnedLabelKey,
			InternalPostgresSCCOwnedLabelValue,
		)
	}
	return nil
}
