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
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres/instance"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/resources/gvk"
	securityv1 "github.com/openshift/api/security/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDesiredInternalPostgresSCC_UID26Contract(t *testing.T) {
	g := NewWithT(t)
	scc := DesiredInternalPostgresSCC()

	g.Expect(scc.Name).To(Equal(InternalPostgresSCCName))
	g.Expect(scc.AllowPrivilegedContainer).To(BeFalse())
	g.Expect(scc.AllowHostDirVolumePlugin).To(BeFalse())
	g.Expect(scc.AllowPrivilegeEscalation).NotTo(BeNil())
	g.Expect(*scc.AllowPrivilegeEscalation).To(BeFalse())
	g.Expect(scc.RequiredDropCapabilities).To(Equal([]corev1.Capability{"ALL"}))
	g.Expect(scc.SeccompProfiles).To(Equal([]string{"runtime/default"}))

	g.Expect(scc.RunAsUser.Type).To(Equal(securityv1.RunAsUserStrategyMustRunAs))
	g.Expect(scc.RunAsUser.UID).NotTo(BeNil())
	g.Expect(*scc.RunAsUser.UID).To(Equal(instance.InternalPostgresUID))

	g.Expect(scc.FSGroup.Type).To(Equal(securityv1.FSGroupStrategyMustRunAs))
	g.Expect(scc.FSGroup.Ranges).To(Equal([]securityv1.IDRange{{
		Min: instance.InternalPostgresUID,
		Max: instance.InternalPostgresUID,
	}}))
	g.Expect(scc.SupplementalGroups.Type).To(Equal(securityv1.SupplementalGroupsStrategyMustRunAs))
	g.Expect(scc.SupplementalGroups.Ranges).To(Equal(scc.FSGroup.Ranges))

	g.Expect(scc.Volumes).To(ContainElements(
		securityv1.FSTypePersistentVolumeClaim,
		securityv1.FSTypeSecret,
		securityv1.FSTypeConfigMap,
	))
	g.Expect(scc.Volumes).NotTo(ContainElement(securityv1.FSTypeHostPath))
}

func TestDesiredInternalPostgresSCCClusterRole_UseOnlyNamedSCC(t *testing.T) {
	g := NewWithT(t)
	role := DesiredInternalPostgresSCCClusterRole()

	g.Expect(role.Name).To(Equal(InternalPostgresSCCClusterRoleName))
	g.Expect(role.Rules).To(HaveLen(1))
	g.Expect(role.Rules[0].Verbs).To(Equal([]string{"use"}))
	g.Expect(role.Rules[0].ResourceNames).To(Equal([]string{InternalPostgresSCCName}))
	g.Expect(role.Rules[0].Resources).To(Equal([]string{"securitycontextconstraints"}))
}

func TestDesiredInternalPostgresSCCRoleBinding_BindsSAInInternalNamespace(t *testing.T) {
	g := NewWithT(t)
	binding := DesiredInternalPostgresSCCRoleBinding("db-ns", "rhai-db-postgres", "rhai-db")

	g.Expect(binding.Namespace).To(Equal("db-ns"))
	g.Expect(binding.Name).To(Equal("rhai-db-postgres-scc"))
	g.Expect(binding.RoleRef.Kind).To(Equal("ClusterRole"))
	g.Expect(binding.RoleRef.Name).To(Equal(InternalPostgresSCCClusterRoleName))
	g.Expect(binding.Subjects).To(Equal([]rbacv1.Subject{{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      "rhai-db-postgres",
		Namespace: "db-ns",
	}}))
}

func TestInternalServiceAccountName(t *testing.T) {
	g := NewWithT(t)
	g.Expect(InternalServiceAccountName("rhai-db")).To(Equal("rhai-db-postgres"))
	g.Expect(InternalSCCRoleBindingName("rhai-db")).To(Equal("rhai-db-postgres-scc"))
}

func TestMaxInternalProviderNameLength_FitsDerivedRoleBinding(t *testing.T) {
	g := NewWithT(t)
	g.Expect(MaxInternalProviderNameLength).To(Equal(50))
	g.Expect(MaxInternalProviderNameLength + len("-postgres-scc")).To(Equal(63))
	name := strings.Repeat("a", MaxInternalProviderNameLength)
	g.Expect(InternalSCCRoleBindingName(name)).To(HaveLen(63))
	g.Expect(InternalServiceAccountName(name)).To(HaveLen(MaxInternalProviderNameLength + len("-postgres")))
}

func TestEnsureInternalPostgresSCC_DoesNotCreateWhenMissing(t *testing.T) {
	g := NewWithT(t)
	cli := fake.NewClientBuilder().Build()

	err := EnsureInternalPostgresSCC(context.Background(), cli, true)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("not found"))

	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(gvk.SecurityContextConstraints.GroupVersion().WithKind("SecurityContextConstraintsList"))
	g.Expect(cli.List(context.Background(), list)).To(Succeed())
	g.Expect(list.Items).To(BeEmpty())
}

func TestEnsureInternalPostgresSCC_RejectsUnownedNameCollision(t *testing.T) {
	g := NewWithT(t)
	cli := fake.NewClientBuilder().Build()

	collision := &unstructured.Unstructured{}
	collision.SetGroupVersionKind(gvk.SecurityContextConstraints)
	collision.SetName(InternalPostgresSCCName)
	g.Expect(cli.Create(context.Background(), collision)).To(Succeed())

	err := EnsureInternalPostgresSCC(context.Background(), cli, true)
	g.Expect(err).To(HaveOccurred())
	g.Expect(err.Error()).To(ContainSubstring("not owned"))
	g.Expect(err.Error()).To(ContainSubstring(InternalPostgresSCCOwnedLabelKey))
}
