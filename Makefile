# Include local overrides if present (gitignored).
-include local.mk

# Image URL to use for all building/pushing image targets.
IMG ?= quay.io/opendatahub/opendatahub-db-operator:latest
# YEAR defines the year value used for substituting the YEAR placeholder in the boilerplate header.
YEAR ?= $(shell date +%Y)

# This repo builds container images with podman, not docker.
CONTAINER_TOOL ?= podman

# Setting SHELL to bash allows bash commands to be executed by recipes.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

## Tool Commands -- this repo's dev environment installs these on PATH at
## pinned versions (see docs) rather than `go run`-ing a versioned module per
## invocation. Override on the command line (e.g. `make lint GOLANGCI_LINT=...`)
## to point at a different binary.
CONTROLLER_GEN ?= controller-gen
KUSTOMIZE      ?= kustomize
GOLANGCI_LINT  ?= golangci-lint
HELM           ?= helm
KUBECTL        ?= kubectl
KIND           ?= kind

.PHONY: all
all: build

##@ General

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt",year=$(YEAR) paths="./..."

.PHONY: fmt
fmt: ## Run golangci-lint formatters against code.
	$(GOLANGCI_LINT) fmt

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: deps
deps: ## Tidy and verify Go module dependencies.
	go mod tidy
	go mod verify

.PHONY: lint
lint: ## Run golangci-lint linter.
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: ## Run golangci-lint linter and perform fixes.
	$(GOLANGCI_LINT) run --fix

.PHONY: lint-config
lint-config: ## Verify golangci-lint linter configuration.
	$(GOLANGCI_LINT) config verify

.PHONY: test
test: ## Run unit tests (everything outside test/integration and test/e2e).
	go test $$(go list ./... | grep -v '/test/integration' | grep -v '/test/e2e') -coverprofile cover.out

.PHONY: test-integration-setup
test-integration-setup: ## Prepare the cluster for integration tests.
	@echo "No CRDs to install yet (phase 1 scaffold has none) -- nothing to do."

.PHONY: test-integration-run
test-integration-run: ## Run integration tests against the current kubeconfig context's cluster.
	go test ./test/integration/... -v -timeout 5m -failfast

.PHONY: test-integration
test-integration: test-integration-setup test-integration-run ## Set up and run integration tests.

.PHONY: test-e2e-setup
test-e2e-setup: ## Prepare a cluster for e2e tests (no-op until phase 3 adds a Helm-installable operator).
	@echo "e2e install path not wired up yet (phase 1 scaffold has no chart to install) -- nothing to do."

.PHONY: test-e2e-run
test-e2e-run: ## Run e2e tests only (operator must already be deployed).
	go test ./test/e2e/... -v -timeout 10m -failfast

.PHONY: test-e2e
test-e2e: test-e2e-setup test-e2e-run ## Set up and run e2e tests.

##@ Build

VERSION     ?= 0.0.0-dev
GIT_COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
GOOS        ?= $(shell go env GOOS)
GOARCH      ?= $(shell go env GOARCH)
CGO_ENABLED ?= 0
BIN_DIR     ?= bin
BIN_NAME    ?= manager

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	mkdir -p "$(BIN_DIR)"
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -o "$(BIN_DIR)/$(BIN_NAME)" ./cmd/main.go

.PHONY: build-bin
build-bin: ## Build manager binary only, no codegen (for Containerfile; run manifests/generate on host first).
	mkdir -p "$(BIN_DIR)"
	CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		go build -o "$(BIN_DIR)/$(BIN_NAME)" ./cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	ODH_MODULE_OPERATOR_CONTROLLER_LEADER_ELECTION_ENABLED=false go run ./cmd/main.go operator

.PHONY: container-build
container-build: ## Build container image with the manager (podman).
	$(CONTAINER_TOOL) build -f Containerfile -t "$(IMG)" .

.PHONY: container-push
container-push: ## Push container image with the manager.
	$(CONTAINER_TOOL) push "$(IMG)"

KIND_CLUSTER ?= db-operator-dev

.PHONY: container-load-kind
container-load-kind: ## Load $(IMG) into a Kind cluster. `kind load docker-image` doesn't work with the podman provider.
	tmp="$$(mktemp)"; \
	$(CONTAINER_TOOL) save "$(IMG)" -o "$$tmp.tar" && \
	$(KIND) load image-archive "$$tmp.tar" --name "$(KIND_CLUSTER)" && \
	rm -f "$$tmp.tar"

##@ Helm

.PHONY: helm
helm: manifests generate ## Generate a Helm chart from kustomize output via chartgen.
	# chartgen itself only replaces config/chart's contents after fully
	# rendering and validating the new chart (see run() in
	# cmd/chartgen/chartgen.go) -- deleting the directory upfront here would
	# defeat that: a failed run would still leave config/chart empty/gone.
	$(KUSTOMIZE) build config/default | go run ./cmd/main.go chartgen --output config/chart

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	@out="$$( $(KUSTOMIZE) build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | $(KUBECTL) apply -f -; else echo "No CRDs to install; skipping."; fi

.PHONY: uninstall
uninstall: manifests ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config.
	@out="$$( $(KUSTOMIZE) build config/crd 2>/dev/null || true )"; \
	if [ -n "$$out" ]; then echo "$$out" | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -; else echo "No CRDs to delete; skipping."; fi

.PHONY: deploy
deploy: manifests ## Deploy controller to the K8s cluster specified in ~/.kube/config, via kustomize.
	cd config/manager && $(KUSTOMIZE) edit set image controller="$(IMG)"
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: ## Undeploy controller from the K8s cluster specified in ~/.kube/config.
	# config/default's bundle includes the operator's Namespace (kubebuilder's
	# default scaffold shape). Piping the whole bundle straight to `kubectl
	# delete -f -` would delete that Namespace object -- and, cascading from
	# it, every resource in it, including anything created independently of
	# this chart/kustomization (Secrets, other workloads, PVCs). The awk
	# filter below drops the Namespace document before it ever reaches
	# `kubectl delete`, so `make undeploy` only ever removes the resources
	# this kustomization actually manages.
	$(KUSTOMIZE) build config/default | awk ' \
		BEGIN { doc = ""; isns = 0 } \
		/^kind: Namespace$$/ { isns = 1 } \
		/^---$$/ { \
			if (!isns && doc != "") printf "%s---\n", doc; \
			doc = ""; isns = 0; \
			next; \
		} \
		{ doc = doc $$0 "\n" } \
		END { if (!isns && doc != "") printf "%s", doc } \
	' | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -
