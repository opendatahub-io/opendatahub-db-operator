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

package databaseprovider

import (
	"context"
	"fmt"
	"strings"

	api "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	odhtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	platformtls "github.com/opendatahub-io/odh-platform-utilities/pkg/tls"
	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	moduleconfig "github.com/opendatahub-io/opendatahub-db-operator/pkg/config"
	dbcontroller "github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (m *Controller) reconcileExternalAction(ctx context.Context, rr *odhtypes.ReconciliationRequest) error {
	obj, ok := rr.Instance.(*infraApi.DatabaseProvider)
	if !ok {
		return fmt.Errorf("instance is not a DatabaseProvider")
	}
	if obj.Spec.Type != infraApi.ProviderTypeExternal {
		return nil
	}

	cfg, err := loadExternalConfig(ctx, rr.Client, obj)
	if err != nil {
		obj.Status.Connection = infraApi.ProviderConnectionStatus{}
		obj.Status.TLS = nil
		obj.Status.ServerVersion = ""
		reason := externalFailureReason(err)
		message := err.Error()
		switch reason {
		case "ConnectionSecretUnavailable":
			message += ". Verify that the referenced admin Secret exists in the configured namespace."
		case "ConnectionSecretInvalid":
			message += ". Correct the admin Secret's connection fields and retry."
		}
		rr.Conditions.Mark(ConditionReachable, metav1.ConditionFalse, conditions.WithReason(reason), conditions.WithMessage(message))
		rr.Conditions.Mark(ConditionTLSConfiguration, metav1.ConditionUnknown, conditions.WithReason(reason), conditions.WithMessage(message))
		rr.Conditions.Mark(ConditionServerVersion, metav1.ConditionUnknown, conditions.WithReason(reason), conditions.WithMessage("Server version is unavailable until a connection can be verified"))
		return nil
	}
	obj.Status.Connection = providerConnectionStatus(cfg)
	obj.Status.TLS = &infraApi.ProviderTLSStatus{Enabled: cfg.TLSEnabled(), Ready: cfg.TLSReady()}
	certManagerMissing := false
	unsupportedCiphers := []string(nil)
	if cfg.TLSEnabled() {
		profile, err := platformtls.Load(ctx, rr.Client)
		if err != nil {
			return m.markConnectionFailure(rr, cfg, fmt.Errorf("loading cluster TLS profile: %w", err))
		}
		mutator, unsupported := platformtls.ConfigFromProfile(profile.Spec)
		cfg.TLSConfigMutator = mutator
		unsupportedCiphers = unsupported
		available, err := dbcontroller.CertManagerAvailable(ctx, rr.Client)
		if err != nil {
			return fmt.Errorf("checking cert-manager availability: %w", err)
		}
		if !available {
			certManagerMissing = true
		}
	}
	switch {
	case !cfg.TLSEnabled():
		rr.Conditions.Mark(ConditionTLSConfiguration, metav1.ConditionFalse, conditions.WithSeverity(api.ConditionSeverityInfo), conditions.WithReason(reasonTLSNotEnabled), conditions.WithMessage("TLS is not enabled for this external provider"))
	case cfg.TLSReady():
		message := "External provider TLS configuration resolved"
		if len(unsupportedCiphers) > 0 {
			message = fmt.Sprintf("%s; ignored unsupported cluster TLS ciphers: %s", message, strings.Join(unsupportedCiphers, ", "))
		}
		rr.Conditions.Mark(ConditionTLSConfiguration, metav1.ConditionTrue, conditions.WithReason(reasonTLSConfigured), conditions.WithMessage(message))
	default:
		rr.Conditions.Mark(ConditionTLSConfiguration, metav1.ConditionFalse, conditions.WithReason(reasonTLSProvisioning), conditions.WithMessage("External provider TLS configuration is pending"))
	}
	if certManagerMissing {
		dbcontroller.MarkCertManagerMissing(rr)
	}
	factory := m.PostgresClientFactory
	if factory == nil {
		factory = postgres.DefaultClientFactory
	}
	pgClient, err := factory(ctx, cfg)
	if err != nil {
		return m.markConnectionFailure(rr, cfg, err)
	}
	defer pgClient.Close()
	if err := pgClient.Ping(ctx); err != nil {
		return m.markConnectionFailure(rr, cfg, err)
	}
	version, err := postgres.ServerVersionNum(ctx, pgClient)
	if err != nil {
		return m.markConnectionFailure(rr, cfg, err)
	}
	obj.Status.ServerVersion = postgres.FormatServerVersion(version)
	if version < moduleconfig.TestedPostgresVersionMin || version > moduleconfig.TestedPostgresVersionMax {
		rr.Conditions.Mark(ConditionServerVersion, metav1.ConditionFalse,
			conditions.WithReason("ServerVersionOutsideTestedRange"),
			conditions.WithMessagef("PostgreSQL %s is outside the tested range (13 through 17); connections and claims remain allowed", obj.Status.ServerVersion))
	} else {
		rr.Conditions.Mark(ConditionServerVersion, metav1.ConditionTrue,
			conditions.WithReason("ServerVersionSupported"),
			conditions.WithMessagef("PostgreSQL %s is within the tested range", obj.Status.ServerVersion))
	}
	rr.Conditions.Mark(ConditionReachable, metav1.ConditionTrue, conditions.WithReason("ConnectionVerified"), conditions.WithMessage("Connection verified"))
	return nil
}

func (m *Controller) markConnectionFailure(rr *odhtypes.ReconciliationRequest, cfg postgres.Config, err error) error {
	err = postgres.SanitizeError(err, cfg.Password)
	if obj, ok := rr.Instance.(*infraApi.DatabaseProvider); ok {
		obj.Status.ServerVersion = ""
	}
	rr.Conditions.Mark(ConditionServerVersion, metav1.ConditionUnknown,
		conditions.WithReason(externalFailureReason(err)),
		conditions.WithMessage("Server version is unavailable until a connection can be verified"))
	rr.Conditions.Mark(ConditionReachable, metav1.ConditionFalse,
		conditions.WithReason(externalFailureReason(err)),
		conditions.WithMessagef("%s. Verify the admin Secret and that PostgreSQL is reachable from the operator namespace.", err.Error()))
	if retryErr := dbcontroller.StopWithQuickRetryIfConnectionRefused(err); retryErr != nil {
		return retryErr
	}
	return nil
}
