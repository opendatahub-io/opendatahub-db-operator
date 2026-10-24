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

package controller_test

import (
	"strings"
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
)

func TestRoleNameFor_CollisionResistant(t *testing.T) {
	g := NewWithT(t)

	a := controller.RoleNameFor("ns", "my-db")
	b := controller.RoleNameFor("ns", "my.db")
	g.Expect(a).NotTo(Equal(b))
	g.Expect(len(a)).To(BeNumerically("<=", 63))
	g.Expect(len(b)).To(BeNumerically("<=", 63))
	g.Expect(a).To(Equal(controller.RoleNameFor("ns", "my-db")))
}

func TestRoleNameFor_TruncatesLongNames(t *testing.T) {
	g := NewWithT(t)

	long := strings.Repeat("a", 80)
	role := controller.RoleNameFor("ns", long)
	g.Expect(len(role)).To(BeNumerically("<=", 63))
	g.Expect(role).To(MatchRegexp(`_[0-9a-f]{8}$`))
}

func TestRoleNameForSchemaClaim_DistinctFromDatabaseClaim(t *testing.T) {
	g := NewWithT(t)

	dbRole := controller.RoleNameFor("app-ns", "claim-a")
	scRole := controller.RoleNameForSchemaClaim("app-ns", "claim-a")
	g.Expect(scRole).NotTo(Equal(dbRole))
	g.Expect(scRole).To(HavePrefix("sc_"))
	g.Expect(len(scRole)).To(BeNumerically("<=", 63))
	g.Expect(scRole).To(Equal(controller.RoleNameForSchemaClaim("app-ns", "claim-a")))
}

func TestSecretNameForSchemaClaim_DistinctFromDatabaseClaim(t *testing.T) {
	g := NewWithT(t)

	sc := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "app"},
	}
	dc := &infraApi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "foo", Namespace: "app"},
	}

	g.Expect(controller.SecretNameForSchemaClaim(sc)).To(Equal("sc-foo"))
	g.Expect(controller.SecretNameForDatabaseClaim(dc)).To(Equal("foo"))
	g.Expect(controller.SecretNameForSchemaClaim(sc)).
		NotTo(Equal(controller.SecretNameForDatabaseClaim(dc)))
}

func TestSecretNameForSchemaClaim_FitsKubernetesNameLimit(t *testing.T) {
	g := NewWithT(t)

	// sc- (3) + 250 = 253: still fits without hashing.
	exact := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 250)},
	}
	exactName := controller.SecretNameForSchemaClaim(exact)
	g.Expect(exactName).To(Equal("sc-" + strings.Repeat("a", 250)))
	g.Expect(exactName).To(HaveLen(253))

	// sc- + 251 would be 254; must shorten and stay unique/stable.
	over := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 251)},
	}
	overName := controller.SecretNameForSchemaClaim(over)
	g.Expect(len(overName)).To(BeNumerically("<=", 253))
	g.Expect(overName).To(HavePrefix("sc-"))
	g.Expect(overName).To(MatchRegexp(`-[0-9a-f]{8}$`))
	g.Expect(overName).To(Equal(controller.SecretNameForSchemaClaim(over)))

	other := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: strings.Repeat("a", 250) + "b"},
	}
	g.Expect(controller.SecretNameForSchemaClaim(other)).NotTo(Equal(overName))
	g.Expect(len(controller.SecretNameForSchemaClaim(other))).To(BeNumerically("<=", 253))
}

func TestSchemaNameFor_UsesSpecOverride(t *testing.T) {
	g := NewWithT(t)

	claim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "claim-a"},
		Spec:       infraApi.SchemaClaimSpec{Schema: "explicit_schema"},
	}
	g.Expect(controller.SchemaNameFor(claim)).To(Equal("explicit_schema"))
}

func TestSchemaNameFor_DefaultIsCollisionResistant(t *testing.T) {
	g := NewWithT(t)

	claim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: "app-ns", Name: "claim-a"},
	}
	schema := controller.SchemaNameFor(claim)
	g.Expect(schema).To(HavePrefix("app_ns_claim_a_"))
	g.Expect(schema).To(MatchRegexp(`_[0-9a-f]{8}$`))
	g.Expect(len(schema)).To(BeNumerically("<=", 63))
	g.Expect(schema).To(Equal(controller.SchemaNameFor(claim)))

	// Sanitized forms collide; unsanitized identity must still diverge.
	abC := &infraApi.SchemaClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "a-b", Name: "c"}}
	aBC := &infraApi.SchemaClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "b-c"}}
	aBDotC := &infraApi.SchemaClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "b.c"}}
	g.Expect(controller.SchemaNameFor(abC)).NotTo(Equal(controller.SchemaNameFor(aBC)))
	g.Expect(controller.SchemaNameFor(aBC)).NotTo(Equal(controller.SchemaNameFor(aBDotC)))
}

func TestSchemaNameFor_TruncatesLongNames(t *testing.T) {
	g := NewWithT(t)

	claim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns",
			Name:      strings.Repeat("a", 80),
		},
	}
	schema := controller.SchemaNameFor(claim)
	g.Expect(len(schema)).To(BeNumerically("<=", 63))
	g.Expect(schema).To(MatchRegexp(`_[0-9a-f]{8}$`))
}

func TestExternalProviderAllows_NilDenies(t *testing.T) {
	g := NewWithT(t)
	g.Expect(controller.ExternalProviderAllows(nil, infraApi.ExternalCapabilityCreateDatabase)).To(BeFalse())
}

func TestExternalProviderAllows_InternalAllows(t *testing.T) {
	g := NewWithT(t)
	provider := &infraApi.DatabaseProvider{
		Spec: infraApi.DatabaseProviderSpec{Type: infraApi.ProviderTypeInternal},
	}
	g.Expect(controller.ExternalProviderAllows(provider, infraApi.ExternalCapabilityCreateDatabase)).To(BeTrue())
}

func TestExternalProviderAllows_ExternalRequiresCapability(t *testing.T) {
	g := NewWithT(t)
	denied := &infraApi.DatabaseProvider{
		Spec: infraApi.DatabaseProviderSpec{
			Type:     infraApi.ProviderTypeExternal,
			External: &infraApi.ExternalProviderSpec{},
		},
	}
	allowed := &infraApi.DatabaseProvider{
		Spec: infraApi.DatabaseProviderSpec{
			Type: infraApi.ProviderTypeExternal,
			External: &infraApi.ExternalProviderSpec{
				Capabilities: []infraApi.ExternalCapability{infraApi.ExternalCapabilityCreateDatabase},
			},
		},
	}
	g.Expect(controller.ExternalProviderAllows(denied, infraApi.ExternalCapabilityCreateDatabase)).To(BeFalse())
	g.Expect(controller.ExternalProviderAllows(allowed, infraApi.ExternalCapabilityCreateDatabase)).To(BeTrue())
}
