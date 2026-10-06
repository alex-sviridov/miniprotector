package filesystem

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/storage/pack"
)

// Compaction tests use 100-byte chunks so every record is exactly recordSize
// bytes and a segment of segmentFor(n) holds exactly n records: the next
// append rotates.
const (
	chunkLen   = 100
	recordSize = pack.HeaderSize + chunkLen
	magicSize  = 8 // pack's segment magic
)

func segmentFor(records int) int64 { return magicSize + int64(records)*recordSize }

func newSmallStore(t *testing.T, recordsPerSegment int) *Store {
	t.Helper()
	s, err := newWithOptions(t.TempDir(), pack.Options{SegmentSize: segmentFor(recordsPerSegment)})
	require.NoError(t, err)
	t.Cleanup(func() { s.Close() })
	return s
}

// fixedChunk is a distinct chunkLen-byte payload for a name.
func fixedChunk(name string) []byte {
	return []byte(fmt.Sprintf("%-*s", chunkLen, name))
}

// storeRaw stores and flushes chunks with no file data or links; tests that
// call reclaimSegments directly delete rows by hand to make records dead.
func storeRaw(t *testing.T, s *Store, names ...string) [][]byte {
	t.Helper()
	var hashes [][]byte
	for _, n := range names {
		data := fixedChunk(n)
		h := makeChunk(t, data)
		require.NoError(t, s.StoreChunk(h, data))
		hashes = append(hashes, h)
	}
	require.NoError(t, s.flush())
	return hashes
}

func names(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return out
}

func dropRows(t *testing.T, s *Store, hashes [][]byte) {
	t.Helper()
	for _, h := range hashes {
		require.NoError(t, s.RawDB().Where("hash = ?", hex.EncodeToString(h)).Delete(&ChunkRecord{}).Error)
	}
}

func segmentExists(t *testing.T, s *Store, id int64) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(s.packDir(), segmentName(id)))
	if os.IsNotExist(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

// requireAllRowsReadable checks that every chunk row reads back verified.
func requireAllRowsReadable(t *testing.T, s *Store) {
	t.Helper()
	var rows []ChunkRecord
	require.NoError(t, s.RawDB().Find(&rows).Error)
	for _, r := range rows {
		h, err := hex.DecodeString(r.Hash)
		require.NoError(t, err)
		_, err = s.ReadChunk(h)
		require.NoError(t, err, "row %s (segment %d) is unreadable", r.Hash, r.Segment)
	}
}

// addFixedFile is addFile with fixedChunk payloads.
func addFixedFile(t *testing.T, s *Store, jobID, fileID string, expireAt int64, chunk string) []byte {
	t.Helper()
	require.NoError(t, s.EnsureBackupJob(jobID, "hosta"))
	require.NoError(t, s.CreateFileData(fileID, chunkLen))
	data := fixedChunk(chunk)
	h := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(h, data))
	require.NoError(t, s.LinkChunkToFileData(h, fileID, 0))
	require.NoError(t, s.FinalizeFileData(fileID, []byte{1}))
	require.NoError(t, s.EnsureFileVersion(jobID, fileID, "hosta", "/"+fileID, "f", nil, 1, expireAt))
	return h
}

func expireAll(t *testing.T, s *Store) {
	t.Helper()
	finish(t, s, "done")
	_, err := s.CleanupExpired(context.Background(), now, 100, false)
	require.NoError(t, err)
}

func TestReclaim_FullyDeadSealedSegmentIsRemovedButNeverTheActiveOne(t *testing.T) {
	s := newSmallStore(t, 4)
	for i := 0; i < 5; i++ { // 4 fill segment 1, the 5th opens segment 2
		addFixedFile(t, s, "done", fmt.Sprintf("f%d", i), now.Unix()-1, fmt.Sprintf("c%d", i))
	}
	require.Equal(t, uint32(2), s.log.ActiveSegment())
	expireAll(t, s)

	res, err := s.VacuumOnline(context.Background(), 100, time.Hour)
	require.NoError(t, err)

	assert.Equal(t, int64(5), res.OrphanedChunksRemoved)
	assert.Equal(t, int64(1), res.SegmentsRemoved)
	assert.Zero(t, res.SegmentsCompacted)
	assert.Equal(t, segmentFor(4), res.BytesReclaimed, "only the removed segment's bytes count")
	assert.False(t, segmentExists(t, s, 1))
	assert.True(t, segmentExists(t, s, 2), "the active segment stays even when all dead")
}

func TestReclaim_CompactsLowLiveSegmentAndLeavesMostlyLiveOneAlone(t *testing.T) {
	s := newSmallStore(t, 4)
	// Segment 1: one kept chunk of four (25% live).
	kept := addFixedFile(t, s, "done", "k0", 0, "k0")
	for i := 1; i < 4; i++ {
		addFixedFile(t, s, "done", fmt.Sprintf("e%d", i), now.Unix()-1, fmt.Sprintf("e%d", i))
	}
	// Segment 2: three kept of four (75% live).
	var mostlyLive [][]byte
	for i := 0; i < 3; i++ {
		mostlyLive = append(mostlyLive, addFixedFile(t, s, "done", fmt.Sprintf("m%d", i), 0, fmt.Sprintf("m%d", i)))
	}
	addFixedFile(t, s, "done", "x", now.Unix()-1, "x")
	addFixedFile(t, s, "done", "active", 0, "active") // opens segment 3
	require.Equal(t, uint32(3), s.log.ActiveSegment())
	before2 := chunkRow(t, s, mostlyLive[0])
	expireAll(t, s)

	res, err := s.VacuumOnline(context.Background(), 100, time.Hour)
	require.NoError(t, err)

	assert.Equal(t, int64(1), res.SegmentsCompacted)
	assert.Zero(t, res.SegmentsRemoved)
	assert.Equal(t, segmentFor(4)-recordSize, res.BytesReclaimed, "removed segment minus bytes copied")
	assert.False(t, segmentExists(t, s, 1))
	assert.True(t, segmentExists(t, s, 2), "a 75%-live segment is not worth compacting")

	got, err := s.ReadChunk(kept)
	require.NoError(t, err)
	assert.Equal(t, fixedChunk("k0"), got)
	assert.GreaterOrEqual(t, chunkRow(t, s, kept).Segment, int64(3), "the row points at the newer segment")
	assert.Equal(t, before2, chunkRow(t, s, mostlyLive[0]))
	requireAllRowsReadable(t, s)
}

func TestReclaim_OrphanRowsAloneReclaimNoBytes(t *testing.T) {
	s := newTestStore(t) // default segment size: everything stays in the active segment
	addFixedFile(t, s, "done", "gone", now.Unix()-1, "gone")
	expireAll(t, s)

	res, err := s.VacuumOnline(context.Background(), 100, time.Hour)
	require.NoError(t, err)

	assert.Equal(t, int64(1), res.OrphanedChunksRemoved)
	assert.Zero(t, res.BytesReclaimed, "dead bytes in a kept segment free nothing on disk")
	assert.Zero(t, res.SegmentsRemoved)
}

// cancelAfter is a context whose Err reports cancellation after it has been
// asked n times, so a test can stop a batch loop between two given batches.
type cancelAfter struct {
	context.Context
	n int
}

func (c *cancelAfter) Err() error {
	if c.n <= 0 {
		return context.Canceled
	}
	c.n--
	return nil
}

func TestReclaim_CompactsAcrossBatchesAndSurvivesCancellation(t *testing.T) {
	s := newSmallStore(t, 24)
	all := storeRaw(t, s, names("c", 24)...)
	storeRaw(t, s, "opens segment 2")
	require.Equal(t, uint32(2), s.log.ActiveSegment())
	live := all[:10]
	dropRows(t, s, all[10:]) // 10 of 24 live

	removed, compacted, _, err := s.reclaimSegments(&cancelAfter{Context: context.Background(), n: 2}, 2)
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, removed+compacted)
	assert.True(t, segmentExists(t, s, 1), "segment 1 still holds live records")
	var left int64
	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("segment = 1").Count(&left).Error)
	assert.Greater(t, left, int64(0))
	assert.Less(t, left, int64(10), "some batches moved records before the cancel")
	requireAllRowsReadable(t, s)

	removed, compacted, bytes, err := s.reclaimSegments(context.Background(), 2)
	require.NoError(t, err)
	assert.Zero(t, removed)
	assert.Equal(t, int64(1), compacted)
	assert.Equal(t, segmentFor(24)-left*recordSize, bytes)
	assert.False(t, segmentExists(t, s, 1))
	for i, h := range live {
		got, err := s.ReadChunk(h)
		require.NoError(t, err)
		assert.Equal(t, fixedChunk(fmt.Sprintf("c-%d", i)), got)
	}
	requireAllRowsReadable(t, s)
}

func TestReclaim_CorruptRecordIsDroppedLikeMarkChunkCorruptedAndCompactionContinues(t *testing.T) {
	s := newSmallStore(t, 6)
	require.NoError(t, s.CreateFileData("F", chunkLen))
	all := storeRaw(t, s, names("c", 6)...)
	bad, good := all[0], all[1]
	require.NoError(t, s.LinkChunkToFileData(bad, "F", 0))
	require.NoError(t, s.FinalizeFileData("F", []byte{1}))
	storeRaw(t, s, "opens segment 2")
	dropRows(t, s, all[2:]) // 2 of 6 live

	rec := chunkRow(t, s, bad)
	f, err := os.OpenFile(filepath.Join(s.packDir(), segmentName(rec.Segment)), os.O_RDWR, 0)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte{'X'}, rec.Offset+pack.HeaderSize)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	removed, compacted, _, err := s.reclaimSegments(context.Background(), 100)
	require.NoError(t, err)

	assert.Zero(t, removed)
	assert.Equal(t, int64(1), compacted)
	assert.False(t, segmentExists(t, s, 1))
	assert.False(t, chunkKnown(s, bad), "the corrupt chunk's row is gone")
	assert.Zero(t, countRows(t, s, &FileDataChunkRecord{}), "and its links")
	ok, err := s.FileDataExists("F")
	require.NoError(t, err)
	assert.False(t, ok, "a file that used it must be uploaded again")
	got, err := s.ReadChunk(good)
	require.NoError(t, err)
	assert.Equal(t, fixedChunk("c-1"), got)
}

func TestVacuum_RemovesStraySegmentsAndCompactsAtStartup(t *testing.T) {
	s := newSmallStore(t, 4)
	// Segment 1 has no rows at all: what a crash after compaction committed
	// but before the file was removed leaves behind.
	dropRows(t, s, storeRaw(t, s, names("stray", 4)...))
	// Segment 2 ends up 25% live once Vacuum drops the expired files' rows.
	kept := addFixedFile(t, s, "done", "k", 0, "k")
	for i := 0; i < 3; i++ {
		addFixedFile(t, s, "done", fmt.Sprintf("e%d", i), now.Unix()-1, fmt.Sprintf("e%d", i))
	}
	addFixedFile(t, s, "done", "active", 0, "active") // opens segment 3
	require.Equal(t, uint32(3), s.log.ActiveSegment())
	expireAll(t, s)

	res, err := s.Vacuum()
	require.NoError(t, err)

	assert.Equal(t, int64(1), res.SegmentsRemoved)
	assert.Equal(t, int64(1), res.SegmentsCompacted)
	assert.Equal(t, 2*segmentFor(4)-recordSize, res.BytesReclaimed)
	assert.False(t, segmentExists(t, s, 1))
	assert.False(t, segmentExists(t, s, 2))
	assert.True(t, segmentExists(t, s, 3))
	got, err := s.ReadChunk(kept)
	require.NoError(t, err)
	assert.Equal(t, fixedChunk("k"), got)
	requireAllRowsReadable(t, s)
}
