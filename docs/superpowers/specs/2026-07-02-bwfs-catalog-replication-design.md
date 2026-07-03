# bwfs Catalog Replication — Design

## Problem

Each `bwfs` node's SQLite database is the only record of what that node has backed up. There is no
central, cross-node view of backup history — the goal of a "backup catalog" per the project's core
goals (`README.md`: "Complete backup history tracking and reporting"). The catalog service itself
does not exist yet and will be designed/built separately. This spec covers only how a `bwfs` node's
`file_versions` rows get out to wherever the catalog ends up living, in a way that:

- Never affects `bwfs`'s own write path or availability.
- Tolerates the catalog being down for arbitrary periods without losing data or blocking `bwfs`.
- Stays simple and cheap given backup metadata's inherently low write rate.

## Goals

- A new standalone component, `catalogsync`, that reads a `bwfs` node's `file_versions` table and
  forwards new rows to a catalog, asynchronously and continuously.
- Full decoupling: `bwfs` has no awareness `catalogsync` exists (beyond an additive schema change);
  `catalogsync` has no effect on `bwfs` if it's slow, stalled, or crashed; the catalog being down
  only delays delivery, never loses data or blocks `bwfs`.
- `catalogsync` opens `bwfs`'s database strictly read-only at the driver level — a bug in
  `catalogsync` cannot corrupt or write to `bwfs`'s data.
- Simple, resumable delivery: a local cursor tracks replication progress; batches are retried with
  backoff until confirmed sent; nothing is marked "done" until the send is confirmed.

## Non-Goals

- The catalog service itself (storage, schema, ingestion API implementation) — future spec.
- The wire protocol / RPC contract `catalogsync` will eventually speak to a real catalog —
  deferred. `catalogsync` is built against an abstract `Sender` interface; this iteration's only
  implementation is a `LoggingSender` that logs batches and always succeeds, proving the pipeline
  end-to-end. A real gRPC client drops in later behind the same interface.
- Gating replication on the parent job's outcome. File versions replicate as soon as they're
  written, regardless of `backup_jobs.status`. If a job later fails, `bwfs` purges its own
  `file_versions` rows for that job (existing `FinalizeBackupJob`/`FailStaleInProgressJobs`
  behavior, unchanged) — but a batch already sent to the catalog may reference rows that no longer
  exist locally. Reconciling/cleaning up such entries is the catalog's responsibility, out of scope
  here.
- Coordinating multiple `bwfs` nodes. Each `bwfs` instance runs its own independent `catalogsync`
  instance; there is no cross-node coordination or shared state.
- Any change to `bwfs`'s request latency or throughput. `catalogsync` is purely additive.

## Architecture

### Schema Change (`src/storage/filesystem/models.go`)

```go
type FileVersionRecord struct {
    Seq       int64  `gorm:"primaryKey;autoIncrement"`
    ObjectID  string `gorm:"uniqueIndex:idx_job_object"`
    JobID     string `gorm:"uniqueIndex:idx_job_object"`
    Metadata  []byte
    Ctime     int64
    CreatedAt time.Time
}
```

`UUID` is removed. Two things replace what it used to provide, for two different consumers:

1. **`Seq`** — a real `INTEGER PRIMARY KEY AUTOINCREMENT` column, used *only* as `catalogsync`'s
   local polling cursor. It is never sent to the catalog and has no meaning outside this one
   `bwfs` instance.

   This must be a genuine `AUTOINCREMENT` column, not SQLite's bare implicit `rowid`. Without
   `AUTOINCREMENT`, SQLite assigns a new row `max(rowid)+1`, and if the row currently holding that
   max gets deleted (which happens routinely here — `FinalizeBackupJob`/`FailStaleInProgressJobs`
   delete `file_versions` rows for failed jobs), the next insert can reuse the deleted row's
   number. If `catalogsync`'s cursor had already advanced past a since-reused number, the new row
   at that number would be silently skipped forever. `AUTOINCREMENT` prevents this by tracking the
   historical high-water mark in SQLite's internal `sqlite_sequence` table, immune to deletes.

2. **`(JobID, ObjectID)`** — the record's external identity, both locally (already the existing
   unique index, `idx_job_object`) and in the catalog (the natural idempotency/dedup key a future
   ingest endpoint uses). `ObjectID` alone is not unique per row — an unchanged file re-observed
   across multiple backup runs produces the same `ObjectID` (`fs://host:type:path:mtime`) in
   multiple jobs by design (this is how cross-run, file-level dedup already works; see
   `LatestFileVersion`/`FileVersionAtTime`). `JobID` is what makes the pair unique: it's a
   `uuid.New()` generated per run by `brfs` (`cmd/brfs/main.go`), globally unique independent of
   hostnames.

Ripple effects from removing `UUID`, all included in this change:

- `storage.FileVersion` (`src/storage/interface.go`): drop `UUID`; add `JobID` (present on the
  record today but missing from this struct) so `(JobID, ObjectID)` is available to callers.
- `RemoveFileVersion(versionID string) error` → `RemoveFileVersion(jobID, objectID string) error`,
  deletes by the composite key. (Currently unused outside `store_test.go` — no other production
  call sites.)
- `EnsureFileVersion` no longer generates a UUID; drops the now-unused `github.com/google/uuid`
  import from `fileversion.go` (unrelated to `FileDataRecord.UUID` in `filedata.go`, untouched).
- `store_test.go` updated for the new `RemoveFileVersion` signature and the removed field.

### New Component: `catalogsync` (`src/cmd/catalogsync/`)

A new standalone binary, deployed colocated with each `bwfs` instance (it needs local filesystem
access to that instance's `metadata.db`). No subcommands — a single long-running daemon:

```
catalogsync <storage_path> [--debug]
```

**`ReplicaReader`** (new `src/storage/filesystem/replicareader.go`) — a small, dedicated,
genuinely read-only accessor. Deliberately *not* the existing `NewReadOnly`/`*Store`: that
constructor already has a real, different job — it skips the exclusive administrative flock so
`bwfs`'s own in-process `listStore`/`restoreStore` can run alongside the live writer, but it still
opens a normal read-write connection, because `restoreStore` needs to call
`MarkChunkCorrupted` when a chunk read fails during restore (`cmd/bwfs/restoreserver.go`). Reusing
it for `catalogsync` would give a separate *process* silent write access it should never have.
`ReplicaReader` instead opens `metadata.db` via SQLite's `?mode=ro` URI flag — real read-only
enforcement at the driver level — and exposes exactly one method:

```go
type ReplicaReader struct{ db *gorm.DB }

func OpenReplicaReader(basePath string) (*ReplicaReader, error)
func (r *ReplicaReader) FileVersionsSince(seq int64, limit int) ([]FileVersionRecord, error)
func (r *ReplicaReader) Close() error
```

`FileVersionsSince` runs `WHERE seq > ? ORDER BY seq LIMIT ?`. This is index-only (leading column
of the existing composite index) regardless of table size. SQLite's WAL mode is designed for
concurrent multi-process readers, so this coexists with `bwfs`'s own writer without lock
contention, provided each poll is a short, immediately-closed read rather than a long-lived
transaction (a long-lived reader snapshot would otherwise block `bwfs`'s WAL checkpoint).

**Cursor store** (new `src/cmd/catalogsync/cursor.go`) — a single plain-text integer, not a
structured file (there's only one value; a JSON envelope would be pure overhead):

```
<storage_path>/catalogsync.cursor    # decimal Seq, e.g. "12345\n"
```

Written via temp-file-then-rename after each successfully-sent batch, so a crash mid-write never
leaves a torn/partial cursor. If the file is missing (first run, or lost), `catalogsync` starts
from `seq=0` (full replay) — safe, since the catalog treats `(job_id, object_id)` as an idempotency
key for at-least-once delivery.

**`Sender` interface** (new `src/cmd/catalogsync/sender.go`):

```go
type Sender interface {
    Send(batch []filesystem.FileVersionRecord) error
}
```

This iteration's only implementation, `LoggingSender`, logs each batch (count + object IDs) and
always returns success. It makes `catalogsync` fully runnable end-to-end today. A real gRPC client
against the future catalog service replaces it later behind the same interface — no other part of
`catalogsync` changes when that happens.

**Poll loop** (new `src/cmd/catalogsync/sync.go`):

1. `batch := reader.FileVersionsSince(cursor, batchSize)`.
2. If `len(batch) == 0`: sleep `PollIntervalSec`, go to 1.
3. `err := sender.Send(batch)`.
   - Success: persist cursor as `batch[last].Seq`. If `len(batch) == batchSize` (there may be
     more backlog), go to 1 immediately without sleeping; otherwise sleep `PollIntervalSec` first.
   - Failure: sleep with exponential backoff (starting at 1s, doubling, capped at
     `MaxBackoffSec`), then poll again from the same, unadvanced cursor — the cursor never
     advances on failure, so a crash or restart mid-retry resumes from the last confirmed point
     with no gap. Because the cursor didn't move, the retry is guaranteed to re-include every row
     from the failed attempt, plus any newly-arrived rows if more were written during the backoff
     sleep — harmless (nothing skipped or lost), and it lets a retry absorb backlog growth instead
     of resending a stale, undersized batch first.

Graceful shutdown via `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)`,
the same pattern `brfs` already uses (`cmd/brfs/main.go`).

### Config (`src/common/config/config.go`)

New fields on the existing shared `Config` struct, following the current flat-struct/case-per-key
pattern (e.g. `JobTimeoutSec`):

| Key | Default | Meaning |
|-----|---------|---------|
| `CatalogSyncBatchSize` | 500 | Max rows per poll/send batch |
| `CatalogSyncPollIntervalSec` | 5 | Idle poll cadence |
| `CatalogSyncMaxBackoffSec` | 60 | Cap for retry backoff when `Sender.Send` fails |

## Data Flow

```
bwfs (unchanged write path):
  EnsureFileVersion(jobID, objectID, ...) → INSERT file_versions (seq autoincrement, never reused)

catalogsync (separate process, same host, same storage_path):
  loop:
    batch = ReplicaReader.FileVersionsSince(cursor, batchSize)   [read-only, WAL, short txn]
    if empty: sleep(pollInterval); continue
    err = Sender.Send(batch)                                    [LoggingSender today]
    if err == nil:
      cursor = batch[last].Seq; persist to catalogsync.cursor (temp+rename)
      if len(batch) == batchSize: continue immediately  # drain backlog
      else: sleep(pollInterval)
    else:
      sleep(backoff); backoff = min(backoff*2, maxBackoff)      # re-poll from same cursor, unadvanced
```

## Error Handling

- **Catalog/Sender unavailable**: exponential backoff, re-poll from the same unadvanced cursor
  indefinitely (guaranteed to re-include every row from the failed attempt, plus any newly
  arrived since). No data loss — `bwfs`'s own `file_versions` table already durably retains
  everything regardless of `catalogsync`'s state, so it doubles as the replication backlog for
  free; there is no separate buffer to bound or overflow.
- **`catalogsync` crash/restart**: resumes from the last persisted cursor. At-least-once delivery —
  a batch sent but crashed before the cursor was persisted gets resent; the catalog must treat
  `(job_id, object_id)` as an idempotency key.
- **Cursor file missing or corrupt**: treated as `seq=0`, triggering a full replay. Safe for the
  same idempotency reason; the tradeoff is a one-time large resend burst, accepted rather than
  adding a separate integrity check for a single-integer file.
- **A job whose file versions were already replicated later fails and gets purged locally**: the
  catalog ends up with an entry `bwfs` no longer has. Explicitly deferred to the catalog's own
  reconciliation logic (see Non-Goals) — not handled by `catalogsync`.
- **Long-lived read transactions blocking `bwfs`'s WAL checkpoint**: avoided by design — each poll
  opens, fetches one bounded batch, and closes immediately rather than holding a snapshot open.

## Testing

- Unit: inserting, deleting (simulating a failed job's purge), and inserting again around what
  would have been a reused `rowid` — proves `AUTOINCREMENT` prevents the skip-on-reuse hazard this
  design specifically guards against.
- Unit: `FileVersionsSince` pagination — respects `limit`, excludes `seq <= cursor`, orders
  ascending, returns fewer than `limit` when backlog is exhausted.
- Unit: `RemoveFileVersion(jobID, objectID)` deletes the targeted row only.
- Unit: cursor persistence — write/read roundtrip, atomic replace (no torn state on simulated
  crash-mid-write), missing-file fallback to 0.
- Unit: poll loop against a fake failing `Sender` — backoff increases on repeated failure, caps at
  `MaxBackoffSec`, resets after a subsequent success; cursor is untouched across failed attempts.
- Unit: `LoggingSender` receives exactly the batch handed to it.
- Integration: run `catalogsync` against a `bwfs` storage directory under active write load from a
  real `bwfs` server — confirms no lock contention/blocking in WAL mode and eventual full drain of
  every written row through to the `Sender`.

## Documentation Impact

Per `.claude/CLAUDE.md`, before merging:
- New `docs/components/catalogsync.md` (usage, config keys, `Sender`/cursor/backoff behavior).
- `README.md` — add `catalogsync` to the component list and documentation index.
- `docs/ARCHITECTURE.md` — add `catalogsync` to the component table and data-flow diagram, using
  the diagram's existing (currently unused) `planned` style — dashed edge — for the link to the
  not-yet-built catalog node.
- `CHANGELOG.md` — entry before merging to `main`.
