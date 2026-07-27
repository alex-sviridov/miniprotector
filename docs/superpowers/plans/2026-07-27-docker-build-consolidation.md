# Docker Build Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop `agent`, `certclient`, and `policyclient` from being recompiled from scratch in up to six separate Docker builder stages every time the demo or control-plane stack is built.

**Architecture:** Replace 7 separate per-image Dockerfiles with one `deploy/build/Dockerfile` containing a single shared `builder` stage (compiles all 15 Go binaries once) and one final runtime stage per image, selected via Compose's `build.target`. Both `demo/docker-compose.yml` and `deploy/control-plane/docker-compose.yml` point at this one file.

**Tech Stack:** Docker BuildKit (multi-stage builds, `--mount=type=cache`), Docker Compose `build.target`, Docker Compose Bake (`COMPOSE_BAKE=true`), Go 1.26, existing repo `Makefile`.

## Global Constraints

- Every final stage's runtime behavior (installed packages, users, entrypoint, file layout) must be byte-for-byte equivalent to today's — this is a build-mechanics change only, no runtime behavior change. (spec: "Approach")
- `clientmanager` must remain a `CGO_ENABLED=0` static binary; every other binary keeps `CGO_ENABLED=1`. (spec: "The `clientmanager` CGO exception")
- `issuer-demo` and `issuer-controlplane` stay as two distinct final stages — do not merge them. (spec: "Approach")
- `web/Dockerfile` is out of scope — do not touch it.
- Delete the 7 superseded Dockerfiles once their content is fully ported; do not leave them in place as dead files. (spec: "Cleanup")

---

## Task 1: Shared `builder` and `vector-source` stages

**Files:**
- Create: `deploy/build/Dockerfile`

**Interfaces:**
- Produces: a `builder` stage at `/build/bin/{brfs,bwfs,rwfs,certclient,catalogsync,catalog,agent,clientmanager,issuer,policy-server,policyclient,log-gateway,clientmanager-api,clientmanager-admin-api,api-server}`, and a `vector-source` stage with `/usr/bin/vector` — both consumed by every task from here on.

- [ ] **Step 1: Create the directory and write the Dockerfile's first two stages**

```dockerfile
# deploy/build/Dockerfile
#
# Single shared build for every demo/control-plane image. All Go binaries are
# compiled once in `builder`; each runtime image is a separate final stage
# below that copies only the binaries it needs. Select an image with
# `docker build --target <stage-name>` (or Compose's `build.target`).

FROM golang:1.26 AS builder
WORKDIR /build
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make build
# clientmanager is rebuilt statically (CGO_ENABLED=0) via a direct `go build`,
# NOT `make clientmanager`: the Makefile sets CGO_ENABLED with `:=`, which
# overrides an environment variable of the same name when invoked through
# `make` — `CGO_ENABLED=0 make clientmanager` would silently produce a
# dynamically-linked binary. Its runtime image (smallstep/step-ca, see the
# `ca` stage below) has no libgcc-s1 installed, so a dynamically-linked
# clientmanager would fail to start there. This mirrors the original
# demo/ca/Dockerfile's own build line exactly, including the lack of an
# LDFLAGS version stamp (`-trimpath` only) that every other binary gets via
# `make`.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /build/bin/clientmanager ./cmd/clientmanager

FROM timberio/vector:0.46.0-debian AS vector-source
```

- [ ] **Step 2: Build the `builder` stage standalone to verify it compiles everything**

Run: `docker build -f deploy/build/Dockerfile --target builder -t mp-builder-check .` (from repo root)
Expected: build succeeds; output shows `make build` compiling all 15 binaries (`brfs`, `bwfs`, `rwfs`, `certclient`, `catalogsync`, `catalog`, `agent`, `clientmanager`, `issuer`, `policy-server`, `policyclient`, `log-gateway`, `clientmanager-api`, `clientmanager-admin-api`, `api-server`), followed by the second `RUN`'s direct `go build` recompiling `clientmanager` alone.

- [ ] **Step 3: Confirm `clientmanager` is statically linked and the rest exist**

Run:
```bash
docker run --rm mp-builder-check sh -c "ldd /build/bin/clientmanager; ls /build/bin"
```
Expected: `ldd` reports `not a dynamic executable` (or `statically linked`) for `clientmanager`; `ls` lists all 15 binary names.

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add shared builder stage in deploy/build/Dockerfile"
```

---

## Task 2: `backup-host` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder` stage binaries, `vector-source` stage (Task 1).
- Produces: `backup-host` final stage, used by demo's `database`/`webserver`/`store` services.

- [ ] **Step 1: Append the stage**

```dockerfile

FROM debian:bookworm-slim AS backup-host
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates netcat-openbsd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs /build/bin/catalogsync /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY --chmod=0755 demo/backup-host/entrypoint.sh ./entrypoint.sh
ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: Build it and confirm the `builder` stage is reused from cache**

Run: `docker build -f deploy/build/Dockerfile --target backup-host -t mp-backup-host-check .`
Expected: the `make build` / `make clientmanager` steps show `CACHED` (they already ran in Task 1's `docker build`, on the same machine's local BuildKit cache); only the new `backup-host`-specific layers actually execute.

- [ ] **Step 3: Confirm the image has the right binaries**

Run: `docker run --rm mp-backup-host-check ls /app`
Expected: `brfs bwfs rwfs catalogsync certclient agent policyclient vector entrypoint.sh`

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add backup-host stage to deploy/build/Dockerfile"
```

---

## Task 3: `ca` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder` stage's `/build/bin/clientmanager` (statically linked, from Task 1).
- Produces: `ca` final stage, used by demo's `ca` service.

- [ ] **Step 1: Append the stage**

```dockerfile

FROM smallstep/step-ca AS ca

USER root
COPY --chmod=0755 --from=builder /build/bin/clientmanager /usr/local/bin/clientmanager
RUN mkdir -p /home/step/templates /data/client-manager && chown step:step /data/client-manager
COPY --chmod=0644 deploy/control-plane/ca/templates/leaf.tpl /home/step/templates/leaf.tpl
COPY --chmod=0755 demo/ca/entrypoint.sh /home/step/entrypoint.sh
USER step

ENTRYPOINT ["/home/step/entrypoint.sh"]
```

- [ ] **Step 2: Build it**

Run: `docker build -f deploy/build/Dockerfile --target ca -t mp-ca-check .`
Expected: builds successfully; `builder` stage shows `CACHED`.

- [ ] **Step 3: Confirm `clientmanager` is present and static in the final image**

Run: `docker run --rm --user root mp-ca-check ldd /usr/local/bin/clientmanager`
Expected: `not a dynamic executable` (or equivalent "statically linked" message) — confirms the CGO_ENABLED=0 override survived into this final image.

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add ca stage to deploy/build/Dockerfile"
```

---

## Task 4: `issuer-demo` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder` stage's `/build/bin/issuer`.
- Produces: `issuer-demo` final stage, used by demo's `issuer` service.

- [ ] **Step 1: Append the stage**

```dockerfile

# uid/gid 1000 below must match the smallstep/step-ca base image's `step` user
# (see the `ca` stage above), so the shared client-manager-data volume stays
# writable by both clientmanager (running as `step` in ca's container) and
# issuer here. If that base image's step uid ever changes, this coupling
# breaks silently with an ownership mismatch.
FROM debian:bookworm-slim AS issuer-demo
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -g 1000 issuer && useradd -u 1000 -g 1000 -M -s /usr/sbin/nologin issuer \
    && mkdir -p /data && chown issuer:issuer /data

WORKDIR /app
COPY --from=builder /build/bin/issuer ./
USER issuer
ENTRYPOINT ["./issuer", "serve", "--hostname", "issuer", \
            "--ca-url", "https://ca:9000", \
            "--root", "/ca-data/certs/root_ca.crt", \
            "--provisioner", "admin@backup.internal", \
            "--password-file", "/ca-data/secrets/password"]
```

- [ ] **Step 2: Build it**

Run: `docker build -f deploy/build/Dockerfile --target issuer-demo -t mp-issuer-demo-check .`
Expected: builds successfully; `builder` stage shows `CACHED`.

- [ ] **Step 3: Confirm the uid-1000 user and binary**

Run: `docker run --rm mp-issuer-demo-check sh -c "id issuer; ls /app"`
Expected: `id issuer` reports uid=1000, gid=1000; `ls /app` shows `issuer`.

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add issuer-demo stage to deploy/build/Dockerfile"
```

---

## Task 5: `issuer-controlplane` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder` stage's `/build/bin/issuer`.
- Produces: `issuer-controlplane` final stage, used by `deploy/control-plane/docker-compose.yml`'s `issuer` service.

- [ ] **Step 1: Append the stage**

```dockerfile

FROM debian:bookworm-slim AS issuer-controlplane
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/issuer ./
ENTRYPOINT ["./issuer", "serve", "--hostname", "issuer", \
            "--ca-url", "https://step-ca:9000", \
            "--root", "/data/root_ca.crt", \
            "--provisioner", "admin@backup.internal", \
            "--password-file", "/data/secrets/password"]
```

- [ ] **Step 2: Build it**

Run: `docker build -f deploy/build/Dockerfile --target issuer-controlplane -t mp-issuer-cp-check .`
Expected: builds successfully; `builder` stage shows `CACHED`.

- [ ] **Step 3: Confirm it runs as root (unlike `issuer-demo`) and has the binary**

Run: `docker run --rm mp-issuer-cp-check sh -c "whoami; ls /app"`
Expected: `whoami` prints `root`; `ls /app` shows `issuer`.

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add issuer-controlplane stage to deploy/build/Dockerfile"
```

---

## Task 6: `api-server` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder`, `vector-source`.
- Produces: `api-server` final stage, used by both compose files' `api-server` service.

- [ ] **Step 1: Append the stage**

```dockerfile

FROM debian:bookworm-slim AS api-server
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/api-server /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/api-server/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: Build it**

Run: `docker build -f deploy/build/Dockerfile --target api-server -t mp-api-server-check .`
Expected: builds successfully; `builder`/`vector-source` stages show `CACHED`.

- [ ] **Step 3: Confirm binaries**

Run: `docker run --rm mp-api-server-check ls /app`
Expected: `api-server certclient agent policyclient vector entrypoint.sh`

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add api-server stage to deploy/build/Dockerfile"
```

---

## Task 7: `catalog` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder`, `vector-source`.
- Produces: `catalog` final stage, used by both compose files' `catalog` service.

- [ ] **Step 1: Append the stage**

```dockerfile

FROM debian:bookworm-slim AS catalog
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates sqlite3 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/catalog /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/catalog/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: Build it**

Run: `docker build -f deploy/build/Dockerfile --target catalog -t mp-catalog-check .`
Expected: builds successfully; `builder`/`vector-source` stages show `CACHED`.

- [ ] **Step 3: Confirm binaries and sqlite3**

Run: `docker run --rm mp-catalog-check sh -c "ls /app; sqlite3 --version"`
Expected: `ls /app` shows `catalog certclient agent policyclient vector entrypoint.sh`; `sqlite3 --version` prints a version string.

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add catalog stage to deploy/build/Dockerfile"
```

---

## Task 8: `policy-server` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder`, `vector-source`.
- Produces: `policy-server` final stage, used by both compose files' `policy-server` service.

- [ ] **Step 1: Append the stage**

```dockerfile

FROM debian:bookworm-slim AS policy-server
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/policy-server /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/policy-server/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: Build it**

Run: `docker build -f deploy/build/Dockerfile --target policy-server -t mp-policy-server-check .`
Expected: builds successfully; `builder`/`vector-source` stages show `CACHED`.

- [ ] **Step 3: Confirm binaries**

Run: `docker run --rm mp-policy-server-check ls /app`
Expected: `policy-server certclient agent policyclient vector entrypoint.sh`

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add policy-server stage to deploy/build/Dockerfile"
```

---

## Task 9: `log-gateway` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder`, `vector-source`.
- Produces: `log-gateway` final stage, used by both compose files' `log-gateway` service.

- [ ] **Step 1: Append the stage**

```dockerfile

FROM debian:bookworm-slim AS log-gateway
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/log-gateway /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/log-gateway/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: Build it**

Run: `docker build -f deploy/build/Dockerfile --target log-gateway -t mp-log-gateway-check .`
Expected: builds successfully; `builder`/`vector-source` stages show `CACHED`.

- [ ] **Step 3: Confirm binaries**

Run: `docker run --rm mp-log-gateway-check ls /app`
Expected: `log-gateway certclient agent policyclient vector entrypoint.sh`

- [ ] **Step 4: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add log-gateway stage to deploy/build/Dockerfile"
```

---

## Task 10: `clientmanager-api` final stage

**Files:**
- Modify: `deploy/build/Dockerfile` (append stage)

**Interfaces:**
- Consumes: `builder`, `vector-source`.
- Produces: `clientmanager-api` final stage, used by both compose files' `clientmanager-api` service. This is the last stage added — `deploy/build/Dockerfile` is now complete with all 9 final stages plus `builder` and `vector-source`.

- [ ] **Step 1: Append the stage**

```dockerfile

FROM debian:bookworm-slim AS clientmanager-api
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/clientmanager-api /build/bin/clientmanager-admin-api /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/clientmanager-api/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: Build it**

Run: `docker build -f deploy/build/Dockerfile --target clientmanager-api -t mp-clientmanager-api-check .`
Expected: builds successfully; `builder`/`vector-source` stages show `CACHED`.

- [ ] **Step 3: Confirm binaries**

Run: `docker run --rm mp-clientmanager-api-check ls /app`
Expected: `clientmanager-api clientmanager-admin-api certclient agent policyclient vector entrypoint.sh`

- [ ] **Step 4: Clean up the throwaway check images from Tasks 1-10**

Run: `docker rmi mp-builder-check mp-backup-host-check mp-ca-check mp-issuer-demo-check mp-issuer-cp-check mp-api-server-check mp-catalog-check mp-policy-server-check mp-log-gateway-check mp-clientmanager-api-check`
Expected: all ten throwaway tags removed (they're not referenced by any compose file, so nothing in the repo depends on them existing).

- [ ] **Step 5: Commit**

```bash
git add deploy/build/Dockerfile
git commit -m "build: add clientmanager-api stage, completing deploy/build/Dockerfile"
```

---

## Task 11: Wire up demo compose, delete demo Dockerfiles, verify the full demo stack

**Files:**
- Modify: `demo/docker-compose.yml`
- Modify: `demo/up.sh`
- Delete: `demo/backup-host/Dockerfile`, `demo/ca/Dockerfile`, `demo/issuer/Dockerfile`

**Interfaces:**
- Consumes: `deploy/build/Dockerfile`'s `backup-host`, `ca`, `issuer-demo`, `api-server`, `catalog`, `policy-server`, `log-gateway`, `clientmanager-api` stages (Tasks 1-10).

- [ ] **Step 1: Update every `build:` block in `demo/docker-compose.yml`**

For the `ca` service, change:
```yaml
  ca:
    build:
      context: ..
      dockerfile: demo/ca/Dockerfile
```
to:
```yaml
  ca:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: ca
```

For the `issuer` service, change:
```yaml
  issuer:
    build:
      context: ..
      dockerfile: demo/issuer/Dockerfile
```
to:
```yaml
  issuer:
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: issuer-demo
```

For `clientmanager-api`, `catalog`, `api-server`, `policy-server`, `log-gateway`, change each `dockerfile: deploy/control-plane/<name>/Dockerfile` to:
```yaml
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: <name>
```
(e.g. `clientmanager-api`'s block gets `dockerfile: deploy/build/Dockerfile` / `target: clientmanager-api`, and so on for `catalog`, `api-server`, `policy-server`, `log-gateway`).

For `database`, `webserver`, and `store` (all three currently use `dockerfile: demo/backup-host/Dockerfile`), change each to:
```yaml
    build:
      context: ..
      dockerfile: deploy/build/Dockerfile
      target: backup-host
```

`web` and `loki` are unchanged (`web` builds from `web/Dockerfile`, unrelated to this refactor; `loki` uses a prebuilt image, no `build:` block).

- [ ] **Step 2: Add `COMPOSE_BAKE=true` to `demo/up.sh`**

In `demo/up.sh`, right after the existing `cd "$(dirname "$0")"` line, add:
```sh
export COMPOSE_BAKE=true
```

- [ ] **Step 3: Delete the superseded demo Dockerfiles**

```bash
git rm demo/backup-host/Dockerfile demo/ca/Dockerfile demo/issuer/Dockerfile
```

- [ ] **Step 4: Bring the demo stack up from a clean slate and verify it**

```bash
cd demo && docker compose down -v 2>/dev/null; cd ..
make demo-up
```
Expected: all 8 demo images build (`docker compose build` output shows `agent`/`certclient`/`policyclient`/etc. compiling once, in the `builder` stage, then every consuming final stage building without recompiling them); `demo/up.sh` completes through enrollment of `catalog`, `policy-server`, `database`, `webserver`, `store` without error.

- [ ] **Step 5: Run the demo's own smoke test from `demo/README.md`**

```bash
docker compose -f demo/docker-compose.yml exec database ./brfs /var/lib/dbdata --destination store:8080
docker compose -f demo/docker-compose.yml exec database ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec database ./rwfs verify store:8080
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
```
Expected: `brfs` backs the directory up without error; `rwfs list` shows the backed-up file; `rwfs verify` reports success; the `sqlite3` query returns at least one row for the file just backed up.

- [ ] **Step 6: Confirm the incremental-rebuild cache-mount benefit**

```bash
touch src/cmd/agent/main.go
cd demo && docker compose build database; cd ..
```
Expected: the `builder` stage's `RUN ... make build` re-executes (cache-busted because `COPY . .` now sees a changed file), but the `go build` output shows only the `agent` package recompiling, not the full set of 15 binaries from scratch — confirms the `--mount=type=cache` mounts from Task 1 are working. `database` uses the `backup-host` target, which depends on `builder`.

- [ ] **Step 7: Tear down and commit**

```bash
cd demo && docker compose down -v; cd ..
git add demo/docker-compose.yml demo/up.sh
git commit -m "build(demo): build all images from deploy/build/Dockerfile"
```

---

## Task 12: Wire up control-plane compose, delete its Dockerfiles, verify the build

**Files:**
- Modify: `deploy/control-plane/docker-compose.yml`
- Modify: `Makefile:180-187` (`control-plane-up` target)
- Delete: `deploy/control-plane/api-server/Dockerfile`, `deploy/control-plane/catalog/Dockerfile`, `deploy/control-plane/clientmanager-api/Dockerfile`, `deploy/control-plane/issuer/Dockerfile`, `deploy/control-plane/log-gateway/Dockerfile`, `deploy/control-plane/policy-server/Dockerfile`

**Interfaces:**
- Consumes: `deploy/build/Dockerfile`'s `issuer-controlplane`, `api-server`, `catalog`, `policy-server`, `log-gateway`, `clientmanager-api` stages (Tasks 1-10).

- [ ] **Step 1: Update every `build:` block in `deploy/control-plane/docker-compose.yml`**

For `issuer`, change:
```yaml
  issuer:
    build:
      context: ../..
      dockerfile: deploy/control-plane/issuer/Dockerfile
```
to:
```yaml
  issuer:
    build:
      context: ../..
      dockerfile: deploy/build/Dockerfile
      target: issuer-controlplane
```

For `clientmanager-api`, `catalog`, `api-server`, `policy-server`, `log-gateway`, change each `dockerfile: deploy/control-plane/<name>/Dockerfile` to:
```yaml
    build:
      context: ../..
      dockerfile: deploy/build/Dockerfile
      target: <name>
```

`step-ca` and `loki` are unchanged (both use prebuilt images, no `build:` block).

- [ ] **Step 2: Add `COMPOSE_BAKE=true` to the `control-plane-up` Makefile target**

In `Makefile`, the `control-plane-up` target currently reads:
```makefile
control-plane-up: ## Initialize (if needed) and start the control-plane stack (ca + catalog)
	@if [ ! -f $(CONTROL_PLANE_DIR)/ca/data/secrets/password ]; then \
		echo -e "$(BLUE)Generating CA provisioner password...$(NC)"; \
		mkdir -p $(CONTROL_PLANE_DIR)/ca/data/secrets; \
		openssl rand -base64 32 > $(CONTROL_PLANE_DIR)/ca/data/secrets/password; \
	fi
	@cd $(CONTROL_PLANE_DIR) && docker compose up -d
	@echo -e "$(GREEN)Control plane up.$(NC) ca: https://localhost:9000  catalog: localhost:15723"
```
Change the `docker compose up -d` line to export `COMPOSE_BAKE` first:
```makefile
	@cd $(CONTROL_PLANE_DIR) && COMPOSE_BAKE=true docker compose up -d
```

- [ ] **Step 3: Delete the superseded control-plane Dockerfiles**

```bash
git rm deploy/control-plane/api-server/Dockerfile deploy/control-plane/catalog/Dockerfile \
  deploy/control-plane/clientmanager-api/Dockerfile deploy/control-plane/issuer/Dockerfile \
  deploy/control-plane/log-gateway/Dockerfile deploy/control-plane/policy-server/Dockerfile
```

- [ ] **Step 4: Build the control-plane stack and verify**

```bash
COMPOSE_BAKE=true docker compose -f deploy/control-plane/docker-compose.yml build
```
Expected: all 6 build-based images (`issuer`, `clientmanager-api`, `catalog`, `api-server`, `policy-server`, `log-gateway`) build successfully, reusing the already-warm `builder`/`vector-source` cache from Task 11's demo build (since it's the same `deploy/build/Dockerfile`, same context content) — build output should show these stages as `CACHED`.

- [ ] **Step 5: Confirm `issuer-controlplane`'s distinguishing behavior survived the move**

```bash
docker compose -f deploy/control-plane/docker-compose.yml run --rm --no-deps --entrypoint sh issuer -c whoami
```
Expected: prints `root` (confirms this is still the root-running control-plane variant, not accidentally swapped for `issuer-demo`'s uid-1000 user).

- [ ] **Step 6: Commit**

```bash
git add deploy/control-plane/docker-compose.yml Makefile
git commit -m "build(control-plane): build all images from deploy/build/Dockerfile"
```

---

## Task 13: Changelog and final stale-reference check

**Files:**
- Modify: `CHANGELOG.md`

**Interfaces:**
- None — this is a documentation-only task, no code interfaces.

- [ ] **Step 1: Search for any remaining reference to a deleted Dockerfile path**

```bash
grep -rn "demo/backup-host/Dockerfile\|demo/ca/Dockerfile\|demo/issuer/Dockerfile\|deploy/control-plane/api-server/Dockerfile\|deploy/control-plane/catalog/Dockerfile\|deploy/control-plane/clientmanager-api/Dockerfile\|deploy/control-plane/issuer/Dockerfile\|deploy/control-plane/log-gateway/Dockerfile\|deploy/control-plane/policy-server/Dockerfile" \
  --include="*.md" --include="*.yml" --include="*.sh" --include="Makefile" .
```
Expected: no matches outside `docs/superpowers/specs/` and `docs/superpowers/plans/` (historical design/plan documents are point-in-time records and are not updated). If any match turns up in a live doc (README, `docs/components/`, `docs/ARCHITECTURE.md`), fix that reference to point at `deploy/build/Dockerfile` instead before continuing.

- [ ] **Step 2: Add the changelog entry**

Add to the top of `CHANGELOG.md`, right after the `# Changelog` header and its intro line:

```markdown
## 2026-07-27 — build: consolidate demo/control-plane Dockerfiles

The 7 separate per-image Dockerfiles under `demo/` and `deploy/control-plane/` are replaced by one
`deploy/build/Dockerfile` with a single shared `builder` stage and nine final runtime stages,
selected via Compose's `build.target`. Previously, six of those Dockerfiles each ran their own
`make <binary list>`, and because each list differed, Docker's layer cache never matched between
them — `agent`, `certclient`, and `policyclient` were recompiled from scratch in up to six separate
builder stages on every `make demo-up` or control-plane build, despite producing byte-identical
binaries each time. The shared stage also gains persistent `--mount=type=cache` mounts for Go's
build and module caches, so even a source-code change only recompiles the affected packages instead
of the whole stage. `demo/up.sh` and the `control-plane-up` Makefile target now export
`COMPOSE_BAKE=true` before building, so Compose builds the shared stage once and fans out to the
final stages in parallel. No runtime behavior changes: every final image's installed packages,
users, and entrypoint are unchanged from before this refactor.
```

- [ ] **Step 3: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog entry for Docker build consolidation"
```
