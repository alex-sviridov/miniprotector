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
CERTCLIENT_CMD := cmd/certclient
CATALOGSYNC_CMD := cmd/catalogsync
CATALOG_CMD := cmd/catalog
AGENT_CMD := cmd/agent
CLIENTMANAGER_CMD := cmd/clientmanager
ISSUER_CMD := cmd/issuer
POLICY_SERVER_CMD := cmd/policy-server
POLICYCLIENT_CMD := cmd/policyclient
LOG_GATEWAY_CMD := cmd/log-gateway
CLIENTMANAGER_API_CMD := cmd/clientmanager-api
CLIENTMANAGER_ADMIN_API_CMD := cmd/clientmanager-admin-api
API_SERVER_CMD := cmd/api-server

# Deployment
CONTROL_PLANE_DIR := deploy/control-plane

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[0;33m
BLUE := \033[0;34m
NC := \033[0m # No Color

.PHONY: all build clean proto check-deps help brfs bwfs rwfs certclient catalogsync catalog agent clientmanager issuer policy-server policyclient log-gateway clientmanager-api clientmanager-admin-api api-server test test-e2e lint control-plane-up demo-up demo-down

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

agent: $(BINARY_DIR) ## Build agent binary
	@printf "$(BLUE)Building agent...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/agent ./$(AGENT_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/agent"

clientmanager: $(BINARY_DIR) ## Build client-manager binary
	@printf "$(BLUE)Building client-manager...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/clientmanager ./$(CLIENTMANAGER_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/clientmanager"

issuer: $(BINARY_DIR) ## Build issuer binary
	@printf "$(BLUE)Building issuer...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/issuer ./$(ISSUER_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/issuer"

policy-server: $(BINARY_DIR) ## Build policy-server binary
	@printf "$(BLUE)Building policy-server...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/policy-server ./$(POLICY_SERVER_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/policy-server"

policyclient: $(BINARY_DIR) ## Build policyclient binary
	@printf "$(BLUE)Building policyclient...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/policyclient ./$(POLICYCLIENT_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/policyclient"

log-gateway: $(BINARY_DIR) ## Build log-gateway binary
	@printf "$(BLUE)Building log-gateway...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/log-gateway ./$(LOG_GATEWAY_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/log-gateway"

clientmanager-api: $(BINARY_DIR) ## Build clientmanager-api binary
	@printf "$(BLUE)Building clientmanager-api...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/clientmanager-api ./$(CLIENTMANAGER_API_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/clientmanager-api"

clientmanager-admin-api: $(BINARY_DIR) ## Build clientmanager-admin-api binary
	@printf "$(BLUE)Building clientmanager-admin-api...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/clientmanager-admin-api ./$(CLIENTMANAGER_ADMIN_API_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/clientmanager-admin-api"

api-server: $(BINARY_DIR) ## Build api-server binary
	@printf "$(BLUE)Building api-server...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/api-server ./$(API_SERVER_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/api-server"

test: ## Run unit and integration tests
	cd src && go test ./...

test-e2e: ## Run e2e smoke test against the running demo lab (run `make demo-up` first)
	cd src && go test -tags=e2e -timeout=30s ./e2e/...

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
	@cd $(CONTROL_PLANE_DIR) && COMPOSE_BAKE=true docker compose up -d
	@echo -e "$(GREEN)Control plane up.$(NC) ca: https://localhost:9000  catalog: localhost:15723"

demo-up: ## Bring up the self-contained demo lab (ca + issuer + catalog + policy-server + database + webserver + store)
	@./demo/up.sh

demo-down: ## Tear down the demo lab and wipe all its volumes
	@cd demo && docker compose down -v
