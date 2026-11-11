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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesApi "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseservice/operands"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func operandsTestClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{apiextensionsv1.AddToScheme, servicesApi.AddToScheme, infraApi.AddToScheme, rbacv1.AddToScheme} {
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
	m := NewModule(&moduleconfig.Config{OperatorNamespace: "odh-db-operator-system"})
	m.registerInfra = register
	operandFS, err := operands.FS()
	if err != nil {
		t.Fatal(err)
	}
	// The namespace fn mirrors production NewReconciler: the rendered
	// RoleBinding's metadata namespace is the operator namespace.
	m.renderOperands = renderKustomize.NewAction(
		renderKustomize.WithEngineFS(operandFS),
		renderKustomize.WithCache(true),
		renderKustomize.WithNamespaceFn(func(_ context.Context, _ *odhtypes.ReconciliationRequest) (string, error) {
			return m.cfg.OperatorNamespace, nil
		}),
	)
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

// notFoundCRD is the error a CRD get returns once the object is gone.
func notFoundCRD() error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"}, "claim-crd")
}

// stubListReader returns canned errors for List and, when the object is a
// CRD, for Get — enough to drive listToleratingMissingCRD's decision table.
type stubListReader struct {
	listErr error
	// crdGetErr is returned for CustomResourceDefinition gets; a nil value
	// means the CRD object exists.
	crdGetErr error
}

func (s *stubListReader) Get(_ context.Context, _ client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	if _, ok := obj.(*apiextensionsv1.CustomResourceDefinition); ok {
		if s.crdGetErr == nil {
			return nil
		}
		return s.crdGetErr
	}
	return nil
}

func (s *stubListReader) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return s.listErr
}

// TestListToleratingMissingCRD verifies when a 404 from the resource path
// is trusted as "kind absent": only once the CRD object itself is gone. A
// CRD that still exists — terminating, or recreated and not yet served —
// must keep the caller retrying, because instances may remain and the
// gates built on this list guard irreversible deletions.
func TestListToleratingMissingCRD(t *testing.T) {
	ctx := context.Background()
	notFound := apierrors.NewNotFound(
		schema.GroupResource{Group: infraApi.GroupName, Resource: "databaseclaims"}, "")
	crdName := infraApi.DatabaseClaimCRDName

	for _, tc := range []struct {
		name      string
		reader    *stubListReader
		wantErr   bool
		wantError string
	}{
		{
			name:   "list succeeds",
			reader: &stubListReader{},
		},
		{
			name:   "kind never registered and CRD gone is tolerated",
			reader: &stubListReader{listErr: &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: infraApi.GroupName, Kind: "DatabaseClaim"}}, crdGetErr: notFoundCRD()},
		},
		{
			name:      "NoMatch with CRD still present retries",
			reader:    &stubListReader{listErr: &meta.NoKindMatchError{GroupKind: schema.GroupKind{Group: infraApi.GroupName, Kind: "DatabaseClaim"}}},
			wantErr:   true,
			wantError: "still exists",
		},
		{
			name:      "404 with CRD still present retries",
			reader:    &stubListReader{listErr: notFound},
			wantErr:   true,
			wantError: "still exists",
		},
		{
			name:      "404 with CRD get failing is surfaced",
			reader:    &stubListReader{listErr: notFound, crdGetErr: apierrors.NewForbidden(schema.GroupResource{}, "", nil)},
			wantErr:   true,
			wantError: "getting CRD",
		},
		{
			name:   "404 with CRD gone is tolerated",
			reader: &stubListReader{listErr: notFound, crdGetErr: notFoundCRD()},
		},
		{
			name:      "other list errors are surfaced",
			reader:    &stubListReader{listErr: apierrors.NewForbidden(schema.GroupResource{Group: infraApi.GroupName, Resource: "databaseclaims"}, "", nil)},
			wantErr:   true,
			wantError: "forbidden",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := listToleratingMissingCRD(ctx, tc.reader, &infraApi.DatabaseClaimList{}, crdName)
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected tolerance, got: %v", err)
			}
		})
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

	if len(rr.Resources) != 5 {
		t.Fatalf("rendered %d resources, want 5 (3 CRDs + ClusterRole + RoleBinding)", len(rr.Resources))
	}
	names := make([]string, 0, len(rr.Resources))
	for i := range rr.Resources {
		if rr.Resources[i].GetKind() != "CustomResourceDefinition" {
			continue
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
		t.Fatalf("rendered CRDs %v, want exactly %v", names, want)
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

// TestCleanupOperandsBlocksOnAnyClaim verifies the operand gate blocks on
// any remaining claim — not just default-provider-bound ones — because CRD
// deletion cascades every instance.
func TestCleanupOperandsBlocksOnAnyClaim(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
		Status:     servicesApi.DatabaseServiceStatus{DefaultProviderName: "rhai-db"},
	}
	// A claim with a foreign provider name: the default-provider cleanup
	// would not gate on it, but the operand gate must.
	foreignClaim := &infraApi.DatabaseClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team"},
		Spec:       infraApi.DatabaseClaimSpec{Provider: infraApi.ProviderRef{Name: "other-provider"}},
	}
	// A default-provider claim: both gates block on this.
	defaultClaim := &infraApi.SchemaClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "internal-app", Namespace: "other"},
		Status:     infraApi.SchemaClaimStatus{Provider: "rhai-db"},
	}
	cli := operandsTestClient(t, service, foreignClaim, defaultClaim,
		establishedCRD(infraApi.DatabaseProviderCRDName),
		establishedCRD(infraApi.DatabaseClaimCRDName),
		establishedCRD(infraApi.SchemaClaimCRDName),
	)

	m := operandsTestModule(t, nil)
	err := m.cleanupOperands(ctx, operandsTestRequest(t, cli, service))
	if err == nil || !strings.Contains(err.Error(), "DatabaseClaim team/app") || !strings.Contains(err.Error(), "SchemaClaim other/internal-app") {
		t.Fatalf("operand removal should be blocked by both claims: %v", err)
	}
	condition := conditions.FindStatusCondition(service, ConditionOperandsReady)
	if condition == nil || condition.Reason != "BlockingClaims" {
		t.Fatalf("blocking condition not reported: %+v", condition)
	}
	// The CRDs must still exist — no deletion was issued.
	for _, name := range []string{infraApi.DatabaseProviderCRDName, infraApi.DatabaseClaimCRDName, infraApi.SchemaClaimCRDName} {
		if err := cli.Get(ctx, client.ObjectKey{Name: name}, &apiextensionsv1.CustomResourceDefinition{}); err != nil {
			t.Fatalf("operand CRD %q was deleted while claims block: %v", name, err)
		}
	}
}

// TestCleanupOperandsBlocksOnAnyProvider verifies the provider gate: with
// no claims but a user-created provider remaining, pruning the CRD would
// cascade the provider and garbage-collect its storage, so removal stays
// blocked (the default provider is already gone by finalizer order, with
// its retention steps).
func TestCleanupOperandsBlocksOnAnyProvider(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
		Status:     servicesApi.DatabaseServiceStatus{DefaultProviderName: "rhai-db"},
	}
	// No claims: the claims gate passes. A provider the default-provider
	// cleanup would never manage — only the operand gate protects it.
	userProvider := &infraApi.DatabaseProvider{
		ObjectMeta: metav1.ObjectMeta{Name: "team-db"},
	}
	cli := operandsTestClient(t, service, userProvider,
		establishedCRD(infraApi.DatabaseProviderCRDName),
		establishedCRD(infraApi.DatabaseClaimCRDName),
		establishedCRD(infraApi.SchemaClaimCRDName),
	)

	m := operandsTestModule(t, nil)
	err := m.cleanupOperands(ctx, operandsTestRequest(t, cli, service))
	if err == nil || !strings.Contains(err.Error(), "DatabaseProvider team-db") {
		t.Fatalf("operand removal should be blocked by the user provider: %v", err)
	}
	condition := conditions.FindStatusCondition(service, ConditionOperandsReady)
	if condition == nil || condition.Reason != "BlockingProviders" {
		t.Fatalf("blocking condition not reported: %+v", condition)
	}
	// The CRDs must still exist — no deletion was issued.
	for _, name := range []string{infraApi.DatabaseProviderCRDName, infraApi.DatabaseClaimCRDName, infraApi.SchemaClaimCRDName} {
		if err := cli.Get(ctx, client.ObjectKey{Name: name}, &apiextensionsv1.CustomResourceDefinition{}); err != nil {
			t.Fatalf("operand CRD %q was deleted while a provider blocks: %v", name, err)
		}
	}
	// The provider itself must be untouched.
	if err := cli.Get(ctx, client.ObjectKey{Name: "team-db"}, &infraApi.DatabaseProvider{}); err != nil {
		t.Fatalf("blocking provider was deleted: %v", err)
	}
}

// TestCleanupOperandsPollsThroughAsyncRemoval verifies the bounded
// in-reconcile poll: operand deletion is asynchronous (CRD termination
// cascades instances), so the finalizer must keep checking within one
// reconcile instead of returning an error that requeues on the controller's
// exponential backoff — which the blocking phases inflate.
func TestCleanupOperandsPollsThroughAsyncRemoval(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
	}
	cli := operandsTestClient(t, service,
		establishedCRD(infraApi.DatabaseProviderCRDName),
		establishedCRD(infraApi.DatabaseClaimCRDName),
		establishedCRD(infraApi.SchemaClaimCRDName),
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "internal-provider-writer"}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "internal-provider-writer", Namespace: "odh-db-operator-system"}},
	)

	// Simulate asynchronous termination: the first two verification GETs
	// report the operands as still terminating; afterwards the (already
	// deleted) objects report NotFound, as a real API server would once
	// CRD cleanup finishes.
	getCalls := 0
	slowRemoval := interceptor.NewClient(cli, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			getCalls++
			if getCalls <= 2 {
				return nil // still terminating
			}
			return cli.Get(ctx, key, obj, opts...)
		},
	})

	m := operandsTestModule(t, nil)
	if err := m.cleanupOperands(ctx, operandsTestRequest(t, slowRemoval, service)); err != nil {
		t.Fatalf("prune should absorb the async removal within its poll budget: %v", err)
	}
}

// TestCleanupOperandsSurfacesPollErrors verifies the finalizer wraps the
// poll's error: a hard failure from the verification (for example a
// Forbidden GET) must reach the caller instead of being discarded behind
// an operand-less "waiting" message.
func TestCleanupOperandsSurfacesPollErrors(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
	}
	cli := operandsTestClient(t, service,
		establishedCRD(infraApi.DatabaseProviderCRDName),
		establishedCRD(infraApi.DatabaseClaimCRDName),
		establishedCRD(infraApi.SchemaClaimCRDName),
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "internal-provider-writer"}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "internal-provider-writer", Namespace: "odh-db-operator-system"}},
	)

	forbidden := apierrors.NewForbidden(
		schema.GroupResource{Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
		infraApi.DatabaseClaimCRDName, errors.New("rbac"))
	failing := interceptor.NewClient(cli, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return forbidden
		},
	})

	m := operandsTestModule(t, nil)
	err := m.cleanupOperands(ctx, operandsTestRequest(t, failing, service))
	if err == nil {
		t.Fatal("prune should fail when operand verification hits a hard error")
	}
	if !strings.Contains(err.Error(), "waiting for operands to be removed") || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("error should wrap the poll cause, got: %v", err)
	}
}

// TestCleanupOperandsPrunesRenderedSet verifies the render-and-delete
// parity: the finalizer renders the same operand set, deletes each, and
// only returns nil once every object is gone.
func TestCleanupOperandsPrunesRenderedSet(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
	}
	cli := operandsTestClient(t, service,
		establishedCRD(infraApi.DatabaseProviderCRDName),
		establishedCRD(infraApi.DatabaseClaimCRDName),
		establishedCRD(infraApi.SchemaClaimCRDName),
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "internal-provider-writer"}},
		// The render action injects cfg.OperatorNamespace (matching
		// production); the pre-created RoleBinding must be in the same
		// namespace or the deletion targets the wrong key and silently
		// orphans it.
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "internal-provider-writer", Namespace: "odh-db-operator-system"}},
	)

	m := operandsTestModule(t, nil)
	// The fake client deletes synchronously, so the first pass issues the
	// deletes and the verification GETs all return NotFound in one pass.
	// On a real cluster this takes multiple reconciles (CRD cascading).
	if err := m.cleanupOperands(ctx, operandsTestRequest(t, cli, service)); err != nil {
		t.Fatalf("first pass should delete all operands and verify removal: %v", err)
	}
	for _, check := range []struct {
		key client.ObjectKey
		obj client.Object
	}{
		{client.ObjectKey{Name: infraApi.DatabaseProviderCRDName}, &apiextensionsv1.CustomResourceDefinition{}},
		{client.ObjectKey{Name: infraApi.DatabaseClaimCRDName}, &apiextensionsv1.CustomResourceDefinition{}},
		{client.ObjectKey{Name: infraApi.SchemaClaimCRDName}, &apiextensionsv1.CustomResourceDefinition{}},
		{client.ObjectKey{Name: "internal-provider-writer"}, &rbacv1.ClusterRole{}},
		{client.ObjectKey{Namespace: "odh-db-operator-system", Name: "internal-provider-writer"}, &rbacv1.RoleBinding{}},
	} {
		if err := cli.Get(ctx, check.key, check.obj); !apierrors.IsNotFound(err) {
			t.Fatalf("operand %v was not deleted: %v", check.key, err)
		}
	}
	// Second pass: everything gone → nil.
	if err := m.cleanupOperands(ctx, operandsTestRequest(t, cli, service)); err != nil {
		t.Fatalf("cleanup with all operands gone should succeed: %v", err)
	}
}

// TestCleanupOperandsRendersNonemptySet guards against the silent-empty
// orphan: the finalizer request starts with empty rr.Manifests, so skipping
// initOperandManifests renders nothing and the deletion loop succeeds
// vacuously. This test fails if the render produces fewer than the five
// operand objects.
func TestCleanupOperandsRendersNonemptySet(t *testing.T) {
	ctx := context.Background()
	service := &servicesApi.DatabaseService{
		ObjectMeta: metav1.ObjectMeta{Name: servicesApi.DatabaseServiceInstanceName},
	}
	cli := operandsTestClient(t, service)

	m := operandsTestModule(t, nil)
	rr := operandsTestRequest(t, cli, service)
	if err := m.initOperandManifests(ctx, rr); err != nil {
		t.Fatal(err)
	}
	if err := m.renderOperands(ctx, rr); err != nil {
		t.Fatalf("rendering operand manifests: %v", err)
	}
	if len(rr.Resources) != 5 {
		t.Fatalf("rendered %d operand objects, want 5 (3 CRDs + ClusterRole + RoleBinding); "+
			"a vacuous render here means the finalizer silently orphans every operand", len(rr.Resources))
	}
}

// TestCustomizeOperandRoleBinding verifies the subject finalization from
// runtime configuration.
func TestCustomizeOperandRoleBinding(t *testing.T) {
	m := NewModule(&moduleconfig.Config{
		OperatorNamespace:      "operator-ns",
		OperatorServiceAccount: "my-operator-sa",
	})
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "internal-provider-writer"},
		"subjects": []any{map[string]any{
			"kind":      "ServiceAccount",
			"name":      "operator",
			"namespace": "system",
		}},
	}}
	if err := m.customizeOperandRoleBinding(context.Background(), nil, nil, obj, nil); err != nil {
		t.Fatalf("customizing operand RoleBinding: %v", err)
	}
	subjects, _, _ := unstructured.NestedSlice(obj.Object, "subjects")
	subject := subjects[0].(map[string]any)
	if subject["name"] != "my-operator-sa" || subject["namespace"] != "operator-ns" {
		t.Fatalf("subject not finalized from config: %+v", subject)
	}
}

// TestCustomizeOperandRoleBindingRequiresSA verifies the explicit failure
// when the configuration is missing.
func TestCustomizeOperandRoleBindingRequiresSA(t *testing.T) {
	m := NewModule(&moduleconfig.Config{})
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "internal-provider-writer"},
		"subjects": []any{map[string]any{"kind": "ServiceAccount"}},
	}}
	err := m.customizeOperandRoleBinding(context.Background(), nil, nil, obj, nil)
	if err == nil || !strings.Contains(err.Error(), "operator-service-account") {
		t.Fatalf("missing SA configuration should fail explicitly: %v", err)
	}
}
