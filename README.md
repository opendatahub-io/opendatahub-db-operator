# opendatahub-db-operator

This repository implements the `opendatahub-db-operator` module operator, providing the shared database
infrastructure service described by
[ODH-ADR-Operator-0017](https://github.com/opendatahub-io/architecture-decision-records/blob/main/operator/ODH-ADR-Operator-0017-shared-database-infrastructure-service.md).
It runs as an ODH platform module, reconciling the `DatabaseService` API and, in later phases, the
infrastructure CRDs consumers use to request schemas and database claims.

## Development

- `make build` — build the manager binary.
- `make test` — run unit tests only; it excludes `test/integration`, `test/envtest`, and `test/e2e`. Use
  `make test-integration`, `make test-envtest`, or `make test-e2e` to run those suites.
- `make lint` — run static analysis via `golangci-lint`.

## Installing this chart

The chart has no namespace setting in `values.yaml`; install it into the namespace this operator uses elsewhere
in this repo's kustomize bundle (`odh-db-operator-system`), or your own choice of namespace:

```sh
helm install opendatahub-db-operator config/chart --create-namespace --namespace odh-db-operator-system
```

After installation, apply the cluster-scoped `DatabaseService` singleton and wait for it to reconcile to Ready:

```sh
kubectl apply -f - <<'EOF'
apiVersion: services.platform.opendatahub.io/v1alpha1
kind: DatabaseService
metadata:
  name: default-db-operator
EOF
kubectl wait --for=condition=Ready databaseservice/default-db-operator --timeout=5m
```

Helm installs the `DatabaseService` CRD from `crds/` on the first install, but does not apply CRD changes on
upgrade. Apply an updated schema separately with `kubectl apply -f config/chart/crds/` alongside `helm upgrade`.
