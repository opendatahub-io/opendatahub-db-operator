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
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseclaim"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseprovider"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/schemaclaim"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
)

// NewInfrastructureRegistration returns a function that registers the
// infrastructure controllers (DatabaseProvider, DatabaseClaim, SchemaClaim)
// on mgr. Registration is idempotent per controller and safe to call
// repeatedly, including concurrently and on a running manager: each
// controller registers at most once, a failed registration is sticky
// (registration failures are setup errors such as scheme or name conflicts
// that a retry cannot fix), and CRD absence is retryable — RHOAIENG-99478's
// operand install path installs the CRDs at runtime and calls registration
// again once they are established, so no pod restart is required.
func NewInfrastructureRegistration(
	ctx context.Context,
	mgr ctrl.Manager,
	cfg *moduleconfig.Config,
) func() error {
	r := &infrastructureRegistration{
		ctx:    ctx,
		mgr:    mgr,
		cfg:    cfg,
		reader: mgr.GetAPIReader(),
		log:    ctrl.Log.WithName("manager"),
		registerProvider: func() error {
			return databaseprovider.NewReconciler(ctx, mgr, cfg, databaseprovider.Options{
				Recorder: mgr.GetEventRecorder(infraApi.DatabaseProviderResource),
			})
		},
		registerDatabaseClaims: func() error {
			return databaseclaim.NewReconciler(ctx, mgr, cfg, databaseclaim.Options{
				Recorder: mgr.GetEventRecorder(infraApi.DatabaseClaimResource),
			})
		},
		registerSchemaClaims: func() error {
			return schemaclaim.NewReconciler(ctx, mgr, cfg, schemaclaim.Options{
				Recorder: mgr.GetEventRecorder(infraApi.SchemaClaimResource),
			})
		},
	}
	return r.register
}

// infrastructureRegistration registers the infrastructure controllers on
// one manager. The per-controller registration functions are fields so the
// guard semantics are testable without a manager.
type infrastructureRegistration struct {
	ctx    context.Context
	mgr    ctrl.Manager
	cfg    *moduleconfig.Config
	reader client.Reader
	log    logr.Logger

	registerProvider       func() error
	registerDatabaseClaims func() error
	registerSchemaClaims   func() error

	provider       registrationGuard
	databaseClaims registrationGuard
	schemaClaims   registrationGuard
}

// register registers every infrastructure controller whose CRDs are
// present. CRD absence is not an error: only the boot call and the operand
// install path (which calls registration once the CRDs are established)
// can observe absence, and both treat it as "not yet". A controller that
// has registered — or failed to register — is never touched again.
func (r *infrastructureRegistration) register() error {
	providerAvailable, err := crdAvailable(r.ctx, r.reader, infraApi.DatabaseProviderCRDName)
	if err != nil {
		return fmt.Errorf("checking DatabaseProvider CRD: %w", err)
	}
	if !providerAvailable {
		// Claim controllers also watch DatabaseProvider, so nothing
		// registers until the provider CRD exists. On a fresh install the
		// module installs the operand CRDs and registration follows.
		r.log.Info("infrastructure CRDs are absent; controllers will register once the module installs them")
		return nil
	}

	if err := r.provider.register(r.registerProvider); err != nil {
		return fmt.Errorf("creating databaseprovider reconciler: %w", err)
	}

	// Claims watch DatabaseProvider, so both the claim CRD and provider CRD
	// must be present for each claim reconciler.
	if available, err := crdAvailable(r.ctx, r.reader, infraApi.DatabaseClaimCRDName); err != nil {
		return fmt.Errorf("checking DatabaseClaim CRD: %w", err)
	} else if available {
		if err := r.databaseClaims.register(r.registerDatabaseClaims); err != nil {
			return fmt.Errorf("creating databaseclaim reconciler: %w", err)
		}
	}
	if available, err := crdAvailable(r.ctx, r.reader, infraApi.SchemaClaimCRDName); err != nil {
		return fmt.Errorf("checking SchemaClaim CRD: %w", err)
	} else if available {
		if err := r.schemaClaims.register(r.registerSchemaClaims); err != nil {
			return fmt.Errorf("creating schemaclaim reconciler: %w", err)
		}
	}

	return nil
}

// registrationGuard makes one controller's registration idempotent: fn runs
// at most once, and a failed attempt is sticky. CRD absence never reaches
// the guard, so it stays retryable.
type registrationGuard struct {
	mu   sync.Mutex
	done bool
	err  error
}

func (g *registrationGuard) register(fn func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.done {
		return g.err
	}
	if err := fn(); err != nil {
		g.err = err
		g.done = true
		return err
	}
	g.done = true
	return nil
}

// crdAvailable reports whether the named CRD exists.
func crdAvailable(ctx context.Context, reader client.Reader, name string) (bool, error) {
	crd := &apiextensionsv1.CustomResourceDefinition{}
	err := reader.Get(ctx, client.ObjectKey{Name: name}, crd)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
