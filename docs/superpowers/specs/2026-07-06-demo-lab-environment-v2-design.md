# Demo Lab Environment v2 — Design

> Supersedes `docs/superpowers/specs/2026-07-03-demo-lab-environment-design.md`, which predates
> `issuer`, the two-tier bootstrap/operating credential split, `client-manager` (it assumed the
> now-retired `certrequest`), and credential-tier enforcement. The original design's *shape* — a
> self-contained `demo/` tree, one image per node role, hostnames doubling as Compose DNS names and
> mTLS SANs, a single `up.sh` bring-up script — is still correct and is kept. What changes is
> everything downstream of enrollment: which binaries each image bundles, and how identities are
> obtained.

## Problem

There's still no way to see the whole current system running together — CA-issued mTLS identities
across the real two-tier bootstrap/operating model, `issuer` enforcing revoke and attributes, a
`brfs` backup landing on `bwfs`, `catalogsync` replicating it to `catalog`, `agent` keeping every
node's credentials fresh on two independent cadences — without hand-assembling hosts. The stale
2026-07-03 design can't be run as written: it targets binaries and a credential model that no
longer exist (`certrequest`, single-tier `certclient`, one `cert-refresh` policy).

## Goals

- A `demo/` docker-compose stack, fully self-contained (never touches `deploy/control-plane`'s
  compose file, volumes, or ports), standing up `ca`, `issuer`, `catalog`, and two backup-capable
  nodes (`client`, `store`), all mutually enrolled via mTLS, with one script.
- Real network communication between distinct containers for every hop: `client`→`store` (backup
  protocol), `store`'s `catalogsync`→`catalog`, every enrolled node→`ca`/`issuer`.
- The current two-tier credential model (bootstrap + operating) and `issuer`-enforced
  revoke/attributes/SANs are genuinely exercised, not simulated.
- Cheap to reset: `docker compose down -v` plus a re-run of `up.sh` gives a byte-for-byte clean
  slate.
- Enough out-of-the-box sample content to run an actual backup within a minute of the stack coming
  up.

## Non-Goals

- Not a production deployment reference — `deploy/control-plane/` remains that.
- No multi-host simulation beyond Docker networking; no more than the two backup-capable nodes
  described below.
- No catalog query UI/CLI — verification goes through `sqlite3` directly against `catalog`'s DB, or
  through `bwfs`/`rwfs` on the store side, same as `deploy/control-plane`.
- No host ports published. Every health check and interaction goes through `docker compose exec`
  (a deliberate change from the 2026-07-03 design, which published `9000`/`15723` to the host and
  `curl`'d them directly — that risks colliding with a real `deploy/control-plane` stack running on
  the same machine). This makes "never reuses ports" true by construction instead of by convention.

## Architecture

Five containers on one Compose network:

| Service | Base | Role |
|---|---|---|
| `ca` | `smallstep/step-ca` + `client-manager` binary baked in | Certificate authority (port 9000, internal only) |
| `issuer` | `debian:bookworm-slim` + `issuer` binary | Operating-certificate service (port 9200, internal only) |
| `catalog` | `debian:bookworm-slim` + `catalog`/`certclient`/`agent` | Central catalog (port 15723, internal only) |
| `client` | `debian:bookworm-slim` + `brfs`/`rwfs`/`bwfs`/`catalogsync`/`certclient`/`agent` | Interactive backup source |
| `store` | same image as `client` | Backup target: runs `bwfs`+`catalogsync` in the background |

`client` and `store` are **one image** (`demo/backup-host/Dockerfile`); which role a container plays
is decided entirely by whether `STORAGE_PATH` is set in its environment — mirrors the existing
multi-binary bundling pattern already used by `deploy/control-plane/catalog/Dockerfile` (which
bundles `catalog`+`certclient`+`agent` into one image today).

Hostnames double as Compose DNS names and mTLS SANs: `client` reaches `store` at `store:8080`;
`store`'s `catalogsync` reaches `catalog` at `catalog:15723`. Since `certmint.Mint` always includes
the hostname itself as a SAN (`allSANs := append([]string{hostname}, sans...)`,
`src/common/certmint`), minting each node's enrollment token with `client-manager add <name>` (no
`--san` needed) is sufficient — the cert's SAN already exactly matches the Compose DNS name used to
dial it.

`ca/templates/leaf.tpl` is **not duplicated**: the `ca` Dockerfile `COPY`s it directly from
`deploy/control-plane/ca/templates/leaf.tpl` (build context is the repo root), so the demo
automatically carries whatever attribute-embedding/tier-enforcement logic that template has, rather
than risking a hand-copied second file silently drifting from the original.

A new shared named volume, `client-manager-data`, is mounted into `ca` (where the baked-in
`client-manager` binary writes its SQLite DB) and into `issuer` (where it reads `revoked`/attribute/
SAN state) — the same shape as `deploy/control-plane`'s real `client-manager/data` volume, just
Docker-managed instead of a host bind-mount.

### `client`/`store` entrypoint: reuse, don't reinvent

`deploy/control-plane/catalog/entrypoint.sh` already solves the exact sequencing problem this phase
needs (bootstrap-if-missing-else-renew the long-lived credential, background `agent serve`, wait up
to 60s for `agent`'s first `operating-refresh` tick to produce `client.crt`/`client.key`, then exec
the real binary) — `agent`'s reconcile loop runs every policy immediately on a fresh cache
(`isDue` returns true when `LastSuccessAt == nil`, `src/cmd/agent/reconcile.go`), so no extra
"trigger a refresh now" step is needed. `demo/backup-host/entrypoint.sh` copies this pattern
verbatim, adding only the `STORAGE_PATH`-gated background start of `bwfs`+`catalogsync` (with a
`trap ... TERM`) that the original 2026-07-03 design already called for:

```sh
if [ -f certs/bootstrap.crt ]; then
    ./certclient renew
else
    ./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

./agent serve &

timeout=60
while [ ! -f certs/client.crt ] && [ "$timeout" -gt 0 ]; do
    sleep 1; timeout=$((timeout - 1))
done
[ -f certs/client.crt ] || { echo "agent did not produce an operating certificate within 60s" >&2; exit 1; }

if [ -n "$STORAGE_PATH" ]; then
    mkdir -p "$STORAGE_PATH"
    ./bwfs "$STORAGE_PATH" server --port 8080 &
    ./catalogsync &
    trap 'kill %1 %2 2>/dev/null' TERM
fi

wait -n   # keep the background agent (and, on store, bwfs/catalogsync) as PID 1's supervised children
```

(Exact process-supervision wiring — e.g. whether `agent serve` itself becomes the final `exec`'d
foreground process instead of a backgrounded one — is a plan-stage detail; the sequencing above,
proven in `catalog`'s entrypoint today, is what matters.)

## File Layout

```
demo/
  docker-compose.yml
  up.sh
  README.md
  local.conf                    # shared config, bind-mounted read-only into issuer/catalog/client/store
  sample-data/                  # 3-4 small files, backed up out of the box
  ca/
    Dockerfile                  # golang:1.26 builder (make clientmanager) -> FROM smallstep/step-ca
    entrypoint.sh                # copy of deploy/control-plane/ca/entrypoint.sh
  issuer/
    Dockerfile                  # same shape as deploy/control-plane/issuer/Dockerfile
    local.conf
  catalog/
    Dockerfile                  # same shape as deploy/control-plane/catalog/Dockerfile
    entrypoint.sh                # copy of deploy/control-plane/catalog/entrypoint.sh, unchanged
    local.conf
  backup-host/
    Dockerfile                  # golang:1.26 builder (make brfs bwfs rwfs catalogsync certclient agent)
                                 # -> debian:bookworm-slim + libgcc-s1 ca-certificates
    entrypoint.sh                # new; catalog's entrypoint pattern + STORAGE_PATH branch (above)
```

Root `Makefile` gains `demo-up` (calls `demo/up.sh`) and `demo-down`
(`docker compose -f demo/docker-compose.yml down -v`), matching the existing `control-plane-up`
convention.

## Configuration

One `demo/local.conf`, bind-mounted read-only into `issuer`/`catalog`/`client`/`store` (unused keys
are simply ignored by `ParseConfig`, same as the original design relied on):

```
default_port=8080
default_streams=4
logfolder=/var/log/miniprotector
issuer_host=issuer
issuer_port=9200
catalog_host=catalog
catalog_port=15723
ReconcileIntervalSec=30
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
JobTimeoutSec=30
```

`STORAGE_PATH` stays a per-service env var (not in `local.conf`): unset for `client`, `/data/storage`
for `store` and `catalog`.

## Enrollment Flow

```
operator: make demo-up
  up.sh:
    1. docker compose up -d --build ca
       poll: docker compose exec ca curl -fsk https://localhost:9000/health, bounded retries
    2. docker compose up -d --build issuer   (self-mints its own identity, no token needed)
    3. for each of catalog, client, store:
         skip entirely if that node's bootstrap.crt already exists (idempotent re-run)
         TOKEN=$(docker compose exec ca client-manager add <name> \
                   --ca-url https://localhost:9000 --root ... --password-file ... --defaults-file ...)
         MP_CERT_TOKEN="$TOKEN" docker compose up -d --build <name>
    4. print ready-to-use example commands
```

The token must be injected via Compose `environment:` at container **start** (`docker compose up`),
not via `docker compose exec -e` — the entrypoint reads `$MP_CERT_TOKEN` once at boot, exactly like
`deploy/control-plane`'s real, working usage today (`MP_CERT_TOKEN=<token> docker compose up -d
catalog`). This is a smaller, scripted version of exactly what `deploy/control-plane/README.md`'s
"Smoke test: enroll, connect, revoke" section already does by hand for one node, looped across
three.

## Error Handling

- **`ca` not ready when `up.sh` starts minting**: bounded health-poll retries (e.g. 30s), then
  `up.sh` exits non-zero with a clear message.
- **Idempotent re-run**: `client-manager add` errors on an already-tracked hostname, and
  `certclient bootstrap` (confirmed from source, `src/cmd/certclient/bootstrap.go`) has no
  existing-identity guard of its own — unlike the old single-`main()` `certclient`, which branched
  on file presence itself. The guard therefore lives in `up.sh`: check for `bootstrap.crt` inside
  each node's container before minting a new token; if present, skip straight to
  `docker compose up -d <name>` with no token, and the entrypoint's own file-check takes the
  `renew` branch.
- **`store`'s background `bwfs`/`catalogsync` dying without killing the container**: `trap ... TERM`
  only fires on container stop, not a crash mid-run — visible via `docker compose logs store`, not
  auto-restarted. Acceptable for a lab meant to be watched, not unattended reliability (unchanged
  from the original design's stance).
- **`debian:bookworm-slim` runtime missing `CGO_ENABLED=1` shared libraries**: install `libgcc-s1`
  (and `ca-certificates`) explicitly in `backup-host`'s runtime stage, matching
  `deploy/control-plane/catalog/Dockerfile` and `src/e2e/Dockerfile`'s existing convention.

## Testing / Validation

Deployment tooling, not Go code — validation is running the stack:

- `make demo-up` succeeds from a cold `docker compose down -v` state.
- Re-running `make demo-up` without `demo-down` first completes without error (idempotency check).
- All five containers reach steady running state, no restart loops.
- `docker compose exec client ./brfs /data/sample --destination store:8080` succeeds;
  `./rwfs list store:8080` / `./rwfs verify store:8080` from `client` both succeed.
- `catalog`'s `entry_records` table gets a row for the backed-up file within the poll interval.
- `docker compose exec catalog|client|store ./agent list-policies` shows both `bootstrap-refresh`
  and `operating-refresh` as `ok`.
- `docker compose exec ca client-manager revoke <name>` causes that node's next
  `operating-refresh` to fail while `bootstrap-refresh` keeps succeeding independently — the
  "identity survives, only mesh access lapses" property, now runnable in one command instead of a
  hand-run walkthrough.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule:

- New `demo/README.md` — first-time setup and the full walkthrough.
- `README.md` (repo root) — one link under Quick Start, framed as "try the whole system in a lab."
- `docs/ARCHITECTURE.md` — one-line cross-reference to `demo/README.md` alongside the existing
  `deploy/control-plane` mentions, if any exist there today; no topology change otherwise.
- No `.proto`/wire changes → no `docs/protocols/*` or `docs/components/*` updates needed.
- `CHANGELOG.md` entry before merge, per the standing rule.

## Relationship to the 2026-07-03 Design

Kept: the self-contained `demo/` tree, one-image-per-role bundling, hostnames doubling as DNS names
and SANs, a single `up.sh` entry point, `docker compose down -v` as the reset mechanism. Replaced:
`certrequest` → `client-manager` (baked into the `ca` image instead); single-shot `certclient` →
the current `bootstrap`/`renew`/`operating-refresh` subcommands, driven by `agent`'s two
independent-cadence policies instead of one `cert-refresh` policy; `ubuntu:24.04` → matching the
project's now-established `debian:bookworm-slim` + `libgcc-s1` convention; host-published ports →
none, everything via `docker compose exec`. The `catalog`/`certclient`/`agent` bundling pattern and
the bootstrap-wait-timeout entrypoint sequencing are lifted directly from `deploy/control-plane`'s
already-working, already-tested implementation rather than redesigned.
