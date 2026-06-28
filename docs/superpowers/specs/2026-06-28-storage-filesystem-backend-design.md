# Storage Filesystem Backend — Design Spec

**Date:** 2026-06-28  
**Scope:** Replace all mock stubs in `src/storage/filesystem/` with a real implementation backed by filesystem chunk storage and SQLite metadata via GORM.

---

## Goal

Make `bwfs` actually persist data. Chunks go to disk in a content-addressable layout. Metadata (file data records, chunk references, file versions) goes to SQLite via GORM. Reliability and correctness are the top priority; simplicity is second.

---

## File Layout

No structural changes — extend what exists:

```
storage/filesystem/
  models.go       ← GORM model structs (new)
  db.go           ← DB open + AutoMigrate (new)
  store.go        ← Store gains db field; New() opens DB and chunk dir
  chunks.go       ← real chunk FS + DB logic
  filedata.go     ← real FileData logic
  fileversion.go  ← real FileVersion logic
  info.go         ← StoreInfo + Vacuum (mostly real)
```

---

## Data Model

### `models.go`

Four GORM structs. Field names match the `storage` interface types directly.

**ChunkRecord**
```go
type ChunkRecord struct {
    Hash      string `gorm:"primaryKey"`  // BLAKE3 hex, full 64 chars
    Size      int64
    CreatedAt time.Time
}
```
Hash is the primary key — natural deduplication, no surrogate key needed.

**FileDataRecord**
```go
type FileDataRecord struct {
    ID         string  `gorm:"primaryKey"`  // UUID
    FileID     string  `gorm:"index"`
    Size       int64
    Checksum   []byte  // NULL until FinalizeFileData; NULL = incomplete
    ChunkCount int
    CreatedAt  time.Time
}
```
`Checksum IS NOT NULL` is the completion signal. `FileDataExists` returns true only when checksum is set.

**FileDataChunkRecord**
```go
type FileDataChunkRecord struct {
    FileDataID string `gorm:"primaryKey;index"`
    ChunkHash  string `gorm:"primaryKey"`
    Index      int64  `gorm:"primaryKey"`
}
```
Composite PK on `(FileDataID, ChunkHash, Index)`. Insert with `OnConflict: DoNothing` is safe under concurrent streams.

**FileVersionRecord**
```go
type FileVersionRecord struct {
    ID        string `gorm:"primaryKey"`  // UUID
    ObjectID  string `gorm:"index"`
    FileID    string
    Metadata  []byte
    Ctime     int64
    CreatedAt time.Time
}
```

---

## Database Init (`db.go`)

`openDB(basePath string) (*gorm.DB, error)`:
1. Opens `<basePath>/metadata.db` using `gorm.io/driver/sqlite`
2. Sets `PRAGMA journal_mode=WAL` for concurrent reads during writes
3. Calls `AutoMigrate` on all four models
4. Returns `*gorm.DB`

WAL mode is important: multiple backup streams read the DB concurrently while writes happen.

---

## `store.go`

```go
type Store struct {
    basePath string
    db       *gorm.DB
}
```

`New(basePath string)`:
1. Creates `<basePath>/chunks/` directory if missing
2. Calls `openDB(basePath)`
3. Returns `*Store`

`Close()`:
```go
sqlDB, _ := s.db.DB()
return sqlDB.Close()
```

---

## Chunk Storage (`chunks.go`)

### Filesystem Layout

```
<basePath>/chunks/<hex[0:2]>/<hex[2:4]>/<hex[4:]>
```

Example: hash `aabbccddee1122...` → `chunks/aa/bb/ccddee1122...`

Two levels of directories (4 hex chars total = 256×256 = 65536 buckets) keeps directory entry counts manageable for large backups.

Helper:
```go
func (s *Store) chunkPath(hexHash string) string {
    return filepath.Join(s.basePath, "chunks", hexHash[0:2], hexHash[2:4], hexHash[4:])
}
```

All chunk methods receive `chunkHash []byte` from the interface. Convert to hex string at the top of each method with `hex.EncodeToString(chunkHash)` before computing paths or DB keys.

### `ChunkExists(chunkHash []byte) error`

`os.Stat(chunkPath)` only — no DB query. The file's presence on disk is the truth. Returns `ErrChunkNotFound` if stat fails with `os.IsNotExist`.

This is the hot path for incremental backups where most chunks already exist. Zero DB load.

### `StoreChunk(chunkHash []byte, data []byte) error`

1. Recompute BLAKE3 over `data`; return error if it doesn't match `chunkHash` — corrupt data rejected before touching disk
2. `os.Stat` the final path — if it exists, do nothing (idempotent, already stored)
3. `os.MkdirAll` the two parent directories
4. Write data to a unique temp file: `<chunkPath>.<random>.tmp`
5. `os.Rename(tempPath, finalPath)` — atomic; concurrent renames of same content are harmless
6. DB insert: `INSERT INTO chunk_records ... ON CONFLICT DO NOTHING`

If the DB insert fails after the file is written, the chunk is orphaned on disk but not corrupt. `Vacuum()` reconciles these.

### `ReadChunk(chunkHash []byte) ([]byte, error)`

`os.ReadFile(chunkPath)`. No DB needed.

---

## FileData (`filedata.go`)

### `FileDataExists(fileID string) (bool, error)`

```sql
SELECT id FROM file_data_records
WHERE file_id = ? AND checksum IS NOT NULL
LIMIT 1
```
Returns true only for finalized records.

### `CreateFileData(fileID string, size int64) error`

Insert a new `FileDataRecord` with a fresh UUID, `checksum = NULL`.

### `FinalizeFileData(fileID string, checksum []byte) error`

```sql
UPDATE file_data_records
SET checksum = ?, chunk_count = (SELECT COUNT(*) FROM file_data_chunk_records WHERE file_data_id = id)
WHERE file_id = ? AND checksum IS NULL
```
Setting checksum atomically marks the record complete. Only updates incomplete records — idempotent.

### `FileData(fileID string) (*storage.FileData, error)`

Query by `file_id` where `checksum IS NOT NULL`, return the most recent one.

### `FileDataChunks(fileID string) iter.Seq2[[]byte, error]`

Join `file_data_records` → `file_data_chunk_records` ordered by `index`, yield `chunk_hash` bytes.

---

## FileVersion (`fileversion.go`)

### `CreateFileVersion(objectID, fileID string, metadata []byte, ctime int64) (string, error)`

Insert `FileVersionRecord` with fresh UUID. Return the UUID.

### `RemoveFileVersion(versionID string) error`

Delete by primary key.

### `LatestFileVersion(objectID string) (*storage.FileVersion, error)`

```sql
SELECT * FROM file_version_records
WHERE object_id = ?
ORDER BY created_at DESC
LIMIT 1
```

### `FileVersionAtTime(objectID string, timestamp time.Time) (*storage.FileVersion, error)`

```sql
SELECT * FROM file_version_records
WHERE object_id = ? AND created_at <= ?
ORDER BY created_at DESC
LIMIT 1
```

### `FileVersionsInPeriod(from, to time.Time) ([]*storage.FileVersion, error)`

```sql
SELECT * FROM file_version_records
WHERE created_at BETWEEN ? AND ?
ORDER BY created_at ASC
```

---

## StoreInfo + Vacuum (`info.go`)

### `StoreInfo()`

Four COUNT/SUM queries against the DB tables. Simple.

### `Vacuum()`

Runs in order:

1. **Incomplete FileData**: delete `FileDataRecord` rows where `checksum IS NULL` and `created_at < now - threshold` (e.g. 1h). Also delete their `FileDataChunkRecord` rows.
2. **Orphaned FileData**: delete `FileDataRecord` rows with no corresponding `FileVersionRecord.file_id`. Delete their chunk links too.
3. **Orphaned chunks in DB**: delete `ChunkRecord` rows with no `FileDataChunkRecord` referencing them.
4. **Orphaned chunk files**: walk `chunks/` directory, for each file check if its hex name exists in `chunk_records`. Delete if not. Count bytes reclaimed.

Step 4 is the expensive one (full directory walk). Acceptable for a maintenance operation.

---

## Reliability Summary

| Failure | Outcome |
|---|---|
| Crash mid-chunk write | Temp file orphaned, final path never written — `Vacuum()` cleans temp files |
| Hash mismatch on receive | Rejected before disk write — corrupt data never stored |
| Two streams write same chunk | Both write unique temps, race on rename is harmless (same content) |
| DB insert fails after file write | Chunk orphaned on disk — `Vacuum()` reconciles |
| Client disconnects mid-file | `FileDataRecord.checksum` stays NULL — invisible to future backups — `Vacuum()` cleans |
| Read of unfinished FileData | `FileDataExists` requires non-NULL checksum — impossible to serve incomplete data |

---

## Dependencies to Add

```
gorm.io/gorm
gorm.io/driver/sqlite
github.com/google/uuid
```

SQLite driver: use `gorm.io/driver/sqlite` with `modernc.org/sqlite` (pure Go, no CGo). No C toolchain required. Import `_ "modernc.org/sqlite"` in `db.go`.

---

## Out of Scope

- Restore path (`rwfs`, `rrfs`)
- Background scrubber / periodic re-verification
- Encryption
- Compression
