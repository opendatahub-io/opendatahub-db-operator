package postgres

import (
	"errors"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// IsPgError reports whether err is a PostgreSQL error with the given SQLSTATE
// code. Use pgerrcode constants (e.g. pgerrcode.UndefinedObject) as the code.
func IsPgError(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

// IsUndefinedObject reports whether err is SQLSTATE 42704 (undefined_object).
// PostgreSQL returns this for any missing database object — role, schema,
// function, etc. — that a statement references. Callers can use it to
// distinguish "already gone" from real failures and treat it as a no-op.
func IsUndefinedObject(err error) bool {
	return IsPgError(err, pgerrcode.UndefinedObject)
}

// IsInvalidCatalogName reports whether err is SQLSTATE 3D000
// (invalid_catalog_name) — typically a missing database.
func IsInvalidCatalogName(err error) bool {
	return IsPgError(err, pgerrcode.InvalidCatalogName)
}

// IsDuplicateDatabase reports whether err is SQLSTATE 42P04
// (duplicate_database). Concurrent CREATE DATABASE races land here.
func IsDuplicateDatabase(err error) bool {
	return IsPgError(err, pgerrcode.DuplicateDatabase)
}

// IsDuplicateSchema reports whether err is SQLSTATE 42P06
// (duplicate_schema). Concurrent CREATE SCHEMA races land here.
func IsDuplicateSchema(err error) bool {
	return IsPgError(err, pgerrcode.DuplicateSchema)
}

// IsDuplicateObject reports whether err is SQLSTATE 42710 (duplicate_object).
// Concurrent CREATE ROLE races land here.
func IsDuplicateObject(err error) bool {
	return IsPgError(err, pgerrcode.DuplicateObject)
}
