# Handler Completion & SQLite Concurrency Fix

**Date:** 2026-06-29  
**Status:** Approved

## Problem

Three observable bugs with a shared root cause:

1. `SQLITE_BUSY` errors under concurrent gRPC streams — multiple goroutines contend on SQLite writes
2. `CreateFileData` and `CreateFileVersion` are never called in the handler — deduplication never fires, the backup catalog is never populated
3. `brfs` logs an empty file hash for already-known files — symptom of `fileWritten` never being reached on the skip path

## Scope

Changes to three files only:
- `src/storage/filesystem/db.go` — SQLite connection settings
- `src/storage/filesystem/store.go` — exclusive process lock
- `src/cmd/bwfs/handler.go` — wire `CreateFileData` and `CreateFileVersion` into the write path

No changes to the storage interface, models, `brfs`, or any other file.

## Design

### 1. SQLite connection fix (`db.go`)

Two additions after `sql.Open`:

```go
sqlDB.SetMaxOpenConns(1)
```

And append `?_busy_timeout=5000` to the DSN path so SQLite waits up to 5 seconds before returning BUSY instead of failing immediately.

**Why `SetMaxOpenConns(1)`:** All gRPC stream goroutines share one process. With a single connection, writes serialize cleanly through the connection pool queue. The pool blocks callers instead of them racing on the SQLite write lock.

**Why `_busy_timeout`:** Defense-in-depth. If a second process somehow opens the same DB (before the lockfile check below), SQLite retries for 5 seconds rather than returning an immediate error.

**Performance:** Not a bottleneck. The hot path (`ChunkExists`) uses `os.Stat` only — no DB query. All DB writes are small row inserts. SQLite WAL handles ~50k–100k simple inserts/second; chunk I/O will dominate by orders of magnitude.

### 2. Exclusive process lock (`store.go`)

`bwfs` is a single-process server. A second `bwfs` opening the same store directory would corrupt the backup catalog through concurrent writes. On startup, `New()` acquires an exclusive non-blocking `flock` on `<basePath>/metadata.lock`.

```go
// store.go: Store gains a lockFile field
type Store struct {
    basePath string
    db       *gorm.DB
    lockFile *os.File
}
```

In `New()`:
1. Create/open `<basePath>/metadata.lock`
2. Call `syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)`
3. If it returns `syscall.EWOULDBLOCK` — another `bwfs` holds the lock; return a clear error
4. Store the `*os.File` in `Store` to keep the fd open (OS releases lock when fd closes)

In `Close()`:
```go
s.lockFile.Close() // releases flock automatically
```

**Platform note:** `bwfs` is Linux-only by design. `brfs` (the reader/client) supports Windows; `bwfs` (the server) does not. `flock` is the correct primitive here.

### 3. Handler write path (`handler.go`)

#### `handleFileInfoRequest` — two new call sites

**When the file is needed** (will be transferred, `needed == true`):

```go
if err := h.store.CreateFileData(h.currentFile.ID(), h.currentFile.Size()); err != nil {
    return fmt.Errorf("create file data: %w", err)
}
```

Called after `needed` is determined, before sending the response. Creates the incomplete `FileDataRecord` row that `FinalizeFileData` will later update with checksum and chunk count.

`CreateFileData` is only reached when `FileDataExists` returned false — meaning no finalized record exists for this `fileID`. The two branches are mutually exclusive, so no duplicate `FileDataRecord` can be created for the same `fileID` in the same backup run.

**When the file is not needed** (already exists or non-file type, `needed == false`):

```go
if _, err := h.store.CreateFileVersion(
    h.currentFile.ID(),
    h.currentFile.ID(),
    h.currentFile.MetadataBlob(),
    h.currentFile.Ctime(),
); err != nil {
    return fmt.Errorf("create file version: %w", err)
}
```

Called before sending the response. Records that this object existed at backup time even though no data transfer occurred. This is what populates the backup catalog for already-deduplicated files.

#### `fileWritten` — one new call site

After `FinalizeFileData` succeeds:

```go
if _, err := h.store.CreateFileVersion(
    h.currentFile.ID(),
    h.currentFile.ID(),
    h.currentFile.MetadataBlob(),
    h.currentFile.Ctime(),
); err != nil {
    return fmt.Errorf("create file version: %w", err)
}
```

Records the backup event in the catalog after content is safely stored and finalized.

#### Why `objectID == fileID` here

`FileVersion.ObjectID` is the backup-client's identifier for the object (what file this was).  
`FileVersion.FileID` is the content identity (which `FileData` holds the bytes).  
In this system, `FileInfo.ID()` returns `fs://host:type:path:mtime` — it encodes both identity and content version in one string. So both fields get the same value. This is correct: if the file content changes, `mtime` changes, `ID()` changes, and a new `FileData` is created.

## Invariants After This Fix

- Every processed file produces exactly one `FileVersionRecord`
- Every `FileVersionRecord` has a corresponding `FileDataRecord` (either pre-existing finalized, or newly created and finalized in the same stream)
- `FileDataExists` correctly returns true on the second backup of an identical file, skipping all chunk transfer
- No gRPC stream returns `SQLITE_BUSY`; concurrent streams queue on the single DB connection
- Only one `bwfs` process can open a given store directory at a time

## Testing

Existing 21 tests in `store_test.go` remain green — no interface or model changes.

New tests to add in `store_test.go` or a handler test file:
- Lock: second `New()` on same directory returns an error
- Handler integration (if feasible without full gRPC setup): after processing a file, `FileVersionRecord` exists; after processing the same file twice, second run skips chunks but still creates a `FileVersionRecord`
