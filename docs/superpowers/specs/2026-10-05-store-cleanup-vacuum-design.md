# Design: Scheduled Store Cleanup and Vacuum (Retention, Part 3)

> Parts [1](2026-10-05-retention-policies-design.md) and [2](2026-10-05-retention-expiry-stamping-design.md)
> decide and record `expire_at` on every file version; nothing yet acts on it, so a `bwfs` store
> only ever grows. This part adds two regular background processes inside `bwfs server` —
> **cleanup** (delete expired file versions) and **vacuum** (reclaim file data and chunks no version
> references any more) — and propagates deletions to the catalog so the web UI never offers a
> version that no longer exists.

## Decisions

- **Both processes run inside `bwfs server`.** The server holds the store's exclusive lock, so an
  external scheduler (e.g. `agent` exec'ing a subcommand) cannot open the store while it runs.
- **Cleanup is frequent, vacuum is rare**, each with its own interval config key; `0` disables.
  Defaults: cleanup hourly, vacuum daily. Both are **on by default**; a `StoreCleanupDryRun` key
  makes cleanup log what it would delete without deleting. Versions with no `expire_at` (everything
  recorded before retention existed, and "never expire" rules) are never touched.
- **Pure `expire_at` semantics** (decided): there is no "keep the latest backup of each host"
  protection. A host that stops backing up loses everything once its retention passes.
- **Deletions replicate to the catalog** (decided) through a deletion log in `bwfs`, consumed by
  `catalogsync` and applied by a new `catalog` RPC. This also closes the previously documented gap
  where a *failed* job's purged versions stayed in the catalog.

## Why the existing `Vacuum()` cannot simply be scheduled

It was written for startup, when nothing is in flight. Run periodically it would corrupt live
backups:

1. **Dedup race.** `handleChunkHashRequest` finds a chunk file already on disk, then links it to
   the file as a second step. If vacuum removes that (just-orphaned) chunk between the two, the
   backup succeeds but references a missing chunk — exactly the case where cleanup orphans a chunk
   and the same content is backed up again.
2. **Known-file race.** The skip path checks `FileDataExists` then records a version; vacuum
   removing that file's `FileData` in between leaves an unrestorable version.
3. **Stored-but-unrecorded chunk.** `StoreChunk` renames the chunk file into place before inserting
   its record; a disk walk in that gap deletes a fresh chunk.
4. **Incomplete-file rule.** It deletes incomplete `FileData` older than 1 h; a large file in
   legitimate transfer can take longer.
5. **One long transaction** on a single-connection database blocks every backup operation.
6. **Scale.** The `chunk_hash` anti-join has no index; the disk walk issues one query per chunk.

## Concurrency: a store operation guard

`Store` gets an `opGuard sync.RWMutex`. Every backup-stream message handler runs under the shared
side (`BeginBackupOp()` in `handleRequest`); each cleanup/vacuum **batch** runs under the exclusive
side. Each invariant above lives inside a single handler call (chunk exists → link; file known →
record version; store chunk → record → link; finalize → record version), so excluding a batch from
every handler call closes all of them. Batches are bounded (`StoreGCBatchSize`, default 500) and the
guard is released between batches, so backups pause for milliseconds, never for a whole run. Files
in transfer between messages are protected by their own rows: incomplete `FileData` is never
considered orphaned, and its chunk links keep its chunks referenced.

## Cleanup

Per batch, under the exclusive guard and one transaction: select up to `StoreGCBatchSize` versions
with `expire_at IS NOT NULL AND expire_at <= now` whose job is **not `in_progress`** (a short
retention must never delete versions out from under a running job — `BackupCommit` would then see a
hash mismatch and fail it), insert a row per version into `file_version_deletions`, delete the
versions. Repeats until a batch comes back short or the context is cancelled. An index on
`expire_at` keeps each poll cheap. It then prunes `file_version_deletions` older than
`StoreDeletionLogRetentionSec` (default 30 days). Dry-run counts the candidates and logs them.

`FinalizeBackupJob(false)` and `FailStaleInProgressJobs` also write deletion-log rows for the
versions they purge, in the same transaction.

## Vacuum (online)

`VacuumOnline` runs the same four DB-driven steps as startup vacuum, but each in bounded batches
under the guard, and **without a disk walk**:

1. incomplete `FileData` older than `StoreIncompleteFileDataGraceSec` (default 24 h; at startup, with
   no live streams, the existing 1 h rule is unchanged);
2. finalized `FileData` no version references;
3. chunk links whose `file_id` has no `FileData`;
4. chunk records no link references — their files are removed directly (the hashes are known),
   `BytesReclaimed` summing the recorded sizes.

An index on `file_data_chunk_records.chunk_hash` makes step 4's anti-join cheap. After a run that
removed anything it issues `PRAGMA wal_checkpoint(TRUNCATE)` (best effort) so the WAL doesn't stay
large. The full chunk-directory walk (strays, crash-leftover `.tmp` files) stays startup-only.

## Catalog propagation

- `bwfs`: table `file_version_deletions(seq autoincrement, job_id, object_id, deleted_at)`;
  `ReplicaReader.FileVersionDeletionsSince(ctx, cursor, limit)`.
- `catalogsync`: a second cursor file (`catalogsync-deletions.cursor`); after each versions pass it
  drains deletions in batches via `Sender.SendDeletions`, persisting the cursor only after success
  (same at-least-once discipline as versions).
- `catalog`: `rpc DeleteFileVersions(DeleteVersionsRequest) returns (DeleteVersionsResponse)`
  (`store` role, like `SyncFileVersions`); deletes entries by `(store_node, job_id, object_id)`
  where `store_node` is the verified mTLS peer — idempotent, a missing entry is a no-op.
  Directory rows (`catalog_directories`) are not pruned: an emptied directory may linger in the
  catalog's directory list (known limitation).

## Configuration (`common/config`)

| Key | Default | Meaning |
|---|---|---|
| `StoreCleanupIntervalSec` | 3600 | how often expired versions are deleted; `0` disables |
| `StoreVacuumIntervalSec` | 86400 | how often unreferenced data/chunks are reclaimed; `0` disables |
| `StoreGCBatchSize` | 500 | rows per batch (bounds how long backups can be paused) |
| `StoreCleanupDryRun` | false | log what cleanup would delete, delete nothing |
| `StoreIncompleteFileDataGraceSec` | 86400 | age after which online vacuum treats incomplete file data as abandoned |
| `StoreDeletionLogRetentionSec` | 2592000 | how long `catalogsync` has to consume deletions before they're pruned |

The first run of each loop is one interval after startup (startup vacuum already ran).

## Error handling and observability

A failed run logs at Error and the loop continues on the next tick (never fatal after startup).
Each run is a job in the Jobs view (`cleanup:<host>:<unix>` / `vacuum:<host>:<unix>`): an
`event=start` line and an `event=finish` line carrying `status`, `duration` and the run's
statistics, or the error (see the bwfs component doc). Cleanup is a job only when it has something
to expire, so hourly no-op runs don't flood the list. Context cancellation (shutdown) stops between batches.

## Testing

Store: cleanup deletes only expired versions of non-`in_progress` jobs, leaves NULL/future
`expire_at`, batches correctly, records deletions, dry-run deletes nothing; failed-job purges record
deletions; online vacuum steps and batching, chunk files removed and bytes counted, incomplete
grace honored, results idempotent; the guard blocks a batch while a handler call holds the shared
side (and vice versa); deletion-log prune. `bwfs`: scheduler runs both loops at their intervals,
`0` disables, dry-run, shutdown, and a **stress test** of concurrent backups (same content,
unchanged files) against back-to-back cleanup+vacuum asserting every committed version restores.
`catalogsync`: deletion drain/cursor/retry. `catalog`: RPC, identity scoping, idempotency, authz.

## Out of scope

SQLite file compaction (`VACUUM INTO`/shrinking the DB file); pruning `backup_jobs` rows; pruning
emptied catalog directories; disk-pressure-triggered runs; protecting a host's last backup
(decided against); a CLI to run cleanup/vacuum on demand.
