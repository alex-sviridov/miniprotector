# Demo Lab Environment v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a self-contained `demo/` docker-compose stack (CA, `issuer`, `catalog`, two backup-capable nodes) that a single script brings up fully enrolled via the current two-tier bootstrap/operating credential model, replacing the stale, pre-`issuer` 2026-07-03 demo design.

**Architecture:** Five containers (`ca`, `issuer`, `catalog`, `client`, `store`) on one Compose network, all built from this repo, no host ports published, no host bind-mounts of secrets — everything Docker-managed named volumes plus one canonical file (`deploy/control-plane/ca/templates/leaf.tpl`) reused directly from the existing control-plane deployment rather than copied. `client`/`store` share one image (`demo/backup-host`), differentiated by `STORAGE_PATH`. `demo/up.sh` mints each node's enrollment token via a `clientmanager` binary baked into the `ca` image and redeems it via each node's own `certclient bootstrap`, then lets `agent serve`'s existing reconcile loop take over.

**Tech Stack:** Docker Compose, the existing Go binaries (`clientmanager`, `issuer`, `catalog`, `certclient`, `agent`, `brfs`, `bwfs`, `rwfs`, `catalogsync`), `smallstep/step-ca` (Alpine-based; `curl`/`base64`/`step` already present, confirmed by inspection), `debian:bookworm-slim` for the Go-binary images (existing project convention).

## Global Constraints

- Full spec: `docs/superpowers/specs/2026-07-06-demo-lab-environment-v2-design.md`.
- No host ports published anywhere in `demo/docker-compose.yml`. All interaction is `docker compose exec`/`run`.
- No host bind-mounts of secrets or generated data — every persistent path is a Docker-managed named volume. The only files read from the host repo at **build time** (not runtime) are `deploy/control-plane/ca/templates/leaf.tpl` and, for `catalog`, the entire existing `deploy/control-plane/catalog/Dockerfile` (referenced directly, not copied — see Task 3) — this keeps the demo from silently drifting from the real control-plane's proven image.
- The `clientmanager` binary's actual filename (per the root `Makefile`'s `clientmanager: ... -o ../bin/clientmanager ./cmd/clientmanager`) is `clientmanager`, no hyphen, even though prose docs and cobra's `Use` string say `client-manager`. Every command in this plan invokes it as `clientmanager`.
- `debian:bookworm-slim` images that run `CGO_ENABLED=1` binaries need `libgcc-s1` (and `ca-certificates`) installed explicitly — matches `deploy/control-plane/catalog/Dockerfile` and `src/e2e/Dockerfile`'s existing convention.
- `issuer`'s binary, invoked as `./issuer serve --hostname ... --ca-url ... --root ... --provisioner ... --password-file ...`, is confirmed (by building and running it directly) to accept and ignore the `serve` token — `cmd/issuer/arguments.go`'s root `cobra.Command` has no `Run`/`RunE`, so Cobra's not-runnable short-circuit fires before its `Args: cobra.NoArgs` validator ever runs, silently discarding `serve` and printing one extra help line to stdout. This matches `deploy/control-plane/issuer/Dockerfile`'s existing, working `ENTRYPOINT` verbatim — reuse the pattern, don't try to "fix" it.
- `config.ParseConfig` (`src/common/config/config.go`) errors on a truly **unknown** key but silently accepts (and simply doesn't use) any recognized key a given binary doesn't need — the shared `demo/local.conf` approach is safe as long as every key in it appears in that file's `switch` statement. All keys used in this plan (`default_port`, `default_streams`, `logfolder`, `ca_host`, `issuer_host`, `issuer_port`, `catalog_host`, `catalog_port`, `ReconcileIntervalSec`, `BootstrapCertRefreshIntervalSec`, `OperatingCertFetchIntervalSec`, `JobTimeoutSec`) were confirmed present in that `switch`.
- `clientmanager add <hostname>` prints **only** the token to stdout (`cmd/clientmanager/add.go`'s `runAdd`: one `fmt.Fprintln(out, token)`, no banner) — safe to capture directly via `$(...)`, no `tail`/`grep` needed.

---

### Task 1: `demo/ca` — step-ca image with `clientmanager` baked in

**Files:**
- Create: `demo/ca/entrypoint.sh`
- Create: `demo/ca/Dockerfile`
- Create: `demo/ca/clientmanager-local.conf`
- Create: `demo/docker-compose.yml`

**Interfaces:**
- Consumes: nothing from other demo tasks (first task).
- Produces: a running `ca` service reachable at Compose DNS name `ca:9000`, with `clientmanager` invocable via `docker compose exec ca clientmanager ...`, and a `client-manager-data` named volume (mounted at `/data/client-manager` inside `ca`) that Task 2's `issuer` will also mount, at the same path, to read the same SQLite file.

- [ ] **Step 1: Write `demo/ca/entrypoint.sh`**

```sh
#!/bin/sh
set -e

# Demo is fully self-contained (no host bind-mounts of secrets), so unlike
# deploy/control-plane's Makefile-driven host-side password generation, the
# provisioner password is generated into the ca-data volume on first boot.
# head/base64 are confirmed present in the smallstep/step-ca Alpine base
# (openssl the CLI is not guaranteed to be).
mkdir -p /home/step/secrets
if [ ! -f /home/step/secrets/password ]; then
    head -c32 /dev/urandom | base64 > /home/step/secrets/password
fi

if [ ! -f /home/step/config/ca.json ]; then
    step ca init --deployment-type=standalone \
        --name="Miniprotector Demo CA" \
        --dns="ca,localhost" \
        --address=":9000" \
        --provisioner="admin@backup.internal" \
        --password-file=/home/step/secrets/password
fi

# Runs unconditionally, every boot (not just first init) -- an
# already-initialized CA must still pick up template changes on upgrade,
# the exact gap b212082 fixed for deploy/control-plane's own entrypoint.
step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl

exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
```

- [ ] **Step 2: Write `demo/ca/clientmanager-local.conf`**

```
default_port=15722
default_streams=4
logfolder=/tmp
var_path=/data/client-manager
```

- [ ] **Step 3: Write `demo/ca/Dockerfile`**

```dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN cd src && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /build/bin/clientmanager ./cmd/clientmanager

FROM smallstep/step-ca

USER root
COPY --chmod=0755 --from=builder /build/bin/clientmanager /usr/local/bin/clientmanager
RUN mkdir -p /home/step/templates /data/client-manager && chown step:step /data/client-manager
COPY --chmod=0644 deploy/control-plane/ca/templates/leaf.tpl /home/step/templates/leaf.tpl
COPY --chmod=0755 demo/ca/entrypoint.sh /home/step/entrypoint.sh
USER step

ENTRYPOINT ["/home/step/entrypoint.sh"]
```

(Verified during planning, empirically: `golang:1.26`'s builder is glibc-based while
`smallstep/step-ca`'s final image is Alpine/musl-based — a `CGO_ENABLED=1` build produces a
glibc-linked binary that fails at runtime on musl. `storage/clientmanager/db.go` opens its SQLite
connection via the pure-Go `modernc.org/sqlite` driver (`_ "modernc.org/sqlite"`, `sql.Open("sqlite",
...)`), not the cgo-based `mattn/go-sqlite3` (an unused indirect dependency pulled in by
`gorm.io/driver/sqlite`) — so `CGO_ENABLED=0` produces a fully static, libc-independent binary that
runs unmodified on Alpine, confirmed by both `file` reporting "statically linked" and running it
inside a real `smallstep/step-ca` container. Two further fixes, also confirmed empirically: (1)
`COPY --chmod=0644` applied to a file whose parent directory doesn't yet exist applies that same
mode to the auto-created directory too, producing a directory with no execute bit
(`drw-r-Sr--`) that nothing — not even root — can traverse; pre-creating `/home/step/templates` with
a plain `RUN mkdir -p` (default 0755) avoids this. (2) `smallstep/step-ca`'s base image already sets
`USER step` (uid 1000, non-root), so a bare `RUN mkdir -p /data/...` fails with "Permission denied"
at `/`; `USER root` before the `RUN`/`COPY` block (switching back to `USER step` after) is required —
matches the base image's own default runtime user, just bracketing the one build step that needs
root to prepare `/data/client-manager` (owned by `step:step`, since `clientmanager` — running as
`step` at container start, per this same `USER step` line — needs write access to create its SQLite
file there).

- [ ] **Step 4: Write `demo/docker-compose.yml`**

```yaml
services:
  ca:
    build:
      context: ..
      dockerfile: demo/ca/Dockerfile
    volumes:
      - ca-data:/home/step
      - client-manager-data:/data/client-manager
      - ./ca/clientmanager-local.conf:/data/client-manager-conf/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data/client-manager-conf
    restart: unless-stopped

volumes:
  ca-data:
  client-manager-data:
```

- [ ] **Step 5: Build and boot `ca`, confirm health**

Run:
```bash
cd demo
docker compose up -d --build ca
sleep 5
docker compose exec -T ca curl -fsk https://localhost:9000/health
```
Expected: `{"status":"ok"}` (confirmed by an equivalent throwaway `smallstep/step-ca` run during planning — this is step-ca's real health response, not assumed).

- [ ] **Step 6: Confirm `clientmanager` runs inside the `ca` container**

Run:
```bash
docker compose exec -T ca clientmanager list
```
Expected: empty output (no clients enrolled yet), exit code 0 — proves `clientmanager` reads its own `local.conf` via `MP_CONFIG_PATH=/data/client-manager-conf` and opens its SQLite store at `/data/client-manager` without error.

- [ ] **Step 7: Commit**

```bash
git add demo/ca demo/docker-compose.yml
git commit -m "$(cat <<'EOF'
feat(demo): add self-contained ca service with clientmanager baked in

First piece of the rewritten demo lab: a step-ca container that
generates its own provisioner password (no host bind-mount, unlike
deploy/control-plane's Makefile-driven approach) and bundles the
clientmanager binary directly, so tokens can be minted via
`docker compose exec ca clientmanager add ...` with no separate
throwaway container.
EOF
)"
```

---

### Task 2: `demo/issuer` — operating-certificate service, wired to `ca`

**Files:**
- Create: `demo/issuer/Dockerfile`
- Create: `demo/local.conf`
- Modify: `demo/docker-compose.yml`

**Interfaces:**
- Consumes: `ca-data` volume (Task 1, mounted read-only to reach the CA's root cert and provisioner password without a host bind-mount), `client-manager-data` volume (Task 1).
- Produces: a running `issuer` service reachable at Compose DNS name `issuer:9200`; `demo/local.conf`, the shared config file Tasks 3 and 4's services will also bind-mount.

- [ ] **Step 1: Write `demo/local.conf`**

```
default_port=8080
default_streams=4
logfolder=/var/log/miniprotector
ca_host=ca:9000
issuer_host=issuer
issuer_port=9200
catalog_host=catalog
catalog_port=15723
ReconcileIntervalSec=30
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
JobTimeoutSec=30
var_path=/data/client-manager
ConnectionTimeOutSec=30
```

(`ca_host` is required by `certclient renew`/`bootstrap` — an omission in the earlier design spec's
illustrative `local.conf`, corrected here. `var_path` is required by `issuer` — confirmed via
`config.ResolveVarDir`, which falls back to the binary's own directory when unset, so without it
`issuer` never sees the SQLite database `clientmanager` actually writes to at
`/data/client-manager`; `deploy/control-plane/issuer/local.conf` already sets this correctly, this
demo's consolidated shared config had simply dropped it. `ConnectionTimeOutSec` is required by
`certclient operating-refresh`'s dial to `issuer` (`cmd/certclient/main.go`, `common/connection`'s
`checkConnection`) — with no default in `config.ParseConfig`, it's Go's zero value, producing an
already-expired context and an instant `"connection timeout"` on every attempt regardless of
whether `issuer` is reachable; `30` matches `src/e2e/config.conf`'s existing value. Both gaps were
found empirically during Task 3's implementation — see that task's notes; `ConnectionTimeOutSec`'s
absence appears to be a latent, pre-existing gap in `deploy/control-plane`'s real config too
(`deploy/control-plane/catalog/local.conf` also lacks it), flagged for separate follow-up, not fixed
here since it's outside this plan's scope.)

- [ ] **Step 2: Write `demo/issuer/Dockerfile`**

Because `issuer` runs in its own container, not `ca`'s, it can't reach `ca`'s root cert/password file via `/home/step/...` directly — the `ca-data` volume is mounted read-only into `issuer` too (at `/ca-data`), so no host bind-mount is needed and no file is duplicated:

```dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make issuer

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/issuer ./
ENTRYPOINT ["./issuer", "serve", "--hostname", "issuer", \
            "--ca-url", "https://ca:9000", \
            "--root", "/ca-data/certs/root_ca.crt", \
            "--provisioner", "admin@backup.internal", \
            "--password-file", "/ca-data/secrets/password"]
```

- [ ] **Step 3: Add the `issuer` service to `demo/docker-compose.yml`**

Insert after the `ca` service (before the closing `volumes:` block), and add `issuer-data` to the `volumes:` block:

```yaml
  issuer:
    build:
      context: ..
      dockerfile: demo/issuer/Dockerfile
    depends_on:
      - ca
    volumes:
      - issuer-data:/data
      - ./local.conf:/data/local.conf:ro
      - client-manager-data:/data/client-manager
      - ca-data:/ca-data:ro
    environment:
      - MP_CONFIG_PATH=/data
    restart: unless-stopped
```

```yaml
volumes:
  ca-data:
  client-manager-data:
  issuer-data:
```

- [ ] **Step 4: Boot `issuer`, confirm it reached steady state**

Run:
```bash
docker compose up -d --build issuer
sleep 5
docker compose ps issuer
docker compose logs issuer | tail -20
```
Expected: `docker compose ps issuer` shows state `Up` or `running` (not `Restarting`); logs contain `"minting own server identity"` and `"issuer started"` (the two `logger.Info` calls in `cmd/issuer/main.go`), with no `"failed to mint own server identity"` error. If `issuer` starts before `ca`'s first-boot `step ca init` has finished writing `/ca-data/certs/root_ca.crt`, it will exit and Compose's `restart: unless-stopped` will retry automatically — this is the same expected, harmless race `deploy/control-plane/README.md` already documents for `catalog`; re-run the `docker compose ps`/`logs` check after a few seconds if the first attempt shows a restart.

- [ ] **Step 5: Commit**

```bash
git add demo/issuer demo/local.conf demo/docker-compose.yml
git commit -m "$(cat <<'EOF'
feat(demo): add issuer service, sharing ca's root cert via a volume

issuer reads ca's root certificate and provisioner password from the
ca-data volume mounted read-only at /ca-data, rather than a host
bind-mount -- keeps the demo fully self-contained. Also adds
demo/local.conf, the shared config file every later service reuses.
EOF
)"
```

---

### Task 3: `demo/catalog` — reuse `deploy/control-plane/catalog/Dockerfile` directly

**Files:**
- Modify: `demo/docker-compose.yml`

**Interfaces:**
- Consumes: `demo/local.conf` (Task 2); `deploy/control-plane/catalog/Dockerfile` (existing file, referenced, not copied).
- Produces: a running, enrolled `catalog` service reachable at `catalog:15723`.

`deploy/control-plane/catalog/Dockerfile`'s `COPY` instructions (`deploy/control-plane/catalog/entrypoint.sh`, `make catalog certclient agent`) are already relative to the **repo root** — the same root `demo/`'s own build context resolves to. So Compose can point `dockerfile:` at that existing file directly, with no new Dockerfile or entrypoint.sh created for `demo/catalog` at all — zero duplication, and any future fix to the real control-plane image benefits the demo automatically.

- [ ] **Step 1: Add the `catalog` service to `demo/docker-compose.yml`**

Insert after the `issuer` service, and add `catalog-data` to `volumes:`:

```yaml
  catalog:
    build:
      context: ..
      dockerfile: deploy/control-plane/catalog/Dockerfile
    depends_on:
      - ca
      - issuer
    volumes:
      - catalog-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - STORAGE_PATH=/data/storage
    restart: unless-stopped
```

```yaml
volumes:
  ca-data:
  client-manager-data:
  issuer-data:
  catalog-data:
```

- [ ] **Step 2: Build `catalog`, confirm it's present but unenrolled (expected crash-loop)**

Run:
```bash
docker compose up -d --build catalog
sleep 5
docker compose ps catalog
```
Expected: state shows repeated restarts (`Restarting`) — `catalog`'s entrypoint has no `bootstrap.crt` yet and no `MP_CERT_TOKEN`, so `certclient bootstrap --token ""` fails. This is the exact same "expected, not a bug" state `deploy/control-plane/README.md` documents before enrollment; it's proof the image itself builds and runs, not proof of full success yet — full enrollment is Task 5's `up.sh`.

- [ ] **Step 3: Manually enroll `catalog` once, to prove the mechanism before automating it**

Run:
```bash
TOKEN=$(docker compose exec -T ca clientmanager add catalog \
    --ca-url https://ca:9000 \
    --root /home/step/certs/root_ca.crt \
    --password-file /home/step/secrets/password \
    --defaults-file /home/step/config/defaults.json)
MP_CERT_TOKEN="$TOKEN" docker compose up -d catalog
sleep 10
docker compose ps catalog
docker compose exec -T catalog ./agent list-policies
```
Expected: `docker compose ps catalog` shows a stable `Up`/`running` state (no more restarts); `agent list-policies` lists both `bootstrap-refresh` and `operating-refresh` with a recent successful run.

(`--ca-url https://ca:9000`, not `https://localhost:9000` — even though `clientmanager` itself runs
inside the `ca` container where `localhost` would also work for the *minting* call, `ca.Bootstrap`
on the *redeeming* side, run inside `catalog`'s own container, dials whatever URL is baked into the
token at mint time, not `local.conf`'s `ca_host`. `localhost` there would mean `catalog`'s own
loopback, unreachable — the exact trap `deploy/control-plane/README.md` already documents and
warns against for its own `catalog` enrollment. Found empirically during Task 3's implementation,
confirmed against `src/cmd/certclient/bootstrap.go`'s use of `ca.CreateSignRequest`/`ca.Bootstrap`.
The same correction applies everywhere else this plan mints a token for a non-`ca` node.)

- [ ] **Step 4: Commit**

```bash
git add demo/docker-compose.yml
git commit -m "$(cat <<'EOF'
feat(demo): add catalog service, reusing control-plane's Dockerfile

Points directly at deploy/control-plane/catalog/Dockerfile (build
context is the repo root either way) instead of duplicating it --
catalog's image needs nothing demo-specific.
EOF
)"
```

---

### Task 4: `demo/backup-host` — shared `client`/`store` image

**Files:**
- Create: `demo/backup-host/entrypoint.sh`
- Create: `demo/backup-host/Dockerfile`
- Create: `demo/sample-data/hello.txt`
- Create: `demo/sample-data/notes.txt`
- Modify: `demo/docker-compose.yml`

**Interfaces:**
- Consumes: `demo/local.conf` (Task 2).
- Produces: `client` (reachable only via `docker compose exec`, no listener) and `store` (reachable at `store:8080` for backups, dials `catalog:15723` via `catalogsync`).

- [ ] **Step 1: Write `demo/backup-host/entrypoint.sh`**

Mirrors `deploy/control-plane/catalog/entrypoint.sh`'s proven bootstrap-wait-timeout sequencing, adding the `STORAGE_PATH`-gated background workload. Unlike `catalog` (which has one clear foreground binary to `exec`), this image has zero (`client`) or two (`store`) long-running workloads, so the entrypoint itself stays PID 1 throughout — background everything, trap `TERM`, `wait`:

```sh
#!/bin/sh
set -e

if [ -n "$STORAGE_PATH" ]; then
    mkdir -p "$STORAGE_PATH"
fi

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart) of the long-lived bootstrap credential -- same
# pattern as deploy/control-plane/catalog/entrypoint.sh.
if [ -f /data/certs/bootstrap.crt ]; then
    ./certclient renew
else
    ./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

./agent serve &
AGENT_PID=$!

# Wait for agent's first operating-refresh to produce client.crt/client.key
# (due immediately for a never-run policy -- see cmd/agent/reconcile.go's
# isDue) before starting any workload that needs it.
timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
    sleep 1
    timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
    echo "agent did not produce an operating certificate within 60s" >&2
    exit 1
fi

if [ -n "$STORAGE_PATH" ]; then
    ./bwfs "$STORAGE_PATH" server --port 8080 --debug="${DEBUG:-false}" &
    BWFS_PID=$!
    ./catalogsync "$STORAGE_PATH" --debug="${DEBUG:-false}" &
    CATALOGSYNC_PID=$!
fi

# Set only now (not before backgrounding) so the shell process -- which
# never execs away, unlike catalog's entrypoint -- stays this container's
# PID 1 and keeps receiving TERM directly, with a trap that's still live
# to forward it to every backgrounded child.
trap 'kill $AGENT_PID $BWFS_PID $CATALOGSYNC_PID 2>/dev/null || true' TERM
wait
```

- [ ] **Step 2: Write `demo/backup-host/Dockerfile`**

```dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make brfs bwfs rwfs catalogsync certclient agent

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs /build/bin/catalogsync /build/bin/certclient /build/bin/agent ./
COPY --chmod=0755 demo/backup-host/entrypoint.sh ./entrypoint.sh
ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 3: Write two small sample files**

`demo/sample-data/hello.txt`:
```
Hello from the miniprotector demo lab.
This file is backed up by brfs when you run the walkthrough.
```

`demo/sample-data/notes.txt`:
```
A second sample file, so the demo backs up more than one file at once.
```

- [ ] **Step 4: Add `client` and `store` services to `demo/docker-compose.yml`**

Insert after `catalog`, and add `client-data`/`store-data` to `volumes:`:

```yaml
  client:
    build:
      context: ..
      dockerfile: demo/backup-host/Dockerfile
    depends_on:
      - ca
      - issuer
    volumes:
      - client-data:/data
      - ./local.conf:/data/local.conf:ro
      - ./sample-data:/data/sample-data:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    restart: unless-stopped

  store:
    build:
      context: ..
      dockerfile: demo/backup-host/Dockerfile
    depends_on:
      - ca
      - issuer
      - catalog
    volumes:
      - store-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - STORAGE_PATH=/data/storage
    restart: unless-stopped
```

```yaml
volumes:
  ca-data:
  client-manager-data:
  issuer-data:
  catalog-data:
  client-data:
  store-data:
```

- [ ] **Step 5: Manually enroll `store`, confirm `bwfs` comes up listening**

Run:
```bash
TOKEN=$(docker compose exec -T ca clientmanager add store \
    --ca-url https://ca:9000 \
    --root /home/step/certs/root_ca.crt \
    --password-file /home/step/secrets/password \
    --defaults-file /home/step/config/defaults.json)
MP_CERT_TOKEN="$TOKEN" docker compose up -d --build store
sleep 10
docker compose ps store
docker compose exec -T store ./agent list-policies
```
Expected: `store` reaches a stable `Up` state; `agent list-policies` shows both policies `ok`. (Full brfs→bwfs→catalog proof is Task 7 — this step only proves the image/entrypoint mechanics.)

- [ ] **Step 6: Manually enroll `client`, confirm it reaches steady state with no listener**

Run:
```bash
TOKEN=$(docker compose exec -T ca clientmanager add client \
    --ca-url https://ca:9000 \
    --root /home/step/certs/root_ca.crt \
    --password-file /home/step/secrets/password \
    --defaults-file /home/step/config/defaults.json)
MP_CERT_TOKEN="$TOKEN" docker compose up -d --build client
sleep 10
docker compose ps client
docker compose exec -T client ls /data/sample-data
```
Expected: `client` reaches a stable `Up` state; `ls` lists `hello.txt` and `notes.txt`.

- [ ] **Step 7: Commit**

```bash
git add demo/backup-host demo/sample-data demo/docker-compose.yml
git commit -m "$(cat <<'EOF'
feat(demo): add backup-host image for client/store, plus sample data

One image for both roles, differentiated by STORAGE_PATH. Unlike
catalog's entrypoint (which execs its one real workload as PID 1),
this entrypoint stays PID 1 itself throughout, since it has zero or
two long-running children depending on role -- the TERM trap is set
after backgrounding, not before an exec, so it stays live.
EOF
)"
```

---

### Task 5: `demo/up.sh` — one-command bring-up

**Files:**
- Create: `demo/up.sh`

**Interfaces:**
- Consumes: every service from Tasks 1-4.
- Produces: `demo/up.sh`, invoked directly or via the Task 6 Makefile target.

- [ ] **Step 1: Write `demo/up.sh`**

```sh
#!/bin/sh
set -e

cd "$(dirname "$0")"

echo "Building images..."
docker compose build

echo "Starting ca..."
docker compose up -d ca

echo "Waiting for ca to become healthy..."
timeout=30
until docker compose exec -T ca curl -fsk https://localhost:9000/health >/dev/null 2>&1; do
    timeout=$((timeout - 1))
    if [ "$timeout" -le 0 ]; then
        echo "ca did not become healthy within 30s" >&2
        exit 1
    fi
    sleep 1
done

echo "Starting issuer..."
docker compose up -d issuer

# Idempotent per-node enrollment: probes the node's own persistent volume
# for an already-redeemed bootstrap credential via a throwaway container
# (docker compose run), rather than `exec`ing into a service that may
# never have started -- correct on both a cold volume and a re-run.
enroll() {
    name="$1"
    if docker compose run --rm --no-deps --entrypoint sh "$name" \
        -c 'test -f /data/certs/bootstrap.crt' >/dev/null 2>&1; then
        echo "$name already enrolled, starting..."
        docker compose up -d "$name"
        return
    fi
    echo "Enrolling $name..."
    token=$(docker compose exec -T ca clientmanager add "$name" \
        --ca-url https://ca:9000 \
        --root /home/step/certs/root_ca.crt \
        --password-file /home/step/secrets/password \
        --defaults-file /home/step/config/defaults.json)
    MP_CERT_TOKEN="$token" docker compose up -d "$name"
}

enroll catalog
enroll client
enroll store

cat <<'MSG'

Demo stack is up. Try:
  docker compose -f demo/docker-compose.yml exec client ./brfs /data/sample-data --destination store:8080
  docker compose -f demo/docker-compose.yml exec client ./rwfs list store:8080
  docker compose -f demo/docker-compose.yml exec client ./rwfs verify store:8080
  docker compose -f demo/docker-compose.yml logs -f store
  docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
  docker compose -f demo/docker-compose.yml exec store ./agent list-policies

Reset with: docker compose -f demo/docker-compose.yml down -v
MSG
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x demo/up.sh
```

- [ ] **Step 3: Cold-start run**

Run:
```bash
cd demo && docker compose down -v 2>/dev/null; cd ..
./demo/up.sh
```
Expected: script completes with exit code 0, prints the "Demo stack is up." message; `docker compose -f demo/docker-compose.yml ps` shows all five services `Up`/`running`, none restarting.

- [ ] **Step 4: Idempotent re-run**

Run:
```bash
./demo/up.sh
```
Expected: exit code 0; output shows `"catalog already enrolled, starting..."` (and the same for `client`/`store`) rather than re-minting tokens; no `clientmanager add` "already exists" error surfaces (the `enroll` function's `docker compose run` probe correctly detected the existing `bootstrap.crt` in each node's volume and skipped straight to `docker compose up -d`).

- [ ] **Step 5: Commit**

```bash
git add demo/up.sh
git commit -m "$(cat <<'EOF'
feat(demo): add up.sh, one-command enrollment for the whole stack

Probes each node's own persistent volume (via a throwaway
`docker compose run`) to decide bootstrap vs. skip, rather than
exec-ing into a container that may never have started -- correct on
both a cold volume and a re-run against an already-enrolled stack.
EOF
)"
```

---

### Task 6: Root `Makefile` targets

**Files:**
- Modify: `Makefile`

**Interfaces:**
- Consumes: `demo/up.sh` (Task 5), `demo/docker-compose.yml` (Tasks 1-4).
- Produces: `make demo-up`, `make demo-down`.

- [ ] **Step 1: Add `demo-up`/`demo-down` to the `.PHONY` line**

Replace:
```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certclient catalogsync catalog agent clientmanager issuer test test-e2e lint control-plane-up
```
With:
```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certclient catalogsync catalog agent clientmanager issuer test test-e2e lint control-plane-up demo-up demo-down
```

- [ ] **Step 2: Add the targets after `control-plane-up`**

Append immediately after the existing `control-plane-up` target's recipe (the line `@echo -e "$(GREEN)Control plane up.$(NC) ca: https://localhost:9000  catalog: localhost:15723"`):

```makefile

demo-up: ## Bring up the self-contained demo lab (ca + issuer + catalog + client + store)
	@./demo/up.sh

demo-down: ## Tear down the demo lab and wipe all its volumes
	@cd demo && docker compose down -v
```

- [ ] **Step 3: Confirm the targets are wired**

Run:
```bash
make help | grep demo
```
Expected:
```
  demo-up         Bring up the self-contained demo lab (ca + issuer + catalog + client + store)
  demo-down       Tear down the demo lab and wipe all its volumes
```

- [ ] **Step 4: Confirm `make demo-down` and `make demo-up` both work end to end**

Run:
```bash
make demo-down
make demo-up
```
Expected: same as Task 5 Step 3's cold-start expectations, invoked through `make` this time.

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "$(cat <<'EOF'
feat(deploy): add demo-up/demo-down Makefile targets

Matches the existing control-plane-up convention.
EOF
)"
```

---

### Task 7: End-to-end walkthrough proof

**Files:** none (validation only — this is deployment tooling, proven by running the actual stack, the same honest framing the credential-tier-enforcement plan used for its own CA-template-only task).

**Interfaces:**
- Consumes: the fully running stack from Tasks 1-6.
- Produces: nothing for later tasks — this is the terminal proof that the whole spec's Testing/Validation section holds.

- [ ] **Step 1: Ensure a clean, fully up stack**

Run:
```bash
make demo-down
make demo-up
```
Expected: exit code 0, all five services `Up` (per Task 6 Step 4).

- [ ] **Step 2: Back up the sample files**

Run:
```bash
docker compose -f demo/docker-compose.yml exec -T client ./brfs /data/sample-data --destination store:8080
```
Expected: exit code 0, brfs reports both `hello.txt` and `notes.txt` transferred successfully.

- [ ] **Step 3: List and verify from `client` against `store`**

Run:
```bash
docker compose -f demo/docker-compose.yml exec -T client ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec -T client ./rwfs verify store:8080
```
Expected: `list` shows both sample files under the `client` hostname; `verify` reports both files' BLAKE3/CRC32 checks passing, exit code 0.

- [ ] **Step 4: Confirm catalog replication**

Run:
```bash
sleep 10  # CatalogSyncPollIntervalSec default is 5s
docker compose -f demo/docker-compose.yml exec -T catalog sqlite3 /data/storage/catalog.db "select count(*) from entry_records;"
```
Expected: a count of at least 2 (one row per replicated file version).

- [ ] **Step 5: Confirm agent policy health on every agent-managed node**

Run:
```bash
docker compose -f demo/docker-compose.yml exec -T catalog ./agent list-policies
docker compose -f demo/docker-compose.yml exec -T client ./agent list-policies
docker compose -f demo/docker-compose.yml exec -T store ./agent list-policies
```
Expected: all three show `bootstrap-refresh` and `operating-refresh` as `ok`, with recent last-run timestamps.

- [ ] **Step 6: Prove revoke actually lapses mesh access**

Run:
```bash
docker compose -f demo/docker-compose.yml exec -T ca clientmanager revoke store
sleep 2
docker compose -f demo/docker-compose.yml exec -T store ./certclient operating-refresh; echo "exit: $?"
docker compose -f demo/docker-compose.yml exec -T store ./certclient renew; echo "exit: $?"
docker compose -f demo/docker-compose.yml exec -T ca clientmanager unrevoke store
```
Expected: `operating-refresh` exits non-zero (`issuer` refuses the revoked hostname); `renew` (bootstrap-tier, independent of `issuer`) exits 0 — the "identity survives, only mesh access lapses" property working end to end. `unrevoke` restores `store` for anyone re-running the walkthrough afterward.

- [ ] **Step 7: Record the result**

No commit for this task (no files changed) — note in the PR description / final review that this walkthrough was run and passed, per the spec's Testing/Validation section.

---

### Task 8: Documentation

**Files:**
- Create: `demo/README.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Write `demo/README.md`**

```markdown
# Demo Lab Environment

A self-contained `docker compose` stack — CA, `issuer`, `catalog`, and two backup-capable nodes
(`client`, `store`) — mutually enrolled via mTLS, brought up with one script. Unlike
[`deploy/control-plane`](../deploy/control-plane/README.md), this never touches your host
filesystem beyond this directory: every secret and every byte of state lives in Docker-managed
named volumes, and no port is published to the host. Everything is reached via `docker compose
exec`.

## Bring it up

```bash
make demo-up
```

Equivalent to `./demo/up.sh` directly. Builds all five images, brings up `ca` and `issuer` first,
then mints and redeems an enrollment token for `catalog`, `client`, and `store` in turn (skipping
re-minting on a re-run against an already-enrolled node).

## Try it

```bash
docker compose -f demo/docker-compose.yml exec client ./brfs /data/sample-data --destination store:8080
docker compose -f demo/docker-compose.yml exec client ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec client ./rwfs verify store:8080
docker compose -f demo/docker-compose.yml logs -f store          # watch bwfs receive + catalogsync replicate
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
docker compose -f demo/docker-compose.yml exec catalog ./agent list-policies
docker compose -f demo/docker-compose.yml exec client ./agent list-policies
docker compose -f demo/docker-compose.yml exec store ./agent list-policies
```

## Revoke, and watch mesh access lapse without losing identity

```bash
docker compose -f demo/docker-compose.yml exec ca clientmanager revoke store
docker compose -f demo/docker-compose.yml exec store ./certclient operating-refresh   # fails
docker compose -f demo/docker-compose.yml exec store ./certclient renew               # still succeeds
docker compose -f demo/docker-compose.yml exec ca clientmanager unrevoke store
```

## Reset

```bash
make demo-down
```

Removes every container and volume — the next `make demo-up` starts from a byte-for-byte clean
slate, including a freshly generated CA and provisioner password.

## See Also

- [Design: Demo Lab Environment v2](../docs/superpowers/specs/2026-07-06-demo-lab-environment-v2-design.md)
- [Control Plane](../deploy/control-plane/README.md) — the production-shaped deployment reference this demo deliberately never reuses (separate compose file, volumes, and ports)
- [Architecture](../docs/ARCHITECTURE.md)
- [Security Model](../docs/SECURITY.md)
```

- [ ] **Step 2: Update `README.md`**

Add a line under `## Quick Start`, immediately before the `**Backup files:**` subsection:

```markdown
Try the whole system running together in one command: see [demo/README.md](demo/README.md).

```

- [ ] **Step 3: Update `docs/ARCHITECTURE.md`**

Add one line to the `## Control Plane vs. Agents` section's introduction (immediately before the
table), cross-referencing the demo:

```markdown
See [demo/README.md](../demo/README.md) for a self-contained, one-command lab environment
exercising this whole topology end to end.

```

- [ ] **Step 4: Add a `CHANGELOG.md` entry**

Insert immediately after the `All notable changes...` line (before the existing top entry):

```markdown
## 2026-07-06 — Self-contained demo lab environment, updated for the current architecture

The 2026-07-03 demo lab design predated `issuer`, the two-tier bootstrap/operating credential
split, and `client-manager` (it assumed the now-retired `certrequest`) — it could no longer be run
as written. `demo/` now stands up `ca`, `issuer`, `catalog`, and two backup-capable nodes
(`client`, `store`) with one command (`make demo-up`), fully self-contained: no host ports
published, no host bind-mounts of secrets, and no dependency on `deploy/control-plane`'s own
compose file or volumes. `catalog`'s image is reused directly rather than duplicated; the CA's
leaf template is read straight from `deploy/control-plane/ca/templates/leaf.tpl` at build time so
the two deployments can't silently drift apart.

```

- [ ] **Step 5: Commit**

```bash
git add demo/README.md README.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: document the rewritten demo lab environment

Adds demo/README.md, links it from the root README and
ARCHITECTURE.md, and adds a CHANGELOG entry explaining why the old
demo design couldn't be run as written and what replaced it.
EOF
)"
```

---

## Final Verification

- [ ] `make demo-down && make demo-up` succeeds from a cold state (no leftover volumes).
- [ ] `docker compose -f demo/docker-compose.yml ps` shows all five services `Up`, none restarting.
- [ ] Re-running `make demo-up` without `make demo-down` first completes without error (idempotency).
- [ ] The full Task 7 walkthrough (backup, list, verify, catalog replication, agent policy health,
      revoke/unrevoke) passes end to end.
- [ ] `git diff main --stat` shows no changes outside `demo/`, `Makefile`, `README.md`,
      `docs/ARCHITECTURE.md`, and `CHANGELOG.md` — confirms `deploy/control-plane` itself was
      never modified, only referenced.
