# Open Data Hub database operator

[![CI](https://github.com/opendatahub-io/opendatahub-db-operator/actions/workflows/ci.yaml/badge.svg)](https://github.com/opendatahub-io/opendatahub-db-operator/actions/workflows/ci.yaml)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)

The Open Data Hub database operator gives platform components a shared way to request
PostgreSQL access on OpenShift. It runs as an OpenShift AI module, manages database
providers, and provisions `DatabaseClaim` credentials in namespaced connection Secrets. See the
[architecture overview](ARCHITECTURE.md) for the design.

| Resource | What it does |
| --- | --- |
| `DatabaseService` | Enables the module through a cluster-scoped singleton. |
| `DatabaseProvider` | Describes an operator-managed PostgreSQL instance or an administrator-managed one. |
| `DatabaseClaim` | Requests database access or, when permitted, a dedicated database for a component. |

## Get started

On an OpenShift cluster with an OpenShift AI Operator release that includes the
`databaseservice` module, a cluster administrator can enable it and wait for the
service to become ready:

```sh
oc patch datasciencecluster default-dsc --type=merge \
  -p '{"spec":{"components":{"databaseservice":{"managementState":"Managed"}}}}'
oc wait --for=condition=Ready databaseservice/default-db-operator --timeout=5m
```

The [installation and usage guide](docs/installation-and-usage.md) has provider and
claim examples, prerequisites, claim status, and operational limits.

## Installing this chart

For standalone evaluation or development, use a cluster with cert-manager installed. Apply the
infrastructure CRDs as described in the
[standalone installation instructions](docs/installation-and-usage.md#enable-the-module),
then install the generated chart:

```sh
helm install opendatahub-db-operator config/chart \
  --create-namespace --namespace odh-db-operator-system
```

Create the `DatabaseService` singleton after installation as shown in the
[standalone installation instructions](docs/installation-and-usage.md#enable-the-module).
Helm installs its CRD on first install but does not update CRDs on upgrade. Apply
an updated schema with `kubectl apply -f config/chart/crds/` alongside `helm upgrade`.

## Contributing

Start with the [contributor guide](CONTRIBUTING.md) for prerequisites, build commands,
and the right test environment. Use `make help` to see the available targets. These
references help locate the relevant work:

| If you are working on… | Start here |
| --- | --- |
| API behavior or resource scope | [Architecture and stable contracts](ARCHITECTURE.md#stable-contracts) and the [API types](api/) |
| Reconciliation or PostgreSQL provisioning | [Repository components](ARCHITECTURE.md#repository-components) |
| Generated manifests or chart packaging | [Make targets](Makefile) and the [chart-generation overview](ARCHITECTURE.md#repository-components) |
| Tests or CI | [Test environments](CONTRIBUTING.md#test-environments) and the [CI workflow](.github/workflows/ci.yaml) |
