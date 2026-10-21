---
description: Non-obvious setup and verification requirements for contributors.
audience: [human, ai]
load: When building, testing, debugging, or reviewing a change
---

# Contributing

Run Make targets from the repository root. Use `make help` for the target list. See [README.md](README.md) for user installation and [ARCHITECTURE.md](ARCHITECTURE.md) for design context.

## Prerequisites

See `go.mod` for the Go version and `.github/workflows/ci.yaml` for CI's Go and helper-tool installation steps.

## Test environments

`make test` and `make test-envtest` run without an external cluster. Envtest needs API server and etcd assets selected by `setup-envtest`. The [CI workflow](.github/workflows/ci.yaml) is the source of truth for current coverage; since it does not exercise the cluster-backed integration or e2e suites, run the relevant suite yourself when your change touches that surface.

`make test-integration` applies CRDs to the kubeconfig cluster only when its current context matches `TEST_INTEGRATION_CONTEXT` (default: the configured Kind cluster). Set that variable explicitly to use another test cluster, such as OpenShift. The suite requires exclusive use of the cluster-scoped `DatabaseService/default-db-operator` and will not reuse or delete an existing instance.

`make test-e2e` requires an existing Kind cluster and a current context matching `kind-$(KIND_CLUSTER)`; see the [Makefile](Makefile) for the default value. It does not create the cluster. Setup installs cert-manager into the cluster when its CRDs are absent (the chart's webhook serving certificate and internal-provider TLS require it), builds and loads the image with Podman, and teardown deletes the configured e2e namespace and `DatabaseService/default-db-operator`, so use an isolated cluster. Uncached `Containerfile` base images and a fresh cert-manager install require network access.
