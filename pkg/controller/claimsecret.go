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

package controller

import (
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	fwapi "github.com/opendatahub-io/odh-platform-utilities/framework/api"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/postgres"
	"github.com/opendatahub-io/opendatahub-db-operator/pkg/resources/gvk"
)

// ApplyClaimCredentials populates an Opaque postgres Connection Secret with the
// ADR credential contract: discrete keys, connection-type annotation, and
// always-present sslmode (defaulting to disable when unset).
func ApplyClaimCredentials(
	secret *corev1.Secret,
	cfg postgres.Config,
	role string,
	password string,
	database string,
) {
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[postgres.AnnotationConnectionTypeProtocol] = postgres.ConnectionTypeProtocolPostgres
	secret.SetGroupVersionKind(gvk.Secret)
	secret.Type = corev1.SecretTypeOpaque

	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = postgres.SSLModeDisable
	}

	secret.Data = map[string][]byte{
		postgres.SecretKeyHost:     []byte(cfg.Host),
		postgres.SecretKeyPort:     []byte(strconv.Itoa(cfg.Port)),
		postgres.SecretKeyUser:     []byte(role),
		postgres.SecretKeyPassword: []byte(password),
		postgres.SecretKeyDatabase: []byte(database),
		postgres.SecretKeySSLMode:  []byte(sslmode),
	}
	if cfg.SSLRootCert != "" {
		secret.Data[postgres.SecretKeyCA] = []byte(cfg.SSLRootCert)
	}
}

func ApplySchemaClaimCredentials(
	secret *corev1.Secret,
	cfg postgres.Config,
	role string,
	password string,
	database string,
	schema string,
) {
	ApplyClaimCredentials(secret, cfg, role, password, database)
	if schema != "" {
		secret.Data[postgres.SecretKeySchema] = []byte(schema)
	}
}

// ProviderReachable reports whether the provider has Reachable=True.
func ProviderReachable(conditions []fwapi.Condition) bool {
	for _, c := range conditions {
		if c.Type == "Reachable" && c.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}
