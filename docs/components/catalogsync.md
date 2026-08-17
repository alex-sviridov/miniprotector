# catalogsync

Replicates a `bwfs` node's `file_versions` records to a central backup catalog, asynchronously
and independently of the `bwfs` server's own availability. `catalogsync` selects its `Sender` at
startup based on configuration: if `catalog_host` is unset in `local.conf`, it uses
`LoggingSender`, which logs each batch and always succeeds — an intentional no-op for deployments
without a `catalog` service. If `catalog_host` is set, it uses `GrpcSender`, a real mTLS gRPC
client against the [catalog](./catalog.md) service, even if the catalog isn't reachable yet: the
underlying gRPC connection is non-blocking and keeps retrying/reconnecting on its own, so a
catalog that's mid-restart (or comes up after `catalogsync` does) recovers automatically, without
needing `catalogsync` itself to restart. A `Send` failure is retried with backoff by the sync loop
and never advances the on-disk cursor, so no batch is ever marked delivered until the catalog
actually acknowledges it — `LoggingSender` is used only for the intentional "no catalog
configured" case, never as an error fallback, since a sender that fakes success would silently
drop that batch for good. `catalogsync` only fails to start over `catalog_host` if the mTLS
credentials in `certs_dir` can't be loaded (missing/corrupt files) — a real misconfiguration that
a restart won't fix either.

In any deployment using "storage"-typed policies (see [agent](./agent.md#storage-policy-supervision)),
`catalogsync` is started and supervised by `agent` alongside its paired `bwfs server` — one
independent ensure-running task per storage policy, crash-restarted on an unexpected exit and sent
`SIGTERM` (already handled gracefully via `signal.NotifyContext`, see `main.go`) when the policy is
edited or removed. It can also still be run directly, as described below.

## Usage

```
catalogsync <storage_path> [--debug]
```

`storage_path` must point at an existing `bwfs` storage directory (the same path passed to
`bwfs <storage_path> server`). `catalogsync` opens `metadata.db` inside it read-only — it never
writes to `bwfs`'s database, and can safely run alongside a live `bwfs server` process on the same
host.

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` | false | Enable debug logging |

## How It Works

`catalogsync` polls `file_versions` for rows newer than its own local cursor, in batches, and
hands each batch to a `Sender`:

1. Fetch up to `CatalogSyncBatchSize` rows with `seq` greater than the last replicated `seq`.
2. If the batch is empty, sleep `CatalogSyncPollIntervalSec` and poll again.
3. Otherwise, call `Sender.Send(batch)`.
   - On success: persist the new cursor, then immediately poll again (no sleep) if the batch was
     full-size — this drains a backlog quickly — otherwise sleep the normal poll interval.
   - On failure: sleep with exponential backoff (starting at 1s, capped at
     `CatalogSyncMaxBackoffSec`) and poll again from the same, unadvanced cursor. Since the cursor
     never moved, the retry is guaranteed to include every row from the failed attempt — it may
     also include newly-arrived rows if more were written during the backoff sleep, which is
     harmless (nothing is skipped or lost either way) and lets a retry absorb backlog growth
     instead of sending a stale, undersized batch first.

The cursor is a single integer stored in `<storage_path>/catalogsync.cursor`, written atomically
(temp file + rename) after each confirmed send. If it's missing or corrupt, `catalogsync` starts
from the beginning (`seq=0`) — safe, because the catalog is expected to treat `(store_node, job_id, object_id)`
as an idempotency key for the resulting at-least-once delivery.

`file_versions.seq` is a genuine `INTEGER PRIMARY KEY AUTOINCREMENT` column, distinct from the
record's external identity `(job_id, object_id)`. It exists purely as `catalogsync`'s local,
never-reused ordering key — SQLite's `AUTOINCREMENT` guarantees a deleted row's number (e.g. from
a failed job's `file_versions` purge) is never handed to a later row, which a bare `rowid` does
not guarantee.

**Note:** file versions replicate as soon as they're written, regardless of their parent job's
`backup_jobs.status`. If a job later fails, `bwfs` purges its local `file_versions` rows for that
job, but a batch already sent to the catalog may reference them — reconciling that is the
catalog's responsibility, not `catalogsync`'s.

## Configuration Keys

- `CatalogSyncBatchSize` — max rows per poll/send batch *(default: 500)*
- `CatalogSyncPollIntervalSec` — idle poll cadence in seconds *(default: 5)*
- `CatalogSyncMaxBackoffSec` — cap for retry backoff in seconds when a send fails *(default: 60)*
- `catalog_host` — hostname of the `catalog` service to send batches to; unset means `catalogsync`
  falls back to `LoggingSender`
- `catalog_port` — port to dial on `catalog_host` *(default: 15723)*

## Building

```bash
make catalogsync
```

## See Also

- [bwfs](./bwfs.md) — the component whose `file_versions` table this replicates
- [catalog](./catalog.md) — the service `catalogsync` replicates to
- [agent](./agent.md#storage-policy-supervision) — starts and supervises this process for a "storage"-typed policy
- [Catalog Sync Protocol](../protocols/catalog-sync.md)
- [Architecture](../ARCHITECTURE.md) — system overview
