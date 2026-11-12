# ==============================================================================
# Define global Makefile variables for later reference

SHELL := /bin/bash

COMMON_SELF_DIR := $(dir $(lastword $(MAKEFILE_LIST)))
# Project root directory
PROJ_ROOT_DIR := $(strip $(abspath $(shell cd $(COMMON_SELF_DIR)/../../ && pwd -P)))
# Directory for build artifacts and temporary files
OUTPUT_DIR := $(PROJ_ROOT_DIR)/_output

# Define the package name
ROOT_PACKAGE=github.com/onexstack/onexai

# Protobuf file directory
APIROOT=$(PROJ_ROOT_DIR)/pkg/api

# ==============================================================================
# Define version-related variables

## The version package used by the application; values are injected into the named variables of that package via `-ldflags -X`
VERSION_PACKAGE=github.com/onexstack/onexstack/pkg/version

## Check if current directory is a Git repository
IS_GIT_REPO := $(shell git rev-parse --is-inside-work-tree 2>/dev/null)         
ifeq ($(IS_GIT_REPO),)            
    # Non-Git repository, use fallback values
    GIT_TREE_STATE := "not_a_git_repo"                                                                                             
    VERSION := v0.0.0                                     
    GIT_COMMIT := "unknown"
else
    # Define the VERSION semantic version (if not already set)
    ifeq ($(origin VERSION), undefined)
        # If a tag exists, use the latest tag; otherwise use v0.0.0
        VERSION := $(shell \
            if git describe --tags --abbrev=0 --match='v*' >/dev/null 2>&1; then \
                git describe --tags --abbrev=0 --match='v*'; \
            else \
                echo v0.0.0; \
            fi)
    endif
 
    # Check if the code repository is in a dirty state (default is dirty)
    GIT_TREE_STATE := "dirty"
    ifeq (, $(shell git status --porcelain 2>/dev/null))
        GIT_TREE_STATE := "clean"
    endif
 
    # Get current Git commit hash
    GIT_COMMIT := $(shell git rev-parse HEAD)
endif

GO_LDFLAGS += \
	-X $(VERSION_PACKAGE).gitVersion=$(VERSION) \
	-X $(VERSION_PACKAGE).gitCommit=$(GIT_COMMIT) \
	-X $(VERSION_PACKAGE).gitTreeState=$(GIT_TREE_STATE) \
	-X $(VERSION_PACKAGE).buildDate=$(shell date -u +'%Y-%m-%dT%H:%M:%SZ') \
	-X google.golang.org/protobuf/reflect/protoregistry.conflictPolicy=warn

# Target operating systems for compilation can be linux/windows/darwin
PLATFORMS ?= darwin_amd64 darwin_arm64 windows_amd64 windows_arm64 linux_amd64 linux_arm64

# Set a specific operating system
ifeq ($(origin PLATFORM), undefined)
	ifeq ($(origin GOOS), undefined)
		GOOS := $(shell go env GOOS)
	endif
	ifeq ($(origin GOARCH), undefined)
		GOARCH := $(shell go env GOARCH)
	endif
	PLATFORM := $(GOOS)_$(GOARCH)
	# When building images, use linux as the default OS
	IMAGE_PLAT := linux_$(GOARCH)
else
	GOOS := $(word 1, $(subst _, ,$(PLATFORM)))
	GOARCH := $(word 2, $(subst _, ,$(PLATFORM)))
	IMAGE_PLAT := $(PLATFORM)
endif

# Set the unit test coverage threshold.
#
# This is a "ratchet" value that only goes up: the current repo-wide statement coverage is about 33%, so the threshold is set to 32 to
# catch real regressions. Raise it in step once coverage improves; for temporary investigation you can use `make cover COVERAGE=0`
# to bypass it.
ifeq ($(origin COVERAGE),undefined)
COVERAGE := 32
endif

# Makefile settings
ifndef V
MAKEFLAGS += --no-print-directory
endif

# Linux command settings
FIND := find . ! -path './third_party/*' ! -path './vendor/*'
XARGS := xargs --no-run-if-empty
