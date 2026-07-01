# Transactional Chunk Cleanup (MarkChunkCorrupted + Vacuum) — Design Spec

Date: 2026-07-01

## Overview

Two operations in `src/storage/filesystem/` — `MarkChunkCorrupted` (chunks.go) and `Vacuum`
(info.go) — each issue multiple sequential, unwrapped SQL statements to delete related rows
across `ChunkRecord`, `FileDataChunkRecord`, and `FileDataRecord`. Because `bwfs` handles each
gRPC stream in its own goroutine against a shared `Store`, these multi-statement sequences can
interleave with a concurrent backup stream's writes, producing silent, permanent data loss.

This spec covers wrapping both operations' DB steps in single SQLite transactions, and wiring
`Vacuum` into `bwfs server` startup so it actually runs (today it has no production caller).

Priorities: **correctness first, minimal surface area second.**

---

## 1. The Race (why this matters)

`MarkChunkCorrupted(hash)` today:

```
1. os.Remove(chunk file)                                  -- filesystem, not transactional
2. Find FileDataChunkRecord WHERE chunk_hash = hash        -- read
3. Delete FileDataChunkRecord WHERE chunk_hash = hash      -- write (blanket, by hash)
4. Delete ChunkRecord WHERE hash = hash                    -- write
5. Delete FileDataRecord WHERE file_id IN (ids from step 2) -- write
```

Steps 2–5 are four separate statements, each committed independently. If a concurrent backup
stream uploads a different file (`fileC`) whose content happens to produce the same chunk hash
(a plausible dedup match — e.g. a common config file backed up by two jobs at once) and calls
`LinkChunkToFileData(hash, fileC, idx)` in the window between steps 2 and 3:

- Step 3's blanket delete removes `fileC`'s brand-new link along with the original file's.
- Step 5 only invalidates the file IDs captured in step 2 — `fileC` isn't among them, since its
  link didn't exist yet when step 2 ran.

Result: `fileC`'s `FileDataRecord` stays valid (so `FileDataExists` returns true and the next
backup skips it forever), but its chunk link is gone. A future restore of `fileC` reads zero
chunks and silently produces a truncated/empty file. Nothing re-triggers self-healing, because
no chunk read ever fails for a file with no linked chunks — this is silent and permanent, not a
transient glitch that a later verify run fixes on its own.

`Vacuum` has an analogous 4-step DB cascade (info.go steps 1–4) that could race the same way
against `handler.go`'s `fileWritten()` (which calls `FinalizeFileData` then `CreateFileVersion`
as two separate statements) — today this isn't reachable in production because `Vacuum` has no
CLI caller. Wiring it into server startup (Section 3) makes it reachable, so it needs the same
fix even though the startup call site itself won't race (Section 3 explains why).

---

## 2. Fix: Wrap Each Cascade in One Transaction

### `MarkChunkCorrupted` (`src/storage/filesystem/chunks.go`)

```go
func (s *Store) MarkChunkCorrupted(chunkHash []byte) error {
	hexHash := hex.EncodeToString(chunkHash)

	path := s.chunkPath(hexHash)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove corrupted chunk file: %w", err)
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var links []FileDataChunkRecord
		if err := tx.Where("chunk_hash = ?", hexHash).Find(&links).Error; err != nil {
			return fmt.Errorf("find files depending on chunk: %w", err)
		}
		if err := tx.Where("chunk_hash = ?", hexHash).Delete(&FileDataChunkRecord{}).Error; err != nil {
			return fmt.Errorf("remove chunk links: %w", err)
		}
		if err := tx.Where("hash = ?", hexHash).Delete(&ChunkRecord{}).Error; err != nil {
			return fmt.Errorf("remove chunk record: %w", err)
		}
		fileIDs := make([]string, len(links))
		for i, link := range links {
			fileIDs[i] = link.FileID
		}
		if len(fileIDs) > 0 {
			if err := tx.Where("file_id IN ?", fileIDs).Delete(&FileDataRecord{}).Error; err != nil {
				return fmt.Errorf("invalidate dependent file data: %w", err)
			}
		}
		return nil
	})
}
```

The disk `os.Remove` stays outside/before the transaction — filesystem operations can't
participate in a SQL transaction, and removing the file first preserves today's behavior for
the narrower filesystem-level race (a concurrent `StoreChunk` that finds the file already gone
will rewrite it with real data, which is correct self-healing, not a bug).

SQLite guarantees mutual exclusion between write transactions regardless of which `*sql.DB`
connection issues them (WAL mode: one writer at a time, `_busy_timeout=5000` retries on
contention). Since `bwfs server`'s `restoreStore` and `backupServer.store` are separate
`Store`/connection instances over the same file (`main.go` creates them independently via
`wfs.New` and `wfs.NewReadOnly`), it's this SQLite-level write-transaction guarantee — not
Go's `MaxOpenConns(1)` alone — that makes the transaction wrap sufficient to close the race
regardless of which connection each side uses.

### `Vacuum` (`src/storage/filesystem/info.go`)

Same treatment: steps 1–4 (delete incomplete FileData, delete orphaned FileData, delete
orphaned chunk links, delete orphaned chunk records) move inside one `s.db.Transaction(...)`,
using `tx` for each `Where(...).Delete(...)` call. Step 5 (walking `chunks/` on disk and
removing files with no matching `ChunkRecord`) stays outside the transaction, executed after
it commits — it already only acts on already-committed DB state, so this doesn't change its
behavior.

`VacuumResult` field population (`result.OrphanedFileDataRemoved = res.RowsAffected`, etc.)
moves to read `RowsAffected` from calls made against `tx` instead of `s.db`; the result struct
itself is unchanged.

---

## 3. Wire Vacuum into `bwfs server` Startup

In `src/cmd/bwfs/main.go`, in the `"server"` case, immediately after `NewBackupServer` succeeds
(store opened, exclusive flock held) and **before** `connection.StartServer(...)` is called:

```go
backupServer, err := NewBackupServer(ctx, logger, arguments.StoragePath)
if err != nil {
	logger.Error("Server initialization failed", "error", err)
	os.Exit(1)
}
defer backupServer.store.Close()

vacuumResult, err := backupServer.store.Vacuum()
if err != nil {
	logger.Error("Startup vacuum failed", "error", err)
	os.Exit(1)
}
logger.Info("Startup vacuum completed",
	"orphaned_file_data_removed", vacuumResult.OrphanedFileDataRemoved,
	"orphaned_chunk_links_removed", vacuumResult.OrphanedChunkLinksRemoved,
	"orphaned_chunks_removed", vacuumResult.OrphanedChunksRemoved,
	"incomplete_file_data_removed", vacuumResult.IncompleteFileData,
	"bytes_reclaimed", vacuumResult.BytesReclaimed,
)

listStore, err := wfs.NewReadOnly(arguments.StoragePath)
// ... unchanged from here
```

This call runs synchronously, before `listStore`/`restoreStore` are opened and before
`connection.StartServer` opens the gRPC listener. No backup/restore/list traffic can exist at
this point, so `Vacuum`'s cascade cannot race `handler.go`'s `FinalizeFileData`→
`CreateFileVersion` sequence at this call site — that race is structurally impossible here, not
merely made unlikely. The transaction wrap from Section 2 still matters for correctness (atomic
cleanup, crash-safety) and for any future caller that might run vacuum while the server is live
(e.g. an operator-triggered CLI command) — not implemented now.

A `Vacuum` failure is treated the same as any other store-initialization failure already in
this function (`NewBackupServer` erroring, `wfs.NewReadOnly` erroring): log and `os.Exit(1)`.
The server does not start serving on a store it couldn't clean up.

---

## 4. Files Changed

| Path | Change |
|------|--------|
| `src/storage/filesystem/chunks.go` | `MarkChunkCorrupted` wraps its DB steps in `s.db.Transaction(...)` |
| `src/storage/filesystem/info.go` | `Vacuum` wraps steps 1–4 in `s.db.Transaction(...)`; step 5 unchanged, runs after commit |
| `src/cmd/bwfs/main.go` | Call `backupServer.store.Vacuum()` synchronously after store init, before `connection.StartServer`; log result; exit 1 on error |
| `src/storage/filesystem/store_test.go` | Existing `Vacuum` tests continue to pass under the transaction wrap; new concurrency stress test for `MarkChunkCorrupted` |
| `docs/protocols/backup.md` | Extend existing corruption-recovery section with a note that the healing operation is transactional |
| `docs/components/bwfs.md` | Document startup vacuum behavior (runs once, synchronously, before the server accepts connections; failure is fatal) |

No proto changes, no new CLI flags, no changes to `src/storage/interface.go` (both methods'
signatures are unchanged).

---

## 5. Testing

**Unit tests (`src/storage/filesystem/store_test.go`):**

1. Existing `TestMarkChunkCorrupted_*` and `Vacuum`-related tests must continue to pass
   unmodified — the transaction wrap changes internals, not observable behavior for the
   single-threaded case.
2. New concurrency stress test for `MarkChunkCorrupted`: set up a chunk shared by two files
   (`fileA`, already finalized; `fileB`, about to be linked). Run `MarkChunkCorrupted(hash)` in
   one goroutine concurrently with `LinkChunkToFileData(hash, fileB, 0)` +
   `FinalizeFileData(fileB, checksum)` in another, repeated across many iterations (e.g. 200) to
   force interleaving under the Go race detector (`go test -race`). Assert the invariant holds
   on every iteration: if `fileB`'s `FileDataRecord` exists and is finalized, its chunk link
   must also exist (i.e. never "finalized but linkless"). The two valid outcomes are: (a)
   `fileB`'s data raced ahead of the mark and its `FileDataRecord` was correctly invalidated
   too, or (b) `fileB`'s data landed entirely after the mark's transaction committed and stays
   valid with its link intact. The invalid outcome — finalized `FileDataRecord` with the link
   silently deleted out from under it — must never occur.

**No new e2e test.** This is an internal consistency/atomicity fix and a startup-logging
change; there's no new externally observable restore/backup behavior worth a docker-level
test. The existing `TestE2E_Backup_HealsCorruptedChunk` (already in the working tree) continues
to cover the end-to-end healing path this fix protects.

---

## Key Design Decisions

**Why a transaction instead of an application-level lock (e.g. a per-chunk-hash mutex)?**
`src/storage/CLAUDE.md` states the package's philosophy explicitly: rely on SQLite/GORM for
concurrency rather than complex application-level locking. A transaction is the idiomatic,
minimal-surface-area fix and requires no new synchronization primitive, no lock granularity
decisions, and no risk of deadlock between independent locks.

**Why not also transaction-protect the filesystem chunk-file removal?**
Filesystem operations can't join a SQL transaction. The existing reactive-healing design
(documented in `docs/protocols/backup.md`) already accepts that a chunk read failure — however
it arises — gets fixed by the next backup or verify run. The narrow disk-level TOCTOU (a
concurrent `StoreChunk` racing the `os.Remove`) self-corrects: if `StoreChunk` wins, it
rewrites real data to the path `MarkChunkCorrupted` is about to unlink from a chunk that no
longer needs healing; if `MarkChunkCorrupted` wins, `StoreChunk` sees the file missing and
writes it fresh. Neither outcome loses data. This is different in kind from the DB-only race
this spec fixes, which had no self-correcting path.

**Why run Vacuum synchronously before the listener starts, instead of in the background?**
Running before any client traffic exists makes the race with `fileWritten()` structurally
impossible rather than merely improbable — no new timing-dependent test or threshold tuning is
needed to reason about correctness. The cost is a startup delay proportional to store size,
which is acceptable for a maintenance step that only runs once per server process lifetime.

**Why exit 1 on vacuum failure instead of logging and continuing?**
Consistent with how `main.go` already treats every other store-initialization failure in this
function (`NewBackupServer` erroring, `wfs.NewReadOnly` erroring) as fatal. A vacuum failure
indicates the store is in an unexpected state; serving backups/restores against it without
understanding why cleanup failed is riskier than refusing to start.

**Why is `Vacuum`'s step 5 (disk file walk) left outside the transaction?**
It already only removes files with no matching `ChunkRecord`, checked against the
already-committed state after steps 1–4 commit. Moving it inside the transaction wouldn't add
correctness (filesystem removal isn't rollback-able anyway) and would hold the single SQLite
write lock for the duration of a full directory walk, which could be slow for large chunk
stores.
