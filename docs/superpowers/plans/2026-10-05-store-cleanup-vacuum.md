# Store Cleanup and Vacuum Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Run cleanup (delete expired file versions) and vacuum (reclaim unreferenced data/chunks) regularly inside `bwfs server`, safely alongside live backups, and propagate deletions to the catalog.

**Architecture:** `Store` gains an operation guard (shared for backup-message handlers, exclusive per bounded GC batch), `CleanupExpired`, `VacuumOnline` and a `file_version_deletions` log written in the same transaction as every version delete. `bwfs` runs two interval loops. `catalogsync` drains the deletion log to a new `catalog.DeleteFileVersions` RPC.

**Tech Stack:** Go 1.26, GORM/SQLite, gRPC (`make proto`), testify.

**Spec:** `docs/superpowers/specs/2026-10-05-store-cleanup-vacuum-design.md`

## Global Constraints

- Run Go from `src/`. A cleanup/vacuum batch holds the exclusive guard only for one bounded transaction (`StoreGCBatchSize`, default 500).
- Cleanup deletes only versions with `expire_at IS NOT NULL AND expire_at <= now` whose job is not `in_progress`; NULL `expire_at` is never expired.
- Every version delete (cleanup, failed-job purge, stale-job purge) inserts `file_version_deletions` rows in the same transaction.
- Online vacuum never walks the chunk directory; incomplete `FileData` is orphaned only after `StoreIncompleteFileDataGraceSec`.
- Config keys and defaults exactly as in the spec table; interval `0` disables a loop.
- Docs per `.claude/CLAUDE.md`; commit trailers per the session reminder.

## Tasks

### Task 1: Store schema, deletion log, cleanup, online vacuum, guard
Files: `src/storage/filesystem/{models,db,backupjob,fileversion,gc,replicareader}.go` (+tests), `src/storage/interface.go`.
Interfaces produced: `FileVersionDeletionRecord{Seq,JobID,ObjectID,DeletedAt}`; `(*Store).BeginBackupOp() func()`; `CleanupExpired(ctx, now time.Time, batchSize int, dryRun bool) (*storage.CleanupResult, error)`; `VacuumOnline(ctx, batchSize int, incompleteGrace time.Duration) (*storage.VacuumResult, error)`; `PruneDeletionLog(ctx, olderThan time.Time) (int64, error)`; `(*ReplicaReader).FileVersionDeletionsSince(ctx, cursor int64, limit int) ([]FileVersionDeletionRecord, error)`; `storage.CleanupResult{VersionsExpired, DryRun bool}`.
- [ ] Failing tests for each behaviour in the spec's Testing/Store paragraph; run → FAIL.
- [ ] Add indexes (`expire_at`, `chunk_hash`) and the deletions table to `AutoMigrate`; implement the three deletion-recording sites via one helper; implement `CleanupExpired`, `VacuumOnline`, `PruneDeletionLog`, guard, `FileVersionDeletionsSince`; extend `BackupStore`.
- [ ] `go test ./storage/...` → PASS. Commit.

### Task 2: Config keys
Files: `src/common/config/config.go` (+test). Six keys and defaults from the spec; `StoreCleanupDryRun` parses `true`/`false`; negative numbers rejected.
- [ ] Failing tests (defaults + parsing + rejection) → implement → PASS → commit.

### Task 3: `bwfs` scheduler, handler guard, wiring
Files: `src/cmd/bwfs/{gc,handler,main}.go` (+`gc_test.go`, stress test in `integration_test.go`).
Interfaces: `type gcSettings struct{...}`; `runStoreGC(ctx, logger, store storage.BackupStore, s gcSettings)`.
- [ ] Failing tests: loops fire at their intervals, `0` disables, dry-run deletes nothing, one run's error doesn't stop the loop, shutdown stops; handler holds the guard per message; stress test (concurrent same-content backups vs back-to-back cleanup+vacuum, every committed version restorable).
- [ ] Implement; start `runStoreGC` after startup reconciliation in `main.go`; `handleRequest` wraps in `BeginBackupOp`.
- [ ] `go test ./cmd/bwfs/ && go test -tags integration ./cmd/bwfs/` → PASS. Commit.

### Task 4: Catalog `DeleteFileVersions`
Files: `src/api/catalog.proto` (+pb.go), `src/cmd/catalog/{server,authz}.go`, `src/storage/catalog/store.go` (+tests).
- [ ] Failing tests (delete scoped to peer store_node, idempotent, other nodes' rows untouched, authz `store` role, authz-matrix test updated) → implement → PASS → commit.

### Task 5: `catalogsync` deletion replication
Files: `src/cmd/catalogsync/{sync,sender,grpcsender,main}.go` (+tests).
- [ ] Failing tests (deletions drained after versions, second cursor persisted only after success, retry/backoff, empty log idle) → implement `Sender.SendDeletions`, reader method, `run` extension, cursor file → PASS → commit.

### Task 6: Docs, changelog, verification
- [ ] `docs/components/{bwfs,catalogsync,catalog}.md`, `docs/protocols/catalog-sync.md`, `docs/ARCHITECTURE.md` if needed, config key tables, CHANGELOG entry; fix the catalogsync "failed job purge" note.
- [ ] `make test`, `go test -tags integration ./cmd/bwfs/`, `make lint` (only the pre-existing `cmd/brfs` warning may remain). Commit.
