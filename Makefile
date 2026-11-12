# Define the default target as `all`
.DEFAULT_GOAL := all

# Define the `all` pseudo-target for Makefile. When `make` is executed, it will default to executing the `all` target.
all: tidy format generate build

# ==============================================================================
# Includes

# Ensure `include common.mk` is at the top, as common.mk defines variables required by the subsequent sub-makefiles.
include scripts/make-rules/common.mk
include scripts/make-rules/all.mk

# ==============================================================================
# Usage

define USAGE_OPTIONS

Options:
  BINS             The binaries to be built. Default is all files in the `cmd` directory.
                   This option can be used with the following commands: make build
                   Example: make build BINS=<BinaryName>
  IMAGES           Backend images to make. Defaults to all files in the `cmd` directory.
                   This option is available when using command: make image
                   Example: make image IMAGES=<BinaryName>
  RUNTIME_ONLY     Set to 1 to use the Dockerfile.runtime-only when building images. Default is 0.
  VERSION          Version information to embed into the binary.
  V                Set to 1 to enable verbose build output. Default is 0.
endef
export USAGE_OPTIONS

## --------------------------------------
## Binaries
## --------------------------------------

##@ build:

.PHONY: build.multiarch                               
build.multiarch: ## Build all applications with all supported arch.
	@$(MAKE) go.build.multiarch

.PHONY: build
#build: go.tidy ## Build all binaries for the selected platform. 
build: ## Build all binaries for the selected platform. 
	@$(MAKE) go.build

.PHONY: image
image: ## Build docker images for host arch.
	$(MAKE) image.build

.PHONY: push
push: ## Build docker images for host arch and push images to registry.
	$(MAKE) image.push

## --------------------------------------
## Compose

COMPOSE_FILE ?= $(PROJ_ROOT_DIR)/manifests/installation/compose/compose.yaml
COMPOSE_ENV_FILE ?= $(PROJ_ROOT_DIR)/manifests/installation/compose/env
COMPOSE_ENV_ARG := $(if $(wildcard $(COMPOSE_ENV_FILE)),--env-file $(COMPOSE_ENV_FILE),)
COMPOSE := docker compose $(COMPOSE_ENV_ARG) -f $(COMPOSE_FILE)

.PHONY: compose.build
compose.build: ## Build the compose stack images.
	@$(COMPOSE) build

.PHONY: compose.up
compose.up: ## Start the compose stack (copy manifests/installation/compose/env.example to env first).
	@$(COMPOSE) up -d --build

.PHONY: compose.up.secure
compose.up.secure: ## Start the compose stack with the secure profile.
	@$(COMPOSE) --profile secure up -d --build

.PHONY: compose.down
compose.down: ## Stop the compose stack.
	@$(COMPOSE) down

.PHONY: compose.logs
compose.logs: ## Follow the compose stack logs.
	@$(COMPOSE) logs -f

.PHONY: compose.smoke
compose.smoke: ## Run the compose smoke test.
	@bash $(PROJ_ROOT_DIR)/manifests/installation/compose/smoke.sh

## --------------------------------------
## Testing
## --------------------------------------

##@ test:

test: ## Run unit tests (-race -cover -shuffle -short).
	@$(MAKE) go.test

cover: ## Run unit tests and check the coverage threshold.
	@$(MAKE) go.cover

## --------------------------------------
## Cleanup
## --------------------------------------

##@ clean:

clean: ## Remove build artifacts and temp files (_output/).
	@echo "===========> Cleaning all build output"
	@-rm -vrf $(OUTPUT_DIR)

## --------------------------------------
## Lint / Verification
## --------------------------------------

##@ lint and verify:

lint: ## Static analysis with golangci-lint (uses .golangci.yaml).
	@$(MAKE) go.lint

lint.defensive: ## Gate on dead nil/empty guards (uses .golangci-defensive.yaml).
	@$(MAKE) go.lint.defensive

lint.kubelistcheck: ## Report list/get calls that bypass the apiserver watch cache.
	@$(MAKE) go.lint.kubelistcheck

verify: verify.public verify-codegen ## Run the CI gates that do not need a cluster.
	@$(MAKE) lint.kubelistcheck
	@$(MAKE) cover

verify.public: ## Verify the tree contains no private material.
	@bash $(PROJ_ROOT_DIR)/scripts/verify-public.sh

tidy: ## Sync dependencies and update go.mod/go.sum (go mod tidy).
	@$(MAKE) go.tidy

format: ## Format Go source files.
	@$(MAKE) go.format

format.protos: tools.verify.protolint ## Lint and fix protobuf files (best effort).
	@protolint -fix -config_path ${PROJ_ROOT_DIR}/.protolint.yaml $(shell find $(APIROOT) -name *.proto) || true

## --------------------------------------
## Generate / Manifests
## --------------------------------------

##@ generate

generate: ## Generate code for all packages using go:generate directives (recursively ./...)
	@$(MAKE) gen.generate

gen-k8s: ## Regenerate the Kubernetes codegen artefacts (deepcopy, clientset, informers, listers, applyconfigurations, openapi).
	@$(MAKE) gen.k8s

verify-codegen: ## Fail if checked-in generated code does not match its sources.
	@$(MAKE) gen.verify-codegen

.PHONY: protoc.%
protoc.%: ## Compile Protobuf files.
	@$(MAKE) gen.protoc.$*

protoc: ## Compile protobuf files.
	@$(MAKE) gen.protoc

## --------------------------------------
## Hack / Tools
## --------------------------------------

##@ hack/tools:

add-copyright: ## Add copyright headers (skip third_party, vendor, _output).
	@$(MAKE) gen.add-copyright

deps: ## Install build and codegen tools.
	@$(MAKE) tools.verify

help: Makefile ## Show available targets and usage.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<TARGETS> <OPTIONS>\033[0m\n\n\033[35mTargets:\033[0m\n"} /^[0-9A-Za-z._-]+:.*?##/ { printf "  \033[36m%-45s\033[0m %s\n", $$1, $$2 } /^\$$\([0-9A-Za-z_-]+\):.*?##/ { gsub("_","-", $$1); printf "  \033[36m%-45s\033[0m %s\n", tolower(substr($$1, 3, length($$1)-7)), $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' Makefile #$(MAKEFILE_LIST)
	@echo -e "$$USAGE_OPTIONS"

# Pseudo targets (to avoid conflicts with files that may have the same name as the targets)
.PHONY: all build test cover clean lint tidy format protoc add-copyright help verify verify.public verify-codegen format.protos gen-k8s lint.kubelistcheck compose.build compose.up compose.up.secure compose.down compose.logs compose.smoke
