package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"lukechampine.com/blake3"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/alex-sviridov/miniprotector/storage/pack"
)

type fakeChunkSource struct {
	readErr error
	marked  int
}

func (f *fakeChunkSource) ReadLocatedChunk(wfs.FileChunk) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	return []byte("data"), nil
}

func (f *fakeChunkSource) MarkChunkCorrupted([]byte) error {
	f.marked++
	return nil
}

func TestReadRestoreChunk_MarksOnlyLostChunks(t *testing.T) {
	chunk := wfs.FileChunk{Hash: []byte{0xab}}
	for _, tc := range []struct {
		name    string
		readErr error
		mark    bool
	}{
		{"corrupt", fmt.Errorf("read chunk: %w", storage.ErrChunkCorrupt), true},
		// Not found keeps marking: it is how a restore that races a backup
		// linking a just-dropped chunk invalidates the affected file.
		{"not found", storage.ErrChunkNotFound, true},
		{"transient I/O", errors.New("open 0000000001.pack: too many open files"), false},
		{"database busy", errors.New("database is locked (5) (SQLITE_BUSY)"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeChunkSource{readErr: tc.readErr}

			_, err := readRestoreChunk(src, quietLogger(), chunk)

			require.ErrorIs(t, err, tc.readErr)
			if tc.mark {
				assert.Equal(t, 1, src.marked)
			} else {
				assert.Zero(t, src.marked, "a possibly transient error must never drop data")
			}
		})
	}

	src := &fakeChunkSource{}
	data, err := readRestoreChunk(src, quietLogger(), chunk)
	require.NoError(t, err)
	assert.Equal(t, []byte("data"), data)
	assert.Zero(t, src.marked)
}

// restoreStream collects what RestoreFile sends; onChunk, if set, runs after
// each chunk event is received.
type restoreStream struct {
	grpc.ServerStream
	events  int // every event, the meta included
	chunks  []*pb.RestoreChunk
	onChunk func(n int)
}

func (r *restoreStream) Send(ev *pb.RestoreEvent) error {
	r.events++
	if c := ev.GetChunk(); c != nil {
		r.chunks = append(r.chunks, c)
		if r.onChunk != nil {
			r.onChunk(len(r.chunks))
		}
	}
	return nil
}

// restoreFixture is a store holding one finalized file, served by a restore
// server over a read-only view of the store, as bwfs does.
type restoreFixture struct {
	dir      string
	writer   *wfs.Store
	server   *restoreServer
	fileID   string
	uuid     string
	payloads [][]byte
	hashes   [][]byte
}

func newRestoreFixture(t *testing.T, nChunks int) *restoreFixture {
	t.Helper()
	f := &restoreFixture{dir: t.TempDir(), fileID: "fs://host:f:/restored:1"}
	var err error
	f.writer, err = wfs.New(f.dir)
	require.NoError(t, err)
	t.Cleanup(func() { f.writer.Close() })

	require.NoError(t, f.writer.CreateFileData(f.fileID, 1))
	for i := range nChunks {
		data := []byte(fmt.Sprintf("restore chunk %d", i))
		sum := blake3.Sum256(data)
		require.NoError(t, f.writer.StoreChunk(sum[:], data))
		require.NoError(t, f.writer.LinkChunkToFileData(sum[:], f.fileID, int64(i)))
		f.payloads = append(f.payloads, data)
		f.hashes = append(f.hashes, sum[:])
	}
	require.NoError(t, f.writer.FinalizeFileData(f.fileID, []byte{1, 2, 3, 4}))
	var uuids []string
	require.NoError(t, f.writer.RawDB().Table("file_data_records").
		Where("file_id = ?", f.fileID).Pluck("uuid", &uuids).Error)
	require.Len(t, uuids, 1)
	f.uuid = uuids[0]

	ro, err := wfs.NewReadOnly(f.dir)
	require.NoError(t, err)
	t.Cleanup(func() { ro.Close() })
	f.server = NewRestoreServer(ro, quietLogger())
	return f
}

func (f *restoreFixture) restore(t *testing.T, stream *restoreStream) error {
	t.Helper()
	return f.server.RestoreFile(&pb.RestoreRequest{FileUuid: f.uuid}, stream)
}

func (f *restoreFixture) fileStillValid(t *testing.T) bool {
	t.Helper()
	ok, err := f.writer.FileDataExists(f.fileID)
	require.NoError(t, err)
	return ok
}

// fileDamaged reports whether the restored file's FileData is flagged.
func (f *restoreFixture) fileDamaged(t *testing.T) bool {
	t.Helper()
	var n int64
	require.NoError(t, f.writer.RawDB().Table("file_data_records").
		Where("uuid = ? AND damaged_at IS NOT NULL", f.uuid).Count(&n).Error)
	return n == 1
}

// chunkIndexed reports whether chunk i still has its chunk row.
func (f *restoreFixture) chunkIndexed(t *testing.T, i int) bool {
	t.Helper()
	var n int64
	require.NoError(t, f.writer.RawDB().Table("chunk_records").
		Where("hash = ?", hex.EncodeToString(f.hashes[i])).Count(&n).Error)
	return n == 1
}

func (f *restoreFixture) segmentPath(id int64) string {
	return filepath.Join(f.dir, "packs", fmt.Sprintf("%010d.pack", id))
}

func (f *restoreFixture) chunkSegment(t *testing.T, i int) int64 {
	t.Helper()
	var seg []int64
	require.NoError(t, f.writer.RawDB().Table("chunk_records").
		Where("hash = ?", hex.EncodeToString(f.hashes[i])).Pluck("segment", &seg).Error)
	require.Len(t, seg, 1)
	return seg[0]
}

func TestRestoreFile_SendsEveryChunkInOrder(t *testing.T) {
	f := newRestoreFixture(t, 4)
	stream := &restoreStream{}

	require.NoError(t, f.restore(t, stream))

	require.Len(t, stream.chunks, 4)
	for i, c := range stream.chunks {
		assert.Equal(t, int64(i), c.Index)
		assert.Equal(t, f.hashes[i], c.Hash)
		assert.Equal(t, f.payloads[i], c.Data)
		assert.Equal(t, i == 3, c.Eof)
	}
	assert.True(t, f.fileStillValid(t))
}

func TestRestoreFile_MissingChunkRowIsMarkedAndFailsAtItsPosition(t *testing.T) {
	f := newRestoreFixture(t, 3)
	require.NoError(t, f.writer.RawDB().Table("chunk_records").
		Where("hash = ?", hex.EncodeToString(f.hashes[1])).Delete(nil).Error)
	stream := &restoreStream{}

	err := f.restore(t, stream)

	assert.Equal(t, codes.DataLoss, status.Code(err), "a lost chunk is final, not a retryable Internal: %v", err)
	assert.Len(t, stream.chunks, 1, "the chunk before the missing one is sent, nothing after")
	assert.False(t, f.fileStillValid(t), "marking invalidates the file so the next backup uploads it")
	assert.True(t, f.fileDamaged(t), "the file is flagged, not deleted")
}

func TestRestoreFile_CorruptChunkIsMarked(t *testing.T) {
	f := newRestoreFixture(t, 3)
	var offset []int64
	require.NoError(t, f.writer.RawDB().Table("chunk_records").
		Where("hash = ?", hex.EncodeToString(f.hashes[1])).Pluck("offset", &offset).Error)
	seg, err := os.OpenFile(f.segmentPath(f.chunkSegment(t, 1)), os.O_RDWR, 0)
	require.NoError(t, err)
	_, err = seg.WriteAt([]byte{'X'}, offset[0]+pack.HeaderSize)
	require.NoError(t, err)
	require.NoError(t, seg.Close())
	stream := &restoreStream{}

	err = f.restore(t, stream)

	require.ErrorContains(t, err, "corrupt")
	assert.Equal(t, codes.DataLoss, status.Code(err), "a corrupt chunk is final, not a retryable Internal: %v", err)
	assert.Len(t, stream.chunks, 1)
	assert.False(t, f.fileStillValid(t))
	assert.True(t, f.fileDamaged(t), "the file is flagged, not deleted")
	assert.False(t, f.chunkIndexed(t, 1), "the corrupt chunk is marked (dropped)")
	assert.True(t, f.chunkIndexed(t, 0), "healthy chunks stay")
}

// Restoring a version already flagged damaged fails at once with DataLoss,
// before the meta event: the client must not start writing a file that can
// never be completed.
func TestRestoreFile_DamagedFileDataFailsWithDataLossBeforeAnyEvent(t *testing.T) {
	f := newRestoreFixture(t, 3)
	require.NoError(t, f.writer.MarkChunkCorrupted(f.hashes[2]))
	require.True(t, f.fileDamaged(t))
	stream := &restoreStream{}

	err := f.restore(t, stream)

	assert.Equal(t, codes.DataLoss, status.Code(err), "got %v", err)
	assert.ErrorContains(t, err, "backup data damaged")
	assert.Zero(t, stream.events, "no event, not even the meta, is sent")
}

// A read error that may be transient (here: permission denied on the
// segment) must not be reported as data loss, and must never drop data.
func TestRestoreFile_TransientReadErrorIsInternalAndMarksNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	f := newRestoreFixture(t, 3)
	seg := f.segmentPath(f.chunkSegment(t, 1))
	t.Cleanup(func() { os.Chmod(seg, 0o644) })
	stream := &restoreStream{onChunk: func(n int) {
		if n == 1 {
			require.NoError(t, os.Chmod(seg, 0))
		}
	}}

	err := f.restore(t, stream)

	assert.Equal(t, codes.Internal, status.Code(err), "got %v", err)
	assert.Len(t, stream.chunks, 1)
	assert.False(t, f.fileDamaged(t), "a possibly transient error must not flag the file")
	assert.True(t, f.fileStillValid(t))
	for i := range f.hashes {
		assert.True(t, f.chunkIndexed(t, i), "chunk %d must not be marked", i)
	}
}

// The restore locates all chunks up front but holds no guard, so compaction
// can move the rest of the file while the first chunk is being sent. The
// later chunks must be found at their new place, and none marked corrupt.
func TestRestoreFile_ChunksMovedAfterLocatingAreStillRead(t *testing.T) {
	f := newRestoreFixture(t, 3)
	old := f.chunkSegment(t, 0)
	const moved = 7
	stream := &restoreStream{onChunk: func(n int) {
		if n != 1 {
			return
		}
		// What compaction does: copy the records to another segment (a
		// whole-file copy keeps the offsets), commit the new locations, then
		// remove the old segment.
		raw, err := os.ReadFile(f.segmentPath(old))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(f.segmentPath(moved), raw, 0o644))
		require.NoError(t, f.writer.RawDB().Table("chunk_records").
			Where("segment = ?", old).Update("segment", moved).Error)
		require.NoError(t, os.Remove(f.segmentPath(old)))
	}}

	require.NoError(t, f.restore(t, stream))

	require.Len(t, stream.chunks, 3)
	for i, c := range stream.chunks {
		assert.Equal(t, f.payloads[i], c.Data)
	}
	assert.True(t, f.fileStillValid(t), "a moved chunk is healthy and must not be marked")
}
