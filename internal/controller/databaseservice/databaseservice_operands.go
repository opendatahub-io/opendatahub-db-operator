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

	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions"
	odherrors "github.com/opendatahub-io/odh-platform-utilities/framework/controller/actions/errors"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	odhtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	"github.com/opendatahub-io/odh-platform-utilities/pkg/cluster"
	"k8s.io/apimachinery/pkg/runtime/schema"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
)

// ConditionOperandsReady reports whether the module operand CRDs are
// installed and the infrastructure controllers are registered.
const ConditionOperandsReady = "OperandsReady"

// operandPartOf overrides the framework's default part-of label value on
// applied operands so the operand-pruning finalizer can select them.
const operandPartOf = "opendatahub-db-operator"

const (
	reasonCRDsEstablishing         = "CRDsEstablishing"
	reasonOperandsEstablished      = "OperandsEstablished"
	reasonOperandRegistrationError = "OperandRegistrationFailed"
	reasonOperandDeployError       = "OperandDeployFailed"
)

// operandCRDGroupKinds lists the infrastructure CRDs the module installs,
// in the order the establishment gate checks them.
var operandCRDGroupKinds = []schema.GroupKind{
	{Group: infraApi.GroupName, Kind: "DatabaseProvider"},
	{Group: infraApi.GroupName, Kind: "DatabaseClaim"},
	{Group: infraApi.GroupName, Kind: "SchemaClaim"},
}

// initOperandManifests points the render action at the embedded operand
// kustomization. operands.FS() roots the engine at the manifests directory,
// so the kustomization resolves at the filesystem root.
func (m *Module) initOperandManifests(_ context.Context, rr *odhtypes.ReconciliationRequest) error {
	rr.Manifests = []odhtypes.ManifestInfo{{Path: "."}}
	return nil
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
