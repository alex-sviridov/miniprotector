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

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/storage/pack"
)

// writeFile stores a finalized file whose chunk i holds payloads[i] and
// returns the chunk hashes in link order.
func writeFile(t *testing.T, s *Store, fileID string, payloads ...[]byte) [][]byte {
	t.Helper()
	require.NoError(t, s.CreateFileData(fileID, 1))
	var hashes [][]byte
	for i, data := range payloads {
		h := makeChunk(t, data)
		require.NoError(t, s.StoreChunk(h, data))
		require.NoError(t, s.LinkChunkToFileData(h, fileID, int64(i)))
		hashes = append(hashes, h)
	}
	require.NoError(t, s.FinalizeFileData(fileID, []byte{1}))
	return hashes
}

func TestLocateFileChunks_MatchesPerChunkReadInLinkOrder(t *testing.T) {
	s := newTestStore(t)
	payloads := [][]byte{[]byte("zero"), []byte("one"), []byte("two"), []byte("zero"), []byte("four")}
	hashes := writeFile(t, s, "A", payloads...)
	writeFile(t, s, "B", []byte("other file"))
	ro := readOnlyView(t, s)

	chunks, err := ro.LocateFileChunks("A")
	require.NoError(t, err)

	require.Len(t, chunks, len(payloads), "a chunk used twice is listed at both indexes")
	for i, c := range chunks {
		assert.Equal(t, int64(i), c.Index)
		assert.Equal(t, hashes[i], c.Hash)
		got, err := ro.ReadLocatedChunk(c)
		require.NoError(t, err)
		want, err := ro.ReadChunk(hashes[i])
		require.NoError(t, err)
		assert.Equal(t, want, got)
		assert.Equal(t, payloads[i], got)
	}
}

func TestLocateFileChunks_UnknownFileHasNoChunks(t *testing.T) {
	s := newTestStore(t)
	chunks, err := s.LocateFileChunks("nope")
	require.NoError(t, err)
	assert.Empty(t, chunks)
}

func TestLocateFileChunks_MissingChunkRowIsNotFoundInPlace(t *testing.T) {
	s := newTestStore(t)
	hashes := writeFile(t, s, "A", []byte("first"), []byte("dropped meanwhile"), []byte("last"))
	// A link whose chunk row is gone: what a restore sees when a backup
	// linked a chunk that was dropped concurrently.
	require.NoError(t, s.RawDB().Where("hash = ?", hex.EncodeToString(hashes[1])).Delete(&ChunkRecord{}).Error)
	ro := readOnlyView(t, s)

	chunks, err := ro.LocateFileChunks("A")
	require.NoError(t, err)

	require.Len(t, chunks, 3, "the link must not be dropped from the list")
	assert.Equal(t, int64(1), chunks[1].Index)
	assert.Equal(t, hashes[1], chunks[1].Hash)
	_, err = ro.ReadLocatedChunk(chunks[1])
	assert.ErrorIs(t, err, storage.ErrChunkNotFound)
	for _, i := range []int{0, 2} {
		_, err := ro.ReadLocatedChunk(chunks[i])
		require.NoError(t, err)
	}
}

func TestLocateFileChunks_InvalidRowLocationReadsAsCorrupt(t *testing.T) {
	s := newTestStore(t)
	hashes := writeFile(t, s, "A", []byte("row with a broken location"))
	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(hashes[0])).
		Update("segment", int64(1)<<40).Error)

	chunks, err := s.LocateFileChunks("A")
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	_, err = s.ReadLocatedChunk(chunks[0])
	assert.ErrorIs(t, err, storage.ErrChunkCorrupt)
	assert.ErrorIs(t, err, pack.ErrCorrupt)
}

func TestReadLocatedChunk_CorruptBytesAreChunkCorrupt(t *testing.T) {
	s := newTestStore(t)
	hashes := writeFile(t, s, "A", []byte("chunk that will rot on disk"))
	rec := chunkRow(t, s, hashes[0])
	f, err := os.OpenFile(filepath.Join(s.packDir(), segmentName(rec.Segment)), os.O_RDWR, 0)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte{'X'}, rec.Offset+pack.HeaderSize)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	chunks, err := s.LocateFileChunks("A")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	_, err = s.ReadLocatedChunk(chunks[0])
	assert.ErrorIs(t, err, storage.ErrChunkCorrupt)
	assert.ErrorIs(t, err, pack.ErrCorrupt)
}

// A restore does not hold the store's guard, so compaction can move a chunk
// after the restore located it. The read must follow the chunk to its new
// segment instead of reporting the stale location as lost.
func TestReadLocatedChunk_StaleSnapshotFollowsCompaction(t *testing.T) {
	s := newSmallStore(t, 4)
	// Segment 1: the restored file's chunk plus three that expire (25% live).
	kept := addFixedFile(t, s, "done", "k0", 0, "k0")
	for i := 1; i < 4; i++ {
		addFixedFile(t, s, "done", fmt.Sprintf("e%d", i), now.Unix()-1, fmt.Sprintf("e%d", i))
	}
	addFixedFile(t, s, "done", "active", 0, "active") // opens segment 2
	ro := readOnlyView(t, s)
	chunks, err := ro.LocateFileChunks("k0")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.Equal(t, int64(1), chunkRow(t, s, kept).Segment)

	expireAll(t, s)
	res, err := s.VacuumOnline(context.Background(), 100, time.Hour)
	require.NoError(t, err)
	require.Equal(t, int64(1), res.SegmentsCompacted)
	require.False(t, segmentExists(t, s, 1), "the snapshot's segment is gone")

	got, err := ro.ReadLocatedChunk(chunks[0])
	require.NoError(t, err)
	assert.Equal(t, fixedChunk("k0"), got)
	ok, err := s.FileDataExists("k0")
	require.NoError(t, err)
	assert.True(t, ok, "a healthy chunk must not be dropped")
}

// When the segment is gone and the row still points there, the chunk really
// is lost; that is reported as corrupt, as ReadChunk does.
func TestReadLocatedChunk_SegmentGoneAndNotMovedIsCorrupt(t *testing.T) {
	s := newTestStore(t)
	hashes := writeFile(t, s, "A", []byte("chunk whose segment vanished"))
	chunks, err := s.LocateFileChunks("A")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.NoError(t, os.Remove(filepath.Join(s.packDir(), segmentName(chunkRow(t, s, hashes[0]).Segment))))

	_, err = s.ReadLocatedChunk(chunks[0])
	assert.ErrorIs(t, err, storage.ErrChunkCorrupt)
	assert.ErrorIs(t, err, pack.ErrSegmentMissing)
}

// The snapshot's "not found" is not final: a backup may store the chunk
// again after LocateFileChunks, and a healthy chunk must then be read, not
// reported lost (restore would mark it).
func TestReadLocatedChunk_RowCommittedAfterSnapshotIsRead(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk stored again after the snapshot")
	hashes := writeFile(t, s, "A", data)
	row := chunkRow(t, s, hashes[0])
	require.NoError(t, s.RawDB().Where("hash = ?", row.Hash).Delete(&ChunkRecord{}).Error)
	ro := readOnlyView(t, s)
	chunks, err := ro.LocateFileChunks("A")
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	require.NoError(t, s.RawDB().Create(&row).Error)

	got, err := ro.ReadLocatedChunk(chunks[0])
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestReadLocatedChunk_RowRepairedAfterSnapshotIsRead(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk whose broken row gets repaired")
	hashes := writeFile(t, s, "A", data)
	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(hashes[0])).
		Update("segment", int64(1)<<40).Error)
	ro := readOnlyView(t, s)
	chunks, err := ro.LocateFileChunks("A")
	require.NoError(t, err)
	require.Len(t, chunks, 1)

	// StoreChunk treats the invalid row as unknown, appends the chunk again
	// and the flush overwrites the row with the new location.
	require.NoError(t, s.StoreChunk(hashes[0], data))
	require.NoError(t, s.flush())

	got, err := ro.ReadLocatedChunk(chunks[0])
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

// Like locate, LocateFileChunks prefers a pending location over the row: a
// chunk re-appended to repair an invalid row is pending until the flush.
func TestLocateFileChunks_PendingLocationWinsOverRow(t *testing.T) {
	s := newTestStore(t)
	data := []byte("re-appended, not yet flushed")
	hashes := writeFile(t, s, "A", data)
	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(hashes[0])).
		Update("segment", int64(1)<<40).Error)
	require.NoError(t, s.StoreChunk(hashes[0], data))

	chunks, err := s.LocateFileChunks("A")
	require.NoError(t, err)
	require.Len(t, chunks, 1)
	require.NoError(t, chunks[0].err, "the pending location is used, not the invalid row")
	got, err := s.ReadLocatedChunk(chunks[0])
	require.NoError(t, err)
	assert.Equal(t, data, got)
}
