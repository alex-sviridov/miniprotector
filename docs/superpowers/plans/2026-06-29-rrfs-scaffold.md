# rrfs Scaffold Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bootstrap `rrfs` (Restore Reader for File System) with argument parsing, a server stub, and a generalized `connection.StartServer` that both `bwfs` and `rrfs` can share.

**Architecture:** `rrfs` mirrors `bwfs` in structure — a `<storage_path> server [flags]` CLI backed by the same config/logging/connection packages. `connection.StartServer` is refactored to accept a `func(*grpc.Server)` registrar so it is no longer tied to `BackupServiceServer`. The `rrfs` server struct is a stub with no stream logic yet — just enough to compile, start, and log.

**Tech Stack:** Go 1.26, Cobra (CLI), gRPC (`google.golang.org/grpc`), `common/config`, `common/logging`, `common/args.go` helpers.

## Global Constraints

- Module path: `github.com/alex-sviridov/miniprotector`
- Config file path constant: `"../.config/local.conf"`
- Port range: 1024–65535 (enforced by `common.ValidatePort`)
- No new proto file in this plan — restore proto is future work
- Do not implement any stream/restore logic — stub only
- Follow the exact same cobra `os.Args` extraction pattern as `bwfs/arguments.go`
- Binary output dir: `../bin/rrfs`
- CGO_ENABLED=1, GOOS=linux, GOARCH=amd64

---

### Task 1: Generalize `connection.StartServer`

**Files:**
- Modify: `src/common/connection/server.go`
- Modify: `src/cmd/bwfs/main.go` (update call site)

**Interfaces:**
- Produces: `StartServer(ctx context.Context, logger *slog.Logger, port int, register func(*grpc.Server)) error`
- The `register` func receives the grpc.Server and calls the appropriate `pb.RegisterXxxServer` on it.

- [ ] **Step 1: Update `StartServer` signature in `src/common/connection/server.go`**

Replace the entire file content with:

```go
package connection

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"google.golang.org/grpc"
)

// StartServer creates and starts a gRPC server on the specified port.
// The register callback receives the bare *grpc.Server so callers can
// register any service (backup, restore, …) without this package
// importing service-specific proto packages.
func StartServer(ctx context.Context, logger *slog.Logger, port int, register func(*grpc.Server)) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	grpcServer := grpc.NewServer()
	register(grpcServer)

	logger.Info("Server ready, accepting connections")

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	return grpcServer.Serve(listener)
}
```

- [ ] **Step 2: Update the call site in `src/cmd/bwfs/main.go`**

Find the `connection.StartServer(...)` call (currently line ~53) and replace:

```go
// before
if err := connection.StartServer(ctx, logger, arguments.Port, backupServer); err != nil {
```

with:

```go
// after
if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {
    pb.RegisterBackupServiceServer(s, backupServer)
}); err != nil {
```

Add `"google.golang.org/grpc"` to the import block in `bwfs/main.go` if not already present.

- [ ] **Step 3: Verify the project still builds**

```bash
cd /home/alex/miniprotector/src && go build ./...
```

Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add common/connection/server.go cmd/bwfs/main.go && \
git commit -m "refactor: generalize connection.StartServer to accept a grpc registrar func"
```

---

### Task 2: Create `rrfs` argument parser

**Files:**
- Create: `src/cmd/rrfs/arguments.go`

**Interfaces:**
- Produces: `type Arguments struct { StoragePath string; Action string; Port int; Debug bool; Quiet bool }`
- Produces: `func parseArguments(conf *config.Config) (*Arguments, error)`

- [ ] **Step 1: Create `src/cmd/rrfs/arguments.go`**

```go
package main

import (
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	StoragePath string
	Action      string // "server"
	Port        int
	Debug       bool
	Quiet       bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	// storage_path is the first positional arg before the subcommand.
	// Extract it before cobra sees os.Args, same pattern as bwfs.
	if len(os.Args) < 3 {
		return nil, fmt.Errorf("usage: rrfs <storage_path> <server> [flags]")
	}
	storagePath := os.Args[1]
	os.Args = append(os.Args[:1], os.Args[2:]...)

	args := &Arguments{StoragePath: storagePath}

	rootCmd := &cobra.Command{
		Use:   "rrfs <storage_path> <command>",
		Short: "Restore reader filesystem tool",
	}

	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Start the restore reader server",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "server" },
	}
	serverCmd.Flags().IntVar(&args.Port, "port", conf.DefaultPort, "Port to listen on")
	serverCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	serverCmd.Flags().BoolVar(&args.Quiet, "quiet", false, "Suppress console logging")

	rootCmd.AddCommand(serverCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: server")
	}

	if err := common.ValidatePort(args.Port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}

	if _, err := common.ValidatePath(args.StoragePath); err != nil {
		return nil, fmt.Errorf("storage path error: %w", err)
	}

	return args, nil
}
```

- [ ] **Step 2: Verify it compiles in isolation**

```bash
cd /home/alex/miniprotector/src && go build ./cmd/rrfs/...
```

Expected: fails with "no Go files" or "undefined: main" — that's fine, `main.go` doesn't exist yet. What must NOT appear: import errors or syntax errors in `arguments.go`.

> Tip: run `go vet ./cmd/rrfs/...` — it will error on missing `main` but must not error on `arguments.go` itself. Alternatively, check with `go build -v ./cmd/rrfs/...` and confirm only "no Go files" or similar, no parse/type errors.

---

### Task 3: Create `rrfs` server stub

**Files:**
- Create: `src/cmd/rrfs/server.go`

**Interfaces:**
- Consumes: `storage.BackupStore` from `src/storage/interface.go` (read-only usage planned — same store, read path only)
- Produces: `type restoreServer struct { store storage.BackupStore; logger *slog.Logger }`
- Produces: `func NewRestoreServer(ctx context.Context, logger *slog.Logger, storagePath string) (*restoreServer, error)`

Note: `rrfs` opens the store read-only via `wfs.NewReadOnly(storagePath)` — it never writes.

- [ ] **Step 1: Create `src/cmd/rrfs/server.go`**

```go
package main

import (
	"context"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type restoreServer struct {
	store  storage.BackupStore
	logger *slog.Logger
}

func NewRestoreServer(ctx context.Context, logger *slog.Logger, storagePath string) (*restoreServer, error) {
	store, err := wfs.NewReadOnly(storagePath)
	if err != nil {
		return nil, err
	}
	return &restoreServer{
		store:  store,
		logger: logger,
	}, nil
}
```

---

### Task 4: Create `rrfs` main and wire everything together

**Files:**
- Create: `src/cmd/rrfs/main.go`
- Modify: `src/Makefile`

**Interfaces:**
- Consumes: `parseArguments(*config.Config) (*Arguments, error)` from Task 2
- Consumes: `NewRestoreServer(ctx, logger, storagePath) (*restoreServer, error)` from Task 3
- Consumes: `connection.StartServer(ctx, logger, port, func(*grpc.Server))` from Task 1

Note: `rrfs` has no gRPC service to register yet — `register` calls `grpc.NewServer()` with no service, so connections will be accepted but immediately return "unimplemented". That is the correct stub behavior.

- [ ] **Step 1: Create `src/cmd/rrfs/main.go`**

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"google.golang.org/grpc"
)

func main() {
	const (
		configPath = "../.config/local.conf"
		appName    = "rrfs"
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = context.WithValue(ctx, "appName", appName)

	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, config.ContextKey, conf)

	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", arguments.Quiet)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	switch arguments.Action {
	case "server":
		logger.Info("Restore reader started",
			"storagePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)
		srv, err := NewRestoreServer(ctx, logger, arguments.StoragePath)
		if err != nil {
			logger.Error("Server initialization failed", "error", err)
			os.Exit(1)
		}
		defer srv.store.Close()

		if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {
			// Restore service registration goes here once the proto is defined.
			_ = s
		}); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}
}
```

- [ ] **Step 2: Add `rrfs` to the Makefile**

In `src/Makefile`, find the line:

```makefile
BWFS_CMD := cmd/bwfs
```

Add below it:

```makefile
RRFS_CMD := cmd/rrfs
```

Find:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs test lint
```

Replace with:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rrfs test lint
```

Find:

```makefile
bwfs: $(BINARY_DIR) ## Build bwfs binary
	@printf "$(BLUE)Building bwfs...$(NC) "
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/bwfs ./$(BWFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/bwfs"
```

Add after it:

```makefile
rrfs: $(BINARY_DIR) ## Build rrfs binary
	@printf "$(BLUE)Building rrfs...$(NC) "
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/rrfs ./$(RRFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/rrfs"
```

- [ ] **Step 3: Build everything**

```bash
cd /home/alex/miniprotector/src && make build
```

Expected output includes:
```
Built successfully:../bin/brfs
Built successfully:../bin/bwfs
Built successfully:../bin/rrfs
```

- [ ] **Step 4: Smoke-test the binary**

```bash
/home/alex/miniprotector/bin/rrfs --help
```

Expected: cobra usage output listing the `server` subcommand, no panic.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector/src && \
git add cmd/rrfs/arguments.go cmd/rrfs/server.go cmd/rrfs/main.go Makefile && \
git commit -m "feat: add rrfs scaffold with argument parser and server stub"
```

---

## Self-Review

**Spec coverage:**
- [x] Argument parser with `<storage_path> server [--port --debug --quiet]` — Task 2
- [x] Server stub that opens the store read-only — Task 3
- [x] `main.go` wiring context/logger/signal handling — Task 4
- [x] `connection.StartServer` generalized — Task 1
- [x] `bwfs` call site updated — Task 1 Step 2
- [x] Makefile updated — Task 4 Step 2
- [x] No network/stream logic introduced — confirmed, stub only

**Placeholder scan:** No TBD/TODO outside the intentional `// Restore service registration goes here` comment, which is load-bearing documentation, not a spec gap.

**Type consistency:**
- `Arguments` defined in Task 2, consumed in Task 4 — field names match.
- `restoreServer` defined in Task 3, consumed in Task 4 — `srv.store.Close()` matches `storage.BackupStore` interface.
- `StartServer` new signature defined in Task 1, used in Task 4 — `func(*grpc.Server)` matches.
