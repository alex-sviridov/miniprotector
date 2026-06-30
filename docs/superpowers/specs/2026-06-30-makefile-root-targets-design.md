# Makefile Relocation and New Targets — Design

## Context

The Makefile currently lives at `src/Makefile`, requiring every build invocation to `cd src` first. This is inconsistent with the README, which already documents top-level `make build` (no `cd src`) — that instruction is currently wrong. Additionally, the Makefile declares `.PHONY: ... test lint` but implements neither target, despite both being referenced as available.

This change moves the Makefile to the repo root and adds working `test`, `test-e2e`, and `lint` targets, aligning the build system with what's already documented and closing the gap between declared and implemented targets.

## Scope

- Move `src/Makefile` → `Makefile` (repo root)
- Add `make test` (fast unit/integration suite)
- Add `make test-e2e` (Docker-based e2e suite, separate from `make test` so the common path stays fast)
- Add `make lint` (`go vet`, no new external tooling)
- Fix `src/e2e/Dockerfile`, which currently depends on the Makefile living inside `src/`
- Update build instructions in `README.md`, `docs/components/bwfs.md`, `docs/components/brfs.md`

## Makefile Changes

The Go module root remains `src/go.mod` — moving the Makefile does not move the module. Every target needing `go`/`protoc` runs via `cd src && ...`, mirroring the existing style (no `$(MAKE) -C`).

**Path changes:**
- `BINARY_DIR := ../bin` → `BINARY_DIR := bin` (same physical location: `<repo-root>/bin/`, now expressed relative to the Makefile's new home)
- `proto` target's `api/*.proto` → `src/api/*.proto`, with `--go_out`/`--go-grpc_out` paths adjusted so generated files still land in `src/api/`
- Each binary target's build command gains a `cd src &&` prefix; `-o` path becomes `../$(BINARY_DIR)/<binary>` relative to `src/`, i.e. `../bin/<binary>` from inside the `cd src` subshell — equivalent to `bin/<binary>` from the repo root

**New targets:**

```makefile
test: ## Run unit and integration tests
	cd src && go test ./...

test-e2e: ## Run Docker-based e2e tests (requires Docker daemon, ~3 min)
	cd src && go test -tags=e2e -timeout=300s ./e2e/...

lint: ## Run go vet
	cd src && go vet ./...
```

`test-e2e` is intentionally separate from `test`: it needs a running Docker daemon and takes ~165s (vs. under a second for the existing unit suite), so keeping it opt-in preserves `make test` as a fast, dependency-light default.

`lint` uses `go vet` only — golangci-lint is not installed in this environment and the project has no existing lint config, so adding it would introduce a new tooling dependency. `go vet` requires nothing beyond the Go toolchain already in use.

## Dockerfile Fix

`src/e2e/Dockerfile`'s builder stage currently does:
```dockerfile
WORKDIR /build/src
COPY src/ .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs
```

This only works because the Makefile is copied along with everything else under `src/` and is invoked from inside that directory. After the move, the Makefile is no longer under `src/`, so this breaks.

**Fix:** copy the full build context (repo root) instead of just `src/`, and run `make` from the copied root:
```dockerfile
WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs
```

`src/e2e/docker.go`'s `addDirToTar` (which builds the Docker build context by hand-walking the filesystem, scoped to `src/` only — a fix from the prior e2e work) needs to widen its walk to include the repo-root `Makefile` alongside `src/`, since the Dockerfile's `COPY . .` now expects both. The binary output paths inside the container (`/build/bin/brfs`, `/build/bin/bwfs`, copied into the runtime stage) stay the same since `BINARY_DIR := bin` resolves to `/build/bin` when `make` runs from `/build`.

This is verified for real (not just by inspection) by running `make test-e2e` after the change, since this is exactly the kind of cross-file path assumption that caused regressions during the original e2e work.

## Documentation Updates

- `README.md`: already says `make build` correctly; add `make test` and `make lint` to the same Building section
- `docs/components/bwfs.md`: `cd src && make build` → `make build`
- `docs/components/brfs.md`: `cd srv && make build` → `make build` (also fixes a pre-existing typo — `srv` was never a real directory in this repo)

## Verification

1. `make build` from repo root — produces `bin/brfs`, `bin/bwfs`, `bin/rrfs`
2. `make test` from repo root — runs the existing unit/integration suite, all passing
3. `make lint` from repo root — runs `go vet` cleanly (the one pre-existing `cmd/brfs/filesstream.go` vet warning is expected and unrelated to this change)
4. `make test-e2e` from repo root — runs the real Docker-based e2e suite end-to-end, confirming the Dockerfile fix works, not just compiles
5. Manual read-through of updated README.md and docs/components/{bwfs,brfs}.md for correctness
