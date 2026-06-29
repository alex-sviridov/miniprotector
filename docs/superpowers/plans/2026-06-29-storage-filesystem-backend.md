# Storage Filesystem Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace all mock stubs in `src/storage/filesystem/` with a real implementation that persists chunks to disk and metadata to SQLite via GORM.

**Architecture:** Chunks are stored in a content-addressable filesystem layout (`chunks/aa/bb/<rest-of-hash>`). Metadata (file data records, chunk links, file versions) lives in SQLite opened via GORM. The `storage.BackupStore` interface is the only contract — `bwfs` is updated to hold the interface, not the concrete type, so backends are swappable.

**Tech Stack:** Go 1.24, GORM (`gorm.io/gorm`), SQLite via `gorm.io/driver/sqlite` + `modernc.org/sqlite` (pure Go, no CGo), `github.com/google/uuid`, `lukechampine.com/blake3` (already present).

## Global Constraints

- All work is under `src/` — run all commands from `/home/alex/miniprotector/src`
- Module path: `github.com/alex-sviridov/miniprotector`
- SQLite driver: `modernc.org/sqlite` (pure Go — no CGo, no C toolchain needed)
- No CGo: `CGO_ENABLED=0` must work for tests
- `lukechampine.com/blake3` is the BLAKE3 library (already in go.mod)
- Tests use `github.com/stretchr/testify` (already in go.mod)
- Test files go in the same package as the code they test
- Run `go build ./...` and `go test ./storage/...` to verify each task

---

## File Map

| File | Action | Responsibility |
|---|---|---|
| `storage/interface.go` | Modify | Add `ErrChunkNotFound` sentinel error |
| `storage/filesystem/models.go` | Create | GORM model structs |
| `storage/filesystem/db.go` | Create | DB open + WAL + AutoMigrate |
| `storage/filesystem/store.go` | Modify | Add `db` field; real `New()` and `Close()` |
| `storage/filesystem/chunks.go` | Modify | Real chunk FS + DB logic |
| `storage/filesystem/filedata.go` | Modify | Real FileData logic |
| `storage/filesystem/fileversion.go` | Modify | Real FileVersion logic |
| `storage/filesystem/info.go` | Modify | Real StoreInfo + Vacuum |
| `storage/filesystem/store_test.go` | Create | Integration tests for the full store |
| `cmd/bwfs/server.go` | Modify | Hold `storage.BackupStore` not `*wfs.Store` |
| `cmd/bwfs/handler.go` | Modify | Hold `storage.BackupStore`; use `storage.ErrChunkNotFound` |

---

## Task 1: Add dependencies + `ErrChunkNotFound` to storage package

**Files:**
- Modify: `storage/interface.go`
- Modify: `go.mod` / `go.sum` (via `go get`)

**Interfaces:**
- Produces: `storage.ErrChunkNotFound` — used by all subsequent tasks and `cmd/bwfs/handler.go`

- [ ] **Step 1: Add dependencies**

```bash
cd /home/alex/miniprotector/src
go get gorm.io/gorm
go get gorm.io/driver/sqlite
go get modernc.org/sqlite
go get github.com/google/uuid
```

Expected: each command prints `go: added ...` and exits 0.

- [ ] **Step 2: Write the failing test**

Create `storage/filesystem/store_test.go`:

```go
package filesystem_test

import (
	"testing"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/stretchr/testify/assert"
)

func TestErrChunkNotFoundIsSentinel(t *testing.T) {
	// Verify the sentinel exists and has the right message
	assert.EqualError(t, storage.ErrChunkNotFound, "chunk not found")
}
```

- [ ] **Step 3: Run test to confirm it fails**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run TestErrChunkNotFoundIsSentinel -v
```

Expected: FAIL — `storage.ErrChunkNotFound undefined`

- [ ] **Step 4: Add `ErrChunkNotFound` to `storage/interface.go`**

Add at the top of `storage/interface.go`, after the import block:

```go
import (
    "errors"
    "iter"
    "time"
)

var ErrChunkNotFound = errors.New("chunk not found")
```

Remove the existing `var ErrChunkNotFound` declaration from `storage/filesystem/chunks.go` (it's currently the only line defining it there). Replace the whole `chunks.go` file content with a temporary stub that imports `storage` and returns `storage.ErrChunkNotFound` — this will be fully replaced in Task 4. For now just ensure it compiles:

```go
package filesystem

import (
	"bytes"
	"fmt"

	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
)

func (s *Store) ChunkExists(chunkHash []byte) error {
	return storage.ErrChunkNotFound
}

func (s *Store) StoreChunk(chunkHash []byte, data []byte) error {
	hash32 := blake3.Sum256(data)
	hash := hash32[:]
	if !bytes.Equal(chunkHash, hash) {
		return fmt.Errorf("chunk hash mismatch")
	}
	return nil
}

func (s *Store) LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error {
	return nil
}

func (s *Store) ReadChunk(chunkHash []byte) ([]byte, error) {
	return nil, fmt.Errorf("chunk not found: %x", chunkHash)
}
```

- [ ] **Step 5: Run test to confirm it passes**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run TestErrChunkNotFoundIsSentinel -v
```

Expected: PASS

- [ ] **Step 6: Confirm build is clean**

```bash
cd /home/alex/miniprotector/src
go build ./...
```

Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add storage/interface.go storage/filesystem/chunks.go storage/filesystem/store_test.go go.mod go.sum
git commit -m "add ErrChunkNotFound to storage package and new dependencies"
```

---

## Task 2: GORM models + DB init

**Files:**
- Create: `storage/filesystem/models.go`
- Create: `storage/filesystem/db.go`

**Interfaces:**
- Produces:
  - `ChunkRecord`, `FileDataRecord`, `FileDataChunkRecord`, `FileVersionRecord` — GORM structs used by all storage methods
  - `openDB(basePath string) (*gorm.DB, error)` — used by `store.go` in Task 3

- [ ] **Step 1: Write the failing test**

Add to `storage/filesystem/store_test.go`:

```go
import (
	"os"
	"path/filepath"
	"testing"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenDB_CreatesSchemaAndFile(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(dir)
	require.NoError(t, err)
	defer func() {
		sqlDB, _ := db.DB()
		sqlDB.Close()
	}()

	// DB file must exist
	_, err = os.Stat(filepath.Join(dir, "metadata.db"))
	assert.NoError(t, err)

	// All four tables must exist (AutoMigrate creates them)
	assert.NoError(t, db.Exec("SELECT 1 FROM chunk_records LIMIT 1").Error)
	assert.NoError(t, db.Exec("SELECT 1 FROM file_data_records LIMIT 1").Error)
	assert.NoError(t, db.Exec("SELECT 1 FROM file_data_chunk_records LIMIT 1").Error)
	assert.NoError(t, db.Exec("SELECT 1 FROM file_version_records LIMIT 1").Error)
}
```

Note: `openDB` is unexported but tested from within the same package (`package filesystem_test` won't work for unexported — change test file package to `package filesystem`).

Update the test file package declaration to `package filesystem` (not `package filesystem_test`) so unexported helpers are accessible.

- [ ] **Step 2: Run test to confirm it fails**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run TestOpenDB_CreatesSchemaAndFile -v
```

Expected: FAIL — `openDB undefined`

- [ ] **Step 3: Create `storage/filesystem/models.go`**

```go
package filesystem

import "time"

type ChunkRecord struct {
	Hash      string `gorm:"primaryKey"`
	Size      int64
	CreatedAt time.Time
}

type FileDataRecord struct {
	ID         string `gorm:"primaryKey"`
	FileID     string `gorm:"index"`
	Size       int64
	Checksum   []byte
	ChunkCount int
	CreatedAt  time.Time
}

type FileDataChunkRecord struct {
	FileDataID string `gorm:"primaryKey"`
	ChunkHash  string `gorm:"primaryKey"`
	Index      int64  `gorm:"primaryKey"`
}

type FileVersionRecord struct {
	ID        string `gorm:"primaryKey"`
	ObjectID  string `gorm:"index"`
	FileID    string
	Metadata  []byte
	Ctime     int64
	CreatedAt time.Time
}
```

- [ ] **Step 4: Create `storage/filesystem/db.go`**

```go
package filesystem

import (
	"fmt"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

func openDB(basePath string) (*gorm.DB, error) {
	dbPath := filepath.Join(basePath, "metadata.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB: %w", err)
	}
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	if err := db.AutoMigrate(
		&ChunkRecord{},
		&FileDataRecord{},
		&FileDataChunkRecord{},
		&FileVersionRecord{},
	); err != nil {
		return nil, fmt.Errorf("automigrate: %w", err)
	}
	return db, nil
}
```

- [ ] **Step 5: Run test to confirm it passes**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run TestOpenDB_CreatesSchemaAndFile -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add storage/filesystem/models.go storage/filesystem/db.go storage/filesystem/store_test.go
git commit -m "add GORM models and DB init with WAL mode"
```

---

## Task 3: Wire `Store` to DB + update `bwfs` to use interface

**Files:**
- Modify: `storage/filesystem/store.go`
- Modify: `cmd/bwfs/server.go`
- Modify: `cmd/bwfs/handler.go`

**Interfaces:**
- Consumes: `openDB(basePath string) (*gorm.DB, error)` from Task 2
- Produces: `filesystem.New(basePath string) (*Store, error)` with real DB; `*Store` satisfies `storage.BackupStore`

- [ ] **Step 1: Update `storage/filesystem/store.go`**

Replace the entire file:

```go
package filesystem

import (
	"fmt"
	"os"
	"path/filepath"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

type Store struct {
	basePath string
	db       *gorm.DB
}

func New(basePath string) (*Store, error) {
	chunksDir := filepath.Join(basePath, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return nil, fmt.Errorf("create chunks dir: %w", err)
	}

	db, err := openDB(basePath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	return &Store{basePath: basePath, db: db}, nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

var _ storage.BackupStore = (*Store)(nil)
```

- [ ] **Step 2: Update `cmd/bwfs/server.go`**

Change the `store` field type and import:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"

	pb "github.com/alex-sviridov/miniprotector/api"

	"google.golang.org/grpc/peer"
)

type backupServer struct {
	pb.UnimplementedBackupServiceServer
	config *config.Config
	store  storage.BackupStore
	logger *slog.Logger
}

func NewBackupServer(ctx context.Context, logger *slog.Logger, storagePath string) (*backupServer, error) {
	conf := config.GetConfigFromContext(ctx)

	store, err := wfs.New(storagePath)
	if err != nil {
		return nil, err
	}
	return &backupServer{
		logger: logger,
		config: conf,
		store:  store,
	}, nil
}

func (server *backupServer) ProcessBackupStream(stream pb.BackupService_ProcessBackupStreamServer) error {
	ctx := stream.Context()

	var clientAddr, clientAuthType string = "unknown", "none"
	if peer, ok := peer.FromContext(ctx); ok {
		clientAddr = peer.Addr.String()
		if peer.AuthInfo != nil {
			clientAuthType = peer.AuthInfo.AuthType()
		}
	}

	streamInfo := fmt.Sprintf("%p", stream)
	logger := server.logger.With(
		slog.String("client_addr", clientAddr),
		slog.Any("grpc_auth_type", clientAuthType),
		slog.String("stream_id", streamInfo),
	)
	ctx = context.WithValue(ctx, config.ContextKey, server.config)

	h := newStreamHandler(ctx, logger, server.store)

	for {
		request, err := stream.Recv()
		if err == io.EOF {
			h.logger.Info("Client stopped sending")
			return nil
		}
		if err != nil {
			h.logger.Error("Error receiving", "error", err)
			return err
		}
		if request == nil {
			continue
		}
		if err := h.handleRequest(ctx, stream, request); err != nil {
			h.logger.Error("Error handling request", "error", err)
		}
		if h.EOF {
			if err := h.fileWritten(ctx, stream); err != nil {
				h.logger.Error("Error finalizing file", "error", err)
			}
		}
	}
}
```

- [ ] **Step 3: Update `cmd/bwfs/handler.go`**

Change the `store` field and import. Replace the `wfs` import with `storage`, and change `wfs.ErrChunkNotFound` to `storage.ErrChunkNotFound`:

```go
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/zeebo/blake3"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type RequestHandlerFunc func(context.Context, pb.BackupService_ProcessBackupStreamServer, *pb.FileRequest) error

type streamHandler struct {
	config            *config.Config
	store             storage.BackupStore
	logger            *slog.Logger
	currentFile       *filesystem.FileInfo
	incrementalHasher *blake3.Hasher
	EOF               bool
	handlerMap        map[string]RequestHandlerFunc
}

func newStreamHandler(ctx context.Context, logger *slog.Logger, store storage.BackupStore) *streamHandler {
	handler := &streamHandler{
		config: config.GetConfigFromContext(ctx),
		store:  store,
		logger: logger,
	}
	handler.handlerMap = map[string]RequestHandlerFunc{
		fmt.Sprintf("%T", &pb.FileRequest_FileInfo{}):  handler.handleFileInfoRequest,
		fmt.Sprintf("%T", &pb.FileRequest_ChunkHash{}): handler.handleChunkHashRequest,
		fmt.Sprintf("%T", &pb.FileRequest_ChunkData{}): handler.handleChunkDataRequest,
	}
	handler.logger.Info("New backup stream connected")
	return handler
}

func (h *streamHandler) handleRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, request *pb.FileRequest) error {
	requestType := fmt.Sprintf("%T", request.RequestType)
	handler, ok := h.handlerMap[requestType]
	if !ok {
		return fmt.Errorf("unknown request type: %s", requestType)
	}
	return handler(ctx, server, request)
}

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
	if h.currentFile.GetType() != 'f' {
		needed = false
	}
	if h.currentFile.Size() == 0 {
		needed = false
	}
	fileLogger.Debug("File existence check",
		"exists", fileExists,
		"needed", needed,
		"file_size", h.currentFile.Size(),
		"file_type", fmt.Sprintf("%c", h.currentFile.GetType()))

	if !needed {
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

func (h *streamHandler) handleChunkHashRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	chunk := req.GetChunkHash()
	if chunk == nil {
		return fmt.Errorf("FileRequest_ChunkHash has empty ChunkHash")
	}
	chunkLogger := h.logger.
		With(slog.String("file_id", h.currentFile.ID())).
		With(slog.String("chunk_hash", hex.EncodeToString(chunk.Hash)))

	chunkLogger.Debug("Received chunk hash")
	var needed bool

	err := h.store.ChunkExists(chunk.Hash)
	if err != nil {
		if errors.Is(err, storage.ErrChunkNotFound) {
			needed = true
		} else {
			return err
		}
	} else {
		needed = false
		h.incrementalHasher.Write(chunk.Hash)
	}

	chunkLogger.Debug("Chunk existence check", "needed", needed)

	response := &pb.FileResponse{
		ResponseType: &pb.FileResponse_ChunkNeeded{
			ChunkNeeded: &pb.ChunkNeeded{
				Hash:   chunk.Hash,
				Needed: needed,
			},
		},
	}
	if chunk.Eof && !needed {
		h.EOF = true
	}
	return server.Send(response)
}

func (h *streamHandler) handleChunkDataRequest(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer, req *pb.FileRequest) error {
	chunk := req.GetChunkData()
	if chunk == nil {
		return fmt.Errorf("FileRequest_ChunkData has empty ChunkData")
	}

	chunkLogger := h.logger.
		With(slog.String("file_id", h.currentFile.ID())).
		With(slog.String("chunk_hash", hex.EncodeToString(chunk.Hash)))

	if err := h.store.StoreChunk(chunk.Hash, chunk.Data); err != nil {
		return err
	}
	h.incrementalHasher.Write(chunk.Hash)
	chunkLogger.Debug("Chunk written")

	if err := h.store.LinkChunkToFileData(chunk.Hash, h.currentFile.ID(), chunk.Index); err != nil {
		return err
	}
	chunkLogger.Debug("Chunk linked")

	response := &pb.FileResponse{
		ResponseType: &pb.FileResponse_ChunkResult{
			ChunkResult: &pb.ChunkResult{
				Hash:    chunk.Hash,
				Success: true,
			},
		},
	}
	if chunk.Eof {
		chunkLogger.Debug("EOF received")
		h.EOF = true
	}
	return server.Send(response)
}

func (h *streamHandler) fileWritten(ctx context.Context, server pb.BackupService_ProcessBackupStreamServer) error {
	fileLogger := h.logger.With(slog.String("file_id", h.currentFile.ID()))
	file_hash := h.incrementalHasher.Sum(nil)
	h.store.FinalizeFileData(h.currentFile.ID(), file_hash)
	fileLogger.Debug("File transfer completed", "file_hash", hex.EncodeToString(file_hash))
	message := server.Send(&pb.FileResponse{
		ResponseType: &pb.FileResponse_Result{
			Result: &pb.FileProcessingResult{
				FileId:  h.currentFile.ID(),
				Success: true,
				Hash:    file_hash,
			},
		},
	})
	h.incrementalHasher = nil
	h.currentFile = nil
	h.EOF = false
	return message
}
```

- [ ] **Step 4: Verify build is clean**

```bash
cd /home/alex/miniprotector/src
go build ./...
```

Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add storage/filesystem/store.go cmd/bwfs/server.go cmd/bwfs/handler.go
git commit -m "wire Store to DB; bwfs uses storage.BackupStore interface"
```

---

## Task 4: Real chunk storage

**Files:**
- Modify: `storage/filesystem/chunks.go`

**Interfaces:**
- Consumes: `ChunkRecord` from `models.go`; `storage.ErrChunkNotFound` from `storage/interface.go`
- Produces:
  - `(s *Store) ChunkExists(chunkHash []byte) error` — `os.Stat` only, no DB
  - `(s *Store) StoreChunk(chunkHash []byte, data []byte) error` — verify hash, atomic write, DB insert
  - `(s *Store) LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error` — DB insert with conflict ignore
  - `(s *Store) ReadChunk(chunkHash []byte) ([]byte, error)` — `os.ReadFile`
  - `(s *Store) chunkPath(hexHash string) string` — unexported helper

- [ ] **Step 1: Write failing tests**

Add to `storage/filesystem/store_test.go`:

```go
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := New(dir)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func makeChunk(t *testing.T, data []byte) (hash []byte) {
	t.Helper()
	h := blake3.Sum256(data)
	return h[:]
}

func TestChunkExists_NotFound(t *testing.T) {
	store := newTestStore(t)
	hash := makeChunk(t, []byte("hello"))
	err := store.ChunkExists(hash)
	assert.ErrorIs(t, err, storage.ErrChunkNotFound)
}

func TestStoreChunk_WritesFile(t *testing.T) {
	store := newTestStore(t)
	data := []byte("chunk data for testing")
	hash := makeChunk(t, data)

	require.NoError(t, store.StoreChunk(hash, data))

	// File must exist on disk
	hexHash := hex.EncodeToString(hash)
	path := filepath.Join(store.basePath, "chunks", hexHash[0:2], hexHash[2:4], hexHash[4:])
	_, err := os.Stat(path)
	assert.NoError(t, err)
}

func TestChunkExists_AfterStore(t *testing.T) {
	store := newTestStore(t)
	data := []byte("chunk data for testing")
	hash := makeChunk(t, data)

	require.NoError(t, store.StoreChunk(hash, data))
	assert.NoError(t, store.ChunkExists(hash))
}

func TestStoreChunk_HashMismatchRejected(t *testing.T) {
	store := newTestStore(t)
	data := []byte("real data")
	wrongHash := makeChunk(t, []byte("different data"))

	err := store.StoreChunk(wrongHash, data)
	assert.Error(t, err)
}

func TestStoreChunk_Idempotent(t *testing.T) {
	store := newTestStore(t)
	data := []byte("idempotent chunk")
	hash := makeChunk(t, data)

	require.NoError(t, store.StoreChunk(hash, data))
	require.NoError(t, store.StoreChunk(hash, data)) // second call must not error
}

func TestReadChunk_ReturnsData(t *testing.T) {
	store := newTestStore(t)
	data := []byte("readable chunk data")
	hash := makeChunk(t, data)

	require.NoError(t, store.StoreChunk(hash, data))
	got, err := store.ReadChunk(hash)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestLinkChunkToFileData_Idempotent(t *testing.T) {
	store := newTestStore(t)
	data := []byte("linked chunk")
	hash := makeChunk(t, data)
	require.NoError(t, store.StoreChunk(hash, data))

	// Two links with same args must not error
	require.NoError(t, store.LinkChunkToFileData(hash, "file-1", 0))
	require.NoError(t, store.LinkChunkToFileData(hash, "file-1", 0))
}
```

Also add imports at the top of `store_test.go`:

```go
import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run "TestChunk|TestStore|TestLink|TestRead" -v
```

Expected: FAIL — methods exist but are stubs

- [ ] **Step 3: Implement `storage/filesystem/chunks.go`**

Replace the entire file:

```go
package filesystem

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"

	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
	"gorm.io/gorm/clause"
)

func (s *Store) chunkPath(hexHash string) string {
	return filepath.Join(s.basePath, "chunks", hexHash[0:2], hexHash[2:4], hexHash[4:])
}

func (s *Store) ChunkExists(chunkHash []byte) error {
	path := s.chunkPath(hex.EncodeToString(chunkHash))
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return storage.ErrChunkNotFound
	}
	return err
}

func (s *Store) StoreChunk(chunkHash []byte, data []byte) error {
	sum := blake3.Sum256(data)
	if !bytes.Equal(chunkHash, sum[:]) {
		return fmt.Errorf("chunk hash mismatch")
	}

	hexHash := hex.EncodeToString(chunkHash)
	finalPath := s.chunkPath(hexHash)

	if _, err := os.Stat(finalPath); err == nil {
		return nil // already exists
	}

	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create chunk dir: %w", err)
	}

	tmpPath := fmt.Sprintf("%s.%016x.tmp", finalPath, rand.Uint64())
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write chunk temp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename chunk: %w", err)
	}

	record := ChunkRecord{Hash: hexHash, Size: int64(len(data))}
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error
}

func (s *Store) LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error {
	record := FileDataChunkRecord{
		FileDataID: fileID,
		ChunkHash:  hex.EncodeToString(chunkHash),
		Index:      index,
	}
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error
}

func (s *Store) ReadChunk(chunkHash []byte) ([]byte, error) {
	path := s.chunkPath(hex.EncodeToString(chunkHash))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("chunk not found: %x", chunkHash)
		}
		return nil, fmt.Errorf("read chunk: %w", err)
	}
	return data, nil
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run "TestChunk|TestStoreChunk|TestLink|TestRead" -v
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add storage/filesystem/chunks.go storage/filesystem/store_test.go
git commit -m "implement real chunk storage with atomic writes and hash verification"
```

---

## Task 5: Real FileData implementation

**Files:**
- Modify: `storage/filesystem/filedata.go`

**Interfaces:**
- Consumes: `FileDataRecord`, `FileDataChunkRecord` from `models.go`
- Produces:
  - `(s *Store) FileDataExists(fileID string) (bool, error)`
  - `(s *Store) CreateFileData(fileID string, size int64) error`
  - `(s *Store) FinalizeFileData(fileID string, checksum []byte) error`
  - `(s *Store) FileData(fileID string) (*storage.FileData, error)`
  - `(s *Store) FileDataChunks(fileID string) iter.Seq2[[]byte, error]`

- [ ] **Step 1: Write failing tests**

Add to `storage/filesystem/store_test.go`:

```go
func TestFileDataExists_FalseWhenMissing(t *testing.T) {
	store := newTestStore(t)
	exists, err := store.FileDataExists("nonexistent")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestFileDataExists_FalseWhenNotFinalized(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.CreateFileData("file-1", 1024))

	exists, err := store.FileDataExists("file-1")
	require.NoError(t, err)
	assert.False(t, exists) // not finalized yet
}

func TestFileDataExists_TrueAfterFinalize(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.CreateFileData("file-1", 1024))
	require.NoError(t, store.FinalizeFileData("file-1", []byte("checksum")))

	exists, err := store.FileDataExists("file-1")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestFileDataChunks_ReturnsOrderedHashes(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.CreateFileData("file-1", 100))

	data0 := []byte("chunk zero data padded to something")
	data1 := []byte("chunk one data padded to something!")
	hash0 := makeChunk(t, data0)
	hash1 := makeChunk(t, data1)

	require.NoError(t, store.StoreChunk(hash0, data0))
	require.NoError(t, store.StoreChunk(hash1, data1))
	require.NoError(t, store.LinkChunkToFileData(hash0, "file-1", 0))
	require.NoError(t, store.LinkChunkToFileData(hash1, "file-1", 1))
	require.NoError(t, store.FinalizeFileData("file-1", []byte("checksum")))

	var hashes [][]byte
	for h, err := range store.FileDataChunks("file-1") {
		require.NoError(t, err)
		hashes = append(hashes, h)
	}
	require.Len(t, hashes, 2)
	assert.Equal(t, hash0, hashes[0])
	assert.Equal(t, hash1, hashes[1])
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run "TestFileData" -v
```

Expected: FAIL — stubs return wrong values

- [ ] **Step 3: Implement `storage/filesystem/filedata.go`**

Replace the entire file:

```go
package filesystem

import (
	"encoding/hex"
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
	if err == gorm.ErrRecordNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CreateFileData(fileID string, size int64) error {
	record := FileDataRecord{
		ID:        uuid.New().String(),
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
				Where("file_data_id = ?", fileID).
				Select("count(*)"),
		}).Error
}

func (s *Store) FileData(fileID string) (*storage.FileData, error) {
	var record FileDataRecord
	err := s.db.
		Where("file_id = ? AND checksum IS NOT NULL", fileID).
		Order("created_at DESC").
		First(&record).Error
	if err == gorm.ErrRecordNotFound {
		return nil, fmt.Errorf("filedata not found: %s", fileID)
	}
	if err != nil {
		return nil, err
	}
	return &storage.FileData{
		ID:         record.ID,
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
			Where("file_data_id = ?", fileID).
			Order("index ASC").
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

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run "TestFileData" -v
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add storage/filesystem/filedata.go storage/filesystem/store_test.go
git commit -m "implement real FileData storage and chunk ordering"
```

---

## Task 6: Real FileVersion implementation

**Files:**
- Modify: `storage/filesystem/fileversion.go`

**Interfaces:**
- Consumes: `FileVersionRecord` from `models.go`
- Produces:
  - `(s *Store) CreateFileVersion(objectID, fileID string, metadata []byte, ctime int64) (string, error)`
  - `(s *Store) RemoveFileVersion(versionID string) error`
  - `(s *Store) LatestFileVersion(objectID string) (*storage.FileVersion, error)`
  - `(s *Store) FileVersionAtTime(objectID string, t time.Time) (*storage.FileVersion, error)`
  - `(s *Store) FileVersionsInPeriod(from, to time.Time) ([]*storage.FileVersion, error)`

- [ ] **Step 1: Write failing tests**

Add to `storage/filesystem/store_test.go`:

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

func TestFileVersionAtTime_ReturnsMostRecentBefore(t *testing.T) {
	store := newTestStore(t)

	// Create two versions with explicit created_at by inserting directly
	now := time.Now()
	old := FileVersionRecord{ID: uuid.New().String(), ObjectID: "obj-1", FileID: "file-old", Metadata: []byte("old"), Ctime: 1, CreatedAt: now.Add(-2 * time.Hour)}
	recent := FileVersionRecord{ID: uuid.New().String(), ObjectID: "obj-1", FileID: "file-recent", Metadata: []byte("recent"), Ctime: 2, CreatedAt: now.Add(-1 * time.Hour)}
	store.db.Create(&old)
	store.db.Create(&recent)

	v, err := store.FileVersionAtTime("obj-1", now.Add(-90*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, "file-old", v.FileID)
}

func TestFileVersionsInPeriod_ReturnsAll(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()

	r1 := FileVersionRecord{ID: uuid.New().String(), ObjectID: "obj-1", FileID: "f1", CreatedAt: now.Add(-3 * time.Hour)}
	r2 := FileVersionRecord{ID: uuid.New().String(), ObjectID: "obj-2", FileID: "f2", CreatedAt: now.Add(-1 * time.Hour)}
	store.db.Create(&r1)
	store.db.Create(&r2)

	versions, err := store.FileVersionsInPeriod(now.Add(-4*time.Hour), now)
	require.NoError(t, err)
	assert.Len(t, versions, 2)
}
```

Add `"time"` and `"github.com/google/uuid"` to the test file imports.

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run "TestFileVersion|TestLatest|TestRemove|TestPeriod" -v
```

Expected: FAIL — stubs return wrong values

- [ ] **Step 3: Implement `storage/filesystem/fileversion.go`**

Replace the entire file:

```go
package filesystem

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

func (s *Store) CreateFileVersion(objectID, fileID string, metadata []byte, ctime int64) (string, error) {
	id := uuid.New().String()
	record := FileVersionRecord{
		ID:        id,
		ObjectID:  objectID,
		FileID:    fileID,
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
	return s.db.Delete(&FileVersionRecord{}, "id = ?", versionID).Error
}

func (s *Store) LatestFileVersion(objectID string) (*storage.FileVersion, error) {
	var record FileVersionRecord
	err := s.db.
		Where("object_id = ?", objectID).
		Order("created_at DESC").
		First(&record).Error
	if err == gorm.ErrRecordNotFound {
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
	if err == gorm.ErrRecordNotFound {
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
		ID:        r.ID,
		ObjectID:  r.ObjectID,
		FileID:    r.FileID,
		Metadata:  r.Metadata,
		Ctime:     r.Ctime,
		CreatedAt: r.CreatedAt,
	}
}
```

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run "TestFileVersion|TestLatest|TestRemove|TestPeriod|TestCreate" -v
```

Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add storage/filesystem/fileversion.go storage/filesystem/store_test.go
git commit -m "implement real FileVersion storage with time-based queries"
```

---

## Task 7: Real StoreInfo and Vacuum

**Files:**
- Modify: `storage/filesystem/info.go`

**Interfaces:**
- Consumes: all four model types; chunk filesystem layout from Task 4
- Produces:
  - `(s *Store) StoreInfo() (*storage.StoreInfo, error)`
  - `(s *Store) Vacuum() (*storage.VacuumResult, error)`

- [ ] **Step 1: Write failing tests**

Add to `storage/filesystem/store_test.go`:

```go
func TestStoreInfo_CountsCorrectly(t *testing.T) {
	store := newTestStore(t)

	data := []byte("a chunk of test data for info test")
	hash := makeChunk(t, data)
	require.NoError(t, store.StoreChunk(hash, data))
	require.NoError(t, store.CreateFileData("file-1", int64(len(data))))
	require.NoError(t, store.LinkChunkToFileData(hash, "file-1", 0))
	require.NoError(t, store.FinalizeFileData("file-1", []byte("checksum")))
	_, err := store.CreateFileVersion("obj-1", "file-1", []byte("meta"), 0)
	require.NoError(t, err)

	info, err := store.StoreInfo()
	require.NoError(t, err)
	assert.Equal(t, int64(1), info.TotalChunks)
	assert.Equal(t, int64(1), info.TotalFileData)
	assert.Equal(t, int64(1), info.TotalFileVersions)
	assert.Equal(t, int64(len(data)), info.TotalSize)
}

func TestVacuum_RemovesIncompleteFileData(t *testing.T) {
	store := newTestStore(t)

	// Create an incomplete FileDataRecord by inserting directly with old timestamp
	old := FileDataRecord{
		ID:        uuid.New().String(),
		FileID:    "incomplete-file",
		Size:      100,
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	store.db.Create(&old)

	result, err := store.Vacuum()
	require.NoError(t, err)
	assert.Greater(t, result.IncompleteFileData, int64(0))

	// Must be gone
	exists, err := store.FileDataExists("incomplete-file")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestVacuum_RemovesOrphanedChunkFiles(t *testing.T) {
	store := newTestStore(t)

	// Write a chunk file without a DB record
	data := []byte("orphan chunk data for vacuum test!")
	hash := makeChunk(t, data)
	hexHash := hex.EncodeToString(hash)
	dir := filepath.Join(store.basePath, "chunks", hexHash[0:2], hexHash[2:4])
	require.NoError(t, os.MkdirAll(dir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, hexHash[4:]), data, 0644))

	result, err := store.Vacuum()
	require.NoError(t, err)
	assert.Greater(t, result.BytesReclaimed, int64(0))

	// File must be gone
	assert.ErrorIs(t, store.ChunkExists(hash), storage.ErrChunkNotFound)
}
```

- [ ] **Step 2: Run tests to confirm they fail**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run "TestStoreInfo|TestVacuum" -v
```

Expected: FAIL — stubs return zeros / do nothing

- [ ] **Step 3: Implement `storage/filesystem/info.go`**

Replace the entire file:

```go
package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/storage"
)

const vacuumIncompleteThreshold = time.Hour

func (s *Store) StoreInfo() (*storage.StoreInfo, error) {
	var totalVersions, totalFileData, totalChunks, totalSize int64

	if err := s.db.Model(&FileVersionRecord{}).Count(&totalVersions).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&FileDataRecord{}).Where("checksum IS NOT NULL").Count(&totalFileData).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&ChunkRecord{}).Count(&totalChunks).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&ChunkRecord{}).Select("COALESCE(SUM(size), 0)").Scan(&totalSize).Error; err != nil {
		return nil, err
	}

	return &storage.StoreInfo{
		TotalFileVersions: totalVersions,
		TotalFileData:     totalFileData,
		TotalChunks:       totalChunks,
		TotalSize:         totalSize,
		UniqueChunks:      totalChunks,
	}, nil
}

func (s *Store) Vacuum() (*storage.VacuumResult, error) {
	result := &storage.VacuumResult{}

	// Step 1: remove incomplete FileData older than threshold
	cutoff := time.Now().Add(-vacuumIncompleteThreshold)
	var incompleteIDs []string
	s.db.Model(&FileDataRecord{}).
		Where("checksum IS NULL AND created_at < ?", cutoff).
		Pluck("id", &incompleteIDs)

	if len(incompleteIDs) > 0 {
		s.db.Where("file_data_id IN ?", incompleteIDs).Delete(&FileDataChunkRecord{})
		res := s.db.Where("id IN ?", incompleteIDs).Delete(&FileDataRecord{})
		result.IncompleteFileData = res.RowsAffected
	}

	// Step 2: remove FileData with no FileVersion referencing them
	var orphanedFileDataIDs []string
	s.db.Model(&FileDataRecord{}).
		Where("file_id NOT IN (SELECT file_id FROM file_version_records)").
		Where("checksum IS NOT NULL").
		Pluck("id", &orphanedFileDataIDs)

	if len(orphanedFileDataIDs) > 0 {
		s.db.Where("file_data_id IN ?", orphanedFileDataIDs).Delete(&FileDataChunkRecord{})
		res := s.db.Where("id IN ?", orphanedFileDataIDs).Delete(&FileDataRecord{})
		result.OrphanedFileDataRemoved = res.RowsAffected
	}

	// Step 3: remove ChunkRecord rows with no FileDataChunkRecord referencing them
	res := s.db.Where("hash NOT IN (SELECT chunk_hash FROM file_data_chunk_records)").Delete(&ChunkRecord{})
	result.OrphanedChunksRemoved = res.RowsAffected

	// Step 4: walk chunk files; delete any not in chunk_records (includes temp files)
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

- [ ] **Step 4: Run tests to confirm they pass**

```bash
cd /home/alex/miniprotector/src
go test ./storage/filesystem/ -run "TestStoreInfo|TestVacuum" -v
```

Expected: all PASS

- [ ] **Step 5: Run the full storage test suite**

```bash
cd /home/alex/miniprotector/src
go test ./storage/... -v
```

Expected: all PASS

- [ ] **Step 6: Verify the full build**

```bash
cd /home/alex/miniprotector/src
go build ./...
```

Expected: no errors

- [ ] **Step 7: Commit**

```bash
git add storage/filesystem/info.go storage/filesystem/store_test.go
git commit -m "implement StoreInfo and Vacuum with orphan cleanup"
```

---

## Self-Review Notes

**Spec coverage:**
- `ErrChunkNotFound` in `storage` package → Task 1 ✓
- GORM models → Task 2 ✓
- DB init with WAL → Task 2 ✓
- `store.go` with `db` field → Task 3 ✓
- `bwfs` uses interface → Task 3 ✓
- `ChunkExists` via `os.Stat` only → Task 4 ✓
- `StoreChunk` with hash verify + atomic write → Task 4 ✓
- `LinkChunkToFileData` with conflict ignore → Task 4 ✓
- `ReadChunk` → Task 4 ✓
- `FileDataExists` requires non-NULL checksum → Task 5 ✓
- `CreateFileData`, `FinalizeFileData`, `FileDataChunks` → Task 5 ✓
- All `FileVersion` methods → Task 6 ✓
- `StoreInfo`, `Vacuum` → Task 7 ✓

**Pre-existing broken test:** `workload/filesystem/chunker_test.go` references `chunk.Checksum()` which doesn't exist on the `workload.Chunk` interface. This was broken before this work and is out of scope. Do not fix it as part of this plan.

**Type consistency check:**
- `chunkPath` takes `string` (hex), all callers pass `hex.EncodeToString(chunkHash)` ✓
- `FileDataChunks` joins on `file_data_id = fileID` matching `LinkChunkToFileData`'s `FileDataID: fileID` ✓
- `FinalizeFileData` updates `WHERE file_id = ?` matching `CreateFileData`'s `FileID: fileID` ✓
- `toStorageFileVersion` maps all fields that `storage.FileVersion` defines ✓
