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
	"fmt"
	"slices"
	"strings"
	"time"

	fwapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/deploy"
	odherrors "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/errors"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	odhtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesv1alpha1 "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
)

// ConditionOperandsReady reports whether the module operand CRDs are
// installed and the infrastructure controllers are registered.
const ConditionOperandsReady = "OperandsReady"

// operandPartOf overrides the framework's default part-of label value on
// applied operands. The label marks resource provenance for inspection
// (kubectl get -l); pruning does not select by it — the finalizer deletes
// exactly the rendered operand set, so what installed also prunes.
const operandPartOf = "opendatahub-db-operator"

const (
	reasonCRDsEstablishing         = "CRDsEstablishing"
	reasonOperandsEstablished      = "OperandsEstablished"
	reasonOperandRegistrationError = "OperandRegistrationFailed"
	reasonOperandDeployError       = "OperandDeployFailed"
)

// operandRemovalPollInterval and operandRemovalPollBudget bound the
// in-reconcile wait for operand deletion (CRD termination cascades
// instances asynchronously). The budget is a floor, not a deadline: when it
// is exhausted the finalizer returns a plain error and the requeue retries
// the whole prune, so a wedged cluster still blocks removal visibly
// instead of passing vacuously.
const (
	operandRemovalPollInterval = 2 * time.Second
	operandRemovalPollBudget   = 45 * time.Second
)

// operandCRDGroupKinds lists the infrastructure CRDs the module installs,
// in the order the establishment gate checks them.
var operandCRDGroupKinds = []schema.GroupKind{
	{Group: infraApi.GroupName, Kind: "DatabaseProvider"},
	{Group: infraApi.GroupName, Kind: "DatabaseClaim"},
	{Group: infraApi.GroupName, Kind: "SchemaClaim"},
}

// operandRoleBindingGVK identifies the operand RoleBinding whose subject is
// finalized from configuration at apply time.
var operandRoleBindingGVK = schema.GroupVersionKind{
	Group:   "rbac.authorization.k8s.io",
	Version: "v1",
	Kind:    "RoleBinding",
}

// operandRoleBindingName is the operand RoleBinding the apply customizer
// finalizes; scoped by name so other RoleBindings in the operand set are
// not affected.
const operandRoleBindingName = "internal-provider-writer"

// initOperandManifests points the render action at the embedded operand
// kustomization. operands.FS() roots the engine at the manifests directory,
// so the kustomization resolves at the filesystem root.
func (m *Module) initOperandManifests(_ context.Context, rr *odhtypes.ReconciliationRequest) error {
	rr.Manifests = []odhtypes.ManifestInfo{{Path: "."}}
	return nil
}

// customizeOperandRoleBinding finalizes the operand RoleBinding's subject.
// The operator's own ServiceAccount name is deployment dependent (chart
// fullname or kustomize namePrefix), so the manifest carries a placeholder
// that only runtime configuration can resolve. Scoped by name as well as
// GVK so a future RoleBinding in the operand set is not silently
// overwritten.
func (m *Module) customizeOperandRoleBinding(
	_ context.Context,
	_ client.Client,
	_ *deploy.Action,
	obj *unstructured.Unstructured,
	_ *unstructured.Unstructured,
) error {
	if obj.GetName() != operandRoleBindingName {
		return nil
	}
	if m.cfg.OperatorServiceAccount == "" {
		return fmt.Errorf("operator-service-account must be set to bind the operand RoleBinding's subject")
	}
	subjects, found, err := unstructured.NestedSlice(obj.Object, "subjects")
	if err != nil || !found || len(subjects) == 0 {
		return fmt.Errorf("operand RoleBinding has no subjects to finalize")
	}
	subject, ok := subjects[0].(map[string]any)
	if !ok {
		return fmt.Errorf("operand RoleBinding subject is not an object")
	}
	subject["name"] = m.cfg.OperatorServiceAccount
	subject["namespace"] = m.cfg.OperatorNamespace
	return unstructured.SetNestedSlice(obj.Object, subjects, "subjects")
}

// deployOperands wraps the operand deploy action so a failed apply is
// visible on the condition users watch for operand readiness, for example
// when the CRD RBAC has not caught up on an upgraded cluster. The error is
// returned unchanged for the framework's requeue handling.
func (m *Module) deployOperands(apply actions.Fn) actions.Fn {
	return func(ctx context.Context, rr *odhtypes.ReconciliationRequest) error {
		if err := apply(ctx, rr); err != nil {
			rr.Conditions.MarkFalse(ConditionOperandsReady,
				conditions.WithReason(reasonOperandDeployError),
				conditions.WithMessagef("deploying operand manifests: %s", err))
			return err
		}
		return nil
	}
}

// awaitOperandsEstablished gates the action chain on all operand CRDs being
// Established, then registers the infrastructure controllers. Registration
// runs on every established reconcile; the injected closure owns
// idempotency, so repeat calls are no-ops there.
func (m *Module) awaitOperandsEstablished(ctx context.Context, rr *odhtypes.ReconciliationRequest) error {
	reader := m.apiReader
	if reader == nil {
		reader = rr.Client
	}
	for _, gk := range operandCRDGroupKinds {
		// The helper checks existence and Established=True with a short
		// internal backoff, so the post-apply happy path normally passes
		// without a requeue.
		if err := cluster.CustomResourceDefinitionExists(ctx, reader, gk); err != nil {
			message := fmt.Sprintf("waiting for %s CRD: %v", gk.Kind, err)
			rr.Conditions.MarkFalse(ConditionOperandsReady,
				conditions.WithReason(reasonCRDsEstablishing),
				conditions.WithMessage(message))
			return odherrors.NewStopErrorW(fmt.Errorf("%s", message)).
				WithRequeueAfter(retryInterval(m.cfg))
		}
	}

	if err := m.registerInfra(); err != nil {
		message := fmt.Sprintf("registering infrastructure controllers: %s", err)
		rr.Conditions.MarkFalse(ConditionOperandsReady,
			conditions.WithReason(reasonOperandRegistrationError),
			conditions.WithMessage(message))
		return fmt.Errorf("%s", message)
	}

	rr.Conditions.MarkTrue(ConditionOperandsReady,
		conditions.WithReason(reasonOperandsEstablished),
		conditions.WithMessage("infrastructure CRDs installed and established"))
	return nil
}

// cleanupOperands prunes the module operand resources when the
// DatabaseService is removed. It runs after cleanupDefaultProvider, so the
// default provider and its data-retention steps complete first. Deleting
// the operand CRDs cascades every DatabaseClaim, SchemaClaim, and
// DatabaseProvider, so any remaining claim blocks removal — and so does any
// remaining provider: by finalizer order the default provider is already
// gone with its retention steps, so whatever is left is user-created, and
// cascading it would garbage-collect its Internal storage without an
// explicit user action. Once nothing remains, the finalizer deletes exactly
// the rendered operand set — the same manifests the install path applies,
// so what installed also prunes — and only returns nil once every operand
// object is gone. Blocking states return plain errors: a StopError here
// would strip the finalizer and delete the singleton while skipping the
// remaining cleanup.
//
// The gates protect state that exists when removal begins. Creates racing
// the removal are bounded by the API server itself: once a CRD deletion is
// issued it rejects further creates of that kind for as long as the CRD
// terminates, so the exposure is limited to the milliseconds between the
// gate lists and the delete calls within this reconcile. Blocking creates
// earlier would require admission control, which this module deliberately
// does not depend on (webhooks are optional in install profiles without
// cert-manager); a create admitted inside that window is swept by the CRD
// cascade, consistent with Kubernetes' own teardown semantics.
func (m *Module) cleanupOperands(ctx context.Context, rr *odhtypes.ReconciliationRequest) error {
	service, ok := rr.Instance.(*servicesv1alpha1.DatabaseService)
	if !ok {
		return fmt.Errorf("instance is not a DatabaseService")
	}

	claims, err := m.blockingOperandClaims(ctx, rr)
	if err != nil {
		return err
	}
	if len(claims) != 0 {
		message := fmt.Sprintf("operand removal is blocked by claims: %s", strings.Join(claims, ", "))
		conditions.SetStatusCondition(service, fwapi.Condition{
			Type: ConditionOperandsReady, Status: metav1.ConditionFalse,
			Reason: "BlockingClaims", Message: message,
		})
		if err := rr.Client.Status().Update(ctx, service); err != nil {
			return fmt.Errorf("reporting claims blocking operand removal: %w", err)
		}
		return fmt.Errorf("%s", message)
	}

	providers, err := m.blockingOperandProviders(ctx, rr)
	if err != nil {
		return err
	}
	if len(providers) != 0 {
		message := fmt.Sprintf(
			"operand removal is blocked by providers: %s — deleting the DatabaseProvider CRD cascades them and their storage; delete the providers first",
			strings.Join(providers, ", "))
		conditions.SetStatusCondition(service, fwapi.Condition{
			Type: ConditionOperandsReady, Status: metav1.ConditionFalse,
			Reason: "BlockingProviders", Message: message,
		})
		if err := rr.Client.Status().Update(ctx, service); err != nil {
			return fmt.Errorf("reporting providers blocking operand removal: %w", err)
		}
		return fmt.Errorf("%s", message)
	}

	// The finalizer request starts with empty rr.Manifests; point it at the
	// embedded kustomization before rendering, or the render produces
	// nothing and the deletion loop would silently succeed.
	if err := m.initOperandManifests(ctx, rr); err != nil {
		return err
	}
	if err := m.renderOperands(ctx, rr); err != nil {
		return fmt.Errorf("rendering operand manifests for removal: %w", err)
	}
	if len(rr.Resources) == 0 {
		return fmt.Errorf("rendered 0 operand resources — refusing to prune vacuously and orphan every operand")
	}

	for i := range rr.Resources {
		obj := rr.Resources[i].DeepCopy()
		if err := rr.Client.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting operand %s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}

	// Operand deletion is asynchronous — the CRDs wait on their cleanup
	// finalizer while instances cascade — so verify removal with a bounded
	// in-reconcile poll. Returning a plain error per attempt would requeue
	// on the controller's exponential backoff, which the blocking phases
	// above have already inflated: after a long-blocked removal the drain
	// would then stretch a seconds-long termination into minutes. The poll
	// absorbs the drain in this reconcile; a plain error is still returned
	// when the budget is exhausted, so the requeue path stays as designed.
	if err := wait.PollUntilContextTimeout(ctx, operandRemovalPollInterval, operandRemovalPollBudget, true,
		func(ctx context.Context) (bool, error) {
			for i := range rr.Resources {
				obj := rr.Resources[i].DeepCopy()
				err := rr.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj)
				if err == nil {
					return false, nil // still terminating
				}
				if !apierrors.IsNotFound(err) {
					return false, fmt.Errorf("getting operand %s %s after deletion: %w",
						obj.GetKind(), obj.GetName(), err)
				}
			}
			return true, nil
		}); err != nil {
		// The poll error carries the cause — context.DeadlineExceeded when
		// the budget ran out, the condition's hard error (for example a
		// Forbidden GET), or context.Canceled during shutdown. Wrap it:
		// discarding it would report an empty operand list and hide what
		// actually blocked the removal. The still-present operands are
		// supplementary detail for the timeout case.
		remaining := make([]string, 0, len(rr.Resources))
		for i := range rr.Resources {
			obj := rr.Resources[i].DeepCopy()
			if getErr := rr.Client.Get(ctx, client.ObjectKeyFromObject(obj), obj); getErr == nil {
				remaining = append(remaining, obj.GetKind()+" "+obj.GetName())
			}
		}
		if len(remaining) > 0 {
			return fmt.Errorf("waiting for operands to be removed: %w; still present: %s",
				err, strings.Join(remaining, ", "))
		}
		return fmt.Errorf("waiting for operands to be removed: %w", err)
	}
	return nil
}

// blockingOperandClaims lists every DatabaseClaim and SchemaClaim in the
// cluster. Unlike the default-provider gate, which matches only claims
// bound to the default provider, the operand gate blocks on any claim:
// deleting the operand CRDs cascades all instances.
func (m *Module) blockingOperandClaims(ctx context.Context, rr *odhtypes.ReconciliationRequest) ([]string, error) {
	reader := m.apiReader
	if reader == nil {
		reader = rr.Client
	}
	databases := &infraApi.DatabaseClaimList{}
	if err := listToleratingMissingCRD(ctx, reader, databases, infraApi.DatabaseClaimCRDName); err != nil {
		return nil, fmt.Errorf("listing DatabaseClaims before operand removal: %w", err)
	}
	schemas := &infraApi.SchemaClaimList{}
	if err := listToleratingMissingCRD(ctx, reader, schemas, infraApi.SchemaClaimCRDName); err != nil {
		return nil, fmt.Errorf("listing SchemaClaims before operand removal: %w", err)
	}
	names := make([]string, 0, len(databases.Items)+len(schemas.Items))
	for _, claim := range databases.Items {
		names = append(names, fmt.Sprintf("DatabaseClaim %s/%s", claim.Namespace, claim.Name))
	}
	for _, claim := range schemas.Items {
		names = append(names, fmt.Sprintf("SchemaClaim %s/%s", claim.Namespace, claim.Name))
	}
	slices.Sort(names)
	return names, nil
}

// blockingOperandProviders lists every DatabaseProvider in the cluster.
// cleanupDefaultProvider runs first and removes the default provider with
// its data-retention steps, so anything left here is user-created: pruning
// the DatabaseProvider CRD would cascade it and garbage-collect its
// Internal storage — destroying data the user never asked to delete. The
// operator's own deletion of a provider (only the default one) retains
// storage; a user deleting their own provider is an explicit act with the
// provider lifecycle's established semantics. Removal stays blocked until
// the user deletes their providers.
func (m *Module) blockingOperandProviders(ctx context.Context, rr *odhtypes.ReconciliationRequest) ([]string, error) {
	reader := m.apiReader
	if reader == nil {
		reader = rr.Client
	}
	providers := &infraApi.DatabaseProviderList{}
	if err := listToleratingMissingCRD(ctx, reader, providers, infraApi.DatabaseProviderCRDName); err != nil {
		return nil, fmt.Errorf("listing DatabaseProviders before operand removal: %w", err)
	}
	names := make([]string, 0, len(providers.Items))
	for _, provider := range providers.Items {
		names = append(names, fmt.Sprintf("DatabaseProvider %s", provider.Name))
	}
	slices.Sort(names)
	return names, nil
}

// listToleratingMissingCRD lists into obj, treating a kind whose CRD is
// genuinely absent as an empty list. Neither failure mode is by itself
// evidence of that: while a CRD is terminating — or has been recreated and
// is not yet served — it can drop out of discovery (the RESTMapper then
// reports NoMatch) or stop serving its resource path (a 404) while
// instances still remain, and on HA control planes an apiserver can
// briefly lag a just-served CRD. Both gates built on this list guard
// irreversible deletions, so either error is only trusted once the CRD
// object itself is confirmed gone; while it exists the caller returns an
// error and retries, letting draining instances (or the CRD's eventual
// absence) settle the question.
func listToleratingMissingCRD(ctx context.Context, cli client.Reader, into client.ObjectList, crdName string) error {
	err := cli.List(ctx, into)
	switch {
	case err == nil:
		return nil
	case meta.IsNoMatchError(err), apierrors.IsNotFound(err):
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if crdErr := cli.Get(ctx, client.ObjectKey{Name: crdName}, crd); crdErr == nil {
			return fmt.Errorf("resource is not served but CRD %s still exists (terminating or establishing)", crdName)
		} else if !apierrors.IsNotFound(crdErr) {
			return fmt.Errorf("getting CRD %s after its resource was not found: %w", crdName, crdErr)
		}
		return nil
	default:
		return err
	}
}
