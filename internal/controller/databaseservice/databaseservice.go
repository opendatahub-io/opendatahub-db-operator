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

// Package databaseservice registers the DatabaseService reconciler.
package databaseservice

import (
	"context"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	fwapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/reconciler"
	servicesv1alpha1 "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
)

// Module holds configuration shared by DatabaseService reconciliations.
type Module struct {
	cfg             *moduleconfig.Config
	platformRelease fwapi.Release
}

// NewModule captures cfg's platform release for reconciliation.
func NewModule(cfg *moduleconfig.Config) *Module {
	return &Module{
		cfg:             cfg,
		platformRelease: cfg.PlatformRelease(),
	}
}

// +kubebuilder:rbac:groups=services.platform.opendatahub.io,resources=databaseservices,verbs=get;list;watch
// +kubebuilder:rbac:groups=services.platform.opendatahub.io,resources=databaseservices/status,verbs=get;update;patch

// Baseline RBAC required for all module operators. The CRD grant is
// deliberately narrower than a typical module operator's: this reconciler
// never creates, updates, or deletes CustomResourceDefinitions, so it only
// needs read access.
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:urls=/metrics,verbs=get

// Framework events use events.k8s.io/v1, so the manager needs create and patch access for events.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// NewReconciler registers the DatabaseService controller with mgr.
func NewReconciler(
	ctx context.Context,
	mgr ctrl.Manager,
	cfg *moduleconfig.Config,
) error {
	m := NewModule(cfg)

	_, err := reconciler.ReconcilerFor(mgr, &servicesv1alpha1.DatabaseService{}).
		WithReconcilerOpts(
			reconciler.WithRelease(m.platformRelease),
			reconciler.WithDefaultRequeueAfter(retryInterval(cfg)),
		).
		WithAction(m.reportStatus).
		WithConditions(conditions.Dependent(fwapi.ConditionType(ConditionProviderHealth), conditions.HealthyWhenTrue)).
		Build(ctx)

	return err
}

// retryInterval returns the configured DatabaseService retry interval,
// falling back to the compiled default if it is unset (e.g. zero-value
// Config in tests).
func retryInterval(cfg *moduleconfig.Config) time.Duration {
	if cfg.DatabaseService.RetryInterval <= 0 {
		return moduleconfig.DefaultRetryInterval
	}
	return cfg.DatabaseService.RetryInterval
}
