# catalogsync

Replicates a `bwfs` node's `file_versions` records to a central backup catalog, asynchronously
and independently of the `bwfs` server's own availability. The catalog service itself does not
exist yet — this component ships against an abstract `Sender` interface, currently implemented by
a `LoggingSender` that logs each batch to prove the pipeline end-to-end.

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
from the beginning (`seq=0`) — safe, because the catalog is expected to treat `(job_id, object_id)`
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

## Building

```bash
make catalogsync
```

## See Also

- [bwfs](./bwfs.md) — the component whose `file_versions` table this replicates
- [Architecture](../ARCHITECTURE.md) — system overview
