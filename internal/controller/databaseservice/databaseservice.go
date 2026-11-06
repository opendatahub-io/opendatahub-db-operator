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
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	fwapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/deploy"
	renderKustomize "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/render/kustomize"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/predicates/dependent"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/reconciler"
	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesv1alpha1 "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	"github.com/opendatahub-io/opendatahub-db-operator/internal/controller/databaseservice/operands"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	dbcontroller "github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/resources/gvk"
)

// Module holds configuration shared by DatabaseService reconciliations.
type Module struct {
	cfg             *moduleconfig.Config
	platformRelease fwapi.Release
	apiReader       client.Reader

	// registerInfra registers the infrastructure controllers once the
	// operand CRDs are established; see Options.RegisterInfrastructure.
	registerInfra func() error
}

// NewModule captures cfg's platform release for reconciliation.
func NewModule(cfg *moduleconfig.Config) *Module {
	return &Module{
		cfg:             cfg,
		platformRelease: cfg.PlatformRelease(),
	}
}

// +kubebuilder:rbac:groups=services.platform.opendatahub.io,resources=databaseservices,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=services.platform.opendatahub.io,resources=databaseservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=services.platform.opendatahub.io,resources=databaseservices/finalizers,verbs=update
// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=databaseproviders,verbs=get;list;watch;create;update;delete
// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=schemaclaims;databaseclaims,verbs=list;watch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims;secrets,verbs=get;patch

// The reconciler installs the infrastructure CRDs as module operands via
// server-side apply. Applying a CRD that does not exist yet authorizes as
// create (verified against a live API server; RBAC cannot scope create by
// resource name), while re-applying an existing one authorizes as patch and
// is scoped to the three operand CRD names. The delete verb arrives with
// the operand pruning finalizer.
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=apiextensions.k8s.io,resources=customresourcedefinitions,resourceNames=databaseproviders.infrastructure.opendatahub.io;databaseclaims.infrastructure.opendatahub.io;schemaclaims.infrastructure.opendatahub.io,verbs=patch
// +kubebuilder:rbac:groups=authentication.k8s.io,resources=tokenreviews,verbs=create
// +kubebuilder:rbac:groups=authorization.k8s.io,resources=subjectaccessreviews,verbs=create
// +kubebuilder:rbac:urls=/metrics,verbs=get

// Framework events use events.k8s.io/v1, so the manager needs create and patch access for events.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Options carries collaborators the DatabaseService reconciler requires
// beyond its manager and configuration.
type Options struct {
	// RegisterInfrastructure registers the DatabaseProvider, DatabaseClaim,
	// and SchemaClaim controllers on the running manager.
	// awaitOperandsEstablished invokes it on every reconcile whose operand
	// CRDs are established; the function must be idempotent, as
	// pkg/manager.NewInfrastructureRegistration guarantees.
	RegisterInfrastructure func() error
}

// NewReconciler registers the DatabaseService controller with mgr.
func NewReconciler(
	ctx context.Context,
	mgr ctrl.Manager,
	cfg *moduleconfig.Config,
	opts Options,
) error {
	if opts.RegisterInfrastructure == nil {
		return fmt.Errorf("infrastructure controller registration is required")
	}

	m := NewModule(cfg)
	m.apiReader = mgr.GetAPIReader()
	m.registerInfra = opts.RegisterInfrastructure

	operandFS, err := operands.FS()
	if err != nil {
		return fmt.Errorf("preparing operand manifest filesystem: %w", err)
	}

	_, err = reconciler.ReconcilerFor(mgr, &servicesv1alpha1.DatabaseService{}).
		// Provider create and status changes re-aggregate the singleton immediately.
		// The status-aware predicate is required because ResourceVersion changes can
		// arrive before the cached list used by aggregation has observed the status.
		Watches(&infraApi.DatabaseProvider{},
			reconciler.Dynamic(reconciler.CrdExists(gvk.DatabaseProvider)),
			reconciler.WithEventMapper(func(_ context.Context, _ client.Object) []reconcile.Request {
				return []reconcile.Request{{
					NamespacedName: client.ObjectKey{Name: servicesv1alpha1.DatabaseServiceInstanceName},
				}}
			}),
			reconciler.WithPredicates(predicate.Or(
				predicate.ResourceVersionChangedPredicate{},
				dependent.New(dependent.WithWatchStatus(true)),
			))).
		Watches(&infraApi.DatabaseClaim{},
			reconciler.Dynamic(reconciler.CrdExists(gvk.DatabaseClaim)),
			reconciler.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			reconciler.WithEventMapper(dbcontroller.BroadcastListMapper(mgr.GetClient(), &servicesv1alpha1.DatabaseServiceList{}))).
		Watches(&infraApi.SchemaClaim{},
			reconciler.Dynamic(reconciler.CrdExists(gvk.SchemaClaim)),
			reconciler.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			reconciler.WithEventMapper(dbcontroller.BroadcastListMapper(mgr.GetClient(), &servicesv1alpha1.DatabaseServiceList{}))).
		WithReconcilerOpts(
			reconciler.WithRelease(m.platformRelease),
			reconciler.WithDefaultRequeueAfter(retryInterval(cfg)),
			reconciler.WithFinalizerName(defaultProviderCleanupFinalizer),
		).
		WithFinalizer(m.cleanupDefaultProvider).
		WithAction(m.reportStatus).
		WithAction(m.initOperandManifests).
		WithAction(renderKustomize.NewAction(
			renderKustomize.WithEngineFS(operandFS),
			renderKustomize.WithCache(true),
		)).
		WithAction(m.deployOperands(deploy.NewAction(
			deploy.WithMode(deploy.ModeSSA),
			deploy.WithPartOfLabelDefault(operandPartOf),
			deploy.WithApplyOrder(),
			deploy.WithCache(),
		))).
		WithAction(m.awaitOperandsEstablished).
		WithAction(m.ensureDefaultProvider).
		// Ready tracks module infrastructure (providers + default-provider
		// management). ClaimHealth stays on status and Degraded so tenant
		// claim bind failures are visible without taking the module out of
		// Ready for every other consumer.
		WithConditions(conditions.Dependent(fwapi.ConditionType(ConditionProviderHealth), conditions.HealthyWhenTrue)).
		WithConditions(conditions.Dependent(ConditionOperandsReady, conditions.HealthyWhenTrue)).
		WithConditions(conditions.Dependent(conditionDefaultProviderReady, conditions.HealthyWhenTrue)).
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
