# Handler Completion & SQLite Concurrency Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix three related bugs: SQLITE_BUSY errors under concurrent streams, missing CreateFileData/CreateFileVersion calls in the handler, and empty file hashes logged by brfs for already-known files.

**Architecture:** Single-connection SQLite pool with busy timeout serializes writers safely. An exclusive flock on a lockfile prevents two bwfs processes from opening the same store. The handler gains three new call sites: CreateFileData when a file transfer begins, and CreateFileVersion on both the transfer-complete path and the already-known skip path.

**Tech Stack:** Go 1.26, `syscall.Flock` (Linux), `modernc.org/sqlite`, GORM, gRPC

## Global Constraints

- Go 1.26.0 — use `go1.26` in all `go` directives
- `CGO_ENABLED=0` must build cleanly throughout
- `modernc.org/sqlite` pure-Go driver only — no `mattn/go-sqlite3`
- `bwfs` is Linux-only; no Windows platform concerns in this plan
- No new dependencies — all needed packages already in `go.mod`
- Package for storage tests: `package filesystem` (internal — needed for `openDB` and `Store.db` access)
- Run all tests from `src/` directory: `go test ./...`

---

### Task 1: SQLite connection pool + busy timeout

**Files:**
- Modify: `src/storage/filesystem/db.go`

**Interfaces:**
- Produces: `openDB(basePath string) (*gorm.DB, error)` — unchanged signature, but now serializes writers and retries on BUSY

- [ ] **Step 1: Write the failing test**

Add to `src/storage/filesystem/store_test.go`:

```go
func TestConcurrentStores_NoSQLiteBusy(t *testing.T) {
	store := newTestStore(t)

	data := []byte("concurrent chunk data for busy test!!")
	hash := makeChunk(t, data)

	// Ten goroutines all writing chunks simultaneously — must not get SQLITE_BUSY
	const workers = 10
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			errs <- store.StoreChunk(hash, data)
		}()
	}
	for i := 0; i < workers; i++ {
		assert.NoError(t, <-errs)
	}
}
```

- [ ] **Step 2: Run test to verify it fails (or is flaky)**

```bash
cd src && go test ./storage/filesystem/ -run TestConcurrentStores_NoSQLiteBusy -v -count=5
```

Expected: occasional `SQLITE_BUSY` errors, or test passes by luck — the point is the current code has no protection.

- [ ] **Step 3: Implement the fix in `db.go`**

Change `openDB` to append `?_busy_timeout=5000` to the DSN and set `MaxOpenConns(1)`:

```go
func openDB(basePath string) (*gorm.DB, error) {
	dbPath := filepath.Join(basePath, "metadata.db") + "?_busy_timeout=5000"

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// One connection: all goroutines queue through the pool instead of
	// racing on the SQLite write lock and returning SQLITE_BUSY.
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	if err := db.AutoMigrate(
		&ChunkRecord{},
		&FileDataRecord{},
		&FileDataChunkRecord{},
		&FileVersionRecord{},
	); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("automigrate: %w", err)
	}
	return db, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd src && go test ./storage/filesystem/ -run TestConcurrentStores_NoSQLiteBusy -v -count=10
```

Expected: PASS all 10 runs, no SQLITE_BUSY.

- [ ] **Step 5: Run full test suite**

```bash
cd src && go test ./storage/filesystem/ -v
```

Expected: all 22 tests PASS.

- [ ] **Step 6: Commit**

```bash
git add src/storage/filesystem/db.go src/storage/filesystem/store_test.go
git commit -m "fix: set MaxOpenConns(1) and busy_timeout to prevent SQLITE_BUSY under concurrent streams"
```

---

### Task 2: Exclusive process lock via flock

**Files:**
- Modify: `src/storage/filesystem/store.go`

**Interfaces:**
- Produces: `New(basePath string) (*Store, error)` — now returns an error if another process holds the lock
- Produces: `(*Store).Close() error` — now also closes the lockfile fd (releasing the flock)

- [ ] **Step 1: Write the failing test**

Add to `src/storage/filesystem/store_test.go`:

```go
func TestNew_ExclusiveLock(t *testing.T) {
	dir := t.TempDir()

	store1, err := New(dir)
	require.NoError(t, err)
	defer store1.Close()

	// Second New on same dir must fail while first is open
	_, err = New(dir)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "already in use")
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd src && go test ./storage/filesystem/ -run TestNew_ExclusiveLock -v
```

Expected: FAIL — second `New` succeeds when it should error.

- [ ] **Step 3: Implement the lock in `store.go`**

```go
package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

type Store struct {
	basePath string
	db       *gorm.DB
	lockFile *os.File
}

func New(basePath string) (*Store, error) {
	chunksDir := filepath.Join(basePath, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return nil, fmt.Errorf("create chunks dir: %w", err)
	}

	lockFile, err := acquireLock(basePath)
	if err != nil {
		return nil, err
	}

	db, err := openDB(basePath)
	if err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("open db: %w", err)
	}

	return &Store{basePath: basePath, db: db, lockFile: lockFile}, nil
}

func acquireLock(basePath string) (*os.File, error) {
	lockPath := filepath.Join(basePath, "metadata.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("store at %s already in use by another process", basePath)
		}
		return nil, fmt.Errorf("acquire store lock: %w", err)
	}
	return f, nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	if err := sqlDB.Close(); err != nil {
		return err
	}
	return s.lockFile.Close()
}

var _ storage.BackupStore = (*Store)(nil)
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd src && go test ./storage/filesystem/ -run TestNew_ExclusiveLock -v
```

Expected: PASS.

- [ ] **Step 5: Run full test suite**

```bash
cd src && go test ./storage/filesystem/ -v
```

Expected: all 23 tests PASS.

- [ ] **Step 6: Verify CGO_ENABLED=0 build**

```bash
cd src && CGO_ENABLED=0 go build ./...
```

Expected: clean, no errors.

- [ ] **Step 7: Commit**

```bash
git add src/storage/filesystem/store.go src/storage/filesystem/store_test.go
git commit -m "feat: exclusive flock on metadata.lock prevents two bwfs processes opening same store"
```

---

### Task 3: Wire CreateFileData and CreateFileVersion into handler

**Files:**
- Modify: `src/cmd/bwfs/handler.go`

**Interfaces:**
- Consumes from storage interface (already exists):
  - `CreateFileData(fileID string, size int64) error`
  - `CreateFileVersion(objectID string, fileID string, metadata []byte, ctime int64) (versionID string, err error)`
- These are called on `h.store` which is `storage.BackupStore`

Note: there is no handler unit test file — the handler is tested via the running system. The test here is a build + integration check.

- [ ] **Step 1: Read the current handler to understand exact insertion points**

Read `src/cmd/bwfs/handler.go` lines 55–103 (`handleFileInfoRequest`) and lines 182–202 (`fileWritten`) before editing.

- [ ] **Step 2: Add CreateFileData call in handleFileInfoRequest (needed path)**

In `handleFileInfoRequest`, after `needed` is determined and before `if !needed { h.EOF = true }`, add the two branches. The full updated `handleFileInfoRequest` function body:

```go
func (h *streamHandler) handleFileInfoRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	fi := req.GetFileInfo()
	if fi == nil {
		return fmt.Errorf("FileRequest_FileInfo has empty FileInfo")
	}

	fileInfo, err := filesystem.DecodeFileInfo(fi.Attributes)
	if err != nil {
		return err
	}
	h.currentFile = fileInfo
	h.incrementalHasher = blake3.New()
	fileLogger := h.logger.With(slog.String("file_id", h.currentFile.ID()))
	fileLogger.Debug("Received file metadata", "file_info", fmt.Sprintf("%s", h.currentFile))

	fileExists, err := h.store.FileDataExists(h.currentFile.ID())
	if err != nil {
		return err
	}

	needed := !fileExists
	// Do not request transmission of non-file objects (dirs, symlinks, etc.)
	if h.currentFile.GetType() != 'f' {
		needed = false
	}
	// Empty files have no chunks to transfer
	if h.currentFile.Size() == 0 {
		needed = false
	}
	fileLogger.Debug("File existence check",
		"exists", fileExists,
		"needed", needed,
		"file_size", h.currentFile.Size(),
		"file_type", fmt.Sprintf("%c", h.currentFile.GetType()))

	if needed {
		// Create the incomplete FileData row; FinalizeFileData will complete it after all chunks arrive.
		if err := h.store.CreateFileData(h.currentFile.ID(), h.currentFile.Size()); err != nil {
			return fmt.Errorf("create file data: %w", err)
		}
	} else {
		// File already known or non-transferable — record it in the backup catalog now,
		// since fileWritten will not be called for this file.
		if _, err := h.store.CreateFileVersion(
			h.currentFile.ID(),
			h.currentFile.ID(),
			h.currentFile.MetadataBlob(),
			h.currentFile.Ctime(),
		); err != nil {
			return fmt.Errorf("create file version: %w", err)
		}
		h.EOF = true
	}

	response := &pb.FileResponse{
		ResponseType: &pb.FileResponse_FileNeeded{
			FileNeeded: &pb.FileNeeded{
				FileId: fi.FileId,
				Needed: needed,
			},
		},
	}
	return server.Send(response)
}
```

Note: the `if !needed { h.EOF = true }` block is now folded into the `else` branch above — remove the old standalone `if !needed { h.EOF = true }` line.

- [ ] **Step 3: Add CreateFileVersion call in fileWritten**

The full updated `fileWritten` function:

```go
func (h *streamHandler) fileWritten(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer) error {
	fileLogger := h.logger.With(slog.String("file_id", h.currentFile.ID()))
	fileHash := h.incrementalHasher.Sum(nil)
	if err := h.store.FinalizeFileData(h.currentFile.ID(), fileHash); err != nil {
		return fmt.Errorf("finalize file data: %w", err)
	}
	// Record this file in the backup catalog now that its content is safely stored.
	if _, err := h.store.CreateFileVersion(
		h.currentFile.ID(),
		h.currentFile.ID(),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
	); err != nil {
		return fmt.Errorf("create file version: %w", err)
	}
	fileLogger.Debug("File transfer completed", "fileHash", hex.EncodeToString(fileHash))
	message := server.Send(&pb.FileResponse{
		ResponseType: &pb.FileResponse_Result{
			Result: &pb.FileProcessingResult{
				FileId:  h.currentFile.ID(),
				Success: true,
				Hash:    fileHash,
			},
		},
	})
	h.incrementalHasher = nil
	h.currentFile = nil
	h.EOF = false
	return message
}
```

- [ ] **Step 4: Build to verify no compile errors**

```bash
cd src && CGO_ENABLED=0 go build ./...
```

Expected: clean build.

- [ ] **Step 5: Run full test suite**

```bash
cd src && go test ./storage/filesystem/ -v
```

Expected: all 23 tests PASS (no handler unit tests exist, but storage tests must stay green).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/bwfs/handler.go
git commit -m "feat: wire CreateFileData and CreateFileVersion into backup handler"
```

---

## Self-Review

**Spec coverage:**
- SQLite `SetMaxOpenConns(1)` + `_busy_timeout=5000` → Task 1 ✓
- Exclusive flock on `metadata.lock` → Task 2 ✓
- `CreateFileData` on needed path → Task 3 Step 2 ✓
- `CreateFileVersion` on skip path → Task 3 Step 2 ✓
- `CreateFileVersion` in `fileWritten` → Task 3 Step 3 ✓
- `objectID == fileID` rationale → covered in Task 3 Step 2 code comments ✓
- `CGO_ENABLED=0` build check → Task 2 Step 6 and Task 3 Step 4 ✓

**Placeholder scan:** No TBDs, no "similar to above", all steps have concrete code. ✓

**Type consistency:**
- `CreateFileData(fileID string, size int64) error` — matches `interface.go` line 16 ✓
- `CreateFileVersion(objectID, fileID string, metadata []byte, ctime int64) (string, error)` — matches `interface.go` line 27 ✓
- `h.currentFile.MetadataBlob() []byte` — defined in `fileinfo.go` line 60 ✓
- `h.currentFile.Ctime() int64` — defined in `fileinfo.go` line 85 ✓
- `h.currentFile.Size() int64` — defined in `fileinfo.go` line 69 ✓
