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

package databaseservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"slices"

	fwapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	odherrors "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/errors"
	renderKustomize "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/render/kustomize"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	odhtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesApi "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseservice/operands"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
)

func operandsTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{apiextensionsv1.AddToScheme, servicesApi.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&servicesApi.DatabaseService{}).
		WithObjects(objects...).Build()
}

func operandsTestRequest(t *testing.T, cli client.Client, service *servicesApi.DatabaseService) *odhtypes.ReconciliationRequest {
	t.Helper()
	aggregator, err := conditions.NewAggregator(fwapi.ConditionTypeReady,
		conditions.Dependent(ConditionOperandsReady, conditions.HealthyWhenTrue))
	if err != nil {
		t.Fatal(err)
	}
	return &odhtypes.ReconciliationRequest{Client: cli, Instance: service, Conditions: conditions.NewManager(service, aggregator)}
}

func operandsTestModule(t *testing.T, register func() error) *Module {
	t.Helper()
	m := NewModule(&moduleconfig.Config{})
	m.registerInfra = register
	return m
}

func establishedCRD(name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: apiextensionsv1.CustomResourceDefinitionStatus{
			Conditions: []apiextensionsv1.CustomResourceDefinitionCondition{{
				Type:   apiextensionsv1.Established,
				Status: apiextensionsv1.ConditionTrue,
			}},
		},
	}
}

// TestOperandRenderActionPinsManifestPath renders through the real render
// action with the embedded filesystem, proving the ManifestInfo path
// resolves against the FS root and produces exactly the operand CRDs.
func TestOperandRenderActionPinsManifestPath(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName}}
	cli := operandsTestClient(t, service)

	operandFS, err := operands.FS()
	if err != nil {
		t.Fatal(err)
	}
	render := renderKustomize.NewAction(
		renderKustomize.WithEngineFS(operandFS),
		renderKustomize.WithCache(true),
	)

	m := operandsTestModule(t, nil)
	rr := operandsTestRequest(t, cli, service)
	if err := m.initOperandManifests(ctx, rr); err != nil {
		t.Fatal(err)
	}
	if err := render(ctx, rr); err != nil {
		t.Fatalf("rendering embedded operand manifests: %v", err)
	}

	if len(rr.Resources) != 3 {
		t.Fatalf("rendered %d resources, want 3 CRDs", len(rr.Resources))
	}
	names := make([]string, 0, len(rr.Resources))
	for i := range rr.Resources {
		if rr.Resources[i].GetKind() != "CustomResourceDefinition" {
			t.Fatalf("rendered resource %q is not a CRD", rr.Resources[i].GetName())
		}
		names = append(names, rr.Resources[i].GetName())
	}
	slices.Sort(names)
	want := []string{
		infraApi.DatabaseClaimCRDName,
		infraApi.DatabaseProviderCRDName,
		infraApi.SchemaClaimCRDName,
	}
	if !slices.Equal(names, want) {
		t.Fatalf("rendered resources %v, want exactly %v", names, want)
	}
}

func TestAwaitOperandsEstablishedRegistersControllers(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName}}
	cli := operandsTestClient(t,
		service,
		establishedCRD(infraApi.DatabaseProviderCRDName),
		establishedCRD(infraApi.DatabaseClaimCRDName),
		establishedCRD(infraApi.SchemaClaimCRDName),
	)

	var registered int
	m := operandsTestModule(t, func() error { registered++; return nil })
	rr := operandsTestRequest(t, cli, service)

	if err := m.awaitOperandsEstablished(ctx, rr); err != nil {
		t.Fatalf("established operands should pass the gate: %v", err)
	}
	if registered != 1 {
		t.Fatalf("registration invoked %d times, want 1", registered)
	}
	condition := conditions.FindStatusCondition(service, ConditionOperandsReady)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != reasonOperandsEstablished {
		t.Fatalf("established condition not reported: %+v", condition)
	}
}

func TestAwaitOperandsEstablishedWaitsForCRDs(t *testing.T) {
	service := &servicesApi.DatabaseService{ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName}}
	cli := operandsTestClient(t, service)

	var registered int
	m := operandsTestModule(t, func() error { registered++; return nil })
	rr := operandsTestRequest(t, cli, service)

	// The gate helper backs off for about five seconds before failing; a
	// short context cuts that wait, which does not change the contract
	// under test: any gate error must stop the chain and requeue.
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	err := m.awaitOperandsEstablished(ctx, rr)
	var stop odherrors.StopError
	if !errors.As(err, &stop) {
		t.Fatalf("unsettled operand CRDs should stop the reconcile with a requeue: %v", err)
	}
	if registered != 0 {
		t.Fatalf("controllers registered before the operand CRDs were established")
	}
	condition := conditions.FindStatusCondition(service, ConditionOperandsReady)
	if condition == nil || condition.Reason != reasonCRDsEstablishing {
		t.Fatalf("establishing condition not reported: %+v", condition)
	}
}

func TestAwaitOperandsEstablishedReportsRegistrationFailure(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName}}
	cli := operandsTestClient(t,
		service,
		establishedCRD(infraApi.DatabaseProviderCRDName),
		establishedCRD(infraApi.DatabaseClaimCRDName),
		establishedCRD(infraApi.SchemaClaimCRDName),
	)

	m := operandsTestModule(t, func() error { return errors.New("controller name conflict") })

	err := m.awaitOperandsEstablished(ctx, operandsTestRequest(t, cli, service))
	if err == nil || !strings.Contains(err.Error(), "controller name conflict") {
		t.Fatalf("registration failure should surface: %v", err)
	}
	condition := conditions.FindStatusCondition(service, ConditionOperandsReady)
	if condition == nil || condition.Reason != reasonOperandRegistrationError {
		t.Fatalf("registration failure condition not reported: %+v", condition)
	}
}

// The action re-invokes the registration closure on every established
// reconcile; idempotency belongs to the closure (pkg/manager), not here.
func TestAwaitOperandsEstablishedReinvokesRegistration(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName}}
	cli := operandsTestClient(t,
		service,
		establishedCRD(infraApi.DatabaseProviderCRDName),
		establishedCRD(infraApi.DatabaseClaimCRDName),
		establishedCRD(infraApi.SchemaClaimCRDName),
	)

	var registered int
	m := operandsTestModule(t, func() error { registered++; return nil })

	for i := range 2 {
		if err := m.awaitOperandsEstablished(ctx, operandsTestRequest(t, cli, service)); err != nil {
			t.Fatalf("repeat reconcile %d should pass: %v", i, err)
		}
	}
	if registered != 2 {
		t.Fatalf("registration closure invoked %d times across reconciles, want 2", registered)
	}
}

func TestDeployOperandsMarksConditionOnFailure(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName}}
	cli := operandsTestClient(t, service)

	m := operandsTestModule(t, nil)
	apply := m.deployOperands(func(context.Context, *odhtypes.ReconciliationRequest) error {
		return errors.New("customresourcedefinitions is forbidden")
	})

	err := apply(ctx, operandsTestRequest(t, cli, service))
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("deploy failure should propagate unchanged: %v", err)
	}
	condition := conditions.FindStatusCondition(service, ConditionOperandsReady)
	if condition == nil || condition.Reason != reasonOperandDeployError {
		t.Fatalf("deploy failure condition not reported: %+v", condition)
	}
}
