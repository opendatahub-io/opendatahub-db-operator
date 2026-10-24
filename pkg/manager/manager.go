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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	webhookserver "sigs.k8s.io/controller-runtime/pkg/webhook"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesv1alpha1 "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseclaim"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseprovider"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseservice"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/schemaclaim"
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

// New creates the manager and registers reconcilers. DatabaseService always
// registers; DatabaseProvider, DatabaseClaim, and SchemaClaim register only
// when their CRDs are present so the operator can still reconcile the module
// singleton when infrastructure CRDs are not installed yet.
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

	var webhookServer webhookserver.Server
	if cfg.Controller.Webhook.Enabled {
		webhookServer = webhookserver.NewServer(webhookserver.Options{
			Port:    cfg.Controller.Webhook.Port,
			CertDir: cfg.Controller.Webhook.CertDir,
		})
	}

	mgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		Scheme:        scheme,
		WebhookServer: webhookServer,
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
	if cfg.Controller.Webhook.Enabled {
		mgr.GetWebhookServer()
	}

	if err := databaseservice.NewReconciler(ctx, mgr, cfg); err != nil {
		return nil, fmt.Errorf("creating databaseservice reconciler: %w", err)
	}

	reader := mgr.GetAPIReader()
	log := ctrl.Log.WithName("manager")

	providerCRDAvailable, err := crdAvailable(ctx, reader, infraApi.DatabaseProviderCRDName)
	if err != nil {
		return nil, fmt.Errorf("checking DatabaseProvider CRD: %w", err)
	}
	if providerCRDAvailable {
		if err := databaseprovider.NewReconciler(ctx, mgr, cfg, databaseprovider.Options{
			Recorder: mgr.GetEventRecorder(infraApi.DatabaseProviderResource),
		}); err != nil {
			return nil, fmt.Errorf("creating databaseprovider reconciler: %w", err)
		}
	} else {
		log.Info("DatabaseProvider CRD is absent; provider reconciliation requires a restart after CRD installation")
	}

	// Claims watch DatabaseProvider, so both the claim CRD and provider CRD
	// must be present for each claim reconciler.
	databaseClaimCRDAvailable, err := crdAvailable(ctx, reader, infraApi.DatabaseClaimCRDName)
	if err != nil {
		return nil, fmt.Errorf("checking DatabaseClaim CRD: %w", err)
	}
	if databaseClaimCRDAvailable && providerCRDAvailable {
		if err := databaseclaim.NewReconciler(ctx, mgr, cfg, databaseclaim.Options{
			Recorder: mgr.GetEventRecorder(infraApi.DatabaseClaimResource),
		}); err != nil {
			return nil, fmt.Errorf("creating databaseclaim reconciler: %w", err)
		}
	} else {
		log.Info("DatabaseClaim reconciliation skipped; requires claim and provider CRDs (restart after install)")
	}

	schemaClaimCRDAvailable, err := crdAvailable(ctx, reader, infraApi.SchemaClaimCRDName)
	if err != nil {
		return nil, fmt.Errorf("checking SchemaClaim CRD: %w", err)
	}
	if schemaClaimCRDAvailable && providerCRDAvailable {
		if err := schemaclaim.NewReconciler(ctx, mgr, cfg, schemaclaim.Options{
			Recorder: mgr.GetEventRecorder(infraApi.SchemaClaimResource),
		}); err != nil {
			return nil, fmt.Errorf("creating schemaclaim reconciler: %w", err)
		}
	} else {
		log.Info("SchemaClaim reconciliation skipped; requires claim and provider CRDs (restart after install)")
	}

	if err := mgr.AddHealthzCheck(healthCheckName, healthz.Ping); err != nil {
		return nil, fmt.Errorf("setting up health check: %w", err)
	}
	if err := mgr.AddReadyzCheck(readyCheckName, healthz.Ping); err != nil {
		return nil, fmt.Errorf("setting up ready check: %w", err)
	}

	return mgr, nil
}

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
