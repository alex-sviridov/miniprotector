# Project configuration
PROJECT_NAME := backup-system
BINARY_DIR := ../bin
GO_MODULE := $(shell go list -m)

# Go build configuration
GO := go
CGO_ENABLED := 1
GOOS := linux
GOARCH := amd64

# Build flags
LDFLAGS := -ldflags "-s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo 'dev')"
BUILDFLAGS := -trimpath -v

# Binary definitions
BINARIES := $(notdir $(wildcard cmd/*))
BRFS_CMD := cmd/brfs
BWFS_CMD := cmd/bwfs

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[0;33m
BLUE := \033[0;34m
NC := \033[0m # No Color

.PHONY: all build clean proto check-deps help brfs bwfs test lint

# Default target
all: check-deps proto build

help: ## Show this help message
	@echo -e "$(BLUE)$(PROJECT_NAME) Build System$(NC)"
	@echo -e ""
	@echo -e "$(YELLOW)Available targets:$(NC)"
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*##/ {printf "  $(GREEN)%-15s$(NC) %s\n", $$1, $$2}' $(MAKEFILE_LIST)


check-deps: ## Check required dependencies
	@printf "$(BLUE)Checking dependencies...$(NC) "
	@which $(GO) >/dev/null || (echo -e "$(RED)❌ Go not found$(NC)" && exit 1)
	@which protoc >/dev/null || (echo -e "$(RED)❌ protoc not found$(NC)" && exit 1)
	@echo -e "$(GREEN)All dependencies found$(NC)"

proto: ## Generate protobuf code
	@printf "$(BLUE)Generating protobuf code...$(NC) "
	@protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		api/*.proto
	@echo -e "$(GREEN)Protobuf code generated in api/$(NC)"

# Build all binaries
build: $(BINARIES) ## Build all binaries

# Build directory setup
$(BINARY_DIR):
	@echo -e "$(BLUE)Creating binary directory: $(BINARY_DIR)$(NC)"
	@mkdir -p $(BINARY_DIR)

# Individual binary targets
brfs: $(BINARY_DIR) ## Build brfs binary
	@printf "$(BLUE)Building brfs...$(NC) "
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/brfs ./$(BRFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/brfs"

bwfs: $(BINARY_DIR) ## Build bwfs binary
	@printf "$(BLUE)Building bwfs...$(NC) "
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/bwfs ./$(BWFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/bwfs"
