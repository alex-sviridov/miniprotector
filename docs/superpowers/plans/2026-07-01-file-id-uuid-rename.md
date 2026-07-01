# file_id / file_uuid Naming Consistency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the naming collision where `file_data_id` sometimes means a UUID surrogate key and sometimes means the `fs://host:type:path:mtime` natural key, by consistently naming the natural key `file_id`/`FileID` and the UUID surrogate key `file_uuid`/`FileUUID` (Go: `UUID`) everywhere — models, storage layer, proto, gRPC consumers, CLI/JSON/log output, tests, and docs. Also fixes a real bug in `Vacuum()` uncovered by this rename: it plucked UUIDs but queried a natural-key column, so chunk-link rows were never actually cleaned up.

**Architecture:** Pure rename/refactor, no new abstractions. `FileDataRecord.ID` → `UUID` (col `uuid`); `FileDataChunkRecord.FileDataID` (misnamed — always held the natural key) → `FileID` (col `file_id`); `FileVersionRecord.ID` → `UUID` (col `uuid`), and `FileVersionRecord.FileID` is **removed** (redundant — every call site already passed the same value for `ObjectID` and `FileID`), leaving `ObjectID` as the sole natural-key reference so future non-file backup entity types aren't forced through file-specific naming. `list.proto`/`restore.proto` rename their `file_data_id` wire field to `file_uuid`; `backup.proto`'s `file_id` fields are untouched (already correct — natural key only, no UUID counterpart). Vacuum's incomplete/orphan-chunk-link cleanup is corrected to operate on `file_id`, and simplified to a single generic "delete chunk links whose file_id no longer has any FileDataRecord" pass instead of two ID-pluck-then-delete blocks that never matched.

**Tech Stack:** Go, GORM (SQLite), protoc/protoc-gen-go/protoc-gen-go-grpc via `make proto`, testify.

## Global Constraints

- No backward compatibility needed — rename directly, no migration shims, no dual-read of old column names.
- `make proto` regenerates `src/api/*.pb.go` from `src/api/*.proto` — never hand-edit generated files.
- Per `.claude/CLAUDE.md`: any proto change requires updating `docs/protocols/` and cross-linking from `README.md` / `docs/components/`. This plan's docs task covers the affected protocol docs (`list.md`, `restore.md`) and component docs (`bwfs.md`, `rwfs.md`); no README/ARCHITECTURE changes are needed since neither mentions these fields.
- Historical files under `docs/superpowers/plans/` and `docs/superpowers/specs/` are dated session artifacts, not living docs — leave them untouched.

---

### Task 1: Rename model fields and update the storage layer

**Files:**
- Modify: `src/storage/filesystem/models.go`
- Modify: `src/storage/filesystem/filedata.go`
- Modify: `src/storage/filesystem/chunks.go`
- Modify: `src/storage/filesystem/fileversion.go`
- Modify: `src/storage/interface.go`

**Interfaces:**
- Produces: `FileDataRecord.UUID`, `FileDataRecord.FileID` (unchanged), `FileDataChunkRecord.FileID`, `FileVersionRecord.UUID`, `FileVersionRecord.ObjectID` (unchanged, sole natural key — `FileID` removed), `storage.FileData.UUID`, `storage.FileVersion.UUID`/`ObjectID` (no `FileID`), `Store.CreateFileVersion(objectID string, metadata []byte, ctime int64) (string, error)` (drops the old `fileID` param).
- Consumed by: Tasks 2, 3, 4, 6, 7, 10.

- [ ] **Step 1: Update `models.go`**

```go
package filesystem

import "time"

type ChunkRecord struct {
	Hash      string `gorm:"primaryKey"`
	Size      int64
	CreatedAt time.Time
}

type FileDataRecord struct {
	UUID       string `gorm:"primaryKey"`
	FileID     string `gorm:"index"`
	Size       int64
	Checksum   []byte
	ChunkCount int
	CreatedAt  time.Time
}

type FileDataChunkRecord struct {
	FileID    string `gorm:"primaryKey"`
	ChunkHash string `gorm:"primaryKey"`
	Index     int64  `gorm:"primaryKey"`
}

type FileVersionRecord struct {
	UUID      string `gorm:"primaryKey"`
	ObjectID  string `gorm:"index"`
	Metadata  []byte
	Ctime     int64
	CreatedAt time.Time
}
```

- [ ] **Step 2: Update `filedata.go`** (`FileDataRecord.ID` → `.UUID`; `FileDataChunkRecord` queries move from `file_data_id` to `file_id`)

```go
package filesystem

import (
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

func (s *Store) FileDataExists(fileID string) (bool, error) {
	var record FileDataRecord
	err := s.db.
		Where("file_id = ? AND checksum IS NOT NULL", fileID).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CreateFileData(fileID string, size int64) error {
	record := FileDataRecord{
		UUID:      uuid.New().String(),
		FileID:    fileID,
		Size:      size,
		CreatedAt: time.Now(),
	}
	return s.db.Create(&record).Error
}

func (s *Store) FinalizeFileData(fileID string, checksum []byte) error {
	return s.db.Model(&FileDataRecord{}).
		Where("file_id = ? AND checksum IS NULL", fileID).
		Updates(map[string]any{
			"checksum": checksum,
			"chunk_count": s.db.Model(&FileDataChunkRecord{}).
				Where("file_id = ?", fileID).
				Select("count(*)"),
		}).Error
}

func (s *Store) FileData(fileID string) (*storage.FileData, error) {
	var record FileDataRecord
	err := s.db.
		Where("file_id = ? AND checksum IS NOT NULL", fileID).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("filedata not found: %s", fileID)
	}
	if err != nil {
		return nil, err
	}
	return &storage.FileData{
		UUID:       record.UUID,
		FileID:     record.FileID,
		Size:       record.Size,
		ChunkCount: record.ChunkCount,
		CreatedAt:  record.CreatedAt,
	}, nil
}

func (s *Store) FileDataChunks(fileID string) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		var links []FileDataChunkRecord
		err := s.db.
			Where("file_id = ?", fileID).
			Order("`index` ASC").
			Find(&links).Error
		if err != nil {
			yield(nil, err)
			return
		}
		for _, link := range links {
			decoded, err := hex.DecodeString(link.ChunkHash)
			if err != nil {
				yield(nil, fmt.Errorf("decode chunk hash: %w", err))
				return
			}
			if !yield(decoded, nil) {
				return
			}
		}
	}
}
```

- [ ] **Step 3: Update `chunks.go`** — only the `LinkChunkToFileData` struct literal changes

```go
func (s *Store) LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error {
	record := FileDataChunkRecord{
		FileID:    fileID,
		ChunkHash: hex.EncodeToString(chunkHash),
		Index:     index,
	}
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error
}
```

- [ ] **Step 4: Update `fileversion.go`** — drop the `fileID` param from `CreateFileVersion`, rename `ID`→`UUID`, drop `FileID` field entirely

```go
package filesystem

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

func (s *Store) CreateFileVersion(objectID string, metadata []byte, ctime int64) (string, error) {
	id := uuid.New().String()
	record := FileVersionRecord{
		UUID:      id,
		ObjectID:  objectID,
		Metadata:  metadata,
		Ctime:     ctime,
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(&record).Error; err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) RemoveFileVersion(versionID string) error {
	return s.db.Delete(&FileVersionRecord{}, "uuid = ?", versionID).Error
}

func (s *Store) LatestFileVersion(objectID string) (*storage.FileVersion, error) {
	var record FileVersionRecord
	err := s.db.
		Where("object_id = ?", objectID).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("file version not found: %s", objectID)
	}
	if err != nil {
		return nil, err
	}
	return toStorageFileVersion(&record), nil
}

func (s *Store) FileVersionAtTime(objectID string, timestamp time.Time) (*storage.FileVersion, error) {
	var record FileVersionRecord
	err := s.db.
		Where("object_id = ? AND created_at <= ?", objectID, timestamp).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("file version not found at time %v: %s", timestamp, objectID)
	}
	if err != nil {
		return nil, err
	}
	return toStorageFileVersion(&record), nil
}

func (s *Store) FileVersionsInPeriod(from, to time.Time) ([]*storage.FileVersion, error) {
	var records []FileVersionRecord
	err := s.db.
		Where("created_at BETWEEN ? AND ?", from, to).
		Order("created_at ASC").
		Find(&records).Error
	if err != nil {
		return nil, err
	}
	result := make([]*storage.FileVersion, len(records))
	for i, r := range records {
		result[i] = toStorageFileVersion(&r)
	}
	return result, nil
}

func toStorageFileVersion(r *FileVersionRecord) *storage.FileVersion {
	return &storage.FileVersion{
		UUID:      r.UUID,
		ObjectID:  r.ObjectID,
		Metadata:  r.Metadata,
		Ctime:     r.Ctime,
		CreatedAt: r.CreatedAt,
	}
}
```

- [ ] **Step 5: Update `src/storage/interface.go`** — mirror structs and the `BackupStore` interface signature

```go
// BackupStore represents contract for any backup storage
// Used by backup server to store file data and metadata incrementally
type BackupStore interface {
	// FileData operations - check if file content already exists (only returns true if complete)
	FileDataExists(fileID string) (exists bool, err error)
	CreateFileData(fileID string, size int64) error
	FinalizeFileData(fileID string, checksum []byte) error

	// Chunk operations - handle individual chunks as they arrive over network
	ChunkExists(chunkHash []byte) error
	StoreChunk(chunkHash []byte, data []byte) error
	LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error
	ReadChunk(chunkHash []byte) (data []byte, err error)

	// FileVersion operations - create metadata version for each backup
	CreateFileVersion(objectID string, metadata []byte, ctime int64) (versionID string, err error)
	RemoveFileVersion(versionID string) error

	// Query operations for restore
	LatestFileVersion(objectID string) (*FileVersion, error)
	FileVersionAtTime(objectID string, timestamp time.Time) (*FileVersion, error)
	FileVersionsInPeriod(from, to time.Time) ([]*FileVersion, error)
	FileData(fileID string) (*FileData, error)
	FileDataChunks(fileID string) iter.Seq2[[]byte, error] // Returns ordered chunk hashes

	// Storage information
	StoreInfo() (*StoreInfo, error)
	Close() error

	// Cleanup operations
	Vacuum() (*VacuumResult, error) // Remove orphaned FileData and Chunks
}

// FileData represents file content information (immutable once created)
type FileData struct {
	UUID       string
	FileID     string // Unique file identifier (e.g., host:path:mtime)
	Size       int64
	CRC32      uint32 // CRC32 checksum of entire file content
	ChunkCount int
	CreatedAt  time.Time
}

// FileVersion represents file metadata for a specific backup
type FileVersion struct {
	UUID      string
	ObjectID  string    // Natural key of the backed-up entity (file today; other entity types later)
	Metadata  []byte    // File attributes, permissions, etc.
	Ctime     int64     // File change time
	CreatedAt time.Time // When backup occurred
}
```

(`StoreInfo`/`VacuumResult` are touched in Task 2, not here.)

- [ ] **Step 6: Build to confirm no leftover references in this layer**

Run: `cd src && go build ./storage/... 2>&1 | head -50`
Expected: errors only in packages outside `storage/` (callers not yet updated — `cmd/bwfs`, `cmd/brfs` won't compile until later tasks). No errors inside `src/storage/`.

- [ ] **Step 7: Commit**

```bash
git add src/storage/filesystem/models.go src/storage/filesystem/filedata.go src/storage/filesystem/chunks.go src/storage/filesystem/fileversion.go src/storage/interface.go
git commit -m "refactor(storage): rename UUID/natural-key fields to UUID/FileID consistently"
```

---

### Task 2: Fix the Vacuum chunk-link bug and simplify

**Files:**
- Modify: `src/storage/filesystem/info.go`
- Modify: `src/storage/interface.go` (add one field to `VacuumResult`)
- Test: `src/storage/filesystem/store_test.go` (new test, added in this task since it's the one true behavior change; Task 4 handles the rest of that file's renames)

**Interfaces:**
- Consumes: `FileDataRecord`, `FileDataChunkRecord`, `FileVersionRecord` from Task 1.
- Produces: `storage.VacuumResult.OrphanedChunkLinksRemoved` (new field), corrected `Store.Vacuum()`.

**Bug being fixed:** the old code did `Pluck("id", &incompleteIDs)` (UUIDs) then `Where("file_data_id IN ?", incompleteIDs).Delete(&FileDataChunkRecord{})` — but `file_data_id` (now `file_id`) has only ever held the natural key, never a UUID, so that delete matched zero rows in both the "incomplete" and "orphaned" branches. Chunk-link rows were never cleaned up.

- [ ] **Step 1: Write the failing test against current (pre-fix) behavior**

Add to `src/storage/filesystem/store_test.go` (this test is written now but will only pass after Step 3's fix — Task 4 will later fold in the rest of this file's field renames, so for this step just add the new test function; don't touch the rest of the file yet):

```go
func TestVacuum_RemovesOrphanedChunkLinksForIncompleteFileData(t *testing.T) {
	store := newTestStore(t)

	data := []byte("chunk data linked to an incomplete file data record")
	hash := makeChunk(t, data)
	require.NoError(t, store.StoreChunk(hash, data))

	old := FileDataRecord{
		UUID:      uuid.New().String(),
		FileID:    "incomplete-file",
		Size:      100,
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	store.db.Create(&old)
	require.NoError(t, store.LinkChunkToFileData(hash, "incomplete-file", 0))

	var before int64
	store.db.Model(&FileDataChunkRecord{}).Where("file_id = ?", "incomplete-file").Count(&before)
	require.Equal(t, int64(1), before)

	_, err := store.Vacuum()
	require.NoError(t, err)

	var after int64
	store.db.Model(&FileDataChunkRecord{}).Where("file_id = ?", "incomplete-file").Count(&after)
	assert.Equal(t, int64(0), after)
}
```

Note: this references `FileDataRecord{UUID: ...}` which requires Task 1 to already be applied (it is, since tasks run in order).

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./storage/filesystem/... -run TestVacuum_RemovesOrphanedChunkLinksForIncompleteFileData -v`
Expected: FAIL — `after` is `1`, not `0` (the stale chunk link survives Vacuum).

- [ ] **Step 3: Add the new `VacuumResult` field**

In `src/storage/interface.go`, change:

```go
// VacuumResult provides feedback about cleanup operations
type VacuumResult struct {
	OrphanedFileDataRemoved int64 // FileData with no FileVersions
	OrphanedChunksRemoved   int64 // Chunks with no FileData references
	BytesReclaimed          int64 // Storage space freed
	IncompleteFileData      int64 // FileData with CRC32=0 (optional cleanup)
}
```

to:

```go
// VacuumResult provides feedback about cleanup operations
type VacuumResult struct {
	OrphanedFileDataRemoved  int64 // FileData with no FileVersions
	OrphanedChunkLinksRemoved int64 // FileDataChunkRecord rows with no FileDataRecord reference
	OrphanedChunksRemoved    int64 // Chunks with no FileData references
	BytesReclaimed           int64 // Storage space freed
	IncompleteFileData       int64 // FileData with CRC32=0 (optional cleanup)
}
```

- [ ] **Step 4: Rewrite `Vacuum()` in `info.go`**

Replace the whole function:

```go
func (s *Store) Vacuum() (*storage.VacuumResult, error) {
	result := &storage.VacuumResult{}

	// Step 1: remove incomplete FileData older than threshold
	cutoff := time.Now().Add(-vacuumIncompleteThreshold)
	res := s.db.Where("checksum IS NULL AND created_at < ?", cutoff).Delete(&FileDataRecord{})
	result.IncompleteFileData = res.RowsAffected

	// Step 2: remove FileData with no FileVersion referencing them
	res = s.db.Where("file_id NOT IN (SELECT object_id FROM file_version_records)").
		Where("checksum IS NOT NULL").
		Delete(&FileDataRecord{})
	result.OrphanedFileDataRemoved = res.RowsAffected

	// Step 3: remove FileDataChunkRecord rows whose file_id no longer has
	// any FileDataRecord at all (covers both rows deleted above — a file_id
	// can be shared by multiple FileDataRecord attempts, so a chunk link is
	// only safe to remove once none of them remain).
	res = s.db.Where("file_id NOT IN (SELECT file_id FROM file_data_records)").Delete(&FileDataChunkRecord{})
	result.OrphanedChunkLinksRemoved = res.RowsAffected

	// Step 4: remove ChunkRecord rows with no FileDataChunkRecord referencing them
	res = s.db.Where("hash NOT IN (SELECT chunk_hash FROM file_data_chunk_records)").Delete(&ChunkRecord{})
	result.OrphanedChunksRemoved = res.RowsAffected

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

This also removes the now-unused `incompleteIDs`/`orphanedFileDataIDs` `Pluck` calls entirely — deleting directly by condition is simpler and was the actual source of the bug (plucking the wrong column).

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd src && go test ./storage/filesystem/... -run TestVacuum_RemovesOrphanedChunkLinksForIncompleteFileData -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add src/storage/filesystem/info.go src/storage/interface.go src/storage/filesystem/store_test.go
git commit -m "fix(storage): Vacuum never cleaned up chunk links (queried uuid against file_id column)"
```

---

### Task 3: Update `bwfs` backup handler call sites

**Files:**
- Modify: `src/cmd/bwfs/handler.go`

**Interfaces:**
- Consumes: `Store.CreateFileVersion(objectID string, metadata []byte, ctime int64)` from Task 1.

- [ ] **Step 1: Update the two `CreateFileVersion` call sites**

In `handleFileInfoRequest` (skip-path branch), change:

```go
			if _, err := h.store.CreateFileVersion(
				h.currentFile.ID(),
				h.currentFile.ID(),
				h.currentFile.MetadataBlob(),
				h.currentFile.Ctime(),
			); err != nil {
```

to:

```go
			if _, err := h.store.CreateFileVersion(
				h.currentFile.ID(),
				h.currentFile.MetadataBlob(),
				h.currentFile.Ctime(),
			); err != nil {
```

In `fileWritten`, change:

```go
	if _, err := h.store.CreateFileVersion(
		h.currentFile.ID(),
		h.currentFile.ID(),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
	); err != nil {
```

to:

```go
	if _, err := h.store.CreateFileVersion(
		h.currentFile.ID(),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
	); err != nil {
```

Nothing else in this file changes — `log key "file_id"` here already correctly refers to the natural key (`h.currentFile.ID()`), and `pb.FileInfo/FileNeeded/FileProcessingResult.FileId` (backup.proto) are untouched by this rename.

- [ ] **Step 2: Build**

Run: `cd src && go build ./cmd/bwfs/... 2>&1 | head -50`
Expected: errors remaining only in `list.go`, `listserver.go`, `restoreserver.go` (not yet updated — Tasks 6–7) and in generated pb.go usage of `FileDataId` (not yet renamed — Task 5). `handler.go` itself must compile clean.

- [ ] **Step 3: Commit**

```bash
git add src/cmd/bwfs/handler.go
git commit -m "refactor(bwfs): drop redundant fileID arg from CreateFileVersion call sites"
```

---

### Task 4: Update `store_test.go` for the Task 1 renames

**Files:**
- Modify: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Consumes: renamed fields from Task 1, `CreateFileVersion(objectID, metadata, ctime)` signature.

- [ ] **Step 1: Update `TestFileVersionAtTime_ReturnsMostRecentBefore`**

Change:

```go
	old := FileVersionRecord{ID: uuid.New().String(), ObjectID: "obj-1", FileID: "file-old", Metadata: []byte("old"), Ctime: 1, CreatedAt: now.Add(-2 * time.Hour)}
	recent := FileVersionRecord{ID: uuid.New().String(), ObjectID: "obj-1", FileID: "file-recent", Metadata: []byte("recent"), Ctime: 2, CreatedAt: now.Add(-1 * time.Hour)}
	store.db.Create(&old)
	store.db.Create(&recent)

	v, err := store.FileVersionAtTime("obj-1", now.Add(-90*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "file-old", v.FileID)
```

to:

```go
	old := FileVersionRecord{UUID: uuid.New().String(), ObjectID: "obj-1", Metadata: []byte("old"), Ctime: 1, CreatedAt: now.Add(-2 * time.Hour)}
	recent := FileVersionRecord{UUID: uuid.New().String(), ObjectID: "obj-1", Metadata: []byte("recent"), Ctime: 2, CreatedAt: now.Add(-1 * time.Hour)}
	store.db.Create(&old)
	store.db.Create(&recent)

	v, err := store.FileVersionAtTime("obj-1", now.Add(-90*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, []byte("old"), v.Metadata)
```

- [ ] **Step 2: Update `TestFileVersionsInPeriod_ReturnsAll`**

Change:

```go
	r1 := FileVersionRecord{ID: uuid.New().String(), ObjectID: "obj-1", FileID: "f1", CreatedAt: now.Add(-3 * time.Hour)}
	r2 := FileVersionRecord{ID: uuid.New().String(), ObjectID: "obj-2", FileID: "f2", CreatedAt: now.Add(-1 * time.Hour)}
```

to:

```go
	r1 := FileVersionRecord{UUID: uuid.New().String(), ObjectID: "obj-1", CreatedAt: now.Add(-3 * time.Hour)}
	r2 := FileVersionRecord{UUID: uuid.New().String(), ObjectID: "obj-2", CreatedAt: now.Add(-1 * time.Hour)}
```

- [ ] **Step 3: Update `TestCreateFileVersion_ReturnsID`, `TestLatestFileVersion_ReturnsNewest`, `TestRemoveFileVersion_Removes`, `TestStoreInfo_CountsCorrectly`**

Change:

```go
func TestCreateFileVersion_ReturnsID(t *testing.T) {
	store := newTestStore(t)
	id, err := store.CreateFileVersion("obj-1", "file-1", []byte("meta"), 12345)
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestLatestFileVersion_ReturnsNewest(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreateFileVersion("obj-1", "file-1", []byte("meta-old"), 100)
	require.NoError(t, err)
	_, err = store.CreateFileVersion("obj-1", "file-2", []byte("meta-new"), 200)
	require.NoError(t, err)

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, "file-2", v.FileID)
	assert.Equal(t, []byte("meta-new"), v.Metadata)
}

func TestRemoveFileVersion_Removes(t *testing.T) {
	store := newTestStore(t)
	id, err := store.CreateFileVersion("obj-1", "file-1", []byte("meta"), 100)
	require.NoError(t, err)

	require.NoError(t, store.RemoveFileVersion(id))

	_, err = store.LatestFileVersion("obj-1")
	assert.Error(t, err)
}
```

to:

```go
func TestCreateFileVersion_ReturnsID(t *testing.T) {
	store := newTestStore(t)
	id, err := store.CreateFileVersion("obj-1", []byte("meta"), 12345)
	require.NoError(t, err)
	assert.NotEmpty(t, id)
}

func TestLatestFileVersion_ReturnsNewest(t *testing.T) {
	store := newTestStore(t)
	_, err := store.CreateFileVersion("obj-1", []byte("meta-old"), 100)
	require.NoError(t, err)
	_, err = store.CreateFileVersion("obj-1", []byte("meta-new"), 200)
	require.NoError(t, err)

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("meta-new"), v.Metadata)
	assert.Equal(t, int64(200), v.Ctime)
}

func TestRemoveFileVersion_Removes(t *testing.T) {
	store := newTestStore(t)
	id, err := store.CreateFileVersion("obj-1", []byte("meta"), 100)
	require.NoError(t, err)

	require.NoError(t, store.RemoveFileVersion(id))

	_, err = store.LatestFileVersion("obj-1")
	assert.Error(t, err)
}
```

And in `TestStoreInfo_CountsCorrectly`, change:

```go
	_, err := store.CreateFileVersion("obj-1", "file-1", []byte("meta"), 0)
```

to:

```go
	_, err := store.CreateFileVersion("obj-1", []byte("meta"), 0)
```

- [ ] **Step 4: Update `TestVacuum_RemovesIncompleteFileData`**

Change:

```go
	old := FileDataRecord{
		ID:        uuid.New().String(),
		FileID:    "incomplete-file",
		Size:      100,
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
```

to:

```go
	old := FileDataRecord{
		UUID:      uuid.New().String(),
		FileID:    "incomplete-file",
		Size:      100,
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
```

- [ ] **Step 5: Run the full package test suite**

Run: `cd src && go test ./storage/... -v 2>&1 | tail -80`
Expected: all tests PASS, including `TestVacuum_RemovesOrphanedChunkLinksForIncompleteFileData` from Task 2.

- [ ] **Step 6: Commit**

```bash
git add src/storage/filesystem/store_test.go
git commit -m "test(storage): update store_test.go for UUID/FileID rename and FileVersionRecord.FileID removal"
```

---

### Task 5: Rename the proto wire field and regenerate

**Files:**
- Modify: `src/api/list.proto`
- Modify: `src/api/restore.proto`
- Regenerate: `src/api/list.pb.go`, `src/api/restore.pb.go` (and their `_grpc.pb.go` siblings, via `make proto`)

**Interfaces:**
- Produces: `pb.FileRow.FileUuid` / `GetFileUuid()`, `pb.RestoreRequest.FileUuid` / `GetFileUuid()`.

- [ ] **Step 1: Update `list.proto`**

Change:

```proto
message FileRow {
  string file_data_id = 1;
```

to:

```proto
message FileRow {
  string file_uuid     = 1;
```

- [ ] **Step 2: Update `restore.proto`**

Change:

```proto
message RestoreRequest {
  string file_data_id = 1;
}
```

to:

```proto
message RestoreRequest {
  string file_uuid = 1;
}
```

`backup.proto` is NOT touched — its `file_id` fields (`FileInfo`, `FileNeeded`, `FileProcessingResult`) already correctly name the natural key with no UUID counterpart.

- [ ] **Step 3: Regenerate**

Run: `make proto`
Expected: `$(GREEN)Protobuf code generated in src/api/$(NC)` with no errors. Confirms `protoc`/`protoc-gen-go`/`protoc-gen-go-grpc` are on PATH (the `check-deps` target in the Makefile verifies `protoc`; if missing, install it before continuing).

- [ ] **Step 4: Verify the generated field name**

Run: `grep -n "FileUuid" src/api/list.pb.go src/api/restore.pb.go | head -10`
Expected: `FileUuid string` struct fields and `GetFileUuid()` methods present in both files (protoc-gen-go's default segment-capitalization of `file_uuid` yields `FileUuid`, not `FileUUID` — every calling-code reference in later tasks must use `FileUuid`/`GetFileUuid()` exactly).

- [ ] **Step 5: Commit**

```bash
git add src/api/list.proto src/api/restore.proto src/api/list.pb.go src/api/restore.pb.go src/api/list_grpc.pb.go src/api/restore_grpc.pb.go
git commit -m "refactor(api): rename file_data_id wire field to file_uuid in list/restore proto"
```

(Only add the `_grpc.pb.go` files if `make proto` actually rewrote them — service definitions didn't change, so they may be untouched; `git status` will show which files actually differ.)

---

### Task 6: Update `bwfs list.go` and `listserver.go`

**Files:**
- Modify: `src/cmd/bwfs/list.go`
- Modify: `src/cmd/bwfs/listserver.go`

**Interfaces:**
- Consumes: `pb.FileRow.FileUuid` (Task 5), `FileDataRecord.UUID`/`FileID` (Task 1), `FileVersionRecord.UUID`/`ObjectID` (Task 1).
- Produces: `listformat.Row.FileUUID` (consumed by Task 9).

- [ ] **Step 1: Update `queryResult` struct and `queryFileRows` in `list.go`**

Change:

```go
type queryResult struct {
	FileDataID string    `gorm:"column:file_data_id"`
	FileID     string    `gorm:"column:file_id"`
	Size       int64     `gorm:"column:size"`
	Chunks     int       `gorm:"column:chunks"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	Versions   int64     `gorm:"column:versions"`
}
```

to:

```go
type queryResult struct {
	FileUUID   string    `gorm:"column:uuid"`
	FileID     string    `gorm:"column:file_id"`
	Size       int64     `gorm:"column:size"`
	Chunks     int       `gorm:"column:chunks"`
	CreatedAt  time.Time `gorm:"column:created_at"`
	Versions   int64     `gorm:"column:versions"`
}
```

Change the query itself — `fd.id AS file_data_id` becomes `fd.uuid AS uuid`, the version-count join and count move from `fv.file_id`/`fv.id` to `fv.object_id`/`fv.uuid`:

```go
	query := store.RawDB().
		Table("file_data_records fd").
		Select("fd.id AS file_data_id, fd.file_id, fd.size, fd.chunk_count AS chunks, fd.created_at, COUNT(DISTINCT fv.id) AS versions").
		Joins("LEFT JOIN file_version_records fv ON fv.file_id = fd.file_id").
```

to:

```go
	query := store.RawDB().
		Table("file_data_records fd").
		Select("fd.uuid AS uuid, fd.file_id, fd.size, fd.chunk_count AS chunks, fd.created_at, COUNT(DISTINCT fv.uuid) AS versions").
		Joins("LEFT JOIN file_version_records fv ON fv.object_id = fd.file_id").
```

The rest of the query (`Where("fd.checksum IS NOT NULL")`, the `fd.created_at = (SELECT MAX...)` subquery, `Group("fd.file_id")`, `Order("fd.created_at ASC")`, the `serverName`/`filter` `fd.file_id LIKE` clauses) is unchanged — those all key off `FileDataRecord.FileID`, not the UUID.

Update the `Row` construction:

```go
		rows = append(rows, listformat.Row{
			FileDataID: r.FileDataID,
```

to:

```go
		rows = append(rows, listformat.Row{
			FileUUID:   r.FileUUID,
```

- [ ] **Step 2: Update `listserver.go`**

Change:

```go
		pbRows[i] = &pb.FileRow{
			FileDataId: r.FileDataID,
```

to:

```go
		pbRows[i] = &pb.FileRow{
			FileUuid:   r.FileUUID,
```

- [ ] **Step 3: Build**

Run: `cd src && go build ./cmd/bwfs/... 2>&1 | head -50`
Expected: errors remaining only in `restoreserver.go` (Task 7 — not yet updated) and any `_test.go` files not yet touched (Task 10). `list.go` and `listserver.go` themselves compile clean once `listformat.Row.FileUUID` exists (added in Task 9) — if Task 9 hasn't run yet, expect a `FileUUID undefined` error here; that's expected until Task 9 completes. Run tasks in the order given.

- [ ] **Step 4: Commit**

```bash
git add src/cmd/bwfs/list.go src/cmd/bwfs/listserver.go
git commit -m "refactor(bwfs): rename file_data_id to file_uuid in list query and gRPC response"
```

---

### Task 7: Update `bwfs restoreserver.go`

**Files:**
- Modify: `src/cmd/bwfs/restoreserver.go`

**Interfaces:**
- Consumes: `pb.RestoreRequest.FileUuid`/`GetFileUuid()` (Task 5), `FileDataRecord.UUID` column `uuid` (Task 1), `FileDataChunkRecord.FileID` column `file_id` (Task 1).

- [ ] **Step 1: Update `fileDataRow` struct and the two lookups**

Change:

```go
type fileDataRow struct {
	ID         string `gorm:"column:id"`
	FileID     string `gorm:"column:file_id"`
	Size       int64  `gorm:"column:size"`
	ChunkCount int    `gorm:"column:chunk_count"`
	Checksum   []byte `gorm:"column:checksum"`
}
```

to:

```go
type fileDataRow struct {
	UUID       string `gorm:"column:uuid"`
	FileID     string `gorm:"column:file_id"`
	Size       int64  `gorm:"column:size"`
	ChunkCount int    `gorm:"column:chunk_count"`
	Checksum   []byte `gorm:"column:checksum"`
}
```

Change the `RestoreFile` body:

```go
func (s *restoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	logger := s.logger.With("file_data_id", req.GetFileDataId())

	var fd fileDataRow
	err := s.store.RawDB().Table("file_data_records").
		Select("id, file_id, size, chunk_count, checksum").
		Where("id = ? AND checksum IS NOT NULL", req.GetFileDataId()).
		First(&fd).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status.Errorf(codes.NotFound, "file_data_id not found or unfinalized: %s", req.GetFileDataId())
		}
		return status.Errorf(codes.Internal, "db error looking up file_data_id: %v", err)
	}
```

to:

```go
func (s *restoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	logger := s.logger.With("file_uuid", req.GetFileUuid())

	var fd fileDataRow
	err := s.store.RawDB().Table("file_data_records").
		Select("uuid, file_id, size, chunk_count, checksum").
		Where("uuid = ? AND checksum IS NOT NULL", req.GetFileUuid()).
		First(&fd).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return status.Errorf(codes.NotFound, "file_uuid not found or unfinalized: %s", req.GetFileUuid())
		}
		return status.Errorf(codes.Internal, "db error looking up file_uuid: %v", err)
	}
```

And the chunk-link lookup:

```go
	var links []chunkLinkRow
	if err := s.store.RawDB().Table("file_data_chunk_records").
		Select("chunk_hash, `index`").
		Where("file_data_id = ?", fd.FileID).
```

to:

```go
	var links []chunkLinkRow
	if err := s.store.RawDB().Table("file_data_chunk_records").
		Select("chunk_hash, `index`").
		Where("file_id = ?", fd.FileID).
```

- [ ] **Step 2: Build**

Run: `cd src && go build ./cmd/bwfs/... 2>&1 | head -50`
Expected: `cmd/bwfs` package compiles clean now (assuming Task 6 and this task both applied); only `_test.go` files remain outstanding (Task 10).

- [ ] **Step 3: Commit**

```bash
git add src/cmd/bwfs/restoreserver.go
git commit -m "refactor(bwfs): rename file_data_id to file_uuid in RestoreFile lookup"
```

---

### Task 8: Update `rwfs list.go` and `verify.go`

**Files:**
- Modify: `src/cmd/rwfs/list.go`
- Modify: `src/cmd/rwfs/verify.go`

**Interfaces:**
- Consumes: `pb.FileRow.FileUuid`, `pb.RestoreRequest{FileUuid: ...}` (Task 5), `listformat.Row.FileUUID` (Task 9).

- [ ] **Step 1: Update `list.go`**

Change:

```go
		rows[i] = listformat.Row{
			FileDataID: r.FileDataId,
```

to:

```go
		rows[i] = listformat.Row{
			FileUUID:   r.FileUuid,
```

- [ ] **Step 2: Update `verify.go`** — rename the `fileDataID` field and every `file_data_id` log key / proto reference

Change the struct:

```go
type verifyResult struct {
	fileDataID string
	source     string
	path       string
	ok         bool
	reason     string
	chunkIndex int64
	size       int64
	chunkCount int32
}
```

to:

```go
type verifyResult struct {
	fileUUID   string
	source     string
	path       string
	ok         bool
	reason     string
	chunkIndex int64
	size       int64
	chunkCount int32
}
```

Change the two `logger.Info("verified"...)`/`logger.Warn(...)` blocks in `runVerify`:

```go
				logger.Info("verified",
					"source", result.source,
					"path", result.path,
					"file_data_id", result.fileDataID,
					"chunks", result.chunkCount,
					"size", result.size,
				)
```

to:

```go
				logger.Info("verified",
					"source", result.source,
					"path", result.path,
					"file_uuid", result.fileUUID,
					"chunks", result.chunkCount,
					"size", result.size,
				)
```

and:

```go
			attrs := []any{
				"source", result.source,
				"path", result.path,
				"file_data_id", result.fileDataID,
				"reason", result.reason,
			}
```

to:

```go
			attrs := []any{
				"source", result.source,
				"path", result.path,
				"file_uuid", result.fileUUID,
				"reason", result.reason,
			}
```

Change the retry-warning log in `verifyFileWithRetry`:

```go
			logger.Warn("stream error, retrying",
				"path", row.Path,
				"file_data_id", row.FileDataId,
				"attempt", attempt,
				"reason", result.reason,
			)
```

to:

```go
			logger.Warn("stream error, retrying",
				"path", row.Path,
				"file_uuid", row.FileUuid,
				"attempt", attempt,
				"reason", result.reason,
			)
```

Change `verifyFile`:

```go
func verifyFile(parent context.Context, client pb.RestoreServiceClient, row *pb.FileRow) verifyResult {
	base := verifyResult{
		fileDataID: row.FileDataId,
		source:     row.Source,
		path:       row.Path,
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	stream, err := client.RestoreFile(ctx, &pb.RestoreRequest{FileDataId: row.FileDataId})
```

to:

```go
func verifyFile(parent context.Context, client pb.RestoreServiceClient, row *pb.FileRow) verifyResult {
	base := verifyResult{
		fileUUID: row.FileUuid,
		source:   row.Source,
		path:     row.Path,
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	stream, err := client.RestoreFile(ctx, &pb.RestoreRequest{FileUuid: row.FileUuid})
```

- [ ] **Step 3: Build**

Run: `cd src && go build ./cmd/rwfs/... 2>&1 | head -50`
Expected: errors remaining only in `_test.go` files not yet touched (Task 10); `list.go` and `verify.go` compile clean once `listformat.Row.FileUUID` exists (Task 9).

- [ ] **Step 4: Commit**

```bash
git add src/cmd/rwfs/list.go src/cmd/rwfs/verify.go
git commit -m "refactor(rwfs): rename file_data_id to file_uuid in list/verify"
```

---

### Task 9: Update `listformat` package and `e2e/validate.go`

**Files:**
- Modify: `src/common/listformat/listformat.go`
- Modify: `src/common/listformat/listformat_test.go`
- Modify: `src/e2e/validate.go`

**Interfaces:**
- Produces: `listformat.Row.FileUUID` (consumed by Tasks 6, 8).

- [ ] **Step 1: Update `listformat.go`**

Change:

```go
type Row struct {
	FileDataID string
	Source     string
	Type       string
	Path       string
	Timestamp  int64
	Size       int64
	Chunks     int
	Versions   int64
	CreatedAt  time.Time
}

type jsonRow struct {
	FileDataID string `json:"file_data_id"`
	Source     string `json:"source"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	Timestamp  int64  `json:"timestamp"`
	Size       int64  `json:"size"`
	Chunks     int    `json:"chunks"`
	Versions   int64  `json:"versions"`
	CreatedAt  string `json:"created_at"`
}

func toJSONRows(rows []Row) []jsonRow {
	out := make([]jsonRow, len(rows))
	for i, r := range rows {
		out[i] = jsonRow{
			FileDataID: r.FileDataID,
			Source:     r.Source,
			Type:       r.Type,
			Path:       r.Path,
			Timestamp:  r.Timestamp,
			Size:       r.Size,
			Chunks:     r.Chunks,
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return out
}
```

to:

```go
type Row struct {
	FileUUID  string
	Source    string
	Type      string
	Path      string
	Timestamp int64
	Size      int64
	Chunks    int
	Versions  int64
	CreatedAt time.Time
}

type jsonRow struct {
	FileUUID  string `json:"file_uuid"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	Timestamp int64  `json:"timestamp"`
	Size      int64  `json:"size"`
	Chunks    int    `json:"chunks"`
	Versions  int64  `json:"versions"`
	CreatedAt string `json:"created_at"`
}

func toJSONRows(rows []Row) []jsonRow {
	out := make([]jsonRow, len(rows))
	for i, r := range rows {
		out[i] = jsonRow{
			FileUUID:  r.FileUUID,
			Source:    r.Source,
			Type:      r.Type,
			Path:      r.Path,
			Timestamp: r.Timestamp,
			Size:      r.Size,
			Chunks:    r.Chunks,
			Versions:  r.Versions,
			CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return out
}
```

- [ ] **Step 2: Update `listformat_test.go`**

Change:

```go
	rows := []Row{{
		FileDataID: "abc-123",
```

to:

```go
	rows := []Row{{
		FileUUID:  "abc-123",
```

Change:

```go
	assert.Contains(t, s, `"file_data_id": "abc-123"`)
```

to:

```go
	assert.Contains(t, s, `"file_uuid": "abc-123"`)
```

- [ ] **Step 3: Run listformat tests**

Run: `cd src && go test ./common/listformat/... -v`
Expected: PASS

- [ ] **Step 4: Update `e2e/validate.go`**

Change:

```go
type listRecord struct {
	FileDataID string `json:"file_data_id"`
	Source     string `json:"source"`
	Type       string `json:"type"`
	Path       string `json:"path"`
	Timestamp  int64  `json:"timestamp"`
	Size       int64  `json:"size"`
	Chunks     int    `json:"chunks"`
	Versions   int64  `json:"versions"`
	CreatedAt  string `json:"created_at"`
}
```

to:

```go
type listRecord struct {
	FileUUID  string `json:"file_uuid"`
	Source    string `json:"source"`
	Type      string `json:"type"`
	Path      string `json:"path"`
	Timestamp int64  `json:"timestamp"`
	Size      int64  `json:"size"`
	Chunks    int    `json:"chunks"`
	Versions  int64  `json:"versions"`
	CreatedAt string `json:"created_at"`
}
```

Change the checksum-lookup call site:

```go
		// Query the checksum stored by bwfs for this file_data_id
		stored := queryChecksum(t, db, rec.FileDataID)
```

to:

```go
		// Query the checksum stored by bwfs for this file_uuid
		stored := queryChecksum(t, db, rec.FileUUID)
```

Change `fileDataRecord` and `queryChecksum`:

```go
type fileDataRecord struct {
	ID       string `gorm:"column:id"`
	Checksum []byte `gorm:"column:checksum"`
}
```

to:

```go
type fileDataRecord struct {
	UUID     string `gorm:"column:uuid"`
	Checksum []byte `gorm:"column:checksum"`
}
```

```go
func queryChecksum(t *testing.T, db *gorm.DB, fileDataID string) uint32 {
	t.Helper()
	var rec fileDataRecord
	err := db.Table("file_data_records").
		Select("id, checksum").
		Where("id = ?", fileDataID).
		First(&rec).Error
	require.NoError(t, err, "failed to query checksum for file_data_id %s", fileDataID)
	require.Len(t, rec.Checksum, 4, "checksum should be 4 bytes")
	return binary.BigEndian.Uint32(rec.Checksum)
}
```

to:

```go
func queryChecksum(t *testing.T, db *gorm.DB, fileUUID string) uint32 {
	t.Helper()
	var rec fileDataRecord
	err := db.Table("file_data_records").
		Select("uuid, checksum").
		Where("uuid = ?", fileUUID).
		First(&rec).Error
	require.NoError(t, err, "failed to query checksum for file_uuid %s", fileUUID)
	require.Len(t, rec.Checksum, 4, "checksum should be 4 bytes")
	return binary.BigEndian.Uint32(rec.Checksum)
}
```

- [ ] **Step 5: Build the e2e package**

Run: `cd src && go build -tags e2e ./e2e/... 2>&1 | head -50`
Expected: no errors (this file has no other occurrences of the renamed identifiers per the earlier inventory).

- [ ] **Step 6: Commit**

```bash
git add src/common/listformat/listformat.go src/common/listformat/listformat_test.go src/e2e/validate.go
git commit -m "refactor(listformat,e2e): rename file_data_id to file_uuid in Row/JSON/e2e assertions"
```

---

### Task 10: Update remaining `cmd/bwfs` tests

**Files:**
- Modify: `src/cmd/bwfs/restore_test.go`
- Verify only (no expected changes, confirm by grep): `src/cmd/bwfs/integration_test.go`, `src/cmd/bwfs/list_test.go`, `src/cmd/bwfs/listserver_test.go`

**Interfaces:**
- Consumes: `pb.RestoreRequest{FileUuid: ...}`, `pb.FileRow.FileUuid` (Task 5).

- [ ] **Step 1: Update `restore_test.go`** — rename `fileDataID` to `fileUUID` throughout, and the proto field references

Change the doc comment and signature:

```go
// restoreAndVerifyCRC calls RestoreFile for fileDataID and checks that:
//   - all chunks' BLAKE3 hashes match the returned data
//   - the accumulated CRC32 matches meta.ExpectedChecksum
func restoreAndVerifyCRC(t *testing.T, client pb.RestoreServiceClient, fileDataID string) {
	t.Helper()

	stream, err := client.RestoreFile(context.Background(), &pb.RestoreRequest{FileDataId: fileDataID})
	require.NoError(t, err, "RestoreFile RPC failed for %s", fileDataID)

	firstEvent, err := stream.Recv()
	require.NoError(t, err, "failed to recv first event for %s", fileDataID)
	meta := firstEvent.GetMeta()
	require.NotNil(t, meta, "first event must be RestoreFileMeta for %s", fileDataID)

	hasher := crc32.NewIEEE()
	chunksReceived := 0

	for {
		event, err := stream.Recv()
		require.NoError(t, err, "stream error while reading chunks for %s", fileDataID)

		chunk := event.GetChunk()
		require.NotNil(t, chunk, "non-chunk event after meta for %s", fileDataID)

		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(chunk.Data))
		chunksReceived++

		if chunk.Eof {
			break
		}
	}

	assert.Equal(t, int(meta.ChunkCount), chunksReceived,
		"chunk count mismatch for %s: meta says %d, got %d", fileDataID, meta.ChunkCount, chunksReceived)

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	assert.True(t, bytes.Equal(buf[:], meta.ExpectedChecksum),
		"CRC32 mismatch for %s", fileDataID)
}

// listLatestFileDataID returns the file_data_id for the latest version of a file by path.
func listLatestFileDataID(t *testing.T, client pb.ListServiceClient, path string) string {
	t.Helper()
	resp, err := client.ListFiles(context.Background(), &pb.ListRequest{})
	require.NoError(t, err)
	for _, row := range resp.Rows {
		if row.Path == path {
			return row.FileDataId
		}
	}
	t.Fatalf("no file found for path %q in list response", path)
	return ""
}
```

to:

```go
// restoreAndVerifyCRC calls RestoreFile for fileUUID and checks that:
//   - all chunks' BLAKE3 hashes match the returned data
//   - the accumulated CRC32 matches meta.ExpectedChecksum
func restoreAndVerifyCRC(t *testing.T, client pb.RestoreServiceClient, fileUUID string) {
	t.Helper()

	stream, err := client.RestoreFile(context.Background(), &pb.RestoreRequest{FileUuid: fileUUID})
	require.NoError(t, err, "RestoreFile RPC failed for %s", fileUUID)

	firstEvent, err := stream.Recv()
	require.NoError(t, err, "failed to recv first event for %s", fileUUID)
	meta := firstEvent.GetMeta()
	require.NotNil(t, meta, "first event must be RestoreFileMeta for %s", fileUUID)

	hasher := crc32.NewIEEE()
	chunksReceived := 0

	for {
		event, err := stream.Recv()
		require.NoError(t, err, "stream error while reading chunks for %s", fileUUID)

		chunk := event.GetChunk()
		require.NotNil(t, chunk, "non-chunk event after meta for %s", fileUUID)

		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(chunk.Data))
		chunksReceived++

		if chunk.Eof {
			break
		}
	}

	assert.Equal(t, int(meta.ChunkCount), chunksReceived,
		"chunk count mismatch for %s: meta says %d, got %d", fileUUID, meta.ChunkCount, chunksReceived)

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	assert.True(t, bytes.Equal(buf[:], meta.ExpectedChecksum),
		"CRC32 mismatch for %s", fileUUID)
}

// listLatestFileUUID returns the file_uuid for the latest version of a file by path.
func listLatestFileUUID(t *testing.T, client pb.ListServiceClient, path string) string {
	t.Helper()
	resp, err := client.ListFiles(context.Background(), &pb.ListRequest{})
	require.NoError(t, err)
	for _, row := range resp.Rows {
		if row.Path == path {
			return row.FileUuid
		}
	}
	t.Fatalf("no file found for path %q in list response", path)
	return ""
}
```

Update the three call sites (`id := listLatestFileDataID(...)` → `listLatestFileUUID(...)`):

```go
	id := listLatestFileDataID(t, env.listClient, srcDir+"/data.txt")
	restoreAndVerifyCRC(t, env.restoreClient, id)
```
```go
	idA := listLatestFileDataID(t, env.listClient, pathA)
	restoreAndVerifyCRC(t, env.restoreClient, idA)

	idB := listLatestFileDataID(t, env.listClient, pathB)
	restoreAndVerifyCRC(t, env.restoreClient, idB)
```
```go
	idB := listLatestFileDataID(t, env.listClient, srcDirB+"/file.txt")
	restoreAndVerifyCRC(t, env.restoreClient, idB)
```

become (respectively):

```go
	id := listLatestFileUUID(t, env.listClient, srcDir+"/data.txt")
	restoreAndVerifyCRC(t, env.restoreClient, id)
```
```go
	idA := listLatestFileUUID(t, env.listClient, pathA)
	restoreAndVerifyCRC(t, env.restoreClient, idA)

	idB := listLatestFileUUID(t, env.listClient, pathB)
	restoreAndVerifyCRC(t, env.restoreClient, idB)
```
```go
	idB := listLatestFileUUID(t, env.listClient, srcDirB+"/file.txt")
	restoreAndVerifyCRC(t, env.restoreClient, idB)
```

- [ ] **Step 2: Confirm `integration_test.go`, `list_test.go`, `listserver_test.go` need no changes**

Run: `grep -n "FileDataId\|FileDataID\|file_data_id\|v\.FileID\b" src/cmd/bwfs/integration_test.go src/cmd/bwfs/list_test.go src/cmd/bwfs/listserver_test.go`
Expected: no matches. These files only use `pb.FileInfo{FileId: ...}` (untouched `backup.proto` field), `v.ObjectID` (untouched), and `parseFileID`/natural-key strings (untouched) — confirming no further edits needed there.

- [ ] **Step 3: Build and run all `cmd/bwfs` tests (including build-tagged ones)**

Run: `cd src && go build ./... 2>&1 | head -80`
Expected: whole repo compiles clean now.

Run: `cd src && go vet ./... 2>&1 | head -80`
Expected: no vet errors.

Run: `cd src && go test ./... 2>&1 | tail -100`
Expected: all non-tagged tests PASS.

Run: `cd src && go test -tags integration ./cmd/bwfs/... -v 2>&1 | tail -150`
Expected: all integration tests PASS, including `TestIntegration_Restore_HappyPath`, `TestIntegration_Restore_DedupChunks_ChunkLinksPresent`, `TestIntegration_Restore_AllChunksDeduped`.

- [ ] **Step 4: Commit**

```bash
git add src/cmd/bwfs/restore_test.go
git commit -m "test(bwfs): rename fileDataID to fileUUID in restore integration tests"
```

---

### Task 11: Update documentation

**Files:**
- Modify: `docs/protocols/list.md`
- Modify: `docs/protocols/restore.md`
- Modify: `docs/components/bwfs.md`
- Modify: `docs/components/rwfs.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: `docs/protocols/list.md`** — update the proto snippet and JSON example

Change:

```proto
message FileRow {
  string file_data_id = 1;
  string source        = 2;
```

to:

```proto
message FileRow {
  string file_uuid     = 1;
  string source        = 2;
```

Change the JSON example:

```json
    "file_data_id": "a1b2c3d4",
```

to:

```json
    "file_uuid": "a1b2c3d4",
```

- [ ] **Step 2: `docs/protocols/restore.md`** — update the proto snippet, sequence diagram, error table, and CLI→RPC mapping section

Change:

```proto
message RestoreRequest {
  string file_data_id = 1;  // FileDataRecord.ID from ListResponse
}
```

to:

```proto
message RestoreRequest {
  string file_uuid = 1;  // FileDataRecord.UUID from ListResponse
}
```

Change:

```
    Client->>Server: RestoreFile(RestoreRequest{file_data_id})
```

to:

```
    Client->>Server: RestoreFile(RestoreRequest{file_uuid})
```

Change the error table row:

```
| `file_data_id` not found or not finalized | gRPC `NotFound` |
```

to:

```
| `file_uuid` not found or not finalized | gRPC `NotFound` |
```

Change the CLI→RPC mapping paragraph:

```
`rwfs verify` calls `ListService.ListFiles` first (same filters as `rwfs list`), then
calls `RestoreFile` for each returned `file_data_id`:

```
rwfs verify myhost:/var/log localhost:8080 --filter nginx
  1. ListFiles{server_name="myhost", path="/var/log", filter="nginx"}
  2. For each FileRow: RestoreFile{file_data_id=row.file_data_id}
```
```

to:

```
`rwfs verify` calls `ListService.ListFiles` first (same filters as `rwfs list`), then
calls `RestoreFile` for each returned `file_uuid`:

```
rwfs verify myhost:/var/log localhost:8080 --filter nginx
  1. ListFiles{server_name="myhost", path="/var/log", filter="nginx"}
  2. For each FileRow: RestoreFile{file_uuid=row.file_uuid}
```
```

- [ ] **Step 3: `docs/components/bwfs.md`** — update the `list` JSON fields line and the `RestoreService` section, and note the `FileVersionRecord.FileID` removal if it's ever mentioned (it isn't currently — only the lookup semantics for restore are documented)

Change:

```
**JSON fields:** `file_data_id`, `source`, `type`, `path`, `timestamp`, `size`, `chunks`, `versions`, `created_at`
```

to:

```
**JSON fields:** `file_uuid`, `source`, `type`, `path`, `timestamp`, `size`, `chunks`, `versions`, `created_at`
```

Change:

```
Provides file reconstruction via server-streaming gRPC RPC. Given a `file_data_id` (UUID from `ListService.ListFiles`), returns file metadata followed by all chunks in index order.

**Lookup semantics:** The handler first queries `file_data_records` by the `file_data_id` UUID to obtain the `file_id` (fs:// path reference), then uses that `file_id` to query `file_data_chunk_records` in index order. The file must be finalized (with a non-NULL checksum) before restore is allowed.

**Error codes:** Returns gRPC `codes.NotFound` when the `file_data_id` doesn't exist in `file_data_records` or the record is unfinalized. Returns gRPC `codes.Internal` when a database error occurs or a chunk file cannot be read from disk. See [Restore Protocol](../protocols/restore.md) for detailed protocol flow and client-side verification responsibilities.
```

to:

```
Provides file reconstruction via server-streaming gRPC RPC. Given a `file_uuid` (UUID from `ListService.ListFiles`), returns file metadata followed by all chunks in index order.

**Lookup semantics:** The handler first queries `file_data_records` by the `file_uuid` (column `uuid`) to obtain the `file_id` (fs:// path reference — the natural key, distinct from `file_uuid`), then uses that `file_id` to query `file_data_chunk_records` in index order. The file must be finalized (with a non-NULL checksum) before restore is allowed.

**Error codes:** Returns gRPC `codes.NotFound` when the `file_uuid` doesn't exist in `file_data_records` or the record is unfinalized. Returns gRPC `codes.Internal` when a database error occurs or a chunk file cannot be read from disk. See [Restore Protocol](../protocols/restore.md) for detailed protocol flow and client-side verification responsibilities.
```

- [ ] **Step 4: `docs/components/rwfs.md`** — update the `list`/`verify` JSON fields line

Change:

```
**JSON fields:** `file_data_id`, `source`, `type`, `path`, `timestamp`, `size`, `chunks`, `versions`, `created_at`
```

to:

```
**JSON fields:** `file_uuid`, `source`, `type`, `path`, `timestamp`, `size`, `chunks`, `versions`, `created_at`
```

- [ ] **Step 5: Grep to confirm no stray mentions remain in living docs**

Run: `grep -rn "file_data_id" README.md docs/ARCHITECTURE.md docs/components/ docs/protocols/`
Expected: no matches (historical `docs/superpowers/plans/` and `docs/superpowers/specs/` are intentionally excluded from this grep and left untouched).

- [ ] **Step 6: Commit**

```bash
git add docs/protocols/list.md docs/protocols/restore.md docs/components/bwfs.md docs/components/rwfs.md
git commit -m "docs: rename file_data_id to file_uuid across list/restore protocol and component docs"
```

---

### Task 12: Final full-repo verification

**Files:** none (verification only).

- [ ] **Step 1: Full build**

Run: `cd src && go build ./... 2>&1`
Expected: clean, no output.

- [ ] **Step 2: Vet**

Run: `cd src && go vet ./... 2>&1`
Expected: clean, no output.

- [ ] **Step 3: Full unit test suite**

Run: `cd src && go test ./... 2>&1 | tail -100`
Expected: all PASS.

- [ ] **Step 4: Integration-tagged tests**

Run: `cd src && go test -tags integration ./... 2>&1 | tail -150`
Expected: all PASS.

- [ ] **Step 5: Grep sweep for leftover old identifiers in source (not docs, already checked in Task 11)**

Run: `grep -rn "FileDataID\|FileDataId\|file_data_id" src/ --include="*.go" --include="*.proto"`
Expected: no matches anywhere in `src/`.

- [ ] **Step 6: Confirm no orphaned `FileVersionRecord.FileID` references remain**

Run: `grep -rn "FileVersionRecord{.*FileID\|\.FileVersion{.*FileID\|v\.FileID\b" src/ --include="*.go"`
Expected: no matches (the field was removed in Task 1; any hit here is a build error waiting to happen or a stale test literal).
