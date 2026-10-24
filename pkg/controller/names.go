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
	"crypto/sha256"
	"fmt"

	infraApi "github.com/opendatahub-io/opendatahub-db-operator/api/infrastructure/v1alpha1"
)

// schemaClaimSecretPrefix distinguishes SchemaClaim credential Secrets from
// DatabaseClaim Secrets that default to the bare claim name in the same
// namespace.
const schemaClaimSecretPrefix = "sc-"

// maxSecretNameLen is Kubernetes' metadata.name limit for Secrets.
const maxSecretNameLen = 253

func SecretNameForSchemaClaim(claim *infraApi.SchemaClaim) string {
	switch {
	case claim == nil:
		return ""
	case claim.Spec.SecretName != "":
		return claim.Spec.SecretName
	default:
		return prefixedSecretName(schemaClaimSecretPrefix, claim.Name)
	}
}

// prefixedSecretName returns prefix+name when that fits in maxSecretNameLen.
// Otherwise it truncates the name and appends a short hash of the full name so
// long claims stay unique and within the Kubernetes name limit.
func prefixedSecretName(prefix, name string) string {
	full := prefix + name
	if len(full) <= maxSecretNameLen {
		return full
	}
	h := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))[:8]
	// prefix + base + "-" + 8 hex
	baseMax := maxSecretNameLen - len(prefix) - 1 - len(h)
	base := name
	if len(base) > baseMax {
		base = base[:baseMax]
	}
	return prefix + base + "-" + h
}

func SecretNameForDatabaseClaim(claim *infraApi.DatabaseClaim) string {
	switch {
	case claim == nil:
		return ""
	case claim.Spec.SecretName != "":
		return claim.Spec.SecretName
	default:
		return claim.Name
	}
}

func InternalAdminSecretName(providerName string) string {
	return providerName + "-admin"
}

func InternalServiceName(providerName string) string {
	return providerName
}

// MaxInternalProviderNameLength is the max metadata.name length for Internal
// providers. The longest derived name appends "-postgres-scc" (13 runes) and
// must stay within the 63-character Kubernetes name limit; the CRD CEL rule
// enforces the same bound.
const MaxInternalProviderNameLength = 63 - len("-postgres-scc")

// InternalServiceAccountName is the dedicated ServiceAccount for an Internal
// Postgres StatefulSet. It must not be the operator SA or namespace default.
func InternalServiceAccountName(providerName string) string {
	return providerName + "-postgres"
}

// InternalSCCRoleBindingName binds the Internal Postgres SA to the shared
// SCC-use ClusterRole in the provider's Internal namespace.
func InternalSCCRoleBindingName(providerName string) string {
	return providerName + "-postgres-scc"
}

func InternalPVCName(providerName string) string {
	return providerName
}

func InternalInitDBConfigMapName(providerName string) string {
	return providerName + "-initdb"
}

func InternalTLSIssuerName(providerName string) string {
	return providerName + "-tls"
}

func InternalTLSCertificateName(providerName string) string {
	return providerName + "-tls"
}

func InternalTLSSecretName(providerName string) string {
	return providerName + "-tls"
}

func InternalNamespace(provider *infraApi.DatabaseProvider, operatorNamespace string) string {
	if provider != nil && provider.Spec.Internal != nil && provider.Spec.Internal.Namespace != "" {
		return provider.Spec.Internal.Namespace
	}
	return operatorNamespace
}

func InternalServiceHost(provider *infraApi.DatabaseProvider, operatorNamespace string) string {
	if provider == nil {
		return ""
	}
	return fmt.Sprintf(
		"%s.%s.svc",
		InternalServiceName(provider.Name),
		InternalNamespace(provider, operatorNamespace),
	)
}
