# Store Transactional Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close a silent-data-loss race in `MarkChunkCorrupted` by wrapping its DB cascade in a transaction, apply the same fix to `Vacuum`, and wire `Vacuum` into `bwfs server` startup so it actually runs.

**Architecture:** `MarkChunkCorrupted` (src/storage/filesystem/chunks.go) and `Vacuum` (src/storage/filesystem/info.go) each currently issue several independent, unwrapped SQL delete statements. A concurrent write from another gRPC stream (different goroutine, same `bwfs` process) can interleave between those statements and silently orphan a file's chunk link without invalidating its `FileData`. Wrapping each cascade in a single `s.db.Transaction(...)` closes this, relying on SQLite's own write-transaction mutual exclusion (not app-level locking, per this package's stated philosophy). `Vacuum` is also wired into `bwfs server` startup, running synchronously before the gRPC listener opens so it can't race live traffic.

**Tech Stack:** Go, GORM (`gorm.io/gorm`), SQLite (`modernc.org/sqlite`), testify (`require`/`assert`).

## Global Constraints

- No changes to `src/storage/interface.go` — both `MarkChunkCorrupted` and `Vacuum` keep their existing signatures.
- No new dependencies — `gorm.io/gorm` is already a transitive import via `store.go`; only need to add it directly to files that reference `*gorm.DB`.
- Per `.claude/CLAUDE.md`: any feature-behavior change (the new startup vacuum behavior) must update `docs/components/bwfs.md`; any protocol/behavior doc affected must update `docs/protocols/backup.md`.
- Follow existing test conventions in `src/storage/filesystem/store_test.go` (`newTestStore(t)`, `makeChunk(t, data)` helpers) — do not introduce new test helpers.

---

### Task 1: Transaction-wrap `MarkChunkCorrupted` + regression test

**Files:**
- Modify: `src/storage/filesystem/chunks.go:70-103` (the `MarkChunkCorrupted` function and its imports)
- Modify: `src/storage/filesystem/store_test.go` (add imports, add new test)
- Modify: `docs/protocols/backup.md:35` (add transactional note to existing corruption-recovery bullet list)

**Interfaces:**
- Consumes: `Store.db *gorm.DB` (store.go:16), `Store.chunkPath(hexHash string) string` (chunks.go:17), existing models `FileDataChunkRecord{FileID, ChunkHash, Index}` and `FileDataRecord{UUID, FileID, Size, Checksum, ChunkCount, CreatedAt}` (models.go).
- Produces: `MarkChunkCorrupted(chunkHash []byte) error` — same signature as before, now atomic for its DB portion. No other task depends on new symbols from this task.

- [ ] **Step 1: Write the concurrency regression test**

Add to `src/storage/filesystem/store_test.go`. First update the import block to add `"fmt"` and `"sync"`:

```go
import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```

Then append this test at the end of the file:

```go
func TestMarkChunkCorrupted_ConcurrentWithNewLink_NoOrphanedFileData(t *testing.T) {
	store := newTestStore(t)

	const iterations = 20
	for i := 0; i < iterations; i++ {
		data := []byte(fmt.Sprintf("shared chunk payload iteration %d", i))
		hash := makeChunk(t, data)
		require.NoError(t, store.StoreChunk(hash, data))

		newFileID := fmt.Sprintf("file-new-%d", i)

		start := make(chan struct{})
		var wg sync.WaitGroup
		var linkErr error
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			_ = store.MarkChunkCorrupted(hash)
		}()

		go func() {
			defer wg.Done()
			<-start
			if err := store.CreateFileData(newFileID, int64(len(data))); err != nil {
				linkErr = err
				return
			}
			if err := store.LinkChunkToFileData(hash, newFileID, 0); err != nil {
				linkErr = err
				return
			}
			linkErr = store.FinalizeFileData(newFileID, []byte("checksum"))
		}()

		close(start)
		wg.Wait()
		require.NoError(t, linkErr, "iteration %d", i)

		exists, err := store.FileDataExists(newFileID)
		require.NoError(t, err)
		if exists {
			var count int64
			store.db.Model(&FileDataChunkRecord{}).Where("file_id = ?", newFileID).Count(&count)
			assert.Greater(t, count, int64(0),
				"iteration %d: %s has finalized FileData but no chunk link — silent data loss", i, newFileID)
		}
	}
}
```

This simulates the race: goroutine A marks a shared chunk corrupted while goroutine B concurrently links, and finalizes, a *different* file (`newFileID`) against that same chunk hash. The invariant checked after each iteration: if `newFileID` ends up with finalized `FileData`, it must still have its chunk link — never "finalized but linkless," which is the silent-data-loss bug.

- [ ] **Step 2: Run test to observe the pre-fix race**

Run: `cd src && go test ./storage/filesystem/... -run TestMarkChunkCorrupted_ConcurrentWithNewLink -count=20 -v`

Expected: at least one iteration across the 20 runs fails with an assertion message containing `"silent data loss"`, demonstrating the race in the current unfixed `MarkChunkCorrupted`. (This is a timing-dependent test — if all 20 runs pass, re-run with `-count=50`; the underlying bug is real regardless of whether a given run happens to catch it, since the fix in the next step makes the invariant hold unconditionally rather than probabilistically.)

- [ ] **Step 3: Wrap `MarkChunkCorrupted` in a transaction**

Replace the function in `src/storage/filesystem/chunks.go` (lines 70-103):

```go
// MarkChunkCorrupted removes a chunk that failed to read correctly (missing
// or otherwise unusable) along with every DB record that depends on it, so
// affected files are treated as needing a fresh upload on the next backup.
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

Add `"gorm.io/gorm"` to the import block at the top of `chunks.go` (it currently imports `"gorm.io/gorm/clause"` only):

```go
import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"

	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)
```

- [ ] **Step 4: Run the regression test to confirm it now passes consistently**

Run: `cd src && go test ./storage/filesystem/... -run TestMarkChunkCorrupted_ConcurrentWithNewLink -count=50 -v`

Expected: `PASS` on every one of the 50 runs — the transaction makes the invariant hold unconditionally.

- [ ] **Step 5: Run the full existing `MarkChunkCorrupted` test suite to confirm no regression**

Run: `cd src && go test ./storage/filesystem/... -run TestMarkChunkCorrupted -v`

Expected: `PASS` for `TestMarkChunkCorrupted_RemovesFileFromDiskIfPresent`, `TestMarkChunkCorrupted_TolerantOfAlreadyMissingFile`, `TestMarkChunkCorrupted_InvalidatesDependentFileData`, and the new `TestMarkChunkCorrupted_ConcurrentWithNewLink_NoOrphanedFileData`.

- [ ] **Step 6: Update the protocol doc**

In `docs/protocols/backup.md`, the corruption-recovery section currently reads (line 35):

```
- Any chunk read failure during restore or verify (`bwfs`'s `RestoreFile`, used by both `rwfs restore` and `rwfs verify`) marks that chunk corrupted: the chunk file is removed if still present, its DB records are deleted, and the `FileData` of every file that referenced it is invalidated
```

Change it to:

```
- Any chunk read failure during restore or verify (`bwfs`'s `RestoreFile`, used by both `rwfs restore` and `rwfs verify`) marks that chunk corrupted: the chunk file is removed if still present, its DB records are deleted, and the `FileData` of every file that referenced it is invalidated. The DB portion runs inside a single transaction, so a concurrent backup that links a new file to the same chunk hash can never lose that link without its `FileData` being invalidated too
```

- [ ] **Step 7: Commit**

```bash
git add src/storage/filesystem/chunks.go src/storage/filesystem/store_test.go docs/protocols/backup.md
git commit -m "fix(storage): wrap MarkChunkCorrupted DB cascade in a transaction

Closes a race where a concurrent backup linking a new file to the same
chunk hash could have its link silently deleted without its FileData
being invalidated, causing permanent silent data loss on restore."
```

---

### Task 2: Transaction-wrap `Vacuum`

**Files:**
- Modify: `src/storage/filesystem/info.go:39-97` (the `Vacuum` function and its imports)

**Interfaces:**
- Consumes: `Store.db *gorm.DB` (store.go:16), `Store.basePath` (store.go:15), models `FileDataRecord`, `FileDataChunkRecord`, `ChunkRecord`.
- Produces: `Vacuum() (*storage.VacuumResult, error)` — same signature as before. Task 3 calls this exact method on `backupServer.store`.

- [ ] **Step 1: Run existing `Vacuum` tests to confirm baseline**

Run: `cd src && go test ./storage/filesystem/... -run TestVacuum -v`

Expected: `PASS` for `TestVacuum_RemovesIncompleteFileData`, `TestVacuum_RemovesOrphanedChunkLinksForIncompleteFileData`, `TestVacuum_RemovesOrphanedChunkFiles`.

- [ ] **Step 2: Wrap the DB cascade (steps 1-4) in a transaction**

Replace `Vacuum` in `src/storage/filesystem/info.go` (lines 39-97):

```go
func (s *Store) Vacuum() (*storage.VacuumResult, error) {
	result := &storage.VacuumResult{}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Step 1: remove incomplete FileData older than threshold
		cutoff := time.Now().Add(-vacuumIncompleteThreshold)
		res := tx.Where("checksum IS NULL AND created_at < ?", cutoff).Delete(&FileDataRecord{})
		if res.Error != nil {
			return res.Error
		}
		result.IncompleteFileData = res.RowsAffected

		// Step 2: remove FileData with no FileVersion referencing them
		res = tx.Where("file_id NOT IN (SELECT object_id FROM file_version_records)").
			Where("checksum IS NOT NULL").
			Delete(&FileDataRecord{})
		if res.Error != nil {
			return res.Error
		}
		result.OrphanedFileDataRemoved = res.RowsAffected

		// Step 3: remove FileDataChunkRecord rows whose file_id no longer has
		// any FileDataRecord at all (a file_id can be shared by multiple
		// FileDataRecord attempts, so a chunk link is only safe to remove once
		// none of them remain).
		res = tx.Where("file_id NOT IN (SELECT file_id FROM file_data_records)").Delete(&FileDataChunkRecord{})
		if res.Error != nil {
			return res.Error
		}
		result.OrphanedChunkLinksRemoved = res.RowsAffected

		// Step 4: remove ChunkRecord rows with no FileDataChunkRecord referencing them
		res = tx.Where("hash NOT IN (SELECT chunk_hash FROM file_data_chunk_records)").Delete(&ChunkRecord{})
		if res.Error != nil {
			return res.Error
		}
		result.OrphanedChunksRemoved = res.RowsAffected

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Step 5: walk chunk files; delete any not in chunk_records (includes temp files)
	chunksRoot := filepath.Join(s.basePath, "chunks")
	filepath.WalkDir(chunksRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// Reconstruct hash from path: last three segments are [aa][bb][rest]
		rel, _ := filepath.Rel(chunksRoot, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 {
			// temp file or unexpected structure — delete
			info, statErr := d.Info()
			if statErr == nil {
				result.BytesReclaimed += info.Size()
			}
			os.Remove(path)
			return nil
		}
		hexHash := parts[0] + parts[1] + parts[2]

		var count int64
		s.db.Model(&ChunkRecord{}).Where("hash = ?", hexHash).Count(&count)
		if count == 0 {
			info, statErr := d.Info()
			if statErr == nil {
				result.BytesReclaimed += info.Size()
			}
			os.Remove(path)
		}
		return nil
	})

	return result, nil
}
```

Add `"gorm.io/gorm"` to the import block at the top of `info.go`:

```go
import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)
```

(Step 5's file-walk stays outside the transaction and unchanged — it already only acts on already-committed DB state.)

- [ ] **Step 3: Run the same tests again to confirm no regression**

Run: `cd src && go test ./storage/filesystem/... -run TestVacuum -v`

Expected: `PASS` for all three tests, same as Step 1.

- [ ] **Step 4: Commit**

```bash
git add src/storage/filesystem/info.go
git commit -m "fix(storage): wrap Vacuum DB cascade in a transaction

Same class of fix as MarkChunkCorrupted: the four sequential deletes
that clean up orphaned FileData/chunks/links are now atomic, closing
a latent race against concurrent backup writes now that Vacuum is
wired into server startup."
```

---

### Task 3: Wire `Vacuum` into `bwfs server` startup

**Files:**
- Modify: `src/cmd/bwfs/main.go:46-64` (the `"server"` case)
- Modify: `docs/components/bwfs.md:26-32` (server section)

**Interfaces:**
- Consumes: `backupServer.store storage.BackupStore` (server.go:21, field access is same-package), `storage.BackupStore.Vacuum() (*storage.VacuumResult, error)` (interface.go:48), `storage.VacuumResult{OrphanedFileDataRemoved, OrphanedChunkLinksRemoved, OrphanedChunksRemoved, BytesReclaimed, IncompleteFileData}` (interface.go:80-86).
- Produces: no new symbols — this task only adds a call site.

- [ ] **Step 1: Add the vacuum call to `main.go`**

In `src/cmd/bwfs/main.go`, the `"server"` case currently reads:

```go
	case "server":
		logger.Info("Backup writer started",
			"StoragePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)
		backupServer, err := NewBackupServer(ctx, logger, arguments.StoragePath)
		if err != nil {
			logger.Error("Server initialization failed", "error", err)
			os.Exit(1)
		}
		defer backupServer.store.Close()

		listStore, err := wfs.NewReadOnly(arguments.StoragePath)
```

Insert the vacuum call between `defer backupServer.store.Close()` and `listStore, err := ...`:

```go
	case "server":
		logger.Info("Backup writer started",
			"StoragePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)
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
```

This runs synchronously, before `listStore`/`restoreStore` are opened and before `connection.StartServer` opens the gRPC listener — no client traffic can exist yet, so this call site cannot race concurrent backup writes.

- [ ] **Step 2: Confirm it compiles**

Run: `cd src && go build ./...`

Expected: no output, exit code 0.

- [ ] **Step 3: Update the component doc**

In `docs/components/bwfs.md`, the `### server` section currently ends with the flags table (line 31) followed by a blank line and `### list`. Insert a new paragraph after the flags table and before `### list`:

```
On startup, before accepting connections, the server runs a vacuum pass over the store
(removes incomplete/orphaned `FileData`, orphaned chunk links, orphaned chunk records, and
orphaned chunk files) and logs the results. A vacuum failure is fatal — the server exits
rather than serving against a store it couldn't clean up.
```

- [ ] **Step 4: Commit**

```bash
git add src/cmd/bwfs/main.go docs/components/bwfs.md
git commit -m "feat(bwfs): run vacuum synchronously at server startup

Vacuum previously had no production caller. Running it once, before
the gRPC listener opens, cleans up orphaned store state on every
server start with no risk of racing live backup/restore traffic."
```

---

### Task 4: Full verification

**Files:** none (verification only — no code changes expected)

**Interfaces:** N/A

- [ ] **Step 1: Run the full unit test suite**

Run: `make test` (from repo root)

Expected: all tests pass, exit code 0.

- [ ] **Step 2: Run the e2e suite**

Run: `make test-e2e` (from repo root; requires Docker daemon running, ~3 min)

Expected: all tests pass, exit code 0, including `TestE2E_Backup_HealsCorruptedChunk` (confirms the now-transactional `MarkChunkCorrupted` still heals correctly end-to-end) and every other e2e test that spins up `bwfs server` (confirms the new startup vacuum call doesn't prevent the server from starting).

- [ ] **Step 3: If anything failed, fix and re-run**

If either command reports a failure, diagnose and fix it in the relevant task's files, then re-run **both** Step 1 and Step 2 from a clean state before considering the plan complete. Do not skip straight to committing a fix without re-running the full suite.

---

## Self-Review Notes

- **Spec coverage:** Section 1 (race analysis) → informs Task 1's test design. Section 2 (`MarkChunkCorrupted` fix) → Task 1. Section 2 (`Vacuum` fix) → Task 2. Section 3 (startup wiring) → Task 3. Section 4 (files changed table) → matches Tasks 1-3's file lists exactly. Section 5 (testing) → Task 1's stress test, Task 2/3's regression runs, Task 4's full-suite run. Docs (backup.md, bwfs.md) → Task 1 Step 6, Task 3 Step 3. All spec sections are covered.
- **Placeholder scan:** no TBD/TODO; every step has concrete code, exact file paths/line numbers, and exact commands with expected output.
- **Type consistency:** `MarkChunkCorrupted(chunkHash []byte) error` and `Vacuum() (*storage.VacuumResult, error)` signatures are unchanged and used identically across Tasks 1-3. `VacuumResult` field names in Task 3's logging call (`OrphanedFileDataRemoved`, `OrphanedChunkLinksRemoved`, `OrphanedChunksRemoved`, `IncompleteFileData`, `BytesReclaimed`) match `src/storage/interface.go:80-86` exactly.
