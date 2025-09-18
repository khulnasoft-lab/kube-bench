# =============================================================================
# Makefile for kube-bench
# =============================================================================

# Project variables
SOURCES := $(shell find . -name '*.go' -not -path './vendor/*')
BINARY_NAME := kube-bench
BINARY := $(BINARY_NAME)
DOCKER_ORG ?= khulnasoft
VERSION ?= $(shell git rev-parse --short=7 HEAD)
KUBEBENCH_VERSION ?= $(shell git describe --tags --abbrev=0)
IMAGE_NAME ?= $(DOCKER_ORG)/$(BINARY):$(VERSION)
IMAGE_NAME_UBI ?= $(DOCKER_ORG)/$(BINARY):$(VERSION)-ubi
IMAGE_NAME_LATEST ?= $(DOCKER_ORG)/$(BINARY):latest
IMAGE_NAME_FIPS ?= $(DOCKER_ORG)/$(BINARY):$(VERSION)-fips

# Go variables
GOOS ?= linux
GOARCH ?= $(shell go env GOARCH)
BUILD_OS := linux
uname := $(shell uname -s)
LDFLAGS := -ldflags "-X github.com/khulnasoft-lab/kube-bench/cmd.KubeBenchVersion=$(KUBEBENCH_VERSION) -w -s"

# Docker variables
BUILDX_PLATFORM ?= linux/amd64,linux/arm64,linux/arm,linux/ppc64le,linux/s390x
DOCKER_ORGS ?= khulnasoft public.ecr.aws/khulnasoft-lab
KUBECTL_VERSION ?= 1.33.0-alpha.1
ARCH ?= $(shell go env GOARCH)
BUILD_DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
VCS_REF := $(VERSION)

# Kind variables
KIND_PROFILE ?= kube-bench
KIND_CONTAINER_NAME=$(KIND_PROFILE)-control-plane
KIND_IMAGE ?= kindest/node:v1.21.1@sha256:69860bda5563ac81e3c0057d654b5253219618a22ec3a346306239bba8cfa1a6

# Output directories
DIST_DIR := dist
COVERAGE_DIR := coverage

# Detect OS
ifneq ($(findstring Microsoft,$(shell uname -r)),)
	BUILD_OS := windows
else ifeq ($(uname),Linux)
	BUILD_OS := linux
else ifeq ($(uname),Darwin)
	BUILD_OS := darwin
endif

# Supported platforms for cross-compilation
PLATFORMS := linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64

# =============================================================================
# Help target - show all targets
# =============================================================================
.PHONY: help
help: ## Show this help message
	@echo 'Usage: make [target]'
	@echo ''
	@echo 'Targets:'
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-20s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

# =============================================================================
# Development targets
# =============================================================================
.PHONY: all
all: clean build test lint ## Run clean, build, test, and lint

.PHONY: build
build: $(BINARY) ## Build the binary for current platform

$(BINARY): $(SOURCES)
	GOOS=$(GOOS) CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY) .

.PHONY: build-all
build-all: ## Build binaries for all supported platforms
	@mkdir -p $(DIST_DIR)
	@for platform in $(PLATFORMS); do \
		GOOS=$$(echo $$platform | cut -d'/' -f1) \
		GOARCH=$$(echo $$platform | cut -d'/' -f2) \
		OUTPUT_NAME=$(DIST_DIR)/$(BINARY_NAME)-$$platform \
		if [ "$$GOOS" = "windows" ]; then OUTPUT_NAME=$$OUTPUT_NAME.exe; fi; \
		echo "Building for $$platform..."; \
		GOOS=$$GOOS GOARCH=$$GOARCH CGO_ENABLED=0 go build $(LDFLAGS) -o $$OUTPUT_NAME .; \
	done

.PHONY: build-fips
build-fips: ## Build FIPS-compliant binary
	GOOS=$(GOOS) CGO_ENABLED=0 GOEXPERIMENT=boringcrypto go build -tags fipsonly $(LDFLAGS) -o $(BINARY) .

.PHONY: clean
clean: ## Clean build artifacts
	rm -f $(BINARY)
	rm -rf $(DIST_DIR)
	rm -rf $(COVERAGE_DIR)
	rm -f coverage.txt
	rm -f test.data
	rm -f ./kubeconfig.kube-bench
	rm -f ./hack/kind.test.yaml
	rm -f ./hack/kind-stig.test.yaml

# =============================================================================
# Docker targets
# =============================================================================
.PHONY: docker
docker: ## Build and push multi-arch Docker images
	set -xe; \
	for org in $(DOCKER_ORGS); do \
		docker buildx build --tag $${org}/kube-bench:$(VERSION) \
		--platform $(BUILDX_PLATFORM) --push . ; \
	done

.PHONY: docker-latest
docker-latest: ## Build and push multi-arch Docker images with latest tag
	set -xe; \
	for org in $(DOCKER_ORGS); do \
		docker buildx build --tag $${org}/kube-bench:latest \
		--platform $(BUILDX_PLATFORM) --push . ; \
	done

.PHONY: build-docker
build-docker: ## Build Docker image for current platform
	docker build \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		--build-arg VCS_REF=$(VCS_REF) \
		--build-arg KUBEBENCH_VERSION=$(KUBEBENCH_VERSION) \
		--build-arg KUBECTL_VERSION=$(KUBECTL_VERSION) \
		--build-arg TARGETARCH=$(ARCH) \
		-t $(IMAGE_NAME) .

.PHONY: build-docker-ubi
build-docker-ubi: ## Build UBI-based Docker image
	docker build -f Dockerfile.ubi \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		--build-arg VCS_REF=$(VCS_REF) \
		--build-arg KUBEBENCH_VERSION=$(KUBEBENCH_VERSION) \
		--build-arg KUBECTL_VERSION=$(KUBECTL_VERSION) \
		--build-arg TARGETARCH=$(ARCH) \
		-t $(IMAGE_NAME_UBI) .

.PHONY: build-docker-fips
build-docker-fips: ## Build FIPS-compliant Docker image
	docker build -f Dockerfile.fips.ubi \
		--build-arg BUILD_DATE=$(BUILD_DATE) \
		--build-arg VCS_REF=$(VCS_REF) \
		--build-arg KUBEBENCH_VERSION=$(KUBEBENCH_VERSION) \
		--build-arg KUBECTL_VERSION=$(KUBECTL_VERSION) \
		--build-arg TARGETARCH=$(ARCH) \
		-t $(IMAGE_NAME_FIPS) .

# =============================================================================
# Test targets
# =============================================================================
.PHONY: test
test: ## Run unit tests
	GO111MODULE=on go test -vet all -short -race -timeout 30s -coverprofile=coverage.txt -covermode=atomic ./...

.PHONY: test-all
test-all: ## Run all tests including integration tests
	GO111MODULE=on go test -vet all -race -timeout 60s -coverprofile=coverage.txt -covermode=atomic ./...

.PHONY: test-verbose
test-verbose: ## Run tests with verbose output
	GO111MODULE=on go test -v -race -timeout 60s ./...

.PHONY: test-coverage
test-coverage: ## Run tests with coverage report
	@mkdir -p $(COVERAGE_DIR)
	GO111MODULE=on go test -race -timeout 60s -coverprofile=$(COVERAGE_DIR)/coverage.out -covermode=atomic ./...
	go tool cover -html=$(COVERAGE_DIR)/coverage.out -o $(COVERAGE_DIR)/coverage.html
	@echo "Coverage report generated: $(COVERAGE_DIR)/coverage.html"

.PHONY: test-bench
test-bench: ## Run benchmarks
	GO111MODULE=on go test -bench=. -benchmem -timeout 120s ./...

# =============================================================================
# Integration test targets
# =============================================================================
.PHONY: integration-test
integration-test: kind-test-cluster kind-run ## Run integration tests

# creates a kind cluster to be used for development.
HAS_KIND := $(shell command -v kind;)
.PHONY: kind-test-cluster
kind-test-cluster:
ifndef HAS_KIND
	@echo "Installing kind..."
	go install sigs.k8s.io/kind@latest
endif
	@if [ -z $$(kind get clusters | grep $(KIND_PROFILE)) ]; then\
		echo "Could not find $(KIND_PROFILE) cluster. Creating...";\
		kind create cluster --name $(KIND_PROFILE) --image $(KIND_IMAGE) --wait 5m;\
	fi

# pushes the current dev version to the kind cluster.
.PHONY: kind-push
kind-push: build-docker
	kind load docker-image $(IMAGE_NAME) --name $(KIND_PROFILE)

# runs the current version on kind using a job and follow logs
.PHONY: kind-run
kind-run: KUBECONFIG = "./kubeconfig.kube-bench"
kind-run: kind-push
	sed "s/\$${VERSION}/$(VERSION)/" ./hack/kind.yaml > ./hack/kind.test.yaml
	kind get kubeconfig --name="$(KIND_PROFILE)" > $(KUBECONFIG)
	-KUBECONFIG=$(KUBECONFIG) \
		kubectl delete job kube-bench
	KUBECONFIG=$(KUBECONFIG) \
		kubectl apply -f ./hack/kind.test.yaml && \
		kubectl wait --for=condition=complete job.batch/kube-bench --timeout=60s && \
		kubectl logs job/kube-bench > ./test.data && \
		diff ./test.data integration/testdata/Expected_output.data

.PHONY: kind-run-stig
kind-run-stig: KUBECONFIG = "./kubeconfig.kube-bench"
kind-run-stig: kind-push
	sed "s/\$${VERSION}/$(VERSION)/" ./hack/kind-stig.yaml > ./hack/kind-stig.test.yaml
	kind get kubeconfig --name="$(KIND_PROFILE)" > $(KUBECONFIG)
	-KUBECONFIG=$(KUBECONFIG) \
		kubectl delete job kube-bench
	KUBECONFIG=$(KUBECONFIG) \
		kubectl apply -f ./hack/kind-stig.test.yaml && \
		kubectl wait --for=condition=complete job.batch/kube-bench --timeout=60s && \
		kubectl logs job/kube-bench > ./test.data && \
		diff ./test.data integration/testdata/Expected_output_stig.data

.PHONY: kind-clean
kind-clean: ## Clean up kind cluster
	kind delete cluster --name $(KIND_PROFILE)
	rm -f ./kubeconfig.kube-bench

# =============================================================================
# Code quality and security targets
# =============================================================================
.PHONY: lint
lint: ## Run linter
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run --timeout=5m; \
	else \
		echo "golangci-lint not found, installing..."; \
		go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest; \
		golangci-lint run --timeout=5m; \
	fi

.PHONY: fmt
fmt: ## Format Go code
	go fmt ./...

.PHONY: fmt-check
fmt-check: ## Check if Go code is formatted
	@diff=$$(gofmt -d .); \
	if [ -n "$$diff" ]; then \
		echo "Please run 'make fmt' to format the code:"; \
		echo "$${diff}"; \
		exit 1; \
	fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: security-scan
security-scan: ## Run security scan
	@if [ -f $$(go env GOPATH)/bin/gosec ]; then \
		$$(go env GOPATH)/bin/gosec ./...; \
	else \
		echo "gosec not found, installing..."; \
		go install github.com/securego/gosec/v2/cmd/gosec@latest; \
		$$(go env GOPATH)/bin/gosec ./...; \
	fi

.PHONY: mod-tidy
mod-tidy: ## Run go mod tidy
	go mod tidy
	go mod verify

.PHONY: mod-check
mod-check: ## Check if go.mod is tidy
	@go mod tidy
	@if [ -n "$$(git diff --name-only go.mod go.sum)" ]; then \
		echo "go.mod or go.sum is not tidy. Please run 'go mod tidy' and commit the changes."; \
		exit 1; \
	fi

# =============================================================================
# Release and CI targets
# =============================================================================
.PHONY: release-check
release-check: ## Check if ready for release
	@echo "Checking if ready for release..."
	@make fmt-check
	@make vet
	@make lint
	@make test
	@make mod-check
	@echo "All checks passed! Ready for release."

.PHONY: ci
ci: ## Run CI checks
	@echo "Running CI checks..."
	@make fmt-check
	@make vet
	@make lint
	@make test
	@make security-scan
	@make mod-check
	@echo "CI checks completed successfully!"

.PHONY: install-tools
install-tools: ## Install required development tools
	@echo "Installing development tools..."
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	go install github.com/securego/gosec/v2/cmd/gosec@latest
	go install sigs.k8s.io/kind@latest
	@echo "Tools installed successfully!"

# =============================================================================
# Utility targets
# =============================================================================
.PHONY: version
version: ## Show version information
	@echo "Version: $(VERSION)"
	@echo "Kube-bench Version: $(KUBEBENCH_VERSION)"
	@echo "Go Version: $$(go version)"
	@echo "Git Commit: $$(git rev-parse HEAD)"
	@echo "Build Date: $(BUILD_DATE)"

.PHONY: deps
deps: ## Download dependencies
	go mod download

.PHONY: update-deps
update-deps: ## Update dependencies
	go get -u ./...
	go mod tidy

.PHONY: generate
generate: ## Generate code (if applicable)
	@echo "Generating code..."
	@if [ -f "scripts/generate.sh" ]; then \
		./scripts/generate.sh; \
	else \
		echo "No generate script found."; \
	fi

# =============================================================================
# Phony targets
# =============================================================================
.PHONY: all build build-all build-fips clean docker docker-latest build-docker build-docker-ubi build-docker-fips test test-all test-verbose test-coverage test-bench integration-test kind-test-cluster kind-push kind-run kind-run-stig kind-clean lint fmt fmt-check vet security-scan mod-tidy mod-check release-check ci install-tools version deps update-deps generate help
