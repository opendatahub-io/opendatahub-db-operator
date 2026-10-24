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
	"testing"

	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
)

func TestShouldDropClaimDatabase_RequiresCreatedAndDelete(t *testing.T) {
	g := NewWithT(t)
	claim := &infraApi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: types.UID("uid-a")},
		Spec: infraApi.DatabaseClaimSpec{
			Provider:       infraApi.ProviderRef{Name: "rhai-db"},
			Database:       "analytics",
			DeletionPolicy: infraApi.DeletionPolicyDelete,
		},
		Status: infraApi.DatabaseClaimStatus{
			Provider:        "rhai-db",
			Database:        "analytics",
			DatabaseCreated: true,
		},
	}

	g.Expect(controller.ShouldDropClaimDatabase(claim, "postgres", nil, nil)).To(BeTrue())

	retain := claim.DeepCopy()
	retain.Spec.DeletionPolicy = infraApi.DeletionPolicyRetain
	g.Expect(controller.ShouldDropClaimDatabase(retain, "postgres", nil, nil)).To(BeFalse())

	notCreated := claim.DeepCopy()
	notCreated.Status.DatabaseCreated = false
	g.Expect(controller.ShouldDropClaimDatabase(notCreated, "postgres", nil, nil)).To(BeFalse())

	defaultDB := claim.DeepCopy()
	defaultDB.Spec.Database = "postgres"
	defaultDB.Status.Database = "postgres"
	g.Expect(controller.ShouldDropClaimDatabase(defaultDB, "postgres", nil, nil)).To(BeFalse())
}

func TestShouldDropClaimDatabase_BlocksWhenShared(t *testing.T) {
	g := NewWithT(t)
	claim := &infraApi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: types.UID("uid-a")},
		Spec: infraApi.DatabaseClaimSpec{
			Provider:       infraApi.ProviderRef{Name: "rhai-db"},
			Database:       "analytics",
			DeletionPolicy: infraApi.DeletionPolicyDelete,
		},
		Status: infraApi.DatabaseClaimStatus{
			Provider:        "rhai-db",
			Database:        "analytics",
			DatabaseCreated: true,
		},
	}
	other := infraApi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "other-ns", UID: types.UID("uid-b")},
		Spec: infraApi.DatabaseClaimSpec{
			Provider: infraApi.ProviderRef{Name: "rhai-db"},
			Database: "analytics",
		},
		Status: infraApi.DatabaseClaimStatus{
			Provider: "rhai-db",
			Database: "analytics",
		},
	}

	g.Expect(controller.ShouldDropClaimDatabase(claim, "postgres", []infraApi.DatabaseClaim{other}, nil)).To(BeFalse())
}

func TestShouldDropClaimDatabase_BlocksWhenSchemaClaimUsesDatabase(t *testing.T) {
	g := NewWithT(t)
	claim := &infraApi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: types.UID("uid-a")},
		Spec: infraApi.DatabaseClaimSpec{
			Provider:       infraApi.ProviderRef{Name: "rhai-db"},
			Database:       "analytics",
			DeletionPolicy: infraApi.DeletionPolicyDelete,
		},
		Status: infraApi.DatabaseClaimStatus{
			Provider:        "rhai-db",
			Database:        "analytics",
			DatabaseCreated: true,
		},
	}
	schemaClaim := infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "sc", Namespace: "app", UID: types.UID("uid-sc")},
		Spec: infraApi.SchemaClaimSpec{
			Provider: infraApi.ProviderRef{Name: "rhai-db"},
			Database: "analytics",
			Schema:   "app_schema",
		},
		Status: infraApi.SchemaClaimStatus{
			Provider: "rhai-db",
			Connection: infraApi.SchemaConnectionStatus{
				Database: "analytics",
			},
		},
	}

	g.Expect(controller.ShouldDropClaimDatabase(claim, "postgres", nil, []infraApi.SchemaClaim{schemaClaim})).To(BeFalse())
}

func TestShouldDropClaimSchema_RequiresCreatedAndDelete(t *testing.T) {
	g := NewWithT(t)
	claim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: types.UID("uid-a")},
		Spec: infraApi.SchemaClaimSpec{
			Provider:       infraApi.ProviderRef{Name: "rhai-db"},
			Schema:         "app_schema",
			Database:       "analytics",
			DeletionPolicy: infraApi.DeletionPolicyDelete,
		},
		Status: infraApi.SchemaClaimStatus{
			Provider:      "rhai-db",
			Schema:        "app_schema",
			SchemaCreated: true,
			Connection: infraApi.SchemaConnectionStatus{
				Database: "analytics",
			},
		},
	}

	g.Expect(controller.ShouldDropClaimSchema(claim, "postgres", nil)).To(BeTrue())

	retain := claim.DeepCopy()
	retain.Spec.DeletionPolicy = infraApi.DeletionPolicyRetain
	g.Expect(controller.ShouldDropClaimSchema(retain, "postgres", nil)).To(BeFalse())

	notCreated := claim.DeepCopy()
	notCreated.Status.SchemaCreated = false
	g.Expect(controller.ShouldDropClaimSchema(notCreated, "postgres", nil)).To(BeFalse())
}

func TestShouldDropClaimSchema_BlocksReserved(t *testing.T) {
	g := NewWithT(t)
	claim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: types.UID("uid-a")},
		Spec: infraApi.SchemaClaimSpec{
			Provider:       infraApi.ProviderRef{Name: "rhai-db"},
			Schema:         "public",
			Database:       "analytics",
			DeletionPolicy: infraApi.DeletionPolicyDelete,
		},
		Status: infraApi.SchemaClaimStatus{
			Provider:      "rhai-db",
			Schema:        "public",
			SchemaCreated: true,
			Connection: infraApi.SchemaConnectionStatus{
				Database: "analytics",
			},
		},
	}

	g.Expect(controller.IsReservedSchema("public")).To(BeTrue())
	g.Expect(controller.IsReservedSchema("pg_toast")).To(BeTrue())
	g.Expect(controller.IsReservedSchema("pg_temp_1")).To(BeTrue())
	g.Expect(controller.IsReservedSchema("app_schema")).To(BeFalse())
	g.Expect(controller.ShouldDropClaimSchema(claim, "postgres", nil)).To(BeFalse())
}

func TestShouldDropClaimSchema_BlocksWhenShared(t *testing.T) {
	g := NewWithT(t)
	claim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: types.UID("uid-a")},
		Spec: infraApi.SchemaClaimSpec{
			Provider:       infraApi.ProviderRef{Name: "rhai-db"},
			Schema:         "app_schema",
			Database:       "analytics",
			DeletionPolicy: infraApi.DeletionPolicyDelete,
		},
		Status: infraApi.SchemaClaimStatus{
			Provider:      "rhai-db",
			Schema:        "app_schema",
			SchemaCreated: true,
			Connection: infraApi.SchemaConnectionStatus{
				Database: "analytics",
			},
		},
	}
	other := infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "other-ns", UID: types.UID("uid-b")},
		Spec: infraApi.SchemaClaimSpec{
			Provider: infraApi.ProviderRef{Name: "rhai-db"},
			Schema:   "app_schema",
			Database: "analytics",
		},
		Status: infraApi.SchemaClaimStatus{
			Provider: "rhai-db",
			Schema:   "app_schema",
			Connection: infraApi.SchemaConnectionStatus{
				Database: "analytics",
			},
		},
	}

	g.Expect(controller.ShouldDropClaimSchema(claim, "postgres", []infraApi.SchemaClaim{other})).To(BeFalse())
}

func TestShouldDropClaimSchema_AllowsWhenSharedPeerIsDeleting(t *testing.T) {
	g := NewWithT(t)
	now := metav1.Now()
	claim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: types.UID("uid-a")},
		Spec: infraApi.SchemaClaimSpec{
			Provider:       infraApi.ProviderRef{Name: "rhai-db"},
			Schema:         "app_schema",
			Database:       "analytics",
			DeletionPolicy: infraApi.DeletionPolicyDelete,
		},
		Status: infraApi.SchemaClaimStatus{
			Provider:      "rhai-db",
			Schema:        "app_schema",
			SchemaCreated: true,
			Connection: infraApi.SchemaConnectionStatus{
				Database: "analytics",
			},
		},
	}
	other := infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "b",
			Namespace:         "other-ns",
			UID:               types.UID("uid-b"),
			DeletionTimestamp: &now,
		},
		Spec: infraApi.SchemaClaimSpec{
			Provider: infraApi.ProviderRef{Name: "rhai-db"},
			Schema:   "app_schema",
			Database: "analytics",
		},
		Status: infraApi.SchemaClaimStatus{
			Provider: "rhai-db",
			Schema:   "app_schema",
			Connection: infraApi.SchemaConnectionStatus{
				Database: "analytics",
			},
		},
	}

	g.Expect(controller.ShouldDropClaimSchema(claim, "postgres", []infraApi.SchemaClaim{other})).To(BeTrue())
}

func TestShouldDropClaimSchema_BlocksWhenSharedViaProviderDefault(t *testing.T) {
	g := NewWithT(t)
	// Explicit default DB name vs omitted Spec.Database must compare equal after
	// resolving against the provider default.
	claim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "a", Namespace: "ns", UID: types.UID("uid-a")},
		Spec: infraApi.SchemaClaimSpec{
			Provider:       infraApi.ProviderRef{Name: "rhai-db"},
			Schema:         "app_schema",
			Database:       "postgres",
			DeletionPolicy: infraApi.DeletionPolicyDelete,
		},
		Status: infraApi.SchemaClaimStatus{
			Provider:      "rhai-db",
			Schema:        "app_schema",
			SchemaCreated: true,
		},
	}
	other := infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "b", Namespace: "other-ns", UID: types.UID("uid-b")},
		Spec: infraApi.SchemaClaimSpec{
			Provider: infraApi.ProviderRef{Name: "rhai-db"},
			Schema:   "app_schema",
			// Database omitted: provider default.
		},
		Status: infraApi.SchemaClaimStatus{
			Provider: "rhai-db",
			Schema:   "app_schema",
		},
	}

	g.Expect(controller.ShouldDropClaimSchema(claim, "postgres", []infraApi.SchemaClaim{other})).To(BeFalse())
}
