# Project configuration
PROJECT_NAME := backup-system
BINARY_DIR := bin
GO_MODULE := $(shell cd src && go list -m)

# Go build configuration
GO := go
CGO_ENABLED := 1
GOOS := linux
GOARCH := amd64

# Build flags
LDFLAGS := -ldflags "-s -w -X main.version=$(shell git describe --tags --always --dirty 2>/dev/null || echo 'dev')"
BUILDFLAGS := -trimpath -v

# Binary definitions
BINARIES := $(notdir $(wildcard src/cmd/*))
BRFS_CMD := cmd/brfs
BWFS_CMD := cmd/bwfs
RWFS_CMD := cmd/rwfs
CERTREQUEST_CMD := cmd/certrequest
CERTCLIENT_CMD := cmd/certclient
CATALOGSYNC_CMD := cmd/catalogsync
CATALOG_CMD := cmd/catalog

# Deployment
CONTROL_PLANE_DIR := deploy/control-plane

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[0;33m
BLUE := \033[0;34m
NC := \033[0m # No Color

.PHONY: all build clean proto check-deps help brfs bwfs rwfs certrequest certclient catalogsync catalog test test-e2e lint control-plane-up

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
	@cd src && protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		api/*.proto
	@echo -e "$(GREEN)Protobuf code generated in src/api/$(NC)"

# Build all binaries
build: $(BINARIES) ## Build all binaries

# Build directory setup
$(BINARY_DIR):
	@echo -e "$(BLUE)Creating binary directory: $(BINARY_DIR)$(NC)"
	@mkdir -p $(BINARY_DIR)

# Individual binary targets
brfs: $(BINARY_DIR) ## Build brfs binary
	@printf "$(BLUE)Building brfs...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/brfs ./$(BRFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/brfs"

bwfs: $(BINARY_DIR) ## Build bwfs binary
	@printf "$(BLUE)Building bwfs...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/bwfs ./$(BWFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/bwfs"

rwfs: $(BINARY_DIR) ## Build rwfs binary
	@printf "$(BLUE)Building rwfs...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/rwfs ./$(RWFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/rwfs"

certrequest: $(BINARY_DIR) ## Build certrequest binary
	@printf "$(BLUE)Building certrequest...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/certrequest ./$(CERTREQUEST_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/certrequest"

certclient: $(BINARY_DIR) ## Build certclient binary
	@printf "$(BLUE)Building certclient...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/certclient ./$(CERTCLIENT_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/certclient"

catalogsync: $(BINARY_DIR) ## Build catalogsync binary
	@printf "$(BLUE)Building catalogsync...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/catalogsync ./$(CATALOGSYNC_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/catalogsync"

catalog: $(BINARY_DIR) ## Build catalog binary
	@printf "$(BLUE)Building catalog...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/catalog ./$(CATALOG_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/catalog"

test: ## Run unit and integration tests
	cd src && go test ./...

test-e2e: ## Run Docker-based e2e tests (requires Docker daemon, ~3 min)
	cd src && go test -tags=e2e -timeout=300s ./e2e/... ./cmd/certrequest/...

lint: ## Run go vet
	cd src && go vet ./...

clean: ## Remove built binaries
	rm -rf $(BINARY_DIR)

control-plane-up: ## Initialize (if needed) and start the control-plane stack (ca + catalog)
	@if [ ! -f $(CONTROL_PLANE_DIR)/ca/data/secrets/password ]; then \
		echo -e "$(BLUE)Generating CA provisioner password...$(NC)"; \
		mkdir -p $(CONTROL_PLANE_DIR)/ca/data/secrets; \
		openssl rand -base64 32 > $(CONTROL_PLANE_DIR)/ca/data/secrets/password; \
	fi
	@cd $(CONTROL_PLANE_DIR) && docker compose up -d
	@echo -e "$(GREEN)Control plane up.$(NC) ca: https://localhost:9000  catalog: localhost:15723"
