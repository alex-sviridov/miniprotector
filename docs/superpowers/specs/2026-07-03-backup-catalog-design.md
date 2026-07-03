# Backup Catalog — Design

## Problem

`catalogsync` (see `2026-07-02-bwfs-catalog-replication-design.md`) replicates each `bwfs` node's
`file_versions` rows outward, but the receiving end doesn't exist yet — today it only proves the
pipeline via a `LoggingSender` stand-in that logs batches and always succeeds. There is still no
central, cross-node view of backup history, the goal of a "backup catalog" per the project's core
goals (`README.md`: "Complete backup history tracking and reporting"). This spec designs and builds
that receiving service: `catalog`, plus the real `Sender` implementation that lets `catalogsync`
talk to it.

## Goals

- A new standalone component, `catalog`, that receives replicated file-version batches over gRPC
  and persists them idempotently to its own SQLite database.
- `catalogsync` gains a `GrpcSender` (a second `Sender` implementation) that dials `catalog` using
  the same mTLS connection machinery `brfs`/`rwfs` already use.
- `catalog` fits the project's existing configuration and certificate conventions exactly: same
  `local.conf`/`MP_CONFIG_PATH` pattern, same `certclient`-bootstrapped mTLS identity, same
  `connection.StartServer` gRPC server helper as `bwfs`.
- `catalog` is a control-plane component and ships with its own `docker compose` deployment,
  mirroring how `ca/` is deployed today.
- Long-running `catalog`/`catalogsync` processes pick up certificates renewed on disk by a
  scheduled `certclient` run without needing a restart.

## Non-Goals

- Any query/report/read API on `catalog` (e.g. "list what's in the catalog"). This phase is
  receive-and-store only; a read API is future control-plane work.
- Reconciling catalog entries against a `bwfs` job that later fails and gets purged locally (see
  the `catalogsync` design's Non-Goals — still the catalog's responsibility, still deferred).
- Automating certificate *renewal* itself (i.e. running `certclient` on a schedule) — this spec
  only ensures a *running* `catalog`/`catalogsync` process picks up a renewal that already
  happened on disk. Triggering that renewal periodically (cron/systemd timer/sidecar) is an
  operational concern outside this spec, same as it already is for every other node today.
- CA root rotation handling — `ca.crt`/`ClientCAs` stays loaded once at startup; root rotation is
  a separate, rare, manual event, unrelated to `certclient`'s routine leaf-cert renewal.

## Architecture

### New Protocol (`src/api/catalog.proto`)

```protobuf
syntax = "proto3";

package catalogservice;

option go_package = "./proto";

service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
}

message FileVersionEntry {
  string job_id     = 1;
  string object_id  = 2;
  bytes  metadata   = 3;
  int64  ctime      = 4;
  int64  source_seq = 5; // bwfs's local file_versions.seq — informational only, no meaning here
  int64  created_at = 6; // unix seconds; bwfs's original recording time
}

message SyncRequest {
  repeated FileVersionEntry entries = 1;
}

message SyncResponse {} // empty ack — GrpcSender only checks error/nil
```

One unary RPC call per batch — `catalogsync` already batches client-side (`CatalogSyncBatchSize`),
so `SyncRequest.entries` carries the whole batch in a single round trip. Error semantics stay
batch-level: any RPC failure fails the whole `Send`, matching `catalogsync`'s existing retry/backoff
loop, which already assumes `Sender.Send(batch) error` is all-or-nothing per call.

### New Component: `catalog` (`src/cmd/catalog/`)

A standalone gRPC server, no subcommands (single mode: receive and store):

```
catalog <storage_path> [--port N] [--debug]
```

`--port` defaults to `conf.CatalogPort`, overridable — same convention as `bwfs server --port`.
Starts its gRPC server via the existing `connection.StartServer(ctx, logger, port, certsDir, ...)`
helper, identical to how `bwfs` registers its services — `catalog` only adds a
`RegisterCatalogServiceServer` call to that same pattern.

### Storage (`<storage_path>/catalog.db`)

A dedicated SQLite database, separate from any `bwfs` node's `metadata.db`, opened with the same
driver/pattern as `storage/filesystem/db.go` (modernc.org/sqlite + GORM, WAL mode, single
connection — `catalog` has exactly one writer, itself).

```go
type CatalogEntryRecord struct {
    ID              int64  `gorm:"primaryKey;autoIncrement"`
    SourceNode      string `gorm:"uniqueIndex:idx_source_job_object"`
    JobID           string `gorm:"uniqueIndex:idx_source_job_object"`
    ObjectID        string `gorm:"uniqueIndex:idx_source_job_object"`
    Metadata        []byte
    Ctime           int64
    SourceSeq       int64
    SourceCreatedAt time.Time
    ReceivedAt      time.Time
}
```

**Why `SourceNode`:** `(job_id, object_id)` is only unique *within a single `bwfs` node* — `catalog`
is meant to receive from a fleet of `bwfs` nodes, so the idempotency key must be
`(source_node, job_id, object_id)` to avoid cross-node collisions. `SourceNode` comes from
`mtls.PeerHostname(ctx)` (`common/mtls/peer.go`) — the CA-verified SAN/CN off the client's mTLS
cert, an existing helper defined but currently unused anywhere. This means source-node identity is
cryptographically attested by the CA, not self-reported by the caller.

**Idempotent write:** same pattern as `bwfs`'s `EnsureFileVersion` —
`ON CONFLICT (source_node, job_id, object_id) DO NOTHING`. A batch resent after a retry (per the
`catalogsync` design's at-least-once delivery guarantee) is a safe no-op for rows already stored.

### `catalogsync`: `GrpcSender` (`src/cmd/catalogsync/grpcsender.go`)

A second implementation of the existing `Sender` interface, alongside `LoggingSender`:

```go
type GrpcSender struct {
    client pb.CatalogServiceClient
}

func (s *GrpcSender) Send(batch []filesystem.FileVersionRecord) error
```

Dials via the existing `connection.Connect(host, port, timeout, certsDir)` helper (same one
`brfs`/`rwfs` use), converts `batch` into one `SyncRequest`, and calls `SyncFileVersions`. In
`cmd/catalogsync/main.go`: if `conf.CatalogHost != ""`, construct `GrpcSender`; otherwise fall back
to today's `LoggingSender` with a warning logged. This keeps `catalogsync` fully runnable in
deployments/tests without a `catalog` instance configured — including the existing e2e suite,
unchanged.

### Config (`src/common/config/config.go`)

Two new optional fields, following the `ca_host` precedent (config-only, no CLI flag — a fixed
operational parameter of the fleet, not a per-invocation destination like `brfs --destination`):

| Key | Default | Meaning |
|-----|---------|---------|
| `catalog_host` | *(none, optional)* | Consumed by `catalogsync`'s `GrpcSender` to know where to dial. Unset means no `catalog` deployed yet — `catalogsync` falls back to `LoggingSender`. |
| `catalog_port` | 15723 | Dial target port (paired with `catalog_host`) **and** `catalog`'s own default listen port — the same field serves both roles, since a single `local.conf` schema is shared fleet-wide across node types. |

### Certificates — same pattern as `bwfs`/`brfs`/`rwfs`

- `catalog` resolves its certs dir via `config.ResolveCertsDir()` and requires/verifies every
  client cert via `connection.StartServer`'s existing mTLS wiring — no `catalog`-specific auth
  logic.
- Identity is bootstrapped/renewed by running the **`certclient` binary** against
  `MP_CONFIG_PATH/certs`, exactly as documented in `docs/components/certclient.md`. `catalog`
  itself never touches enrollment tokens or talks to the CA directly.
- `catalogsync` dialing `catalog` is its first outbound mTLS connection (previously it only read
  `bwfs`'s local SQLite file) — it uses the same identity `certclient` already bootstraps on every
  node.

### Certificate hot-reload (`src/common/mtls/mtls.go`)

Pre-existing gap, not `catalog`-specific: `LoadServerCredentials`/`LoadClientCredentials` currently
read `client.crt`/`client.key`/`ca.crt` from disk **once**, at server-start/dial time, baked
statically into `tls.Config.Certificates`. `bwfs` already has this exposure today — `certclient` is
documented as safe to run on a schedule while the long-running server keeps serving, but nothing
today picks up a renewed cert without a restart. `catalog` (long-running server) and `catalogsync`
(now a long-running mTLS client, for the first time) hit this immediately.

Fixed once, in the shared package, so `bwfs`/`brfs`/`rwfs` benefit too — no caller-visible API
change:

- **Server side** (`serverTLSConfig`): replace the static `tls.Config.Certificates` field with
  `GetCertificate`, Go's built-in hook invoked by the TLS stack on every *new handshake* (i.e. every
  new connection, not every RPC — gRPC connections are kept alive/multiplexed, so this is cheap and
  infrequent). The callback re-reads and re-parses `client.crt`/`client.key` from `certsDir` on each
  call.
- **Client side** (`clientTLSConfig`): same fix via `GetClientCertificate`, the client-side
  equivalent hook, invoked when the server requests a client cert during handshake.
- `ca.crt`/`ClientCAs`/`RootCAs` stay loaded once at startup in both cases (see Non-Goals).

## Data Flow

```
catalogsync (per bwfs node, unchanged poll loop):
  batch = ReplicaReader.FileVersionsSince(cursor, batchSize)
  err = Sender.Send(batch)              # GrpcSender if catalog_host set, else LoggingSender
    → GrpcSender: dial catalog_host:catalog_port (mTLS, cert reloaded per new connection)
    → SyncFileVersions(SyncRequest{entries: batch})

catalog (central, one instance):
  SyncFileVersions(ctx, req):
    sourceNode = mtls.PeerHostname(ctx)                     # CA-verified, from client cert SAN
    for entry in req.entries:
      INSERT INTO catalog_entries (...) ON CONFLICT (source_node, job_id, object_id) DO NOTHING
    return SyncResponse{}
```

## Error Handling

- **RPC failure (network, `catalog` down, etc.)**: `GrpcSender.Send` returns an error for the whole
  batch; `catalogsync`'s existing backoff/retry logic (unchanged) re-polls from the same unadvanced
  cursor — no partial-batch bookkeeping needed anywhere, since the RPC is all-or-nothing per batch.
- **Retried batch partially persisted before a prior failure**: safe — `ON CONFLICT DO NOTHING` on
  `(source_node, job_id, object_id)` makes re-sending the full batch idempotent.
- **`catalog_host` unset**: `catalogsync` falls back to `LoggingSender`, logging a warning — no
  hard failure, so existing deployments/tests without a `catalog` instance keep working unchanged.
- **Certificate renewed on disk while `catalog`/`catalogsync` is running**: picked up automatically
  on the next new TLS handshake via `GetCertificate`/`GetClientCertificate` — no restart required.
  An in-flight connection established before renewal keeps using the cert valid at connection time;
  only a *new* connection sees the renewed one (acceptable — gRPC keepalive-based long-lived
  connections eventually cycle, and this matches standard TLS hot-reload semantics).

## Testing

- Unit: `SyncFileVersions` — idempotent write on duplicate `(source_node, job_id, object_id)`,
  distinct rows for the same `(job_id, object_id)` from two different `SourceNode` values (proves
  the collision this key is designed to avoid), `SourceNode` correctly extracted from a fake
  authenticated context.
- Unit: `GrpcSender.Send` — batch converts to a single `SyncRequest` with matching entry count and
  fields; RPC error propagates as `Send`'s returned error.
- Unit: `catalogsync` `main.go` sender selection — `GrpcSender` chosen when `catalog_host` is set,
  `LoggingSender` otherwise.
- Unit: `mtls.serverTLSConfig`/`clientTLSConfig` — `GetCertificate`/`GetClientCertificate` reflects
  a cert file rewritten on disk after the `tls.Config` was built, without reconstructing it.
- Integration: `catalogsync` (`GrpcSender`) → `catalog` round trip over a real mTLS connection using
  `common/testdata/certs`, confirming persisted rows match the sent batch.
- Integration/e2e: extend `src/e2e` with a `catalog` container alongside `bwfs`, proving
  `brfs → bwfs → catalogsync → catalog` end-to-end.

## Deployment (`catalog/`)

New top-level directory, mirroring `ca/`'s structure — an independently deployable control-plane
component:

```
catalog/
  Dockerfile        # multi-stage: build `catalog` + `certclient`, copy into debian:bookworm-slim
  entrypoint.sh      # certclient (bootstrap-or-renew) then exec catalog server
  docker-compose.yml # service "catalog": build, volumes for storage_path + config/certs, port mapping
  local.conf          # template: catalog_port, ca_host, logfolder + placeholder default_port/
                       # default_streams (ParseConfig requires these regardless of which binary
                       # reads the file — pre-existing friction, not changed here)
  README.md            # first-time setup: enroll via certrequest against the CA, MP_CERT_TOKEN,
                       # docker compose up — same flow ca/README.md documents for any other node
```

`entrypoint.sh`:

```sh
#!/bin/sh
set -e
certclient  # bootstraps if no identity present (uses $MP_CERT_TOKEN), renews if present
exec ./catalog "$STORAGE_PATH" --debug="${DEBUG:-false}"
```

`MP_CERT_TOKEN` is only needed on first boot; subsequent container restarts renew automatically
since `certclient` always renews when an identity already exists. This entrypoint does not itself
trigger renewal *while the container keeps running* — that's a separate scheduled job (see
Non-Goals); this design only ensures the running process picks up a renewal once it happens (see
Certificate hot-reload above).

**Makefile:** new `catalog` target, identical shape to the existing `catalogsync` target
(`CATALOG_CMD := cmd/catalog`, added to `.PHONY`).

## Documentation Impact

Per `.claude/CLAUDE.md`, before merging:
- New `docs/components/catalog.md` (usage, config keys, schema, idempotency note) — same style as
  `docs/components/catalogsync.md`.
- New `docs/protocols/catalog-sync.md` documenting `catalog.proto` (required before committing any
  new `.proto` file per the gRPC protocol doc rule).
- `README.md` — add `catalog` to the Components list, cross-link the new protocol doc.
- `docs/ARCHITECTURE.md` — add a `catalog` row to the components table (status "Implemented"),
  update the mermaid diagram so `catalogsync` points at a solid `catalog` node instead of the
  dashed "planned" `Catalog`, and add `catalog` to the Control Plane vs. Agents table (control
  plane by role, but bootstraps its mTLS identity the same way agents do via `certclient` — worth
  calling out explicitly since it doesn't fit either existing row cleanly).
- `docs/components/catalogsync.md` — update to describe `GrpcSender` as the active `Sender`
  (config-gated by `catalog_host`), `LoggingSender` as the fallback.
- `CHANGELOG.md` — entry before merging to `main` (per existing rule; not part of this spec).
