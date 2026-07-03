# Changelog

All notable changes to this project are documented here, most recent first.

## 2026-07-03 — Node agent v1 (embedded cert-refresh reconciliation)

Added `agent`, a node-level process that replaces the bare cron entry for `certclient` with a
small reconcile loop: on a configurable interval it checks whether the (currently single,
compiled-in) `cert-refresh` policy is due, execs `certclient` if so, and records the outcome to a
local JSON cache — failures back off with jittered delays instead of retrying every tick. `agent
list-policies` reads that same cache to show each policy's health and estimated next run without
needing a running daemon. Also added `var_path` to `common/config`, a general directory for this
kind of runtime/variable data, defaulting to the running binary's own directory when unset. This
is the first concrete slice of a broader `agent` design that will later add queue-dispatched and
policy-server-fetched work on top of the same reconcile primitives.

## 2026-07-03 — Backup catalog service (catalog)

Added `catalog`, the receiving end of `catalogsync`'s replication pipeline: a standalone gRPC
service that persists replicated `bwfs` file-version batches to its own SQLite database, keyed by
`(source_node, job_id, object_id)` — `source_node` comes from the CA-verified mTLS client
certificate, never the payload, so a single catalog can safely receive from a fleet of `bwfs`
nodes. `catalogsync` gained a real `GrpcSender` (config-gated by `catalog_host`/`catalog_port`),
replacing the `LoggingSender` stand-in whenever a catalog is configured and reachable. `catalog`
ships its own `docker compose` deployment (`catalog/`), using the same `certclient`-bootstrapped
mTLS identity every other node uses. Also fixed a pre-existing gap in `common/mtls`: server and
client identity certificates are now re-read from disk on every new connection instead of once at
startup, so a certificate renewed by a scheduled `certclient` run is picked up without restarting
the long-running process — this benefits `bwfs`/`brfs`/`rwfs` too, not just this new pair.

## 2026-07-02 — Async catalog replication (catalogsync)

Added `catalogsync`, a new standalone component that tails a `bwfs` node's `file_versions` table
and forwards new rows to a future backup catalog, independently of `bwfs`'s own availability.
`catalogsync` opens `bwfs`'s SQLite database strictly read-only and tracks its own replication
progress in a small local cursor file, retrying with backoff whenever the catalog (represented
today by a logging stand-in `Sender`) is unreachable — nothing is marked replicated until a batch
is confirmed sent, so an outage or restart never loses data. This required replacing
`file_versions`' synthetic `UUID` primary key with a real `INTEGER PRIMARY KEY AUTOINCREMENT`
`seq` column (immune to the row-number reuse a bare SQLite `rowid` allows after a failed job's
rows are purged) and its natural `(job_id, object_id)` identity for external consumers.

## 2026-07-02 — Backup job completion verification

`bwfs` no longer treats a job as finished just because its streams closed. Added a `BackupCommit`
RPC: after a backup run's streams close, `brfs` submits a hash of the files it believes it sent,
and `bwfs` independently recomputes the same hash from its own catalog before marking the job
`success` — a mismatch marks it `failure` and purges that job's incomplete catalog entries. A
background watchdog now fails jobs that go silent past a configurable timeout (`JobTimeoutSec`,
default 30s), and `bwfs` reconciles any jobs left `in_progress` by an unclean shutdown on restart.
`backup_jobs` gained an explicit `status` column (`in_progress`/`success`/`failure`) as the source
of truth for job outcome.
