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

package manager

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
)

func registrationTestClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func namedCRD(name string) *apiextensionsv1.CustomResourceDefinition {
	return &apiextensionsv1.CustomResourceDefinition{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// testRegistration builds a registration whose controller constructors are
// stubs, so the guard semantics are observable without a manager.
func testRegistration(
	t *testing.T,
	cli client.Client,
	provider func() error,
	databaseClaims func() error,
	schemaClaims func() error,
) *infrastructureRegistration {
	t.Helper()
	return &infrastructureRegistration{
		ctx:                    context.Background(),
		reader:                 cli,
		log:                    logr.Discard(),
		registerProvider:       provider,
		registerDatabaseClaims: databaseClaims,
		registerSchemaClaims:   schemaClaims,
	}
}

func noopRegistration() func() error { return func() error { return nil } }

func TestRegistrationAbsentCRDsIsRetryable(t *testing.T) {
	cli := registrationTestClient(t)

	var provider, claims, schemas atomic.Int32
	r := testRegistration(t, cli,
		func() error { provider.Add(1); return nil },
		func() error { claims.Add(1); return nil },
		func() error { schemas.Add(1); return nil },
	)

	if err := r.register(); err != nil {
		t.Fatalf("absent CRDs must not error: %v", err)
	}
	if provider.Load()+claims.Load()+schemas.Load() != 0 {
		t.Fatalf("controllers registered without their CRDs")
	}

	// Installing the CRDs makes a later call register; no restart happened
	// in between, which is the regression this test guards.
	for _, crd := range []*apiextensionsv1.CustomResourceDefinition{
		namedCRD(infraApi.DatabaseProviderCRDName),
		namedCRD(infraApi.DatabaseClaimCRDName),
		namedCRD(infraApi.SchemaClaimCRDName),
	} {
		if err := cli.Create(context.Background(), crd); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.register(); err != nil {
		t.Fatalf("registration after CRD install failed: %v", err)
	}
	if provider.Load() != 1 || claims.Load() != 1 || schemas.Load() != 1 {
		t.Fatalf("registration counts provider=%d claims=%d schemas=%d, want 1/1/1",
			provider.Load(), claims.Load(), schemas.Load())
	}
}

func TestRegistrationPartialCRDsRegistersOnlyPossibleControllers(t *testing.T) {
	// Claim controllers watch DatabaseProvider, so claims must not register
	// without the provider CRD even when their own CRDs exist.
	cli := registrationTestClient(t,
		namedCRD(infraApi.DatabaseClaimCRDName),
		namedCRD(infraApi.SchemaClaimCRDName),
	)

	var provider, claims, schemas atomic.Int32
	r := testRegistration(t, cli,
		func() error { provider.Add(1); return nil },
		func() error { claims.Add(1); return nil },
		func() error { schemas.Add(1); return nil },
	)

	for i := range 2 {
		if err := r.register(); err != nil {
			t.Fatalf("call %d failed: %v", i, err)
		}
	}
	if provider.Load() != 0 || claims.Load() != 0 || schemas.Load() != 0 {
		t.Fatalf("claims registered without the provider CRD: provider=%d claims=%d schemas=%d",
			provider.Load(), claims.Load(), schemas.Load())
	}

	// Adding the provider CRD registers the provider first; the claim
	// controllers follow on the next call without double-registering.
	if err := cli.Create(context.Background(), namedCRD(infraApi.DatabaseProviderCRDName)); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if err := r.register(); err != nil {
			t.Fatalf("call %d after provider CRD failed: %v", i, err)
		}
	}
	if provider.Load() != 1 || claims.Load() != 1 || schemas.Load() != 1 {
		t.Fatalf("registration counts provider=%d claims=%d schemas=%d, want 1/1/1",
			provider.Load(), claims.Load(), schemas.Load())
	}
}

func TestRegistrationErrorIsSticky(t *testing.T) {
	cli := registrationTestClient(t,
		namedCRD(infraApi.DatabaseProviderCRDName),
		namedCRD(infraApi.DatabaseClaimCRDName),
		namedCRD(infraApi.SchemaClaimCRDName),
	)

	sentinel := errors.New("controller name conflict")
	var provider atomic.Int32
	r := testRegistration(t, cli,
		func() error { provider.Add(1); return sentinel },
		noopRegistration(),
		noopRegistration(),
	)

	for i := range 2 {
		err := r.register()
		if err == nil || !errors.Is(err, sentinel) {
			t.Fatalf("call %d returned %v, want the sticky registration error", i, err)
		}
	}
	if provider.Load() != 1 {
		t.Fatalf("failed registration attempted %d times, want 1", provider.Load())
	}
}

func TestRegistrationGuardRunsOnceUnderConcurrency(t *testing.T) {
	var calls atomic.Int32
	guard := &registrationGuard{}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := guard.register(func() error { calls.Add(1); return nil }); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("fn ran %d times under concurrency, want 1", calls.Load())
	}
}
