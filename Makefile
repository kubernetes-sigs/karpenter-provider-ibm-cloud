# Copyright 2024 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
VERSION ?= $(shell git describe --tags --always --dirty)

# Build settings
BINARY_NAME = karpenter-ibm-controller
BUILD_DIR = bin
PLATFORMS = linux/amd64 linux/arm64

# Build flags
LDFLAGS = -ldflags "-X main.version=${VERSION}"
CGO_ENABLED = 0

# Test settings
GTEST_ARGS = -v -race -timeout=30m

CONTROLLER_GEN = ~/.local/share/go/bin/controller-gen

.PHONY: all
all: build

.PHONY: build
build: generate ## Build binary for current platform
	CGO_ENABLED=$(CGO_ENABLED) go build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) cmd/controller/main.go

.PHONY: build-multiarch
build-multiarch: generate ## Build binaries for all supported platforms
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; \
		arch=$${platform#*/}; \
		echo "Building for $$os/$$arch..."; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=$(CGO_ENABLED) \
			go build $(LDFLAGS) \
			-o $(BUILD_DIR)/$(BINARY_NAME)-$$os-$$arch \
			cmd/controller/main.go || exit 1; \
	done

.PHONY: help
help: ## Display help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: gen-objects
gen-objects: ## generate the controller-gen related objects
	$(CONTROLLER_GEN) object paths="./..."

.PHONY: gen-mocks
gen-mocks: ## generate mocks using mockgen
	@echo "Generating mocks..."
	go generate ./pkg/providers/common/types
	go generate ./pkg/cloudprovider/ibm
	go generate ./pkg/providers/common/pricing

.PHONY: verify-mocks
verify-mocks: ## verify mocks are up to date
	@echo "Verifying mocks are up to date..."
	@$(MAKE) gen-mocks
	@git diff --exit-code pkg/providers/common/types/mock/ pkg/cloudprovider/ibm/mock/ pkg/providers/common/pricing/mock/ || \
		(echo "Error: Generated mocks are out of date. Run 'make gen-mocks' to update them." && exit 1)

.PHONY: generate
generate: gen-objects gen-mocks manifests ## generate all controller-gen files and mocks

.PHONY: manifests
manifests: ## generate the controller-gen kubernetes manifests
	@echo "Generating base Karpenter CRDs and IBM-specific CRDs directly to Helm chart..."
	$(CONTROLLER_GEN) crd paths="./pkg/apis/v1alpha1" output:crd:artifacts:config=charts/crds
	$(CONTROLLER_GEN) crd paths="./vendor/sigs.k8s.io/karpenter/pkg/apis/..." output:crd:artifacts:config=charts/crds
	@echo "Generating RBAC manifests..."
	@rm -f charts/templates/rbac_*.yaml charts/templates/role_*.yaml charts/templates/clusterrole_*.yaml
	GOFLAGS="-mod=mod" $(CONTROLLER_GEN) rbac:roleName=karpenter-manager paths="./pkg/controllers/..." output:rbac:dir=charts/templates

.PHONY: test
test: vendor unit

.PHONY: ci
ci: vendor unit lint ## Run all CI checks (tests + linting)

.PHONY: unit
unit:
	go test $(GTEST_ARGS) ./...

.PHONY: e2e
e2e: ## Run e2e tests against real cluster (requires env vars)
	@echo "Running E2E tests..."
	@if [ -z "$$RUN_E2E_TESTS" ]; then \
		echo "Warning: RUN_E2E_TESTS not set, tests will be skipped"; \
		echo "Set RUN_E2E_TESTS=true and required env vars to run e2e tests"; \
	fi
	go test -v -timeout 45m ./test/e2e/... -run TestE2E

.PHONY: e2e-benchmark
e2e-benchmark: ## Run e2e performance benchmarks
	@echo "Running E2E benchmarks..."
	RUN_E2E_BENCHMARKS=true go test -v -timeout 30m ./test/e2e/... -run=^$$ -bench=.

.PHONY: lint
lint:
	@echo "Executing pre-commit for all files"
	pre-commit run --all-files
	@echo "pre-commit executed."

.PHONY: vendor
vendor: ## update modules and populate local vendor directory
	go mod tidy
	go mod vendor
	go mod verify

.PHONY: clean
clean: ## Clean build artifacts
	rm -rf $(BUILD_DIR)

.PHONY: docker-base
docker-base: ## Build custom base image with IBM Cloud CLI
	docker build -f Dockerfile.base -t karpenter-ibm-base:latest .

.PHONY: docker-build
docker-build: docker-base build ## Build container image locally using Ko
	KO_CONFIG_PATH=.ko.local.yaml ko build --local ./cmd/controller

.PHONY: license
license: ## Add license headers to all Go files
	hack/boilerplate.sh

.PHONY: verify-license
verify-license: ## Verify all Go files have license headers
	hack/verify-boilerplate.sh

.PHONY: pre-commit
pre-commit: ## Run pre-commit checks (license headers + linting)
	hack/pre-commit.sh

.PHONY: lint-commits install-git-hooks
lint-commits: ## Lint commit messages since main
	gitlint --commits origin/main..HEAD

install-git-hooks: ## Install gitlint git hooks
	gitlint install-hook

.PHONY: k8s-support-table
k8s-support-table: ## Generate Kubernetes version support table
	./scripts/generate-k8s-support-table.sh

.PHONY: update-k8s-support
update-k8s-support: ## Update README.md with Kubernetes support table
	./scripts/generate-k8s-support-table.sh --update-readme
