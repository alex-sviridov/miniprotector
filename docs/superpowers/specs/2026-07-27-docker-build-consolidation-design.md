# Design: consolidated Docker build for demo & control-plane images

**Date:** 2026-07-27
**Status:** Approved for planning

## Problem

`demo/docker-compose.yml` and `deploy/control-plane/docker-compose.yml` build their service images
from 7 separate Dockerfiles (`demo/backup-host`, `demo/ca`, `demo/issuer`,
`deploy/control-plane/{api-server,catalog,clientmanager-api,issuer,log-gateway,policy-server}`).
Each one is a self-contained multi-stage build: `FROM golang:1.26 AS builder`, `COPY . .`, then its
own `RUN make <binary list>`.

Six of those `make` invocations overlap heavily — `backup-host`, `api-server`, `catalog`,
`policy-server`, `log-gateway`, and `clientmanager-api` all build `certclient`, `agent`, and
`policyclient` from the exact same source, in addition to their own component binary. Because each
Dockerfile's `RUN make ...` line lists a different set of targets, the command strings differ, so
Docker's layer cache never matches across them — Go recompiles these three binaries from scratch in
up to six separate builder stages every time the demo or control-plane stack is built, even though
the resulting binaries are byte-identical.

## Approach

Replace the 7 Dockerfiles with one multi-stage `deploy/build/Dockerfile` containing a single shared
`builder` stage that compiles every binary once, and one final stage per distinct runtime image.
Both compose files point their `build.dockerfile` at this one file and select their image via
`build.target`.

This was chosen over two alternatives:

- **BuildKit cache mounts only** (persist `GOCACHE`/`GOMODCACHE` across builder stages without
  restructuring) — genuinely helps, and is folded into this design anyway (see below), but on its
  own still runs `go build` up to six times per build; it speeds up recompilation, it doesn't
  eliminate it.
- **A separately pre-built base image** referenced via `FROM miniprotector-builder:local` in each
  Dockerfile — achieves the same one-compile goal, but needs an out-of-band build-order step (build
  and tag the base image before `docker compose build` runs), since compose has no native concept of
  "build this local image first, then use it as another service's `FROM`." The single-Dockerfile
  approach gets the same result using only compose's existing `target:` field.

`api-server`, `catalog`, `policy-server`, `log-gateway`, and `clientmanager-api` are already the
same physical Dockerfile shared by both compose files today (identical `dockerfile:` path,
referenced with different relative `context:`). That sharing is preserved — those five just become
final stages in the unified file instead of standalone files.

`issuer-demo` and `issuer-controlplane` stay as two separate final stages rather than being merged.
They differ in the image itself, not just entrypoint arguments: the demo variant creates a uid-1000
`issuer` user (to keep `/data/client-manager`, a volume shared with the `ca` container, writable by
both), while control-plane's runs as root and takes different CA connection flags. Unifying them
would change control-plane's runtime behavior, which is out of scope for a build-efficiency fix.
Both stages still consume the one shared `builder` stage, so the compile-once goal is met without
touching that difference.

## `deploy/build/Dockerfile` structure

```dockerfile
# --- shared builder ---
FROM golang:1.26 AS builder
WORKDIR /build
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make build
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 make clientmanager

# --- shared vector binary source ---
FROM timberio/vector:0.46.0-debian AS vector-source

# --- one final stage per runtime image ---
FROM debian:bookworm-slim AS backup-host
...
FROM smallstep/step-ca AS ca
...
FROM debian:bookworm-slim AS issuer-demo
...
FROM debian:bookworm-slim AS issuer-controlplane
...
FROM debian:bookworm-slim AS api-server
...
FROM debian:bookworm-slim AS catalog
...
FROM debian:bookworm-slim AS policy-server
...
FROM debian:bookworm-slim AS log-gateway
...
FROM debian:bookworm-slim AS clientmanager-api
...
```

Each final stage is a direct port of its old Dockerfile's second stage (same `apt-get install`
list, same `COPY --from=builder` binary list, same entrypoint script, same user/permission setup)
— only the source of the `builder`/`vector-source` stages changes, not the final-stage content.

### The `clientmanager` CGO exception

`demo/ca/Dockerfile` today builds `clientmanager` with `CGO_ENABLED=0` via a direct `go build`
(bypassing `make`), producing a static binary — its final image (`smallstep/step-ca`) never runs
`apt-get install libgcc-s1`, unlike every other final stage, which strongly suggests this was
intentional: that base image can't satisfy a dynamically-linked binary's libc dependency. Everything
else, including the Makefile's own `clientmanager` target, defaults to `CGO_ENABLED=1`.

This is preserved exactly, not "fixed": `make build` runs first (`CGO_ENABLED=1`, all 15 binaries),
then `make clientmanager` runs again with `CGO_ENABLED=0`, overwriting just that one binary. Every
Makefile target is declared `.PHONY`, so the second invocation always re-runs regardless of file
timestamps — this is a reliable override, not a race against Make's staleness check.

### Cache mounts

`--mount=type=cache` for `GOCACHE` and `GOMODCACHE` is added even though there's now only one
builder stage, because editing any source file still busts the `COPY . .` layer and forces that
stage to rebuild. With the cache mounts, that rebuild only recompiles changed packages instead of
starting from zero — this addresses the inner dev-loop case (edit code, rebuild demo stack) that
stage-sharing alone doesn't help with.

## Compose file changes

Both compose files change every build-based service from its own `dockerfile:` to the shared file
plus a `target:`:

```yaml
# demo/docker-compose.yml
services:
  database:            # and webserver, store
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: backup-host
  ca:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: ca
  issuer:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: issuer-demo
  api-server:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: api-server
  # catalog, policy-server, log-gateway, clientmanager-api analogous
```

```yaml
# deploy/control-plane/docker-compose.yml
services:
  issuer:
    build:
      context: ../..
      dockerfile: deploy/build/Dockerfile
      target: issuer-controlplane
  # api-server, catalog, policy-server, log-gateway, clientmanager-api
  # analogous, context: ../..
```

`web/Dockerfile` (Node/nginx, unrelated binaries) is untouched.

### Enabling parallel shared-stage builds

Plain sequential `docker compose build` already benefits from this refactor with no extra
configuration: every service now resolves to the same Dockerfile and the same `builder` stage, so
BuildKit's ordinary local layer cache hits for every service after the first one builds it.

Docker Compose Bake (`COMPOSE_BAKE=true`) adds parallel building of the independent final stages on
top of that, computing the whole compose file as one `buildx bake` DAG instead of one sequential
`docker build` per service. `demo/up.sh` and the Makefile's `demo-up` and `control-plane-up` targets
will `export COMPOSE_BAKE=true` before invoking `docker compose build`, so this is automatic.

## Cleanup

Delete the 7 superseded Dockerfiles:

- `demo/backup-host/Dockerfile`
- `demo/ca/Dockerfile`
- `demo/issuer/Dockerfile`
- `deploy/control-plane/api-server/Dockerfile`
- `deploy/control-plane/catalog/Dockerfile`
- `deploy/control-plane/clientmanager-api/Dockerfile`
- `deploy/control-plane/issuer/Dockerfile`
- `deploy/control-plane/log-gateway/Dockerfile`
- `deploy/control-plane/policy-server/Dockerfile`

Entrypoint scripts and other non-Dockerfile files in those directories (e.g.
`demo/ca/entrypoint.sh`, `deploy/control-plane/api-server/entrypoint.sh`) stay where they are —
the new final stages `COPY` them from their existing paths, same as the old Dockerfiles did.

## Verification plan

- `make demo-up` from a cold Docker build cache: all 8 demo images build; build output shows
  `agent`/`certclient`/`policyclient` compiling only in the `builder` stage's first execution, with
  every subsequent consuming stage showing a cache hit rather than a recompile; the stack reaches
  the same enrolled/healthy state as today (repeat the `demo/README.md` walkthrough — backup a file,
  list, verify, check the catalog).
- `docker compose -f deploy/control-plane/docker-compose.yml build`: confirms `issuer-controlplane`
  behaves unchanged (runs as root, connects to `step-ca` with its existing flags).
- Confirm the `clientmanager` binary inside the `ca` image is still statically linked
  (`CGO_ENABLED=0`) post-refactor, e.g. via `ldd` reporting "not a dynamic executable".
- Edit one file under `src/` and rebuild: confirm via build output that only the packages
  depending on that file recompile (cache-mount effectiveness), not the full `builder` stage.

## Documentation

Per `.claude/CLAUDE.md`'s feature-change rules:

- `demo/README.md` — update the "Builds all eight images" line and any other reference to the old
  per-service Dockerfile paths.
- `docs/components/*.md` — update any component doc that references its old Dockerfile location.
- `docs/ARCHITECTURE.md` — check during implementation; build topology isn't currently described
  there, so likely no change needed.
- `CHANGELOG.md` — one dated entry describing the consolidation and why (eliminating redundant
  recompilation of shared binaries across demo/control-plane images).

## Out of scope

- Wiring this into CI — not currently requested; the compose files aren't referenced from a CI
  pipeline in this repo today.
- Merging `issuer-demo` and `issuer-controlplane` into one stage — would change control-plane's
  runtime user/permission model, a separate decision from build efficiency.
- Changing which binaries `make build` compiles, or the CGO defaults for anything other than the
  pre-existing `clientmanager` exception.
