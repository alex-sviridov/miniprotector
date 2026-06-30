# Makefile Relocation and New Targets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move `src/Makefile` to the repo root and add working `test`, `test-e2e`, and `lint` targets, fixing the e2e Dockerfile's dependency on the Makefile's old location along the way.

**Architecture:** The Makefile moves to `Makefile` (repo root); the Go module stays at `src/go.mod`, so every target that runs `go`/`protoc` does `cd src && ...` internally. `BINARY_DIR` changes from `../bin` to `bin` (same physical output location, now expressed relative to the Makefile's new home). The e2e Dockerfile's builder stage switches from `COPY src/ .` to `COPY . .` so it picks up the now-root-level Makefile, and `src/e2e/docker.go`'s hand-rolled build-context tar walker widens from `src/`-only to also include the root `Makefile`.

**Tech Stack:** GNU Make, Go 1.26, Docker (multi-stage build), no new dependencies.

## Global Constraints

- Go module root stays at `src/go.mod` — do not move it.
- Binary output location stays `<repo-root>/bin/` (physical path unchanged).
- `make lint` uses `go vet` only — no golangci-lint (not installed in this environment, no existing config).
- `make test-e2e` is separate from `make test` — `make test` must stay fast and not require Docker.
- `test-e2e` command: `go test -tags=e2e -timeout=300s ./e2e/...` (matches the convention used throughout the e2e work — see `docs/superpowers/plans/2026-06-29-e2e-brfs-bwfs.md`).
- The e2e Docker build must keep working — `src/e2e/Dockerfile` and `src/e2e/docker.go`'s `addDirToTar` call are interdependent; both must change together.

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `src/Makefile` → `Makefile` | Move + edit | Build/test/lint targets, paths rewritten for new location |
| `src/e2e/Dockerfile` | Modify | Copy whole build context instead of just `src/` |
| `src/e2e/docker.go` | Modify | Widen build-context tar to include root `Makefile` |
| `README.md` | Modify | Add `make test` / `make lint` to Building section |
| `docs/components/bwfs.md` | Modify | `cd src && make build` → `make build` |
| `docs/components/brfs.md` | Modify | `cd srv && make build` → `make build` (also fixes pre-existing typo) |

---

## Task 1: Move Makefile, add test/lint targets, fix Dockerfile, update docs

**Files:**
- Move: `src/Makefile` → `Makefile`
- Modify: `src/e2e/Dockerfile`
- Modify: `src/e2e/docker.go:54-60` (the comment and the `addDirToTar` call in `buildImage`)
- Modify: `README.md`
- Modify: `docs/components/bwfs.md`
- Modify: `docs/components/brfs.md`

**Interfaces:**
- Produces: `make build`, `make test`, `make test-e2e`, `make lint`, `make proto`, `make clean`, `make brfs`/`bwfs`/`rrfs`, `make help`, `make check-deps` — all runnable from the repo root.

- [ ] **Step 1: Read the current Makefile to confirm exact content before editing**

```bash
cat /home/alex/miniprotector/src/Makefile
```

Confirm it matches this (already read during planning):

```makefile
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
RRFS_CMD := cmd/rrfs

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[0;33m
BLUE := \033[0;34m
NC := \033[0m # No Color

.PHONY: all build clean proto check-deps help brfs bwfs rrfs test lint

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

rrfs: $(BINARY_DIR) ## Build rrfs binary
	@printf "$(BLUE)Building rrfs...$(NC) "
	@CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o $(BINARY_DIR)/rrfs ./$(RRFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/rrfs"
```

If it differs from the above, stop and report the difference before proceeding — the rest of this task assumes this exact starting content.

- [ ] **Step 2: Create the new root Makefile**

Note every `$(GO)`/`protoc` invocation now runs inside `cd src && ...` since the module root stays at `src/`. `BINARY_DIR` becomes `bin` (no `../`) since the Makefile itself now lives at the repo root, one level above where it used to be — `bin` from the new location is the same physical directory as `../bin` was from the old one. Inside the `cd src` subshell, binaries are written to `../$(BINARY_DIR)` (i.e. `../bin`) so they land in the same `<repo-root>/bin/` either way.

Write this exact content to `/home/alex/miniprotector/Makefile`:

```makefile
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
RRFS_CMD := cmd/rrfs

# Colors for output
RED := \033[0;31m
GREEN := \033[0;32m
YELLOW := \033[0;33m
BLUE := \033[0;34m
NC := \033[0m # No Color

.PHONY: all build clean proto check-deps help brfs bwfs rrfs test test-e2e lint

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

rrfs: $(BINARY_DIR) ## Build rrfs binary
	@printf "$(BLUE)Building rrfs...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/rrfs ./$(RRFS_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/rrfs"

test: ## Run unit and integration tests
	cd src && go test ./...

test-e2e: ## Run Docker-based e2e tests (requires Docker daemon, ~3 min)
	cd src && go test -tags=e2e -timeout=300s ./e2e/...

lint: ## Run go vet
	cd src && go vet ./...

clean: ## Remove built binaries
	rm -rf $(BINARY_DIR)
```

(Note: this adds a `clean` target that wasn't in the original Makefile. It's included because `BINARY_DIR` is changing location — without `clean`, stale binaries could be left behind at the old `src/../bin` path with nothing to remove them. The `.PHONY` line above already lists `clean` alongside the other targets, so no further edit is needed there.)

- [ ] **Step 3: Delete the old Makefile**

```bash
rm /home/alex/miniprotector/src/Makefile
```

- [ ] **Step 4: Verify `make build` works from the repo root**

```bash
cd /home/alex/miniprotector && rm -rf bin && make build
ls bin/
```

Expected: `bin/brfs`, `bin/bwfs`, `bin/rrfs` all present, each an executable file.

- [ ] **Step 5: Verify `make test` works**

```bash
cd /home/alex/miniprotector && make test
```

Expected: all existing unit/integration tests pass (same output as `cd src && go test ./...` previously produced — `ok` for `cmd/bwfs`, `storage/filesystem`, `workload/filesystem`; `[no test files]` for the rest).

- [ ] **Step 6: Verify `make lint` works**

```bash
cd /home/alex/miniprotector && make lint
```

Expected: exits non-zero with exactly one pre-existing warning in `cmd/brfs/filesstream.go` (`cancelAllStreams` possible context leak — this predates this change and is not something this task fixes). No other warnings.

- [ ] **Step 7: Fix the e2e Dockerfile to work with the relocated Makefile**

Read current content first:
```bash
cat /home/alex/miniprotector/src/e2e/Dockerfile
```

Replace `/home/alex/miniprotector/src/e2e/Dockerfile`'s builder stage. Change:
```dockerfile
WORKDIR /build/src
COPY src/ .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs
```
to:
```dockerfile
WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs
```

The full file after this edit:
```dockerfile
FROM golang:1.26 AS builder

RUN apt-get update && apt-get install -y --no-install-recommends \
    protobuf-compiler \
    && rm -rf /var/lib/apt/lists/*

RUN go install google.golang.org/protobuf/cmd/protoc-gen-go@latest \
    && go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/brfs /build/bin/bwfs ./
COPY src/e2e/config.conf .config/local.conf
```

(`/build/bin/brfs` and `/build/bin/bwfs` are correct: with `WORKDIR /build` and the new root Makefile's `BINARY_DIR := bin`, binaries land at `/build/bin/brfs` and `/build/bin/bwfs` — same paths as before, since `/build/src/../bin` and `/build/bin` are the same directory.)

- [ ] **Step 8: Widen the e2e build-context tar walker to include the root Makefile**

Read current content first:
```bash
sed -n '1,65p' /home/alex/miniprotector/src/e2e/docker.go
```

In `/home/alex/miniprotector/src/e2e/docker.go`, find this block (around lines 46-62):
```go
// buildImage builds the e2e Docker image from the repo root.
// repoRoot is the directory containing src/ and src/e2e/Dockerfile.
// Returns the image ID. Caller is responsible for cleanup.
func buildImage(ctx context.Context, t testingT, repoRoot string) string {
	t.Helper()
	cli := newDockerClient(t)
	defer cli.Close()

	// Create build context tar. The Dockerfile only ever references src/
	// (COPY src/ .), so only walk that subtree rather than the whole
	// repoRoot — this keeps .git/, bin/, docs/, etc. out of the in-memory
	// tar buffer and out of what's sent to the Docker daemon.
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	err := addDirToTar(tw, filepath.Join(repoRoot, "src"), "src")
	require.NoError(t, err)
	require.NoError(t, tw.Close())
```

Replace it with (the Dockerfile now does `COPY . .`, so the tar needs both `src/` and the root `Makefile`; still excluding `.git/`, `bin/`, `docs/`, etc. by only adding exactly these two entries rather than walking the whole repo root):
```go
// buildImage builds the e2e Docker image from the repo root.
// repoRoot is the directory containing src/, Makefile, and src/e2e/Dockerfile.
// Returns the image ID. Caller is responsible for cleanup.
func buildImage(ctx context.Context, t testingT, repoRoot string) string {
	t.Helper()
	cli := newDockerClient(t)
	defer cli.Close()

	// Create build context tar. The Dockerfile does `COPY . .` against this
	// context, but only needs src/ and the root Makefile — so only walk
	// those rather than the whole repoRoot, keeping .git/, bin/, docs/,
	// etc. out of the in-memory tar buffer and out of what's sent to the
	// Docker daemon.
	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	err := addDirToTar(tw, filepath.Join(repoRoot, "src"), "src")
	require.NoError(t, err)
	err = addFileToTar(tw, filepath.Join(repoRoot, "Makefile"), "Makefile")
	require.NoError(t, err)
	require.NoError(t, tw.Close())
```

This calls a new `addFileToTar` helper (singular file, not a directory walk) that doesn't exist yet. Find the existing `addDirToTar` function in the same file (search for `func addDirToTar`) and add this new function immediately after it:

```go
// addFileToTar adds a single file to the tar writer at the given path.
func addFileToTar(tw *tar.Writer, srcPath, tarPath string) error {
	info, err := os.Stat(srcPath)
	if err != nil {
		return err
	}
	hdr, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	hdr.Name = tarPath
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}
```

Check the existing imports in `src/e2e/docker.go` (top of file) — `os` and `io` should already be imported (used by `addDirToTar`); if not, add them.

- [ ] **Step 9: Verify the e2e Docker build and full e2e suite work end-to-end**

```bash
cd /home/alex/miniprotector/src && go build -tags=e2e ./e2e/...
cd /home/alex/miniprotector/src && go vet -tags=e2e ./e2e/...
```
Expected: both clean, no errors.

```bash
cd /home/alex/miniprotector && make test-e2e
```
Expected: both `TestE2E_SingleSubfolderBackup` and `TestE2E_AllFoldersBackup` PASS, similar timing to prior runs (~14s and ~17s respectively, total suite under 300s). This is the real proof the Dockerfile fix and the widened tar walker work together correctly — not just that they compile.

If this fails, debug using the Docker build output (`buildImage` logs `[docker build]` lines via `t.Log`) before concluding the fix is wrong — check that `/build/Makefile` and `/build/src/` both land correctly in the container's build context.

- [ ] **Step 10: Update README.md's Building section**

Read current content first:
```bash
sed -n '40,50p' /home/alex/miniprotector/README.md
```

Find the Building section (currently just shows `make build`). Replace it with:
```markdown
## Building

```bash
# Build all components
make build

# Run unit and integration tests
make test

# Run go vet
make lint

# Run Docker-based e2e tests (requires Docker daemon, ~3 min)
make test-e2e
```
```

Use the Edit tool to make this change — read the exact surrounding text first since the closing code fence and any trailing content must be preserved correctly.

- [ ] **Step 11: Fix docs/components/bwfs.md's Building section**

Current content (lines 50-54):
```markdown
## Building

```bash
cd src && make build
```
```

Change to:
```markdown
## Building

```bash
make build
```
```

- [ ] **Step 12: Fix docs/components/brfs.md's Building section**

Read the current file first:
```bash
cat /home/alex/miniprotector/docs/components/brfs.md
```

Find the Building section (around lines 43-46, currently `cd srv` then `make build` on separate lines — `srv` is a stale typo, this directory has never existed in this repo). Change:
```markdown
## Building

```bash
cd srv
make build
```
```
to:
```markdown
## Building

```bash
make build
```
```

- [ ] **Step 13: Final full verification**

```bash
cd /home/alex/miniprotector && make build && make test && make lint
```
Expected: build succeeds, test suite passes, lint shows only the one known pre-existing `filesstream.go` warning.

```bash
git status
```
Expected changes: `Makefile` (new, at root), `src/Makefile` (deleted), `src/e2e/Dockerfile` (modified), `src/e2e/docker.go` (modified), `README.md` (modified), `docs/components/bwfs.md` (modified), `docs/components/brfs.md` (modified).

- [ ] **Step 14: Commit**

```bash
cd /home/alex/miniprotector
git add Makefile src/e2e/Dockerfile src/e2e/docker.go README.md docs/components/bwfs.md docs/components/brfs.md
git status
```

Confirm `src/Makefile` shows as deleted (it will appear automatically in `git add -A`-style tracking once the rename/delete is staged — use `git add -A -- src/Makefile Makefile` if `git add Makefile` alone doesn't pick up the deletion):
```bash
git add -A -- src/Makefile Makefile
```

```bash
git commit -m "build: move Makefile to repo root, add test/test-e2e/lint targets

Adds working make test, make test-e2e, and make lint targets (previously
declared in .PHONY but never implemented). Moving the Makefile to the
repo root matches what README.md already documented (plain 'make build',
no 'cd src' prefix). Fixes src/e2e/Dockerfile and its build-context tar
construction in src/e2e/docker.go, both of which depended on the Makefile
living inside src/."
```

---

## Self-Review

**Spec coverage:**
- ✅ Move `src/Makefile` → `Makefile` — Steps 2-3
- ✅ `make test` — Step 2 (target), Step 5 (verified)
- ✅ `make test-e2e` — Step 2 (target), Step 9 (verified)
- ✅ `make lint` (go vet) — Step 2 (target), Step 6 (verified)
- ✅ Fix `src/e2e/Dockerfile` — Step 7
- ✅ Fix `src/e2e/docker.go`'s build-context scope — Step 8
- ✅ Update README.md — Step 10
- ✅ Update docs/components/bwfs.md — Step 11
- ✅ Update docs/components/brfs.md (bonus fix) — Step 12

**Placeholder scan:** No TBDs or vague instructions found.

**Type consistency:** `addFileToTar(tw *tar.Writer, srcPath, tarPath string) error` matches the existing `addDirToTar(tw *tar.Writer, srcDir, prefix string) error` signature style in the same file — consistent parameter ordering and return type.
