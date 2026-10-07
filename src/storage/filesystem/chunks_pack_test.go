package filesystem

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/storage/pack"
)

// readOnlyView opens a second, independent view of the store's database: what
// another process (or the store after a crash) would see. It shows only
// committed rows, never the writer's in-memory pending state.
func readOnlyView(t *testing.T, s *Store) *Store {
	t.Helper()
	ro, err := NewReadOnly(s.basePath)
	require.NoError(t, err)
	t.Cleanup(func() { ro.Close() })
	return ro
}

func countRows(t *testing.T, s *Store, model any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.RawDB().Model(model).Count(&n).Error)
	return n
}

// setFlushThreshold lowers the size trigger for one test.
func setFlushThreshold(t *testing.T, n int64) {
	t.Helper()
	old := flushThreshold
	flushThreshold = n
	t.Cleanup(func() { flushThreshold = old })
}

// copyDir copies a directory tree. Copying a live store simulates power loss:
// what is in the files (page cache included) is what survives.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	}))
}

// segmentName mirrors pack's file naming so tests can damage or move segments.
func segmentName(id int64) string {
	return fmt.Sprintf("%010d.pack", id)
}

func chunkRow(t *testing.T, s *Store, hash []byte) ChunkRecord {
	t.Helper()
	var rec ChunkRecord
	require.NoError(t, s.RawDB().Where("hash = ?", hex.EncodeToString(hash)).First(&rec).Error)
	return rec
}

func TestPack_PendingChunkIsVisibleBeforeAndAfterFlush(t *testing.T) {
	s := newTestStore(t)
	data := []byte("pending chunk data")
	hash := makeChunk(t, data)

	require.Error(t, s.StoreChunk(makeChunk(t, []byte("other")), data), "bad hash must still be rejected")

	require.NoError(t, s.StoreChunk(hash, data))
	assert.NoError(t, s.ChunkExists(hash), "pending chunk must be visible")
	got, err := s.ReadChunk(hash)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	require.NoError(t, s.flush())
	assert.NoError(t, s.ChunkExists(hash), "flushed chunk must be visible")
	got, err = s.ReadChunk(hash)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	rec := chunkRow(t, s, hash)
	assert.Equal(t, int64(len(data)), rec.Size)
	assert.Positive(t, rec.Segment)
	assert.Positive(t, rec.Offset)
}

func TestPack_RowsAndLinksAreCommittedOnlyAtFinalize(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk of file A")
	hash := makeChunk(t, data)
	require.NoError(t, s.CreateFileData("A", int64(len(data))))
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.LinkChunkToFileData(hash, "A", 0))

	ro := readOnlyView(t, s)
	assert.Zero(t, countRows(t, ro, &ChunkRecord{}), "no row may point at unsynced bytes")
	assert.Zero(t, countRows(t, ro, &FileDataChunkRecord{}), "links wait for the same commit")

	require.NoError(t, s.FinalizeFileData("A", []byte{1, 2, 3, 4}))

	assert.Equal(t, int64(1), countRows(t, ro, &ChunkRecord{}))
	assert.Equal(t, int64(1), countRows(t, ro, &FileDataChunkRecord{}))
	var fd FileDataRecord
	require.NoError(t, ro.RawDB().Where("file_id = ?", "A").First(&fd).Error)
	assert.Equal(t, []byte{1, 2, 3, 4}, fd.Checksum)
	assert.Equal(t, 1, fd.ChunkCount)
}

// TestPack_CrashLeavesNoDanglingRows emulates power loss: after the crash
// the unflushed tail of the active segment is gone, entirely or partway
// through a record. Copying the directory alone would keep those bytes (they
// are in the page cache), so the copy's segment is truncated by hand.
func TestPack_CrashLeavesNoDanglingRows(t *testing.T) {
	for _, tc := range []struct {
		name string
		keep int64 // bytes of the unflushed tail that survive
	}{
		{"whole unflushed tail lost", 0},
		{"torn inside the unflushed record", pack.HeaderSize + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			require.NoError(t, s.CreateFileData("A", 2))
			var aChunks [][]byte
			for i, c := range []string{"a-0", "a-1"} {
				h := makeChunk(t, []byte(c))
				require.NoError(t, s.StoreChunk(h, []byte(c)))
				require.NoError(t, s.LinkChunkToFileData(h, "A", int64(i)))
				aChunks = append(aChunks, h)
			}
			require.NoError(t, s.FinalizeFileData("A", []byte{9}))
			active := int64(s.log.ActiveSegment())
			durable := segmentSize(t, s.basePath, active)

			require.NoError(t, s.CreateFileData("B", 2))
			var unflushed [][]byte
			for i, c := range []string{"b-0 unflushed", "b-1 unflushed"} {
				h := makeChunk(t, []byte(c))
				require.NoError(t, s.StoreChunk(h, []byte(c)))
				require.NoError(t, s.LinkChunkToFileData(h, "B", int64(i)))
				unflushed = append(unflushed, h)
			}
			require.Greater(t, segmentSize(t, s.basePath, active), durable+tc.keep)

			crashed := t.TempDir()
			copyDir(t, s.basePath, crashed)
			require.NoError(t, os.Truncate(filepath.Join(crashed, "packs", segmentName(active)), durable+tc.keep))
			// The flock is per open file description, so the copy's lock file is free.
			re, err := New(crashed)
			require.NoError(t, err)
			t.Cleanup(func() { re.Close() })

			ok, err := re.FileDataExists("A")
			require.NoError(t, err)
			assert.True(t, ok, "finalized file survives the crash")
			var got int
			for hash, err := range re.FileDataChunks("A") {
				require.NoError(t, err)
				_, err := re.ReadChunk(hash)
				require.NoError(t, err)
				got++
			}
			assert.Equal(t, len(aChunks), got)

			for _, h := range unflushed {
				assert.ErrorIs(t, re.ChunkExists(h), storage.ErrChunkNotFound)
			}
			ok, err = re.FileDataExists("B")
			require.NoError(t, err)
			assert.False(t, ok)
			requireAllRowsReadable(t, re)
			var dangling int64
			require.NoError(t, re.RawDB().Model(&FileDataChunkRecord{}).
				Where("chunk_hash NOT IN (SELECT hash FROM chunk_records)").Count(&dangling).Error)
			assert.Zero(t, dangling, "no link may reference a chunk without a row")
		})
	}
}

func segmentSize(t *testing.T, base string, id int64) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(base, "packs", segmentName(id)))
	require.NoError(t, err)
	return info.Size()
}

func TestPack_SizeTriggerFlushesWithoutFinalize(t *testing.T) {
	setFlushThreshold(t, 100)
	s := newTestStore(t)
	ro := readOnlyView(t, s)

	first := make([]byte, 60)
	require.NoError(t, s.StoreChunk(makeChunk(t, first), first))
	assert.Zero(t, countRows(t, ro, &ChunkRecord{}), "below the threshold nothing is flushed")

	second := make([]byte, 60)
	second[0] = 1
	require.NoError(t, s.StoreChunk(makeChunk(t, second), second))
	assert.Equal(t, int64(2), countRows(t, ro, &ChunkRecord{}), "crossing the threshold flushes")
}

func TestPack_ReadDetectsCorruptionAndMarkRemovesEverything(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk that will rot on disk")
	hash := makeChunk(t, data)
	require.NoError(t, s.CreateFileData("A", int64(len(data))))
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.LinkChunkToFileData(hash, "A", 0))
	require.NoError(t, s.FinalizeFileData("A", []byte{1}))

	_, err := s.ReadChunk(makeChunk(t, []byte("never stored")))
	assert.ErrorIs(t, err, storage.ErrChunkNotFound)

	rec := chunkRow(t, s, hash)
	f, err := os.OpenFile(filepath.Join(s.packDir(), segmentName(rec.Segment)), os.O_RDWR, 0)
	require.NoError(t, err)
	_, err = f.WriteAt([]byte{'X'}, rec.Offset+pack.HeaderSize)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = s.ReadChunk(hash)
	assert.ErrorIs(t, err, pack.ErrCorrupt)

	require.NoError(t, s.MarkChunkCorrupted(hash))
	assert.ErrorIs(t, s.ChunkExists(hash), storage.ErrChunkNotFound)
	assert.Zero(t, countRows(t, s, &ChunkRecord{}))
	assert.Zero(t, countRows(t, s, &FileDataChunkRecord{}))
	ok, err := s.FileDataExists("A")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestPack_MarkCorruptedFlushesPendingFirst(t *testing.T) {
	s := newTestStore(t)
	data := []byte("pending then corrupted")
	hash := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.LinkChunkToFileData(hash, "A", 0))

	require.NoError(t, s.MarkChunkCorrupted(hash))

	assert.ErrorIs(t, s.ChunkExists(hash), storage.ErrChunkNotFound)
	assert.Zero(t, countRows(t, s, &FileDataChunkRecord{}), "a pending link must not resurface later")
	require.NoError(t, s.flush())
	assert.Zero(t, countRows(t, s, &ChunkRecord{}))
}

func TestPack_RowBeyondSegmentEndIsCorruptNotPanic(t *testing.T) {
	s := newTestStore(t)
	data := []byte("row pointing past the end")
	hash := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.flush())

	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(hash)).
		Update("offset", int64(1<<30)).Error)
	_, err := s.ReadChunk(hash)
	assert.ErrorIs(t, err, pack.ErrCorrupt)

	// Values that do not fit a Location must not wrap around silently.
	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(hash)).
		Updates(map[string]any{"segment": int64(1) << 40, "offset": int64(8)}).Error)
	_, err = s.ReadChunk(hash)
	assert.ErrorIs(t, err, pack.ErrCorrupt)
}

func TestPack_ReadLocatedRelocatesOnceWhenSegmentIsGone(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk moved by compaction")
	hash := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.flush())
	stale, ok, err := s.locate(hex.EncodeToString(hash))
	require.NoError(t, err)
	require.True(t, ok)

	// Simulate compaction: the record now lives in another segment (a copy
	// keeps the offset), the row was updated and the old segment removed.
	const moved = 7
	src := filepath.Join(s.packDir(), segmentName(int64(stale.Segment)))
	dst := filepath.Join(s.packDir(), segmentName(moved))
	raw, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, raw, 0o644))
	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(hash)).
		Update("segment", moved).Error)
	require.NoError(t, os.Remove(src))

	var sum [32]byte
	copy(sum[:], hash)
	got, err := s.readLocated(hex.EncodeToString(hash), sum, stale)
	require.NoError(t, err)
	assert.Equal(t, data, got)

	// If the re-located segment is gone too, the error surfaces.
	require.NoError(t, os.Remove(dst))
	_, err = s.readLocated(hex.EncodeToString(hash), sum, stale)
	assert.True(t, errors.Is(err, pack.ErrSegmentMissing), "got %v", err)
}

func TestPack_NewRejectsLegacyStore(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "chunks"), 0o755))

	_, err := New(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy")
}

func TestPack_ReadOnlyReadsPackedChunksAndWritesNothing(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk read through a read-only store")
	hash := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.flush())
	before, err := pack.Segments(s.packDir())
	require.NoError(t, err)

	ro, err := NewReadOnly(s.basePath)
	require.NoError(t, err)
	got, err := ro.ReadChunk(hash)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	assert.NoError(t, ro.ChunkExists(hash))
	require.NoError(t, ro.Close())

	after, err := pack.Segments(s.packDir())
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestPack_CloseFlushesPending(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	require.NoError(t, err)

	require.NoError(t, s.CreateFileData("A", 1))
	a := makeChunk(t, []byte("a"))
	require.NoError(t, s.StoreChunk(a, []byte("a")))
	require.NoError(t, s.LinkChunkToFileData(a, "A", 0))
	require.NoError(t, s.FinalizeFileData("A", []byte{1}))

	require.NoError(t, s.CreateFileData("B", 1))
	b := makeChunk(t, []byte("b"))
	require.NoError(t, s.StoreChunk(b, []byte("b")))
	require.NoError(t, s.LinkChunkToFileData(b, "B", 0))
	require.NoError(t, s.Close())

	re, err := New(dir)
	require.NoError(t, err)
	t.Cleanup(func() { re.Close() })

	ok, err := re.FileDataExists("A")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = re.FileDataExists("B")
	require.NoError(t, err)
	assert.False(t, ok, "an unfinalized file stays incomplete")

	// Close flushed B's chunk together with its link, so it is consistent.
	assert.NoError(t, re.ChunkExists(b))
	got, err := re.ReadChunk(b)
	require.NoError(t, err)
	assert.Equal(t, []byte("b"), got)
	var links int64
	require.NoError(t, re.RawDB().Model(&FileDataChunkRecord{}).Where("file_id = ?", "B").Count(&links).Error)
	assert.Equal(t, int64(1), links)
}

// The dangerous interleaving: a size-triggered flush (from another stream)
// commits a chunk row between StoreChunk and LinkChunkToFileData, so the row is
// in the database while its link is still pending. Vacuum must flush first or
// it would delete the row as an orphan and later commit a dangling link.
func pendingLinkForCommittedRow(t *testing.T, s *Store) []byte {
	t.Helper()
	require.NoError(t, s.CreateFileData("inflight", 1))
	h := makeChunk(t, []byte("in flight"))
	require.NoError(t, s.StoreChunk(h, []byte("in flight")))
	require.NoError(t, s.flush())
	require.NoError(t, s.LinkChunkToFileData(h, "inflight", 0))
	return h
}

func TestPack_VacuumFlushesFirstSoPendingLinksProtectChunks(t *testing.T) {
	s := newTestStore(t)
	h := pendingLinkForCommittedRow(t, s)

	_, err := s.Vacuum()
	require.NoError(t, err)

	assert.NoError(t, s.ChunkExists(h))
	assert.Equal(t, int64(1), countRows(t, s, &ChunkRecord{}))
	assert.Equal(t, int64(1), countRows(t, s, &FileDataChunkRecord{}))
}

func TestPack_VacuumOnlineFlushesFirstSoPendingLinksProtectChunks(t *testing.T) {
	s := newTestStore(t)
	h := pendingLinkForCommittedRow(t, s)

	_, err := s.VacuumOnline(context.Background(), 100, time.Hour)
	require.NoError(t, err)

	assert.NoError(t, s.ChunkExists(h))
	assert.Equal(t, int64(1), countRows(t, s, &ChunkRecord{}))
	assert.Equal(t, int64(1), countRows(t, s, &FileDataChunkRecord{}))
}

func TestPack_InvalidRowLocationReadsAsMissingAndStoreChunkRepairsIt(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk whose row got damaged")
	hash := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.flush())
	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(hash)).
		Update("segment", 0).Error)

	// The backup stream asks "do you have it?": the answer must be "no", so
	// the client sends the data, not an error that fails the stream.
	assert.ErrorIs(t, s.ChunkExists(hash), storage.ErrChunkNotFound)

	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.flush())
	assert.NoError(t, s.ChunkExists(hash))
	got, err := s.ReadChunk(hash)
	require.NoError(t, err)
	assert.Equal(t, data, got)
	assert.Equal(t, int64(1), countRows(t, s, &ChunkRecord{}))
}

func TestPack_NewReadOnlyRejectsLegacyStore(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "chunks"), 0o755))

	_, err = NewReadOnly(dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "legacy")
}

// setRelocateHook runs fn each time readLocated finds a segment missing,
// before it looks the chunk up again.
func setRelocateHook(t *testing.T, fn func()) {
	t.Helper()
	old := relocateHook
	relocateHook = fn
	t.Cleanup(func() { relocateHook = old })
}

// copySegment places a copy of segment src at id dst (same offsets).
func copySegment(t *testing.T, s *Store, src, dst int64) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.packDir(), segmentName(src)))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(s.packDir(), segmentName(dst)), raw, 0o644))
}

func setRowSegment(t *testing.T, s *Store, hash []byte, seg int64) {
	t.Helper()
	require.NoError(t, s.RawDB().Model(&ChunkRecord{}).Where("hash = ?", hex.EncodeToString(hash)).
		Update("segment", seg).Error)
}

func TestPack_ReadLocatedFollowsAChunkThatMovesTwice(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk compacted twice during one read")
	hash := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.flush())
	stale, ok, err := s.locate(hex.EncodeToString(hash))
	require.NoError(t, err)
	require.True(t, ok)

	// The reader holds segment 1's location. The chunk already moved to 7
	// (and 7 is gone again); while the reader re-locates it moves on to 8.
	copySegment(t, s, int64(stale.Segment), 8)
	setRowSegment(t, s, hash, 7)
	require.NoError(t, os.Remove(filepath.Join(s.packDir(), segmentName(int64(stale.Segment)))))
	calls := 0
	setRelocateHook(t, func() {
		calls++
		if calls == 2 {
			setRowSegment(t, s, hash, 8)
		}
	})

	var sum [32]byte
	copy(sum[:], hash)
	got, err := s.readLocated(hex.EncodeToString(hash), sum, stale)
	require.NoError(t, err)
	assert.Equal(t, data, got)
}

func TestPack_ReadLocatedGivesUpAfterBoundedAttempts(t *testing.T) {
	s := newTestStore(t)
	data := []byte("chunk that keeps moving to missing segments")
	hash := makeChunk(t, data)
	require.NoError(t, s.StoreChunk(hash, data))
	require.NoError(t, s.flush())
	stale, _, err := s.locate(hex.EncodeToString(hash))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(s.packDir(), segmentName(int64(stale.Segment)))))

	calls := 0
	setRelocateHook(t, func() {
		calls++
		setRowSegment(t, s, hash, int64(100+calls)) // always a new, missing segment
	})

	var sum [32]byte
	copy(sum[:], hash)
	_, err = s.readLocated(hex.EncodeToString(hash), sum, stale)
	assert.ErrorIs(t, err, pack.ErrSegmentMissing)
	assert.Equal(t, maxReadAttempts-1, calls, "one re-locate per retry, no more")
}
