# ==============================================================================
# Makefile used for building
#

GO := go
GO_BUILD_FLAGS += -ldflags "$(GO_LDFLAGS)"
CGO_ENABLED ?= 0

ifeq ($(GOOS),windows)
	GO_OUT_EXT := .exe
endif

ifeq ($(ROOT_PACKAGE),)
	$(error the variable ROOT_PACKAGE must be set prior to including golang.mk)
endif

GOPATH := $(shell go env GOPATH)
ifeq ($(origin GOBIN), undefined)
	GOBIN := $(GOPATH)/bin
endif

CMD_DIRS := $(wildcard $(PROJ_ROOT_DIR)/cmd/*)
COMMANDS := $(filter-out $(wildcard %.md), $(foreach dir, $(CMD_DIRS), $(if $(wildcard $(dir)/*.go), $(dir),)))
BINS ?= $(foreach cmd,${CMD_DIRS},$(notdir $(cmd)))

ifeq ($(CMD_DIRS),)
  $(error Could not determine CMD_DIRS, set PROJ_ROOT_DIR or run in source dir)
endif
ifeq ($(BINS),)
  $(error Could not determine BINS, set PROJ_ROOT_DIR or run in source dir)
endif

go.build.verify:
	@if ! which go &>/dev/null; then echo "Cannot found go compile tool. Please install go tool first."; exit 1; fi

.PHONY: go.build.multiarch
go.build.multiarch: $(foreach p,$(PLATFORMS),$(addprefix go.build., $(addprefix $(p)., $(BINS)))) # Build all applications for all supported architectures

go.build.%: ## Build Go source code.
	$(eval COMMAND := $(word 2,$(subst ., ,$*)))
	$(eval PLATFORM := $(word 1,$(subst ., ,$*)))
	$(eval OS := $(word 1,$(subst _, ,$(PLATFORM))))
	$(eval ARCH := $(word 2,$(subst _, ,$(PLATFORM))))
	@echo "===========> Building binary $(COMMAND) $(VERSION) for $(OS) $(ARCH)"
	@mkdir -p $(OUTPUT_DIR)/platforms/$(OS)/$(ARCH)
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=$(OS) GOARCH=$(ARCH) $(GO) build $(GO_BUILD_FLAGS) \
		-o $(OUTPUT_DIR)/platforms/$(OS)/$(ARCH)/$(COMMAND)$(GO_OUT_EXT) \
		$(ROOT_PACKAGE)/cmd/$(COMMAND)

go.build: go.build.verify $(addprefix go.build., $(addprefix $(PLATFORM)., $(BINS))) # Build source code for the specified platform.

go.format: tools.verify.goimports ## Format Go source code.
	@echo "===========> Running formaters to format codes"
	@$(FIND) -type f -name '*.go' | $(XARGS) gofmt -s -w
	@$(FIND) -type f -name '*.go' | $(XARGS) goimports -w -local $(ROOT_PACKAGE)
	@$(GO) mod edit -fmt

go.tidy: ## Automatically add/remove dependencies.
	@echo "===========> Running 'go mod tidy'..."
	@$(GO) mod tidy

go.test: ## Run unit tests.
	@echo "===========> Running unit tests"
	@mkdir -p $(OUTPUT_DIR)
	@$(GO) test -race -cover \
		-coverprofile=$(OUTPUT_DIR)/coverage.out \
		-timeout=10m -shuffle=on -short \
		-v `go list ./...|egrep -v 'tools|vendor|third_party'`

go.cover: go.test ## Run unit tests and verify the coverage threshold.
	@echo "===========> Running code coverage tests"
	@$(GO) tool cover -func=$(OUTPUT_DIR)/coverage.out | awk -v target=$(COVERAGE) -f $(PROJ_ROOT_DIR)/scripts/coverage.awk

go.lint: tools.verify.golangci-lint ## Run static code checks.
	@echo "===========> Running golangci to lint source codes"
	@golangci-lint run -c $(PROJ_ROOT_DIR)/.golangci.yaml $(PROJ_ROOT_DIR)/...

go.lint.kubelistcheck: ## Report list/get calls that bypass the apiserver watch cache.
	@echo "===========> Running kubelistcheck"
	@mkdir -p $(OUTPUT_DIR)
	@$(GO) build -o $(OUTPUT_DIR)/kubelistcheck $(PROJ_ROOT_DIR)/hack/kubelistcheck
	@$(GO) vet -vettool=$(OUTPUT_DIR)/kubelistcheck \
		$(PROJ_ROOT_DIR)/internal/... $(PROJ_ROOT_DIR)/pkg/... $(PROJ_ROOT_DIR)/cmd/...

go.lint.defensive: tools.verify.golangci-lint ## Check only for "dead nil/empty guards"; must be green.
	@echo "===========> Running golangci to lint dead nil/empty guards"
	@golangci-lint run -c $(PROJ_ROOT_DIR)/.golangci-defensive.yaml \
		$(PROJ_ROOT_DIR)/internal/... $(PROJ_ROOT_DIR)/pkg/... $(PROJ_ROOT_DIR)/cmd/...

# Phony targets (prevent conflicts between files and target names)
.PHONY: go.build.verify go.build.% go.build go.format go.tidy go.test go.cover go.lint go.lint.defensive go.lint.kubelistcheck
