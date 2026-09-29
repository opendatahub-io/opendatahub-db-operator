# opendatahub-db-operator

This repository implements the `opendatahub-db-operator` module operator, providing the shared database
infrastructure service described by
[ODH-ADR-Operator-0017](https://github.com/opendatahub-io/architecture-decision-records/blob/main/operator/ODH-ADR-Operator-0017-shared-database-infrastructure-service.md).
It runs as an ODH platform module, reconciling the `DatabaseService` API and, in later phases, the
infrastructure CRDs consumers use to request schemas and database claims.

## Development

- `make build` — build the manager binary.
- `make test` — run the unit and integration test suites.
- `make lint` — run static analysis via `golangci-lint`.
