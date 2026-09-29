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

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
)

const (
	healthCheckName = "healthz"
	readyCheckName  = "readyz"
)

// NewScheme registers the types this module needs.
//
// Phase 1 only registers client-go's built-in types and apiextensions (so
// the manager can start and, once phase 2 exists, controller-gen-produced
// CRD YAML can round-trip through the same scheme). It does NOT register
// any module-specific CRD scheme yet -- there is no CRD to register.
func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding client-go scheme: %w", err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding apiextensions scheme: %w", err)
	}

	// PHASE 2 EXTENSION POINT: register the DatabaseService API's scheme here
	// once api/services/v1alpha1 exists, e.g.:
	//
	//   if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
	//       return nil, fmt.Errorf("adding services scheme: %w", err)
	//   }

	return scheme, nil
}

// New builds the operator's controller-runtime manager: scheme registration,
// leader election, health/ready checks. It does not wire up any reconciler
// yet -- there is no CRD to reconcile until phase 2.
func New(
	ctx context.Context,
	kubeConfig *rest.Config,
	cfg *moduleconfig.Config,
	opts ...Option,
) (ctrl.Manager, error) {
	if kubeConfig == nil {
		return nil, fmt.Errorf("kubeconfig is nil")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	scheme, err := NewScheme()
	if err != nil {
		return nil, err
	}

	managerOpts := Options{}
	for _, opt := range opts {
		if opt != nil {
			opt.applyOption(&managerOpts)
		}
	}

	pprofBindAddress := ""
	if cfg.Controller.Pprof.Enabled {
		pprofBindAddress = cfg.Controller.Pprof.BindAddress
	}

	mgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:    cfg.Controller.Metrics.BindAddress,
			SecureServing:  true,
			FilterProvider: filters.WithAuthenticationAndAuthorization,
		},
		HealthProbeBindAddress:        cfg.Controller.Health.BindAddress,
		PprofBindAddress:              pprofBindAddress,
		LeaderElection:                cfg.Controller.LeaderElection.Enabled,
		LeaderElectionID:              cfg.Controller.LeaderElection.ID,
		LeaderElectionNamespace:       cfg.OperatorNamespace,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return nil, fmt.Errorf("creating manager: %w", err)
	}

	// PHASE 2 EXTENSION POINT: wire up the DatabaseService reconciler here,
	// once internal/controller/databaseservice exists, e.g.:
	//
	//   if err := databaseservice.NewReconciler(ctx, mgr, cfg, databaseservice.Options{
	//       Recorder: mgr.GetEventRecorder(servicesv1alpha1.DatabaseServiceResource),
	//   }); err != nil {
	//       return nil, fmt.Errorf("creating databaseservice reconciler: %w", err)
	//   }

	if err := mgr.AddHealthzCheck(healthCheckName, healthz.Ping); err != nil {
		return nil, fmt.Errorf("setting up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck(readyCheckName, healthz.Ping); err != nil {
		return nil, fmt.Errorf("setting up ready check: %w", err)
	}

	return mgr, nil
}
