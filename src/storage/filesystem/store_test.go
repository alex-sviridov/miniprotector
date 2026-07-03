package filesystem

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

	// All five tables must exist (AutoMigrate creates them)
	assert.NoError(t, db.Exec("SELECT 1 FROM chunk_records LIMIT 1").Error)
	assert.NoError(t, db.Exec("SELECT 1 FROM file_data_records LIMIT 1").Error)
	assert.NoError(t, db.Exec("SELECT 1 FROM file_data_chunk_records LIMIT 1").Error)
	assert.NoError(t, db.Exec("SELECT 1 FROM file_version_records LIMIT 1").Error)
	assert.NoError(t, db.Exec("SELECT 1 FROM backup_job_records LIMIT 1").Error)
}

func TestEnsureBackupJob_CreatesRow(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, "host-a", record.SourceHost)
	assert.Nil(t, record.FinishedAt)
	assert.WithinDuration(t, time.Now(), record.StartedAt, 5*time.Second)
}

func TestEnsureBackupJob_SecondCallIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))
	require.NoError(t, store.EnsureBackupJob("job-1", "host-b"))

	var count int64
	require.NoError(t, store.db.Model(&BackupJobRecord{}).Where("job_id = ?", "job-1").Count(&count).Error)
	assert.Equal(t, int64(1), count)

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, "host-a", record.SourceHost, "first write should win")
}

func TestFinalizeBackupJob_SuccessSetsStatusAndFinishedAt(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta"), 100))

	changed, err := store.FinalizeBackupJob("job-1", true)
	require.NoError(t, err)
	assert.True(t, changed)

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, storage.JobStatusSuccess, record.Status)
	require.NotNil(t, record.FinishedAt)
	assert.WithinDuration(t, time.Now(), *record.FinishedAt, 5*time.Second)

	// file_versions must survive a success finalize
	var count int64
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-1").Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestFinalizeBackupJob_FailurePurgesOnlyThatJobsFileVersions(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))
	require.NoError(t, store.EnsureBackupJob("job-2", "host-a"))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta"), 100))
	require.NoError(t, store.EnsureFileVersion("job-2", "obj-2", []byte("meta"), 100))

	changed, err := store.FinalizeBackupJob("job-1", false)
	require.NoError(t, err)
	assert.True(t, changed)

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, storage.JobStatusFailure, record.Status)
	require.NotNil(t, record.FinishedAt)

	var job1Count, job2Count int64
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-1").Count(&job1Count).Error)
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-2").Count(&job2Count).Error)
	assert.Equal(t, int64(0), job1Count, "failed job's file_versions must be purged")
	assert.Equal(t, int64(1), job2Count, "other job's file_versions must be untouched")
}

func TestFinalizeBackupJob_SecondCallIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	changed1, err := store.FinalizeBackupJob("job-1", true)
	require.NoError(t, err)
	assert.True(t, changed1)

	var firstFinish BackupJobRecord
	require.NoError(t, store.db.First(&firstFinish, "job_id = ?", "job-1").Error)

	changed2, err := store.FinalizeBackupJob("job-1", false)
	require.NoError(t, err)
	assert.False(t, changed2, "job already finalized as success; a later failure call must be a no-op")

	var afterSecond BackupJobRecord
	require.NoError(t, store.db.First(&afterSecond, "job_id = ?", "job-1").Error)
	assert.Equal(t, storage.JobStatusSuccess, afterSecond.Status, "status must not flip on the no-op call")
	assert.Equal(t, firstFinish.FinishedAt.Unix(), afterSecond.FinishedAt.Unix())
}

func TestEnsureBackupJob_SetsInProgressStatus(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	var record BackupJobRecord
	require.NoError(t, store.db.First(&record, "job_id = ?", "job-1").Error)
	assert.Equal(t, storage.JobStatusInProgress, record.Status)
}

func TestGetBackupJob_ReturnsRecord(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-1", "host-a"))

	job, err := store.GetBackupJob("job-1")
	require.NoError(t, err)
	assert.Equal(t, "job-1", job.JobID)
	assert.Equal(t, "host-a", job.SourceHost)
	assert.Equal(t, storage.JobStatusInProgress, job.Status)
	assert.Nil(t, job.FinishedAt)
}

func TestGetBackupJob_NotFound(t *testing.T) {
	store := newTestStore(t)
	_, err := store.GetBackupJob("does-not-exist")
	assert.Error(t, err)
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

func TestMarkChunkCorrupted_RemovesFileFromDiskIfPresent(t *testing.T) {
	store := newTestStore(t)
	data := []byte("chunk data for corrupted-chunk test")
	hash := makeChunk(t, data)
	require.NoError(t, store.StoreChunk(hash, data))

	require.NoError(t, store.MarkChunkCorrupted(hash))

	hexHash := hex.EncodeToString(hash)
	chunkPath := filepath.Join(store.basePath, "chunks", hexHash[0:2], hexHash[2:4], hexHash[4:])
	_, err := os.Stat(chunkPath)
	assert.True(t, os.IsNotExist(err), "corrupted chunk file must be removed from disk")
}

func TestMarkChunkCorrupted_TolerantOfAlreadyMissingFile(t *testing.T) {
	store := newTestStore(t)
	data := []byte("chunk that is already gone from disk")
	hash := makeChunk(t, data)
	require.NoError(t, store.StoreChunk(hash, data))

	hexHash := hex.EncodeToString(hash)
	chunkPath := filepath.Join(store.basePath, "chunks", hexHash[0:2], hexHash[2:4], hexHash[4:])
	require.NoError(t, os.Remove(chunkPath))

	assert.NoError(t, store.MarkChunkCorrupted(hash), "must not error when the chunk file is already gone")
}

func TestMarkChunkCorrupted_InvalidatesDependentFileData(t *testing.T) {
	store := newTestStore(t)
	data := []byte("chunk shared by a finalized file")
	hash := makeChunk(t, data)

	require.NoError(t, store.CreateFileData("file-1", int64(len(data))))
	require.NoError(t, store.StoreChunk(hash, data))
	require.NoError(t, store.LinkChunkToFileData(hash, "file-1", 0))
	require.NoError(t, store.FinalizeFileData("file-1", []byte("checksum")))

	exists, err := store.FileDataExists("file-1")
	require.NoError(t, err)
	require.True(t, exists, "sanity check: file-1 should be finalized before corruption")

	require.NoError(t, store.MarkChunkCorrupted(hash))

	exists, err = store.FileDataExists("file-1")
	require.NoError(t, err)
	assert.False(t, exists, "file-1 must be re-uploaded on next backup after its chunk was marked corrupted")

	assert.ErrorIs(t, store.ChunkExists(hash), storage.ErrChunkNotFound)
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

func TestEnsureFileVersion_CreatesRow(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta"), 12345))

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("meta"), v.Metadata)
	assert.Equal(t, int64(12345), v.Ctime)
}

func TestEnsureFileVersion_DuplicateWithinJobIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("first"), 100))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("second"), 200))

	var count int64
	require.NoError(t, store.db.Model(&FileVersionRecord{}).
		Where("job_id = ? AND object_id = ?", "job-1", "obj-1").
		Count(&count).Error)
	assert.Equal(t, int64(1), count)

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), v.Metadata, "first write within a job should win")
}

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

func TestLatestFileVersion_ReturnsNewest(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta-old"), 100))
	require.NoError(t, store.EnsureFileVersion("job-2", "obj-1", []byte("meta-new"), 200))

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("meta-new"), v.Metadata)
	assert.Equal(t, int64(200), v.Ctime)
}

func TestRemoveFileVersion_Removes(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.db.Create(&FileVersionRecord{
		JobID: "job-1", ObjectID: "obj-1", Metadata: []byte("meta"), Ctime: 100, CreatedAt: time.Now(),
	}).Error)

	require.NoError(t, store.RemoveFileVersion("job-1", "obj-1"))

	_, err := store.LatestFileVersion("obj-1")
	assert.Error(t, err)
}

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

func TestStoreInfo_CountsCorrectly(t *testing.T) {
	store := newTestStore(t)

	data := []byte("a chunk of test data for info test")
	hash := makeChunk(t, data)
	require.NoError(t, store.StoreChunk(hash, data))
	require.NoError(t, store.CreateFileData("file-1", int64(len(data))))
	require.NoError(t, store.LinkChunkToFileData(hash, "file-1", 0))
	require.NoError(t, store.FinalizeFileData("file-1", []byte("checksum")))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", []byte("meta"), 0))

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
		UUID:      uuid.New().String(),
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

func TestNewReadOnly_CanOpenWhileExclusiveLockHeld(t *testing.T) {
	dir := t.TempDir()

	// Simulate a live server holding the exclusive lock
	server, err := New(dir)
	require.NoError(t, err)
	defer server.Close()

	// NewReadOnly must succeed despite the exclusive flock
	ro, err := NewReadOnly(dir)
	require.NoError(t, err)
	defer ro.Close()

	// RawDB must be non-nil and usable
	assert.NotNil(t, ro.RawDB())
	assert.NoError(t, ro.RawDB().Exec("SELECT 1").Error)
}

func TestNewReadOnly_CloseDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	ro, err := NewReadOnly(dir)
	require.NoError(t, err)
	assert.NoError(t, ro.Close()) // must not panic on nil lockFile
}

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

func TestFileVersionsForJob_ReturnsObjectIDsForThatJobOnly(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-a", []byte("meta"), 1))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-b", []byte("meta"), 2))
	require.NoError(t, store.EnsureFileVersion("job-2", "obj-c", []byte("meta"), 3))

	ids, err := store.FileVersionsForJob("job-1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"obj-a", "obj-b"}, ids)
}

func TestFileVersionsForJob_EmptyForUnknownJob(t *testing.T) {
	store := newTestStore(t)
	ids, err := store.FileVersionsForJob("no-such-job")
	require.NoError(t, err)
	assert.Empty(t, ids)
}

func TestFailStaleInProgressJobs_FlipsOnlyInProgressJobs(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureBackupJob("job-stale-1", "host-a"))
	require.NoError(t, store.EnsureBackupJob("job-stale-2", "host-a"))
	require.NoError(t, store.EnsureBackupJob("job-done", "host-a"))
	require.NoError(t, store.EnsureFileVersion("job-stale-1", "obj-1", []byte("meta"), 1))
	require.NoError(t, store.EnsureFileVersion("job-done", "obj-2", []byte("meta"), 1))

	changed, err := store.FinalizeBackupJob("job-done", true)
	require.NoError(t, err)
	require.True(t, changed)

	count, err := store.FailStaleInProgressJobs()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	var stale1, stale2, done BackupJobRecord
	require.NoError(t, store.db.First(&stale1, "job_id = ?", "job-stale-1").Error)
	require.NoError(t, store.db.First(&stale2, "job_id = ?", "job-stale-2").Error)
	require.NoError(t, store.db.First(&done, "job_id = ?", "job-done").Error)
	assert.Equal(t, storage.JobStatusFailure, stale1.Status)
	assert.Equal(t, storage.JobStatusFailure, stale2.Status)
	assert.Equal(t, storage.JobStatusSuccess, done.Status, "already-finalized job must be untouched")

	var staleVersions, doneVersions int64
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-stale-1").Count(&staleVersions).Error)
	require.NoError(t, store.db.Model(&FileVersionRecord{}).Where("job_id = ?", "job-done").Count(&doneVersions).Error)
	assert.Equal(t, int64(0), staleVersions, "stale job's file_versions must be purged")
	assert.Equal(t, int64(1), doneVersions, "already-succeeded job's file_versions must survive")
}

func TestFailStaleInProgressJobs_NoInProgressJobsReturnsZero(t *testing.T) {
	store := newTestStore(t)
	count, err := store.FailStaleInProgressJobs()
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}
