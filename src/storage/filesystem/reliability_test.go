package filesystem

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/storage/pack"
)

// makeUnreadable chmods a segment to 000 for the test, so opening it fails
// with a permission error: an I/O failure that says nothing about the data.
func makeUnreadable(t *testing.T, s *Store, id int64) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	p := filepath.Join(s.packDir(), segmentName(id))
	require.NoError(t, os.Chmod(p, 0))
	t.Cleanup(func() { os.Chmod(p, 0o644) })
}

func storedChunk(t *testing.T, s *Store, data []byte) []byte {
	t.Helper()
	h := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(h, data))
	require.NoError(t, s.flush())
	return h
}

func TestReadChunk_ClassifiesDataLossAsCorruptButNotTransientErrors(t *testing.T) {
	t.Run("flipped byte", func(t *testing.T) {
		s := newTestStore(t)
		h := storedChunk(t, s, []byte("rotting chunk"))
		rec := chunkRow(t, s, h)
		f, err := os.OpenFile(filepath.Join(s.packDir(), segmentName(rec.Segment)), os.O_RDWR, 0)
		require.NoError(t, err)
		_, err = f.WriteAt([]byte{'X'}, rec.Offset+pack.HeaderSize)
		require.NoError(t, err)
		require.NoError(t, f.Close())

		_, err = s.ReadChunk(h)
		assert.ErrorIs(t, err, storage.ErrChunkCorrupt)
		assert.ErrorIs(t, err, pack.ErrCorrupt)
	})
	t.Run("invalid row", func(t *testing.T) {
		s := newTestStore(t)
		h := storedChunk(t, s, []byte("damaged row"))
		require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(h)).
			Update("segment", 0).Error)

		_, err := s.ReadChunk(h)
		assert.ErrorIs(t, err, storage.ErrChunkCorrupt)
	})
	t.Run("missing segment", func(t *testing.T) {
		s := newSmallStore(t, 1)
		h := storeRaw(t, s, "in segment 1")[0]
		storeRaw(t, s, "opens segment 2")
		require.NoError(t, os.Remove(filepath.Join(s.packDir(), segmentName(1))))

		_, err := s.ReadChunk(h)
		assert.ErrorIs(t, err, storage.ErrChunkCorrupt)
		assert.ErrorIs(t, err, pack.ErrSegmentMissing)
	})
	t.Run("unreadable segment is not corrupt", func(t *testing.T) {
		s := newSmallStore(t, 1)
		h := storeRaw(t, s, "in segment 1")[0]
		storeRaw(t, s, "opens segment 2")
		makeUnreadable(t, s, 1)

		_, err := s.ReadChunk(h)
		require.Error(t, err)
		assert.NotErrorIs(t, err, storage.ErrChunkCorrupt)
		assert.NotErrorIs(t, err, storage.ErrChunkNotFound)
	})
}

func TestReclaim_UnreadableSegmentIsSkippedAndTheRunContinues(t *testing.T) {
	s := newSmallStore(t, 4)
	first := storeRaw(t, s, names("one", 4)...)  // segment 1
	second := storeRaw(t, s, names("two", 4)...) // segment 2
	storeRaw(t, s, "opens segment 3")
	dropRows(t, s, first[1:])
	dropRows(t, s, second[1:])
	makeUnreadable(t, s, 1)
	before := chunkRow(t, s, first[0])

	removed, compacted, _, err := s.reclaimSegments(context.Background(), 100)

	require.Error(t, err, "a skipped segment must still be reported")
	assert.Contains(t, err.Error(), "segment 1")
	assert.Zero(t, removed)
	assert.Equal(t, int64(1), compacted, "segment 2 is compacted despite segment 1")
	assert.False(t, segmentExists(t, s, 2))
	assert.True(t, segmentExists(t, s, 1))
	assert.Equal(t, before, chunkRow(t, s, first[0]), "the failed batch was rolled back")

	// Once the disk is readable again a later run finishes the job.
	require.NoError(t, os.Chmod(filepath.Join(s.packDir(), segmentName(1)), 0o644))
	_, compacted, _, err = s.reclaimSegments(context.Background(), 100)
	require.NoError(t, err)
	assert.Equal(t, int64(1), compacted)
	requireAllRowsReadable(t, s)
}

func TestVacuum_ReclaimFailureIsReportedSeparatelyFromDatabaseFailure(t *testing.T) {
	s := newSmallStore(t, 4)
	addFixedFile(t, s, "done", "k", 0, "k")
	for i := 0; i < 3; i++ {
		addFixedFile(t, s, "done", fmt.Sprintf("e%d", i), now.Unix()-1, fmt.Sprintf("e%d", i))
	}
	addFixedFile(t, s, "done", "active", 0, "active") // opens segment 2
	expireAll(t, s)
	makeUnreadable(t, s, 1)

	res, err := s.Vacuum()

	require.ErrorIs(t, err, storage.ErrReclaimIncomplete)
	require.NotNil(t, res, "the committed database cleanup is still reported")
	assert.Equal(t, int64(3), res.OrphanedChunksRemoved)
}

func TestOpenDB_UsesFullSynchronous(t *testing.T) {
	s := newTestStore(t)
	var mode int
	require.NoError(t, s.RawDB().Raw("PRAGMA synchronous").Scan(&mode).Error)
	assert.Equal(t, 2, mode, "synchronous must be FULL (2)")
}

func TestPlanSegments_UsesACoveringIndex(t *testing.T) {
	s := newTestStore(t)
	var plan []struct{ Detail string }
	require.NoError(t, s.RawDB().Raw("EXPLAIN QUERY PLAN SELECT segment, SUM(? + size) AS live FROM chunk_records GROUP BY segment",
		pack.HeaderSize).Scan(&plan).Error)
	var details []string
	for _, p := range plan {
		details = append(details, p.Detail)
	}
	assert.True(t, strings.Contains(strings.Join(details, "; "), "COVERING INDEX"),
		"segment stats must not scan the table: %v", details)
}

func TestFinalizeFileData_FailsWhenTheFileDataVanished(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.CreateFileData("F", 1))
	h := makeChunk(t, []byte("x"))
	require.NoError(t, s.StoreChunk(h, []byte("x")))
	require.NoError(t, s.LinkChunkToFileData(h, "F", 0))
	// Corruption handling dropped the chunk and flagged the file in transfer.
	require.NoError(t, s.MarkChunkCorrupted(h))

	err := s.FinalizeFileData("F", []byte{1})
	require.Error(t, err, "finalizing a file whose data vanished must not report success")
}

func TestFinalizeFileData_DuplicateConcurrentTransferStillSucceeds(t *testing.T) {
	s := newTestStore(t)
	// Two streams transfer the same file: both create a row, the first
	// finalize completes both, the second must not fail its stream.
	require.NoError(t, s.CreateFileData("F", 1))
	require.NoError(t, s.CreateFileData("F", 1))
	require.NoError(t, s.FinalizeFileData("F", []byte{1}))
	require.NoError(t, s.FinalizeFileData("F", []byte{1}))
}

func TestOpenDB_AppliesBusyTimeout(t *testing.T) {
	s := newTestStore(t)
	var ms int
	require.NoError(t, s.RawDB().Raw("PRAGMA busy_timeout").Scan(&ms).Error)
	assert.Equal(t, 5000, ms, "writers from another connection (restore's read-only store) must wait, not fail with SQLITE_BUSY")
}
