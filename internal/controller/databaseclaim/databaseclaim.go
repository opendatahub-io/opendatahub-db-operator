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

package databaseclaim

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/deploy"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/reconciler"
	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	dbcontroller "github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
)

type Controller struct {
	Options
}

func NewController(cfg *moduleconfig.Config, opts ...Option) *Controller {
	m := &Controller{Options: Options{cfg: cfg, platformRelease: cfg.PlatformRelease()}}
	for _, opt := range opts {
		if opt != nil {
			opt.applyOption(&m.Options)
		}
	}
	return m
}

// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=databaseclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=databaseclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=databaseclaims/finalizers,verbs=update
// SchemaClaims may nest in a DatabaseClaim-created database; cleanup lists them
// before DROP DATABASE so an active SchemaClaim is not orphaned.
// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=schemaclaims,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.opendatahub.io,resources=databaseproviders,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete

func NewReconciler(ctx context.Context, mgr ctrl.Manager, cfg *moduleconfig.Config, opts ...Option) error {
	m := NewController(cfg, opts...)

	// Secret watches belong with RHOAIENG-96275's scoped cache; registering
	// them here would cache Secrets from unrelated namespaces. Credentials
	// Secrets are still created/updated via deploy; drift heals on the
	// claim's default requeue until Owns(Secret) can be reintroduced safely.
	_, err := reconciler.ReconcilerFor(mgr, &infraApi.DatabaseClaim{}).
		Watches(
			&infraApi.DatabaseProvider{},
			reconciler.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
			reconciler.WithEventMapper(
				dbcontroller.BroadcastListMapper(mgr.GetClient(), &infraApi.DatabaseClaimList{}),
			)).
		WithReconcilerOpts(
			reconciler.WithRelease(m.platformRelease),
			reconciler.WithDefaultRequeueAfter(retryInterval(cfg)),
			reconciler.WithFinalizerName(FinalizerName),
		).
		WithFinalizer(m.cleanupAction).
		WithAction(m.provisionAction).
		WithAction(deploy.NewAction(
			deploy.WithCache(),
			deploy.WithApplyOrder(),
			deploy.WithLabel("app.opendatahub.io/db-operator", "true"),
		)).
		WithConditions(
			conditions.Dependent(ConditionProvisioned, conditions.HealthyWhenTrue),
			conditions.Dependent(ConditionTLSConfiguration, conditions.HealthyWhenTrue),
		).
		Build(ctx)

	return err
}
