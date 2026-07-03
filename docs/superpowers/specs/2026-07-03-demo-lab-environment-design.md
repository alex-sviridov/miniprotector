# Demo Lab Environment — Design

## Problem

There's no way to see the whole system running together — CA-issued mTLS identities, a `brfs`
backup landing on `bwfs`, `catalogsync` replicating it to a central `catalog`, and `agent`
keeping every node's certificate fresh — without hand-assembling hosts and running each binary's
enrollment steps manually. `deploy/control-plane/` covers the CA+catalog pair but expects real
node hostnames and manual `certrequest`/`certclient` steps per node; there's nothing that stands
up a small, disposable, fully-networked lab in one command.

## Goals

- A `demo/` docker-compose stack that stands up a CA, a catalog, and two backup-capable nodes,
  all mutually enrolled via mTLS, with one script.
- Real network communication between distinct containers (not loopback shortcuts) for every hop:
  `brfs`→`bwfs`, `bwfs`'s `catalogsync`→`catalog`, every node→`ca`.
- Cheap to reset: `docker compose down -v` plus a re-run of the bring-up script gives a byte-for-
  byte clean slate.
- Enough out-of-the-box content (sample files) to run an actual backup within a minute of the
  stack coming up.

## Non-Goals

- Not a production deployment reference — `deploy/control-plane/` remains that. `demo/` is
  self-contained and never reuses `deploy/control-plane/`'s compose file, volumes, or ports.
- No multi-host simulation beyond what Docker networking provides (no simulated latency/packet
  loss, no more than the two backup-capable nodes described below).
- No catalog query UI/CLI — `catalog` genuinely has none yet; verification goes through
  `sqlite3` directly against its DB, or through `bwfs`/`rwfs` on the store side.

## Architecture

Four containers on one Compose network:

| Service | Base | Role |
|---|---|---|
| `ca` | `smallstep/step-ca` + `certrequest` binary | Certificate authority (port 9000) |
| `catalog` | `ubuntu:24.04` + `catalog`, `certclient`, `sqlite3` | Central catalog (port 15723) |
| `client` | `ubuntu:24.04` + `brfs`, `rwfs`, `bwfs`, `catalogsync`, `certclient`, `agent` | Interactive backup source |
| `store` | same image as `client` | Backup target: runs `bwfs` + `catalogsync` in the background |

`client` and `store` are **one image** (`demo/backup-host/Dockerfile`), bundling every node-side
binary — mirrors the existing multi-binary pattern in `src/e2e/Dockerfile`. Which role a
container plays is decided entirely by whether `STORAGE_PATH` is set in its environment; adding a
third backup-host later is just another Compose service block referencing the same image, no new
Dockerfile.

`agent serve` is the final foreground process in both roles — this is `agent`'s first real
deployment (it's `Implemented` per `docs/ARCHITECTURE.md` but "not yet wired into any deployment"
per the latest commit), doing what it's actually for: periodic `certclient` renewal, observable
via `agent list-policies`.

Hostnames double as Compose DNS names and mTLS SANs. `client` reaches `store` at `store:8080`;
`store`'s `catalogsync` reaches `catalog` at `catalog:15723`. Neither is loopback, so each
target's certificate SAN must exactly equal the hostname used to dial it — satisfied by minting
each node's enrollment token with that same string as the positional hostname (see control-plane's
existing SAN-matching rule in `deploy/control-plane/README.md`, which this reuses unchanged).

## File Layout

```
demo/
  docker-compose.yml
  up.sh                # bring up ca, wait for it, mint+push identities, print next steps
  local.conf            # single shared config, bind-mounted into catalog/client/store alike
  README.md
  ca/
    Dockerfile          # golang:1.26 builder (make certrequest) -> FROM smallstep/step-ca
    entrypoint.sh        # generates its own provisioner password on first boot, step ca init, exec step-ca
  catalog/
    Dockerfile          # golang:1.26 builder (make catalog certclient) -> FROM ubuntu:24.04, + sqlite3
    entrypoint.sh        # wait for cert, exec ./catalog
  backup-host/
    Dockerfile          # golang:1.26 builder (make brfs bwfs rwfs catalogsync certclient agent) -> FROM ubuntu:24.04
    entrypoint.sh        # wait for cert; if $STORAGE_PATH set, bwfs+catalogsync in background (trap TERM); exec ./agent serve
  sample-data/
    (3-4 small files, backed up out of the box)
```

Root `Makefile` gains `demo-up` (calls `demo/up.sh`) and `demo-down`
(`docker compose -f demo/docker-compose.yml down -v`), matching the existing `control-plane-up`
convention.

## Configuration

One `demo/local.conf`, bind-mounted read-only into every enrolled service at whatever path its
own `MP_CONFIG_PATH` expects (`/data/local.conf` for `catalog`, `/app/local.conf` for
`client`/`store`):

```
default_port=8080
default_streams=4
logfolder=/var/log/miniprotector
ca_host=ca:9000
catalog_host=catalog
catalog_port=15723
ReconcileIntervalSec=30
JobTimeoutSec=30
```

`logfolder` is a path-neutral absolute directory (not nested under `/app` or `/data`) so the same
file behaves identically regardless of a service's own base directory — `common/logging`
`MkdirAll`s it on first use. Keys a given binary doesn't use (e.g. `catalog_host` on `client`,
which never runs `catalogsync`) are simply ignored — `ParseConfig` only rejects unknown *keys*,
not unused ones.

`STORAGE_PATH` (env var, not part of `local.conf`) stays per-service: `/data/storage` for
`catalog`, `/data` for `store`.

## Enrollment Flow

`certrequest` is built into the `ca` image itself (multistage: `golang:1.26` builder runs
`make certrequest`, final stage is `smallstep/step-ca` with the binary copied in) — no separate
throwaway container. Minting happens via `docker compose exec ca certrequest ...`, dialing
`https://localhost:9000` since it now runs inside the CA's own container.

Every enrolled node's entrypoint only **waits** for its identity to exist; it never invokes
`certclient` on its own initiative:

```sh
while [ ! -f certs/client.crt ]; do sleep 1; done
# (store only) start bwfs + catalogsync in the background here, with a trap to kill them on TERM
exec ./agent serve   # or ./catalog for the catalog service
```

`demo/up.sh`:

1. `docker compose up -d --build ca`; poll `curl -fsk https://localhost:9000/health` (bounded
   retries — see Error Handling) until it responds.
2. `docker compose up -d --build catalog client store` — each starts and blocks in its wait loop.
3. For each of `catalog`, `client`, `store`:
   - `TOKEN=$(docker compose exec ca certrequest <name> --ca-url https://localhost:9000 --defaults-file /home/step/config/defaults.json --root /home/step/certs/root_ca.crt --password-file /home/step/secrets/password)`
   - `docker compose exec -e MP_CERT_TOKEN="$TOKEN" <name> ./certclient`
   - The token exists only in the script's process memory for the duration of one `exec` call; it
     is never written to any file.
4. Print ready-to-use example commands (see Walkthrough).

`ca`'s entrypoint generates its own provisioner password into its own named volume on first boot
(`head -c32 /dev/urandom | base64`, not `openssl rand` — coreutils/busybox primitives are
guaranteed present in any base image, whereas `openssl` the CLI is not necessarily installed in
`smallstep/step-ca`), rather than requiring a host-side pre-step like `deploy/control-plane`'s
`Makefile` target does — there's no host bind-mount to seed ahead of time here, since all state is
Docker-managed named volumes (`ca-data`, `catalog-data`, `store-data`, and a `*-certs` volume per
enrolled node).

## Error Handling

- **`ca` not ready when `up.sh` starts minting**: the health poll retries with a short sleep up to
  a bounded timeout (e.g. 30s); past that, `up.sh` exits non-zero with a clear message rather than
  hanging indefinitely.
- **Re-running `up.sh` against an already-enrolled, still-running stack**: safe and idempotent.
  `certclient` checks for an existing identity (`hasExistingIdentity` — all three of
  `ca.crt`/`client.crt`/`client.key` present) *before* looking at any token; if one exists, it
  renews and ignores `MP_CERT_TOKEN` entirely. Minting a fresh token for an already-enrolled node
  is therefore wasted work but never an error.
- **`docker compose exec` racing an entrypoint still in its wait loop**: harmless — `exec` starts
  a new process inside the container's namespace independent of what PID 1 is doing; the target
  binaries (`certrequest`, `certclient`) are already present in the image regardless of entrypoint
  progress.
- **`store`'s background `bwfs`/`catalogsync` dying without killing the container**: the
  entrypoint's `trap ... TERM` only fires on container stop; a crash of one background process
  mid-run is not auto-restarted (no supervisor) — visible via `docker compose logs store` and a
  gap in `catalogsync`'s replication, not a mechanism failure. Acceptable for a lab meant to be
  watched, not for unattended reliability.
- **`ubuntu:24.04` runtime missing a shared library `CGO_ENABLED=1` binaries need**: install
  `libgcc-s1` explicitly in both the `catalog` and `backup-host` runtime stages regardless —
  the existing `deploy/control-plane/catalog/Dockerfile` and `src/e2e/Dockerfile` both need it on
  `debian:bookworm-slim`, and there's no cost to including it on `ubuntu:24.04` too even if it
  already ships there, so this isn't left to a build-time surprise.

## Walkthrough

```bash
make demo-up
docker compose -f demo/docker-compose.yml exec client ./brfs /data/sample --destination store:8080
docker compose -f demo/docker-compose.yml exec client ./rwfs list store:8080
docker compose -f demo/docker-compose.yml exec client ./rwfs verify store:8080
docker compose -f demo/docker-compose.yml logs -f store          # watch bwfs receive + catalogsync replicate
docker compose -f demo/docker-compose.yml exec catalog sqlite3 /data/storage/catalog.db "select * from entry_records;"
docker compose -f demo/docker-compose.yml exec store ./agent list-policies
make demo-down                                                    # full reset (down -v)
```

## Testing / Validation

This is deployment tooling, not Go code — validation is running the stack, not `go test`:

- `make demo-up` succeeds from a cold `docker compose down -v` state, on a machine with nothing
  pre-existing (no leftover volumes).
- All four containers reach a steady running state (`docker compose ps` shows no restart loops).
- A `brfs` run from `client` against `store:8080` succeeds; `rwfs list`/`rwfs verify` from
  `client` against `store:8080` both succeed.
- `entry_records` in `catalog`'s SQLite DB contains a row matching the file(s) just backed up,
  within `CatalogSyncPollIntervalSec` seconds.
- `agent list-policies` on both `client` and `store` shows `cert-refresh` as `ok`.
- Re-running `make demo-up` without `make demo-down` first completes without error (idempotency
  check from Error Handling).

## Documentation Impact

- New `demo/README.md` covers first-time setup and the walkthrough above in full.
- `README.md` (repo root) gets one link to `demo/README.md` under Quick Start, framed as "try the
  whole system in a lab."
- No `.proto`/wire changes, so no updates needed to `docs/protocols/*` or component docs under
  `docs/components/*`.
