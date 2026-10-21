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

	common "github.com/opendatahub-io/odh-platform-utilities/api/common"
	fwapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	odhtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	servicesv1alpha1 "github.com/opendatahub-io/opendatahub-db-operator/api/services/v1alpha1"
	dbmetrics "github.com/opendatahub-io/opendatahub-db-operator/pkg/metrics"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ConditionProviderHealth = "ProviderHealth"
	ConditionServerVersion  = "ServerVersion"
)

func (m *Module) reportStatus(ctx context.Context, rr *odhtypes.ReconciliationRequest) error {
	obj, ok := rr.Instance.(*servicesv1alpha1.DatabaseService)
	if !ok {
		return fmt.Errorf("instance is not a DatabaseService")
	}
	if err := m.aggregateInputs(ctx, rr); err != nil {
		return err
	}
	obj.Status.Releases = []common.ComponentRelease{m.cfg.ComponentRelease()}
	return nil
}

func (m *Module) aggregateInputs(
	ctx context.Context,
	rr *odhtypes.ReconciliationRequest,
) error {
	providers := &infraApi.DatabaseProviderList{}
	if err := rr.Client.List(ctx, providers); err != nil {
		if meta.IsNoMatchError(err) {
			message := "DatabaseProvider CRD is not installed; provider health is not available"
			rr.Conditions.Mark(ConditionProviderHealth, metav1.ConditionFalse,
				conditions.WithSeverity(fwapi.ConditionSeverityInfo),
				conditions.WithReason("DatabaseProviderCRDMissing"),
				conditions.WithMessage(message))
			rr.Conditions.Mark(ConditionServerVersion, metav1.ConditionUnknown,
				conditions.WithSeverity(fwapi.ConditionSeverityInfo),
				conditions.WithReason("DatabaseProviderCRDMissing"),
				conditions.WithMessage("DatabaseProvider CRD is not installed; server versions are not available"))
			rr.Conditions.Mark(fwapi.ConditionTypeDegraded, metav1.ConditionFalse,
				conditions.WithSeverity(fwapi.ConditionSeverityInfo),
				conditions.WithReason("DatabaseProviderCRDMissing"),
				conditions.WithMessage(message))
			return nil
		}
		return fmt.Errorf("listing DatabaseProviders: %w", err)
	}
	slices.SortFunc(providers.Items, func(a, b infraApi.DatabaseProvider) int {
		return strings.Compare(a.Name, b.Name)
	})
	currentProviders := make([]string, 0, len(providers.Items))
	for _, provider := range providers.Items {
		currentProviders = append(currentProviders, provider.Name)
	}
	dbmetrics.PruneProviderMetrics(currentProviders)

	if len(providers.Items) == 0 {
		message := "No DatabaseProvider resources exist; create a provider to make this service operational"
		rr.Conditions.Mark(ConditionProviderHealth, metav1.ConditionFalse,
			conditions.WithSeverity(fwapi.ConditionSeverityInfo),
			conditions.WithReason("NoProvidersConfigured"),
			conditions.WithMessage(message))
		rr.Conditions.Mark(ConditionServerVersion, metav1.ConditionUnknown,
			conditions.WithSeverity(fwapi.ConditionSeverityInfo),
			conditions.WithReason("NoProvidersConfigured"),
			conditions.WithMessage("No DatabaseProvider resources exist; server versions are not available"))
		rr.Conditions.Mark(fwapi.ConditionTypeDegraded, metav1.ConditionFalse,
			conditions.WithSeverity(fwapi.ConditionSeverityInfo),
			conditions.WithReason("NoProvidersConfigured"),
			conditions.WithMessage(message))
		return nil
	}

	unreachableMessages := make([]string, 0)
	outOfRangeMessages := make([]string, 0)
	for i := range providers.Items {
		provider := &providers.Items[i]
		reachable := conditions.FindStatusCondition(provider, "Reachable")
		if reachable == nil || reachable.Status != metav1.ConditionTrue {
			detail := "provider reachability is unknown"
			if reachable != nil && reachable.Message != "" {
				detail = reachable.Message
			}
			unreachableMessages = append(unreachableMessages, fmt.Sprintf(
				"DatabaseProvider %q is unreachable: %s. Check its connection settings.",
				provider.Name,
				detail,
			))
		}

		version := conditions.FindStatusCondition(provider, "ServerVersion")
		if version != nil && version.Status == metav1.ConditionFalse {
			detail := "server version is outside the tested range"
			if version.Message != "" {
				detail = version.Message
			}
			outOfRangeMessages = append(outOfRangeMessages, fmt.Sprintf("DatabaseProvider %q reports %s", provider.Name, detail))
		}
	}

	if len(unreachableMessages) > 0 {
		message := strings.Join(unreachableMessages, "; ")
		rr.Conditions.Mark(ConditionProviderHealth, metav1.ConditionFalse,
			conditions.WithReason("ProviderUnreachable"),
			conditions.WithMessage(message))
		rr.Conditions.Mark(fwapi.ConditionTypeDegraded, metav1.ConditionTrue,
			conditions.WithReason("ProviderUnreachable"),
			conditions.WithMessage(message))
	} else {
		message := "All DatabaseProvider resources are reachable"
		rr.Conditions.Mark(ConditionProviderHealth, metav1.ConditionTrue,
			conditions.WithReason("ProvidersReachable"),
			conditions.WithMessage(message))
		rr.Conditions.Mark(fwapi.ConditionTypeDegraded, metav1.ConditionFalse,
			conditions.WithReason("ProvidersReachable"),
			conditions.WithMessage(message))
	}

	if len(outOfRangeMessages) > 0 {
		rr.Conditions.Mark(ConditionServerVersion, metav1.ConditionFalse,
			conditions.WithSeverity(fwapi.ConditionSeverityInfo),
			conditions.WithReason("ServerVersionOutsideTestedRange"),
			conditions.WithMessage(strings.Join(outOfRangeMessages, "; ")))
	} else {
		rr.Conditions.Mark(ConditionServerVersion, metav1.ConditionTrue,
			conditions.WithSeverity(fwapi.ConditionSeverityInfo),
			conditions.WithReason("ServerVersionsWithinTestedRange"),
			conditions.WithMessage("No DatabaseProvider reports a server version outside the tested range"))
	}

	return nil
}
