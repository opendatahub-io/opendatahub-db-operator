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

	configv1 "github.com/openshift/api/config/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesv1alpha1 "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseprovider"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseservice"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
)

const (
	healthCheckName = "healthz"
	readyCheckName  = "readyz"
)

// NewScheme returns a scheme with Kubernetes and DatabaseService types registered.
func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()

	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding client-go scheme: %w", err)
	}
	if err := apiextensionsv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding apiextensions scheme: %w", err)
	}
	if err := configv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding OpenShift config scheme: %w", err)
	}
	if err := infraApi.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding infrastructure scheme: %w", err)
	}
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("adding services scheme: %w", err)
	}

	return scheme, nil
}

// New creates the manager and registers the DatabaseService reconciler.
func New(
	ctx context.Context,
	kubeConfig *rest.Config,
	cfg *moduleconfig.Config,
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

	if err := databaseservice.NewReconciler(ctx, mgr, cfg); err != nil {
		return nil, fmt.Errorf("creating databaseservice reconciler: %w", err)
	}
	if err := databaseprovider.NewReconciler(ctx, mgr, cfg, databaseprovider.Options{
		Recorder: mgr.GetEventRecorder(infraApi.DatabaseProviderResource),
	}); err != nil {
		return nil, fmt.Errorf("creating databaseprovider reconciler: %w", err)
	}

	if err := mgr.AddHealthzCheck(healthCheckName, healthz.Ping); err != nil {
		return nil, fmt.Errorf("setting up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck(readyCheckName, healthz.Ping); err != nil {
		return nil, fmt.Errorf("setting up ready check: %w", err)
	}

	return mgr, nil
}
