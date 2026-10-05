package filesystem

import (
	"context"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/storage"
)

// addFile records one complete file the way a backup does: job row, file
// data with its chunks stored and linked, finalized, and a version carrying
// expireAt (0 = none). Returns the chunk hashes.
func addFile(t *testing.T, s *Store, jobID, fileID string, expireAt int64, chunks ...string) [][]byte {
	t.Helper()
	require.NoError(t, s.EnsureBackupJob(jobID, "hosta"))
	require.NoError(t, s.CreateFileData(fileID, int64(len(chunks))))
	var hashes [][]byte
	for i, c := range chunks {
		h := makeChunk(t, []byte(c))
		require.NoError(t, s.StoreChunk(h, []byte(c)))
		require.NoError(t, s.LinkChunkToFileData(h, fileID, int64(i)))
		hashes = append(hashes, h)
	}
	require.NoError(t, s.FinalizeFileData(fileID, []byte{1, 2, 3, 4}))
	require.NoError(t, s.EnsureFileVersion(jobID, fileID, "hosta", "/"+fileID, "f", nil, 1, expireAt))
	return hashes
}

func finish(t *testing.T, s *Store, jobID string) {
	t.Helper()
	ok, err := s.FinalizeBackupJob(jobID, true)
	require.NoError(t, err)
	require.True(t, ok)
}

func versionCount(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.RawDB().Model(&FileVersionRecord{}).Count(&n).Error)
	return n
}

func deletions(t *testing.T, s *Store) []FileVersionDeletionRecord {
	t.Helper()
	var rows []FileVersionDeletionRecord
	require.NoError(t, s.RawDB().Order("seq ASC").Find(&rows).Error)
	return rows
}

func chunkFileExists(s *Store, hash []byte) bool {
	_, err := os.Stat(s.chunkPath(hex.EncodeToString(hash)))
	return err == nil
}

var now = time.Unix(1_000_000, 0)

func TestCleanupExpired_DeletesOnlyExpiredVersionsOfFinishedJobs(t *testing.T) {
	s := newTestStore(t)
	addFile(t, s, "done", "expired", now.Unix()-10, "a")
	addFile(t, s, "done", "boundary", now.Unix(), "b")
	addFile(t, s, "done", "future", now.Unix()+10, "c")
	addFile(t, s, "done", "never", 0, "d")
	finish(t, s, "done")
	addFile(t, s, "running", "expired-but-running", now.Unix()-10, "e") // job still in_progress

	res, err := s.CleanupExpired(context.Background(), now, 100, false)
	require.NoError(t, err)

	assert.Equal(t, int64(2), res.VersionsExpired) // expired + boundary (<=)
	var remaining []string
	require.NoError(t, s.RawDB().Model(&FileVersionRecord{}).Order("object_id").Pluck("object_id", &remaining).Error)
	assert.Equal(t, []string{"expired-but-running", "future", "never"}, remaining)
}

func TestCleanupExpired_RecordsDeletionsForReplication(t *testing.T) {
	s := newTestStore(t)
	addFile(t, s, "done", "gone", now.Unix()-1, "a")
	finish(t, s, "done")

	_, err := s.CleanupExpired(context.Background(), now, 100, false)
	require.NoError(t, err)

	rows := deletions(t, s)
	require.Len(t, rows, 1)
	assert.Equal(t, "done", rows[0].JobID)
	assert.Equal(t, "gone", rows[0].ObjectID)
	assert.NotZero(t, rows[0].DeletedAt)
	assert.Greater(t, rows[0].Seq, int64(0))
}

func TestCleanupExpired_BatchesUntilEverythingExpiredIsGone(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 7; i++ {
		addFile(t, s, "done", "f"+string(rune('a'+i)), now.Unix()-1, "c"+string(rune('a'+i)))
	}
	finish(t, s, "done")

	res, err := s.CleanupExpired(context.Background(), now, 3, false)
	require.NoError(t, err)

	assert.Equal(t, int64(7), res.VersionsExpired)
	assert.Equal(t, int64(0), versionCount(t, s))
	assert.Len(t, deletions(t, s), 7)
}

func TestCleanupExpired_DryRunReportsButDeletesNothing(t *testing.T) {
	s := newTestStore(t)
	addFile(t, s, "done", "gone", now.Unix()-1, "a")
	finish(t, s, "done")

	res, err := s.CleanupExpired(context.Background(), now, 100, true)
	require.NoError(t, err)

	assert.True(t, res.DryRun)
	assert.Equal(t, int64(1), res.VersionsExpired)
	assert.Equal(t, int64(1), versionCount(t, s))
	assert.Empty(t, deletions(t, s))
}

func TestCleanupExpired_CancelledContextStopsWithoutDeleting(t *testing.T) {
	s := newTestStore(t)
	addFile(t, s, "done", "gone", now.Unix()-1, "a")
	finish(t, s, "done")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := s.CleanupExpired(ctx, now, 100, false)

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, int64(1), versionCount(t, s))
}

func TestFinalizeBackupJobFailure_PurgeRecordsDeletions(t *testing.T) {
	s := newTestStore(t)
	addFile(t, s, "bad", "x", 0, "a")

	ok, err := s.FinalizeBackupJob("bad", false)
	require.NoError(t, err)
	require.True(t, ok)

	rows := deletions(t, s)
	require.Len(t, rows, 1)
	assert.Equal(t, "x", rows[0].ObjectID)
}

func TestFailStaleInProgressJobs_PurgeRecordsDeletions(t *testing.T) {
	s := newTestStore(t)
	addFile(t, s, "stale", "x", 0, "a")

	_, err := s.FailStaleInProgressJobs()
	require.NoError(t, err)

	assert.Len(t, deletions(t, s), 1)
}

func TestPruneDeletionLog_RemovesOnlyOlderRows(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.RawDB().Create(&[]FileVersionDeletionRecord{
		{JobID: "j", ObjectID: "old", DeletedAt: 100},
		{JobID: "j", ObjectID: "new", DeletedAt: 900},
	}).Error)

	n, err := s.PruneDeletionLog(context.Background(), time.Unix(500, 0))
	require.NoError(t, err)

	assert.Equal(t, int64(1), n)
	rows := deletions(t, s)
	require.Len(t, rows, 1)
	assert.Equal(t, "new", rows[0].ObjectID)
}

func TestVacuumOnline_KeepsEverythingStillReferencedByAVersion(t *testing.T) {
	s := newTestStore(t)
	hashes := addFile(t, s, "done", "f", 0, "chunk-1", "chunk-2")
	finish(t, s, "done")

	res, err := s.VacuumOnline(context.Background(), 100, time.Hour)
	require.NoError(t, err)

	assert.Equal(t, storage.VacuumResult{}, *res)
	for _, h := range hashes {
		assert.True(t, chunkFileExists(s, h))
	}
}

func TestVacuumOnline_ReclaimsDataLinksChunksAndFilesOfRemovedVersions(t *testing.T) {
	s := newTestStore(t)
	gone := addFile(t, s, "done", "gone", now.Unix()-1, "only-in-gone", "shared")
	kept := addFile(t, s, "done", "kept", 0, "shared", "only-in-kept")
	finish(t, s, "done")
	_, err := s.CleanupExpired(context.Background(), now, 100, false)
	require.NoError(t, err)

	res, err := s.VacuumOnline(context.Background(), 100, time.Hour)
	require.NoError(t, err)

	assert.Equal(t, int64(1), res.OrphanedFileDataRemoved)
	assert.Equal(t, int64(2), res.OrphanedChunkLinksRemoved)
	assert.Equal(t, int64(1), res.OrphanedChunksRemoved)
	assert.Equal(t, int64(len("only-in-gone")), res.BytesReclaimed)
	assert.False(t, chunkFileExists(s, gone[0]), "chunk used only by the removed file is deleted from disk")
	assert.True(t, chunkFileExists(s, gone[1]), "a chunk shared with a kept file stays")
	assert.True(t, chunkFileExists(s, kept[1]))
	ok, err := s.FileDataExists("kept")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = s.FileDataExists("gone")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestVacuumOnline_BatchesAndIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		addFile(t, s, "done", "f"+id, now.Unix()-1, "c"+id)
	}
	finish(t, s, "done")
	_, err := s.CleanupExpired(context.Background(), now, 100, false)
	require.NoError(t, err)

	first, err := s.VacuumOnline(context.Background(), 2, time.Hour)
	require.NoError(t, err)
	second, err := s.VacuumOnline(context.Background(), 2, time.Hour)
	require.NoError(t, err)

	assert.Equal(t, int64(5), first.OrphanedFileDataRemoved)
	assert.Equal(t, int64(5), first.OrphanedChunkLinksRemoved)
	assert.Equal(t, int64(5), first.OrphanedChunksRemoved)
	assert.Equal(t, storage.VacuumResult{}, *second)
}

func TestVacuumOnline_IncompleteFileDataOnlyRemovedAfterGrace(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.CreateFileData("recent", 1))
	require.NoError(t, s.CreateFileData("abandoned", 1))
	require.NoError(t, s.RawDB().Model(&FileDataRecord{}).Where("file_id = ?", "abandoned").
		Update("created_at", time.Now().Add(-48*time.Hour)).Error)

	res, err := s.VacuumOnline(context.Background(), 100, 24*time.Hour)
	require.NoError(t, err)

	assert.Equal(t, int64(1), res.IncompleteFileData)
	var ids []string
	require.NoError(t, s.RawDB().Model(&FileDataRecord{}).Pluck("file_id", &ids).Error)
	assert.Equal(t, []string{"recent"}, ids)
}

func TestVacuumOnline_LeavesAnInFlightFileAndItsChunksAlone(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.CreateFileData("inflight", 2))
	h := makeChunk(t, []byte("partial"))
	require.NoError(t, s.StoreChunk(h, []byte("partial")))
	require.NoError(t, s.LinkChunkToFileData(h, "inflight", 0)) // no version, no checksum yet

	res, err := s.VacuumOnline(context.Background(), 100, 24*time.Hour)
	require.NoError(t, err)

	assert.Equal(t, storage.VacuumResult{}, *res)
	assert.True(t, chunkFileExists(s, h))
}

func TestBackupOpGuard_ExcludesGCBatchesWhileAHandlerCallIsRunning(t *testing.T) {
	s := newTestStore(t)
	addFile(t, s, "done", "gone", now.Unix()-1, "a")
	finish(t, s, "done")

	release := s.BeginBackupOp()
	finished := make(chan struct{})
	go func() {
		_, _ = s.CleanupExpired(context.Background(), now, 100, false)
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("a cleanup batch ran while a backup operation held the guard")
	case <-time.After(150 * time.Millisecond):
	}
	assert.Equal(t, int64(1), versionCount(t, s))

	release()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not proceed after the guard was released")
	}
	assert.Equal(t, int64(0), versionCount(t, s))
}
