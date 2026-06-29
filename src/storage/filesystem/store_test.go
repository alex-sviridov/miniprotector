package filesystem

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

func TestErrChunkNotFoundIsSentinel(t *testing.T) {
	// Verify the sentinel exists and has the right message
	assert.EqualError(t, storage.ErrChunkNotFound, "chunk not found")
}

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
