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

package claimerrors

import (
	"fmt"
	"strings"
)

type DatabaseNotFound struct {
	Database string
}

func (e DatabaseNotFound) Error() string {
	return fmt.Sprintf("database %q not found", e.Database)
}

type DatabaseCreateNotAllowed struct {
	Database string
}

func (e DatabaseCreateNotAllowed) Error() string {
	return fmt.Sprintf("creating database %q is not allowed by the provider", e.Database)
}

type SchemaCreateNotAllowed struct {
	Schema string
}

func (e SchemaCreateNotAllowed) Error() string {
	return fmt.Sprintf("creating schema %q is not allowed by the provider", e.Schema)
}

// SchemaReserved is returned when a claim targets a PostgreSQL system or
// default schema that must never be claimed or dropped.
type SchemaReserved struct {
	Schema string
}

func (e SchemaReserved) Error() string {
	return fmt.Sprintf("schema %q is reserved and cannot be claimed", e.Schema)
}

// SchemaNotOwned is returned when the target schema already exists and this
// claim cannot prove it created it (no matching PostgreSQL ownership marker,
// or Status.SchemaCreated does not cover the requested schema). Binding would
// grant the claim role privileges on another tenant's schema.
type SchemaNotOwned struct {
	Schema string
}

func (e SchemaNotOwned) Error() string {
	return fmt.Sprintf("schema %q already exists and was not created by this claim", e.Schema)
}

// SecretNotOwned is returned when the credentials Secret already exists and is
// not controller-owned by this claim (e.g. SchemaClaim "foo" and DatabaseClaim
// "sc-foo" both default to Secret "sc-foo"). Using it would overwrite another
// claim's credentials and rotate its role password.
type SecretNotOwned struct {
	Secret string
}

func (e SecretNotOwned) Error() string {
	return fmt.Sprintf("credentials Secret %q already exists and is not owned by this claim", e.Secret)
}

// ExtensionNotAvailable is returned when a claim requires PostgreSQL
// extensions that are absent from pg_extension in the target database on an
// External provider. The claim must not bind until an administrator installs
// them in that database.
type ExtensionNotAvailable struct {
	Extensions []string
}

func (e ExtensionNotAvailable) Error() string {
	switch len(e.Extensions) {
	case 0:
		return "required extensions are not installed in the target database"
	case 1:
		return fmt.Sprintf("extension %q is not installed in the target database", e.Extensions[0])
	default:
		return fmt.Sprintf(
			"extensions [%s] are not installed in the target database",
			strings.Join(e.Extensions, ", "),
		)
	}
}

// ExtensionNotSupported is returned when a claim names PostgreSQL extensions
// outside the platform SupportedClaimExtensions allow-list.
type ExtensionNotSupported struct {
	Extensions []string
}

func (e ExtensionNotSupported) Error() string {
	switch len(e.Extensions) {
	case 0:
		return "required extensions are not supported"
	case 1:
		return fmt.Sprintf("extension %q is not supported", e.Extensions[0])
	default:
		return fmt.Sprintf(
			"extensions [%s] are not supported",
			strings.Join(e.Extensions, ", "),
		)
	}
}
