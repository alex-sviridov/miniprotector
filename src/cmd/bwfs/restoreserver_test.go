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
	chunks  []*pb.RestoreChunk
	onChunk func(n int)
}

func (r *restoreStream) Send(ev *pb.RestoreEvent) error {
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

	require.Error(t, err)
	assert.Len(t, stream.chunks, 1, "the chunk before the missing one is sent, nothing after")
	assert.False(t, f.fileStillValid(t), "marking invalidates the file so the next backup uploads it")
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
	assert.Len(t, stream.chunks, 1)
	assert.False(t, f.fileStillValid(t))
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
