# bwfs Catalog Replication (catalogsync) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `catalogsync`, a standalone binary that reads a `bwfs` node's `file_versions` table read-only and forwards new rows to a (not-yet-built) backup catalog, asynchronously and fully decoupled from `bwfs`'s own availability.

**Architecture:** `catalogsync` opens `bwfs`'s SQLite database via a genuinely read-only connection (`ReplicaReader`, new in `storage/filesystem`), polls for rows newer than a locally-persisted cursor, and hands batches to an abstract `Sender` (this iteration: `LoggingSender`, a stand-in for the future catalog client). A schema change replaces `file_versions`' synthetic `UUID` primary key with a real `INTEGER PRIMARY KEY AUTOINCREMENT seq` column (the cursor key, immune to reuse after job-failure purges) and exposes `(job_id, object_id)` — already unique — as the record's external identity.

**Tech Stack:** Go 1.26, GORM + `modernc.org/sqlite` (pure-Go, CGO-free), `spf13/cobra` for CLI, `stretchr/testify` for tests. No new dependencies.

## Global Constraints

- `catalogsync` must open `bwfs`'s `metadata.db` strictly read-only at the SQLite driver level (`?mode=ro`) — never reuse `filesystem.NewReadOnly`, which intentionally keeps write capability for `MarkChunkCorrupted`.
- The replication cursor never advances until a batch is confirmed sent by the `Sender` — no data loss on crash/restart, at-least-once delivery is acceptable.
- File versions replicate as written, regardless of `backup_jobs.status` — no gating on job success/failure (reconciliation of later-purged entries is the catalog's job, out of scope here).
- The catalog's wire protocol/RPC is out of scope. `Sender` is an abstract interface; `LoggingSender` (logs batches, always succeeds) is the only implementation this plan builds.
- No multi-node coordination — each `bwfs` instance runs one independent `catalogsync` instance.
- Per `.claude/CLAUDE.md`: any feature change needs matching updates to `docs/components/`, `README.md`, `docs/ARCHITECTURE.md`, and a `CHANGELOG.md` entry before merge.

---

### Task 1: Replace `FileVersionRecord`'s `UUID` with an autoincrement `Seq` cursor key

**Files:**
- Modify: `src/storage/filesystem/models.go`
- Modify: `src/storage/filesystem/fileversion.go`
- Modify: `src/storage/interface.go`
- Modify: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Produces: `FileVersionRecord{Seq int64, ObjectID string, JobID string, Metadata []byte, Ctime int64, CreatedAt time.Time}` (no `UUID` field) — consumed by Task 2's `ReplicaReader`.
- Produces: `storage.FileVersion{JobID string, ObjectID string, Metadata []byte, Ctime int64, CreatedAt time.Time}` (no `UUID` field).
- Produces: `(*Store).RemoveFileVersion(jobID, objectID string) error` (was `RemoveFileVersion(versionID string) error`).

- [ ] **Step 1: Write the failing test proving `seq` is never reused after a delete**

Add to `src/storage/filesystem/store_test.go` (near the other `FileVersion` tests, after `TestEnsureFileVersion_DuplicateWithinJobIsNoOp`):

```go
func TestFileVersionRecord_SeqNeverReusedAfterDelete(t *testing.T) {
	store := newTestStore(t)

	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("v1"), 100))

	var first FileVersionRecord
	require.NoError(t, store.db.Where("job_id = ? AND object_id = ?", "job-1", "obj-1").First(&first).Error)

	// Simulate FinalizeBackupJob purging a failed job's file_versions rows.
	require.NoError(t, store.db.Delete(&FileVersionRecord{}, "job_id = ?", "job-1").Error)

	require.NoError(t, store.EnsureFileVersion("job-2", "obj-2", []byte("v2"), 200))

	var second FileVersionRecord
	require.NoError(t, store.db.Where("job_id = ? AND object_id = ?", "job-2", "obj-2").First(&second).Error)

	assert.Greater(t, second.Seq, first.Seq, "AUTOINCREMENT must not reuse a deleted row's seq")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./storage/filesystem/... -run TestFileVersionRecord_SeqNeverReusedAfterDelete -v`
Expected: build failure — `FileVersionRecord` has no field `Seq` (it doesn't exist yet).

- [ ] **Step 3: Update the schema**

In `src/storage/filesystem/models.go`, replace the `FileVersionRecord` type:

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

- [ ] **Step 4: Update `EnsureFileVersion`, `RemoveFileVersion`, and `toStorageFileVersion`**

In `src/storage/filesystem/fileversion.go`, replace the whole file:

```go
package filesystem

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage"
)

// EnsureFileVersion idempotently records that objectID was observed during
// jobID's backup run. The first observation of a given (jobID, objectID)
// pair wins — a duplicate send of the same object within the same job (e.g.
// a future retry) is a safe no-op rather than a second catalog row.
func (s *Store) EnsureFileVersion(jobID, objectID string, metadata []byte, ctime int64) error {
	record := FileVersionRecord{
		JobID:     jobID,
		ObjectID:  objectID,
		Metadata:  metadata,
		Ctime:     ctime,
		CreatedAt: time.Now(),
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&record).Error
}

// RemoveFileVersion deletes the file_versions row identified by its natural
// (jobID, objectID) key.
func (s *Store) RemoveFileVersion(jobID, objectID string) error {
	return s.db.Delete(&FileVersionRecord{}, "job_id = ? AND object_id = ?", jobID, objectID).Error
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

// FileVersionsForJob returns the object IDs of every file_versions row
// recorded for jobID, for BackupCommit's hash verification.
func (s *Store) FileVersionsForJob(jobID string) ([]string, error) {
	var objectIDs []string
	err := s.db.Model(&FileVersionRecord{}).
		Where("job_id = ?", jobID).
		Pluck("object_id", &objectIDs).Error
	return objectIDs, err
}

func toStorageFileVersion(r *FileVersionRecord) *storage.FileVersion {
	return &storage.FileVersion{
		JobID:     r.JobID,
		ObjectID:  r.ObjectID,
		Metadata:  r.Metadata,
		Ctime:     r.Ctime,
		CreatedAt: r.CreatedAt,
	}
}
```

- [ ] **Step 5: Update `storage.FileVersion` and the `BackupStore` interface**

In `src/storage/interface.go`, change the `FileVersion` struct:

```go
// FileVersion represents file metadata for a specific backup
type FileVersion struct {
	JobID     string
	ObjectID  string    // Natural key of the backed-up entity (file today; other entity types later)
	Metadata  []byte    // File attributes, permissions, etc.
	Ctime     int64     // File change time
	CreatedAt time.Time // When backup occurred
}
```

And change the `RemoveFileVersion` line in the `BackupStore` interface:

```go
	// FileVersion operations - create metadata version for each backup
	EnsureFileVersion(jobID, objectID string, metadata []byte, ctime int64) error
	RemoveFileVersion(jobID, objectID string) error
```

- [ ] **Step 6: Fix existing tests that construct `FileVersionRecord` with `UUID`**

In `src/storage/filesystem/store_test.go`, replace `TestRemoveFileVersion_Removes`:

```go
func TestRemoveFileVersion_Removes(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.db.Create(&FileVersionRecord{
		JobID: "job-1", ObjectID: "obj-1", Metadata: []byte("meta"), Ctime: 100, CreatedAt: time.Now(),
	}).Error)

	require.NoError(t, store.RemoveFileVersion("job-1", "obj-1"))

	_, err := store.LatestFileVersion("obj-1")
	assert.Error(t, err)
}
```

Replace `TestFileVersionAtTime_ReturnsMostRecentBefore`:

```go
func TestFileVersionAtTime_ReturnsMostRecentBefore(t *testing.T) {
	store := newTestStore(t)

	// Create two versions with explicit created_at by inserting directly
	now := time.Now()
	old := FileVersionRecord{JobID: "job-old", ObjectID: "obj-1", Metadata: []byte("old"), Ctime: 1, CreatedAt: now.Add(-2 * time.Hour)}
	recent := FileVersionRecord{JobID: "job-recent", ObjectID: "obj-1", Metadata: []byte("recent"), Ctime: 2, CreatedAt: now.Add(-1 * time.Hour)}
	store.db.Create(&old)
	store.db.Create(&recent)

	v, err := store.FileVersionAtTime("obj-1", now.Add(-90*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, []byte("old"), v.Metadata)
}
```

Replace `TestFileVersionsInPeriod_ReturnsAll`:

```go
func TestFileVersionsInPeriod_ReturnsAll(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()

	r1 := FileVersionRecord{JobID: "job-1", ObjectID: "obj-1", CreatedAt: now.Add(-3 * time.Hour)}
	r2 := FileVersionRecord{JobID: "job-2", ObjectID: "obj-2", CreatedAt: now.Add(-1 * time.Hour)}
	store.db.Create(&r1)
	store.db.Create(&r2)

	versions, err := store.FileVersionsInPeriod(now.Add(-4*time.Hour), now)
	require.NoError(t, err)
	assert.Len(t, versions, 2)
}
```

Leave every other use of `uuid.New()` in this file untouched (they belong to `FileDataRecord`, a different table, unaffected by this change).

- [ ] **Step 7: Run the full package test suite**

Run: `cd src && go test ./storage/... -v`
Expected: PASS — all tests including `TestFileVersionRecord_SeqNeverReusedAfterDelete`.

- [ ] **Step 8: Commit**

```bash
git add src/storage/filesystem/models.go src/storage/filesystem/fileversion.go src/storage/interface.go src/storage/filesystem/store_test.go
git commit -m "feat(storage): replace file_versions UUID with autoincrement seq + natural key"
```

---

### Task 2: Add `ReplicaReader` — a genuinely read-only accessor for `catalogsync`

**Files:**
- Create: `src/storage/filesystem/replicareader.go`
- Test: `src/storage/filesystem/replicareader_test.go`

**Interfaces:**
- Consumes: `FileVersionRecord` (Task 1), `New(basePath string) (*Store, error)` (existing, used only in tests to seed data).
- Produces: `OpenReplicaReader(basePath string) (*ReplicaReader, error)`, `(*ReplicaReader).FileVersionsSince(cursor int64, limit int) ([]FileVersionRecord, error)`, `(*ReplicaReader).Close() error` — consumed by Task 6's poll loop.

- [ ] **Step 1: Write the failing tests**

Create `src/storage/filesystem/replicareader_test.go`:

```go
package filesystem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenReplicaReader_FileVersionsSince_ReturnsNewRowsInOrder(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("v1"), 100))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-2", []byte("v2"), 100))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-3", []byte("v3"), 100))

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	batch, err := reader.FileVersionsSince(0, 2)
	require.NoError(t, err)
	require.Len(t, batch, 2)
	assert.Equal(t, "obj-1", batch[0].ObjectID)
	assert.Equal(t, "obj-2", batch[1].ObjectID)

	next, err := reader.FileVersionsSince(batch[1].Seq, 2)
	require.NoError(t, err)
	require.Len(t, next, 1)
	assert.Equal(t, "obj-3", next[0].ObjectID)
}

func TestOpenReplicaReader_FileVersionsSince_EmptyWhenCaughtUp(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("v1"), 100))

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	batch, err := reader.FileVersionsSince(0, 10)
	require.NoError(t, err)
	require.Len(t, batch, 1)

	caughtUp, err := reader.FileVersionsSince(batch[0].Seq, 10)
	require.NoError(t, err)
	assert.Empty(t, caughtUp)
}

func TestOpenReplicaReader_CannotWrite(t *testing.T) {
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	defer store.Close()

	reader, err := OpenReplicaReader(dir)
	require.NoError(t, err)
	defer reader.Close()

	err = reader.db.Exec(
		"INSERT INTO file_version_records (object_id, job_id, ctime, created_at) VALUES ('x', 'y', 0, datetime('now'))",
	).Error
	assert.Error(t, err, "a mode=ro connection must reject writes")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./storage/filesystem/... -run TestOpenReplicaReader -v`
Expected: build failure — `OpenReplicaReader` is undefined.

- [ ] **Step 3: Implement `ReplicaReader`**

Create `src/storage/filesystem/replicareader.go`:

```go
package filesystem

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

// ReplicaReader is a strictly read-only accessor for an existing bwfs
// store's metadata.db, for use by a separate process (catalogsync) that
// must never be able to write to bwfs's data, even by accident. It opens
// the database via SQLite's `mode=ro` URI flag — enforced by the driver —
// unlike Store's NewReadOnly, which still opens a normal read-write
// connection (needed elsewhere for MarkChunkCorrupted).
type ReplicaReader struct {
	db *gorm.DB
}

// OpenReplicaReader opens basePath/metadata.db read-only. The database must
// already exist and have its schema migrated (by a real bwfs Store) — a
// read-only connection cannot create it.
func OpenReplicaReader(basePath string) (*ReplicaReader, error) {
	dbPath := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", filepath.Join(basePath, "metadata.db"))

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open read-only: %w", err)
	}
	return &ReplicaReader{db: db}, nil
}

// FileVersionsSince returns up to limit file_versions rows with seq greater
// than cursor, ordered ascending by seq — catalogsync's replication cursor.
func (r *ReplicaReader) FileVersionsSince(cursor int64, limit int) ([]FileVersionRecord, error) {
	var records []FileVersionRecord
	err := r.db.
		Where("seq > ?", cursor).
		Order("seq ASC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

func (r *ReplicaReader) Close() error {
	sqlDB, err := r.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./storage/filesystem/... -run TestOpenReplicaReader -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add src/storage/filesystem/replicareader.go src/storage/filesystem/replicareader_test.go
git commit -m "feat(storage): add ReplicaReader, a true read-only accessor for catalogsync"
```

---

### Task 3: Add `catalogsync` config keys

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `config.Config.CatalogSyncBatchSize int` (default 500), `config.Config.CatalogSyncPollIntervalSec int` (default 5), `config.Config.CatalogSyncMaxBackoffSec int` (default 60) — consumed by Task 7's `main.go`.

- [ ] **Step 1: Write the failing tests**

Add to `src/common/config/config_test.go`:

```go
func TestParseConfig_CatalogSyncBatchSizeDefaultsTo500(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 500, conf.CatalogSyncBatchSize)
}

func TestParseConfig_CatalogSyncBatchSizeParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nCatalogSyncBatchSize=250\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 250, conf.CatalogSyncBatchSize)
}

func TestParseConfig_CatalogSyncPollIntervalSecDefaultsTo5(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 5, conf.CatalogSyncPollIntervalSec)
}

func TestParseConfig_CatalogSyncPollIntervalSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nCatalogSyncPollIntervalSec=15\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 15, conf.CatalogSyncPollIntervalSec)
}

func TestParseConfig_CatalogSyncMaxBackoffSecDefaultsTo60(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 60, conf.CatalogSyncMaxBackoffSec)
}

func TestParseConfig_CatalogSyncMaxBackoffSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nCatalogSyncMaxBackoffSec=120\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 120, conf.CatalogSyncMaxBackoffSec)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./common/config/... -run TestParseConfig_CatalogSync -v`
Expected: build failure — `Config` has no field `CatalogSyncBatchSize` (etc.).

- [ ] **Step 3: Add the fields, defaults, and parsing cases**

In `src/common/config/config.go`, add three fields to the `Config` struct (after `JobTimeoutSec`):

```go
type Config struct {
	DefaultPort                int
	DefaultStreams             int
	LogFolder                  string
	ClientHashQueryBatchSize   int
	ConnectionTimeOutSec       int
	FileLockTimeoutSec         int
	StopStreamOnFileError      bool
	CAHost                     string
	JobTimeoutSec              int
	CatalogSyncBatchSize       int
	CatalogSyncPollIntervalSec int
	CatalogSyncMaxBackoffSec   int
}
```

Change the defaults literal in `ParseConfig`:

```go
	config := &Config{
		JobTimeoutSec:              30,
		CatalogSyncBatchSize:       500,
		CatalogSyncPollIntervalSec: 5,
		CatalogSyncMaxBackoffSec:   60,
	}
```

Add three cases to the `switch key` block (after the existing `"JobTimeoutSec"` case):

```go
		case "CatalogSyncBatchSize":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CatalogSyncBatchSize value at line %d: %s", lineNum, value)
			}
			config.CatalogSyncBatchSize = number
			foundFields["CatalogSyncBatchSize"] = true
		case "CatalogSyncPollIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CatalogSyncPollIntervalSec value at line %d: %s", lineNum, value)
			}
			config.CatalogSyncPollIntervalSec = number
			foundFields["CatalogSyncPollIntervalSec"] = true
		case "CatalogSyncMaxBackoffSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CatalogSyncMaxBackoffSec value at line %d: %s", lineNum, value)
			}
			config.CatalogSyncMaxBackoffSec = number
			foundFields["CatalogSyncMaxBackoffSec"] = true
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS — all config tests including the six new ones.

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add CatalogSyncBatchSize, CatalogSyncPollIntervalSec, CatalogSyncMaxBackoffSec"
```

---

### Task 4: `catalogsync` cursor persistence

**Files:**
- Create: `src/cmd/catalogsync/cursor.go`
- Test: `src/cmd/catalogsync/cursor_test.go`

**Interfaces:**
- Produces: `readCursor(path string) (int64, error)`, `writeCursor(path string, seq int64) error` — consumed by Task 6's poll loop.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/catalogsync/cursor_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCursor_MissingFileReturnsZero(t *testing.T) {
	dir := t.TempDir()
	seq, err := readCursor(filepath.Join(dir, "catalogsync.cursor"))
	require.NoError(t, err)
	assert.Equal(t, int64(0), seq)
}

func TestWriteCursorThenReadCursor_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalogsync.cursor")

	require.NoError(t, writeCursor(path, 42))

	seq, err := readCursor(path)
	require.NoError(t, err)
	assert.Equal(t, int64(42), seq)
}

func TestWriteCursor_OverwritesPreviousValueAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalogsync.cursor")

	require.NoError(t, writeCursor(path, 1))
	require.NoError(t, writeCursor(path, 2))

	seq, err := readCursor(path)
	require.NoError(t, err)
	assert.Equal(t, int64(2), seq)

	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err), "no leftover temp file after a successful write")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/catalogsync/... -run TestReadCursor -v`
Expected: build failure — package `cmd/catalogsync` and `readCursor` don't exist yet.

- [ ] **Step 3: Implement cursor persistence**

Create `src/cmd/catalogsync/cursor.go`:

```go
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// readCursor returns the last replicated seq, or 0 if the cursor file
// doesn't exist yet (first run — replication starts from the beginning).
func readCursor(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read cursor: %w", err)
	}
	seq, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse cursor %q: %w", path, err)
	}
	return seq, nil
}

// writeCursor persists seq atomically: write to a temp file in the same
// directory, then rename over the target. A crash mid-write never leaves a
// torn cursor file, since rename is atomic on the same filesystem.
func writeCursor(path string, seq int64) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.FormatInt(seq, 10)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write temp cursor: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename cursor into place: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/catalogsync/... -v`
Expected: PASS — all three cursor tests.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalogsync/cursor.go src/cmd/catalogsync/cursor_test.go
git commit -m "feat(catalogsync): add atomic plain-integer cursor persistence"
```

---

### Task 5: `Sender` interface and `LoggingSender`

**Files:**
- Create: `src/cmd/catalogsync/sender.go`
- Test: `src/cmd/catalogsync/sender_test.go`

**Interfaces:**
- Consumes: `filesystem.FileVersionRecord` (Task 1).
- Produces: `type Sender interface { Send(batch []filesystem.FileVersionRecord) error }`, `NewLoggingSender(logger *slog.Logger) *LoggingSender` (implements `Sender`) — consumed by Task 6's poll loop and Task 7's `main.go`.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/catalogsync/sender_test.go`:

```go
package main

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func TestLoggingSender_Send_LogsEveryRecordAndSucceeds(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sender := NewLoggingSender(logger)

	batch := []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
		{Seq: 2, JobID: "job-1", ObjectID: "obj-2"},
	}

	require.NoError(t, sender.Send(batch))

	output := buf.String()
	assert.Contains(t, output, "obj-1")
	assert.Contains(t, output, "obj-2")
}

func TestLoggingSender_Send_EmptyBatchSucceeds(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	sender := NewLoggingSender(logger)

	assert.NoError(t, sender.Send(nil))
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/catalogsync/... -run TestLoggingSender -v`
Expected: build failure — `Sender`, `NewLoggingSender` don't exist yet.

- [ ] **Step 3: Implement `Sender` and `LoggingSender`**

Create `src/cmd/catalogsync/sender.go`:

```go
package main

import (
	"log/slog"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

// Sender delivers a batch of file version records to the backup catalog.
// The only implementation today is LoggingSender; a real gRPC client
// against the future catalog service replaces it later behind this
// interface — nothing else in catalogsync needs to change when that
// happens.
type Sender interface {
	Send(batch []wfs.FileVersionRecord) error
}

// LoggingSender logs every batch it's given and always succeeds — a
// stand-in for the not-yet-built catalog client, proving the replication
// pipeline end-to-end.
type LoggingSender struct {
	logger *slog.Logger
}

func NewLoggingSender(logger *slog.Logger) *LoggingSender {
	return &LoggingSender{logger: logger}
}

func (s *LoggingSender) Send(batch []wfs.FileVersionRecord) error {
	for _, r := range batch {
		s.logger.Info("catalog replication entry", "job_id", r.JobID, "object_id", r.ObjectID, "seq", r.Seq)
	}
	s.logger.Info("catalog replication batch sent", "count", len(batch))
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd src && go test ./cmd/catalogsync/... -v`
Expected: PASS — both `LoggingSender` tests plus the earlier cursor tests.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalogsync/sender.go src/cmd/catalogsync/sender_test.go
git commit -m "feat(catalogsync): add Sender interface and LoggingSender stand-in"
```

---

### Task 6: Poll loop tying reader, sender, and cursor together with backoff

**Files:**
- Create: `src/cmd/catalogsync/sync.go`
- Test: `src/cmd/catalogsync/sync_test.go`

**Interfaces:**
- Consumes: `readCursor`/`writeCursor` (Task 4), `Sender` (Task 5), `filesystem.FileVersionRecord` (Task 1).
- Produces: `type syncConfig struct { BatchSize int; PollInterval, InitialBackoff, MaxBackoff time.Duration }`, `func run(ctx context.Context, logger *slog.Logger, rd reader, sender Sender, cursorFile string, cfg syncConfig) error`, `type reader interface { FileVersionsSince(cursor int64, limit int) ([]filesystem.FileVersionRecord, error) }` — consumed by Task 7's `main.go`. Note: `*filesystem.ReplicaReader` (Task 2) already satisfies `reader` structurally.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/catalogsync/sync_test.go`:

```go
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type fakeReader struct {
	mu      sync.Mutex
	records []wfs.FileVersionRecord
}

func (f *fakeReader) FileVersionsSince(cursor int64, limit int) ([]wfs.FileVersionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []wfs.FileVersionRecord
	for _, r := range f.records {
		if r.Seq > cursor {
			out = append(out, r)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

type fakeSender struct {
	mu      sync.Mutex
	batches [][]wfs.FileVersionRecord
	failN   int // number of subsequent Send calls to fail before succeeding
}

func (f *fakeSender) Send(batch []wfs.FileVersionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failN > 0 {
		f.failN--
		return errors.New("simulated send failure")
	}
	f.batches = append(f.batches, batch)
	return nil
}

func (f *fakeSender) sentBatchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestRun_SendsAllRecordsAndAdvancesCursor(t *testing.T) {
	dir := t.TempDir()
	cursorFile := filepath.Join(dir, "catalogsync.cursor")

	rd := &fakeReader{records: []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
		{Seq: 2, JobID: "job-1", ObjectID: "obj-2"},
	}}
	sender := &fakeSender{}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	cfg := syncConfig{BatchSize: 10, PollInterval: 10 * time.Millisecond, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
	err := run(ctx, testLogger(), rd, sender, cursorFile, cfg)
	require.NoError(t, err)

	require.Equal(t, 1, sender.sentBatchCount())
	assert.Len(t, sender.batches[0], 2)

	seq, err := readCursor(cursorFile)
	require.NoError(t, err)
	assert.Equal(t, int64(2), seq)
}

func TestRun_CursorDoesNotAdvanceOnSendFailure(t *testing.T) {
	dir := t.TempDir()
	cursorFile := filepath.Join(dir, "catalogsync.cursor")

	rd := &fakeReader{records: []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
	}}
	sender := &fakeSender{failN: 1000} // fails for the whole test window

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	cfg := syncConfig{BatchSize: 10, PollInterval: 10 * time.Millisecond, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 20 * time.Millisecond}
	err := run(ctx, testLogger(), rd, sender, cursorFile, cfg)
	require.NoError(t, err)

	seq, err := readCursor(cursorFile)
	require.NoError(t, err)
	assert.Equal(t, int64(0), seq, "cursor must not advance while sends keep failing")
}

func TestRun_RetriesAfterTransientFailureThenAdvances(t *testing.T) {
	dir := t.TempDir()
	cursorFile := filepath.Join(dir, "catalogsync.cursor")

	rd := &fakeReader{records: []wfs.FileVersionRecord{
		{Seq: 1, JobID: "job-1", ObjectID: "obj-1"},
	}}
	sender := &fakeSender{failN: 2} // fails twice, then succeeds

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	cfg := syncConfig{BatchSize: 10, PollInterval: 10 * time.Millisecond, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 30 * time.Millisecond}
	err := run(ctx, testLogger(), rd, sender, cursorFile, cfg)
	require.NoError(t, err)

	seq, err := readCursor(cursorFile)
	require.NoError(t, err)
	assert.Equal(t, int64(1), seq)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/catalogsync/... -run TestRun_ -v`
Expected: build failure — `syncConfig`, `run`, `reader` don't exist yet.

- [ ] **Step 3: Implement the poll loop**

Create `src/cmd/catalogsync/sync.go`:

```go
package main

import (
	"context"
	"log/slog"
	"time"

	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

// syncConfig bundles the tunables run needs, decoupled from config.Config
// so tests don't need a parsed config file.
type syncConfig struct {
	BatchSize      int
	PollInterval   time.Duration
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

// reader is the subset of *filesystem.ReplicaReader that run depends on.
type reader interface {
	FileVersionsSince(cursor int64, limit int) ([]wfs.FileVersionRecord, error)
}

// run polls rd for new file_versions rows and hands each batch to sender,
// persisting the cursor via cursorFile only after a batch is successfully
// sent. It runs until ctx is cancelled, at which point it returns nil.
func run(ctx context.Context, logger *slog.Logger, rd reader, sender Sender, cursorFile string, cfg syncConfig) error {
	cursor, err := readCursor(cursorFile)
	if err != nil {
		return err
	}

	backoff := cfg.InitialBackoff

	for {
		if ctx.Err() != nil {
			return nil
		}

		batch, err := rd.FileVersionsSince(cursor, cfg.BatchSize)
		if err != nil {
			logger.Error("read file versions failed", "error", err)
			if !sleepOrDone(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}

		if len(batch) == 0 {
			if !sleepOrDone(ctx, cfg.PollInterval) {
				return nil
			}
			continue
		}

		if err := sender.Send(batch); err != nil {
			logger.Warn("send batch failed, retrying", "error", err, "backoff", backoff)
			if !sleepOrDone(ctx, backoff) {
				return nil
			}
			backoff *= 2
			if backoff > cfg.MaxBackoff {
				backoff = cfg.MaxBackoff
			}
			continue
		}

		backoff = cfg.InitialBackoff
		cursor = batch[len(batch)-1].Seq
		if err := writeCursor(cursorFile, cursor); err != nil {
			return err
		}

		if len(batch) == cfg.BatchSize {
			continue // there may be more backlog — drain it without sleeping
		}
		if !sleepOrDone(ctx, cfg.PollInterval) {
			return nil
		}
	}
}

// sleepOrDone sleeps for d, or returns false immediately if ctx is
// cancelled first.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/catalogsync/... -v`
Expected: PASS — all `TestRun_*` tests plus every earlier `cmd/catalogsync` test.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalogsync/sync.go src/cmd/catalogsync/sync_test.go
git commit -m "feat(catalogsync): add poll loop with backoff and cursor advancement"
```

---

### Task 7: `catalogsync` CLI, `main.go`, and Makefile target

**Files:**
- Create: `src/cmd/catalogsync/arguments.go`
- Create: `src/cmd/catalogsync/main.go`
- Modify: `Makefile`

**Interfaces:**
- Consumes: `config.Config.CatalogSync*` (Task 3), `wfs.OpenReplicaReader` (Task 2), `NewLoggingSender` (Task 5), `syncConfig`/`run` (Task 6).
- Produces: the `catalogsync` binary at `bin/catalogsync` via `make catalogsync` / `make build`.

- [ ] **Step 1: Implement argument parsing**

Create `src/cmd/catalogsync/arguments.go`:

```go
package main

import "github.com/spf13/cobra"

// Arguments holds parsed command line arguments.
type Arguments struct {
	StoragePath string
	Debug       bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}
	cmd := &cobra.Command{
		Use:   "catalogsync <storage_path>",
		Short: "Replicate a bwfs node's file versions to a backup catalog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.StoragePath = cliArgs[0]
			return nil
		},
	}
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return args, nil
}
```

- [ ] **Step 2: Implement `main.go`**

Create `src/cmd/catalogsync/main.go`:

```go
// catalogsync replicates a bwfs node's file_versions to a backup catalog,
// asynchronously and independently of bwfs's own availability.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

const initialBackoff = 1 * time.Second

func main() {
	const appName = "catalogsync"

	arguments, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	replicaReader, err := wfs.OpenReplicaReader(arguments.StoragePath)
	if err != nil {
		logger.Error("failed to open bwfs store read-only", "error", err)
		os.Exit(1)
	}
	defer replicaReader.Close()

	sender := NewLoggingSender(logger)
	cursorFile := filepath.Join(arguments.StoragePath, "catalogsync.cursor")

	cfg := syncConfig{
		BatchSize:      conf.CatalogSyncBatchSize,
		PollInterval:   time.Duration(conf.CatalogSyncPollIntervalSec) * time.Second,
		InitialBackoff: initialBackoff,
		MaxBackoff:     time.Duration(conf.CatalogSyncMaxBackoffSec) * time.Second,
	}

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("catalogsync started", "storage_path", arguments.StoragePath, "batch_size", cfg.BatchSize)

	if err := run(signalCtx, logger, replicaReader, sender, cursorFile, cfg); err != nil {
		logger.Error("catalogsync exited with error", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Add the Makefile target**

In `Makefile`, add a new command variable next to the others near the top:

```makefile
CERTCLIENT_CMD := cmd/certclient
CATALOGSYNC_CMD := cmd/catalogsync
```

Update the `.PHONY` line to include `catalogsync`:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certrequest certclient catalogsync test test-e2e lint
```

Add a build rule after the `certclient` rule:

```makefile
catalogsync: $(BINARY_DIR) ## Build catalogsync binary
	@printf "$(BLUE)Building catalogsync...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/catalogsync ./$(CATALOGSYNC_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/catalogsync"
```

- [ ] **Step 4: Build and smoke-test the binary**

Run: `make catalogsync`
Expected: `bin/catalogsync` is created, with output ending in `Built successfully:bin/catalogsync`.

Run: `./bin/catalogsync` (no args)
Expected: non-zero exit, usage error mentioning the required `storage_path` argument (cobra's `ExactArgs(1)` validation).

- [ ] **Step 5: Run the full test suite once more**

Run: `cd src && go test ./... && go vet ./...`
Expected: PASS with no vet warnings.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/catalogsync/arguments.go src/cmd/catalogsync/main.go Makefile
git commit -m "feat(catalogsync): wire CLI, main, and Makefile build target"
```

---

### Task 8: Documentation and changelog

**Files:**
- Create: `docs/components/catalogsync.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing (documentation only) — this task has no test cycle; it's verified by reading the rendered docs, per `.claude/CLAUDE.md`'s pre-merge documentation rule.

- [ ] **Step 1: Write `docs/components/catalogsync.md`**

Create `docs/components/catalogsync.md`:

```markdown
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
(temp file + rename) after each confirmed send. If it's missing, `catalogsync` starts from the
beginning (`seq=0`) — safe, because the catalog is expected to treat `(job_id, object_id)` as an
idempotency key for the resulting at-least-once delivery.

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
```

- [ ] **Step 2: Update `README.md`'s component list**

In `README.md`, in the `## Components` section, add a new bullet after the `certclient` line:

```markdown
- **[catalogsync](docs/components/catalogsync.md)** - Replicates a bwfs node's file versions to a backup catalog, asynchronously and independent of bwfs's own availability
```

- [ ] **Step 3: Update `docs/ARCHITECTURE.md`'s component table**

In `docs/ARCHITECTURE.md`, add a row to the `## Components` table after the `rwfs` row:

```markdown
| catalogsync | Replicates a bwfs node's file_versions to a backup catalog | Implemented (catalog service itself not yet built) |
```

- [ ] **Step 4: Update `docs/ARCHITECTURE.md`'s data-flow diagram**

In the mermaid diagram's `"Backup Machine"` subgraph, add `catalogsync` and a new `"Catalog (planned)"` subgraph. Change:

```
    subgraph "Backup Machine"
        bwfs[bwfs<br/>Backup Writer]
        BackupFS[Backup Filesystem]
        DB[(SQLite Database)]
    end
```

to:

```
    subgraph "Backup Machine"
        bwfs[bwfs<br/>Backup Writer]
        BackupFS[Backup Filesystem]
        DB[(SQLite Database)]
        catalogsync[catalogsync<br/>Catalog Replicator]
    end

    subgraph "Catalog (planned)"
        Catalog[(Backup Catalog)]
    end
```

Add two new edges after the existing restore-flow edges (before the `classDef` block):

```
    %% Catalog Replication Flow (bwfs's own operation is unaffected either way)
    DB -->|reads file_versions,<br/>read-only| catalogsync
    catalogsync -.->|replicate batches<br/>planned| Catalog
```

Apply the diagram's existing (currently unused) `planned` style to the new catalog node, in the
`class` line at the bottom:

```
    class SrcFS,BackupFS,DstFS filesystem
    class brfs,bwfs,catalogsync component
    class rwfs component
    class DB database
    class Catalog planned
```

- [ ] **Step 5: Add the `CHANGELOG.md` entry**

In `CHANGELOG.md`, add a new heading above the existing `## 2026-07-02 — Backup job completion
verification` entry:

```markdown
## 2026-07-02 — Async catalog replication (catalogsync)

Added `catalogsync`, a new standalone component that tails a `bwfs` node's `file_versions` table
and forwards new rows to a future backup catalog, independently of `bwfs`'s own availability.
`catalogsync` opens `bwfs`'s SQLite database strictly read-only and tracks its own replication
progress in a small local cursor file, retrying with backoff whenever the catalog (represented
today by a logging stand-in `Sender`) is unreachable — nothing is marked replicated until a batch
is confirmed sent, so an outage or restart never loses data. This required replacing
`file_versions`' synthetic `UUID` primary key with a real `INTEGER PRIMARY KEY AUTOINCREMENT`
`seq` column (immune to the row-number reuse a bare SQLite `rowid` allows after a failed job's
rows are purged) and its natural `(job_id, object_id)` identity for external consumers.

```

- [ ] **Step 6: Commit**

```bash
git add docs/components/catalogsync.md README.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document catalogsync component, architecture, and changelog entry"
```

## Self-Review Notes

- **Spec coverage:** schema change + natural key (Task 1), read-only reader (Task 2), config
  (Task 3), cursor (Task 4), Sender/LoggingSender (Task 5), poll loop + backoff (Task 6), CLI +
  Makefile (Task 7), docs/changelog (Task 8) — every section of the design doc maps to a task.
  Non-goals (catalog service, wire protocol, multi-node coordination, job-status gating) are
  deliberately not implemented, matching the spec.
- **Placeholder scan:** no TODOs/TBDs; every step has concrete, complete code.
- **Type consistency:** `FileVersionRecord{Seq, ObjectID, JobID, Metadata, Ctime, CreatedAt}`
  (Task 1) is the exact type threaded through `ReplicaReader.FileVersionsSince` (Task 2),
  `Sender.Send` (Task 5), and `run`'s `reader`/`Sender` parameters (Task 6) — verified consistent
  across all task boundaries. `syncConfig` field names (`BatchSize`, `PollInterval`,
  `InitialBackoff`, `MaxBackoff`) match between Task 6's definition, its tests, and Task 7's
  `main.go` construction.
