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
	"errors"
	"fmt"

	"github.com/opendatahub-io/odh-platform-utilities/framework/controller/conditions"
	odhtypes "github.com/opendatahub-io/odh-platform-utilities/framework/controller/types"
	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
	dbcontroller "github.com/opendatahub-io/opendatahub-db-operator/pkg/controller"
	dbmetrics "github.com/opendatahub-io/opendatahub-db-operator/pkg/metrics"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ConditionReachable        = "Reachable"
	ConditionTLSConfiguration = dbcontroller.ConditionTLSConfiguration
	ConditionServerVersion    = "ServerVersion"
)

type ErrConnectionSecretUnavailable struct {
	Ref   corev1.SecretReference
	Cause error
}

func (e ErrConnectionSecretUnavailable) Error() string {
	return fmt.Sprintf(
		"connection Secret %s/%s is unavailable: %v. Verify that the referenced admin Secret exists in the configured namespace",
		e.Ref.Namespace,
		e.Ref.Name,
		e.Cause,
	)
}
func (e ErrConnectionSecretUnavailable) Unwrap() error { return e.Cause }

type ErrConnectionSecretInvalid struct {
	Name  string
	Cause error
}

func (e ErrConnectionSecretInvalid) Error() string {
	return fmt.Sprintf(
		"connection Secret %q is invalid: %v. Correct the admin Secret's connection fields and retry",
		e.Name,
		e.Cause,
	)
}
func (e ErrConnectionSecretInvalid) Unwrap() error { return e.Cause }

func loadExternalConfig(ctx context.Context, cli client.Client, provider *infraApi.DatabaseProvider) (postgres.Config, error) {
	if provider.Spec.External == nil {
		return postgres.Config{}, ErrConnectionSecretInvalid{Name: provider.Name, Cause: fmt.Errorf("spec.external is required for External providers")}
	}
	ref := provider.Spec.External.ConnectionSecretRef
	secret := &corev1.Secret{}
	if err := cli.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, secret); err != nil {
		return postgres.Config{}, ErrConnectionSecretUnavailable{Ref: ref, Cause: err}
	}
	cfg, err := postgres.ParseAdminSecret(secret.Data)
	if err != nil {
		return postgres.Config{}, ErrConnectionSecretInvalid{Name: client.ObjectKeyFromObject(secret).String(), Cause: err}
	}
	if cfg.DBName == "" {
		cfg.DBName = dbcontroller.ProviderDefaultDatabase(provider)
	}
	if err := cfg.Validate(); err != nil {
		return postgres.Config{}, ErrConnectionSecretInvalid{Name: client.ObjectKeyFromObject(secret).String(), Cause: err}
	}
	if cfg.SSLMode == "" {
		cfg.SSLMode = postgres.SSLModeRequire
	}
	return cfg, nil
}

func externalFailureReason(err error) string {
	if _, ok := errors.AsType[ErrConnectionSecretUnavailable](err); ok {
		return "ConnectionSecretUnavailable"
	}
	if _, ok := errors.AsType[ErrConnectionSecretInvalid](err); ok {
		return "ConnectionSecretInvalid"
	}
	return "ConnectionCheckFailed"
}

func providerConnectionStatus(cfg postgres.Config) infraApi.ProviderConnectionStatus {
	return infraApi.ProviderConnectionStatus{Host: cfg.Host, Port: int32(cfg.Port), Database: cfg.DBName}
}

func markReachable(
	rr *odhtypes.ReconciliationRequest,
	provider *infraApi.DatabaseProvider,
	status metav1.ConditionStatus,
	opts ...conditions.Option,
) {
	rr.Conditions.Mark(ConditionReachable, status, opts...)
	dbmetrics.SetProviderReachable(provider.Name, status == metav1.ConditionTrue)
}

const (
	reasonTLSNotEnabled   = dbcontroller.ReasonTLSNotEnabled
	reasonTLSProvisioning = dbcontroller.ReasonTLSProvisioning
	reasonTLSConfigured   = dbcontroller.ReasonTLSConfigured
)

const (
	reasonAdminSecretUnavailable = "AdminSecretUnavailable"
	reasonInstanceRunning        = "InstanceRunning"
	reasonProvisioning           = "Provisioning"
)
