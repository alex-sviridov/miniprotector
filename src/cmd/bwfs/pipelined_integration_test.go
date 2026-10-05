//go:build integration

package main

import (
	"crypto/rand"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/workload"
	wfs "github.com/alex-sviridov/miniprotector/workload/filesystem"
)

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return b
}

// pipelinedBackup is a sliding-window brfs with an unbounded window: it sends
// every chunk hash before reading a single reply, then sends the data for the
// chunks the server asked for. Chunks the server already holds are therefore
// accounted for ahead of earlier chunks whose data is still in flight.
func pipelinedBackup(t *testing.T, stream pb.BackupService_ProcessBackupStreamClient, file wfs.FileInfo) (serverHash, clientHash []byte) {
	t.Helper()
	encoded, err := file.Encode()
	require.NoError(t, err)
	require.NoError(t, stream.Send(&pb.FileRequest{RequestType: &pb.FileRequest_FileInfo{
		FileInfo: &pb.FileInfo{FileId: file.ID(), Attributes: encoded},
	}}))
	resp, err := stream.Recv()
	require.NoError(t, err)
	require.True(t, resp.GetFileNeeded().Needed)

	var chunks []workload.Chunk
	crc := crc32.NewIEEE()
	for c, err := range file.ChunkIterator() {
		require.NoError(t, err)
		chunks = append(chunks, c)
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], c.Checksum())
		crc.Write(b[:])
		require.NoError(t, stream.Send(&pb.FileRequest{RequestType: &pb.FileRequest_ChunkHash{
			ChunkHash: &pb.ChunkHash{Hash: c.Hash(), Index: c.Index(), Size: int64(c.Size()), Eof: c.IsEOF(), Checksum: c.Checksum()},
		}}))
	}

	// Replies arrive in request order.
	var needed []workload.Chunk
	for _, c := range chunks {
		r, err := stream.Recv()
		require.NoError(t, err)
		require.Equal(t, c.Hash(), r.GetChunkNeeded().Hash)
		if r.GetChunkNeeded().Needed {
			needed = append(needed, c)
		}
	}
	for _, c := range needed {
		require.NoError(t, stream.Send(&pb.FileRequest{RequestType: &pb.FileRequest_ChunkData{
			ChunkData: &pb.ChunkData{Hash: c.Hash(), Index: c.Index(), Data: c.Data(), Eof: c.IsEOF()},
		}}))
	}
	for _, c := range needed {
		r, err := stream.Recv()
		require.NoError(t, err)
		require.Equal(t, c.Hash(), r.GetChunkResult().Hash)
		require.True(t, r.GetChunkResult().Success)
	}
	r, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, r.GetResult(), "file result must follow the last chunk reply")
	require.True(t, r.GetResult().Success)
	return r.GetResult().Hash, crc.Sum(nil)
}

func TestIntegration_Pipelined_CachedChunksAheadOfInFlightData(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	shared := randBytes(t, wfs.ChunkSize) // exactly one chunk, stored by the seed file
	dir := t.TempDir()
	write := func(name string, parts ...[]byte) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(func() []string {
			s := make([]string, len(parts))
			for i, p := range parts {
				s[i] = string(p)
			}
			return s
		}(), "")), 0o644))
	}
	write("seed.bin", shared)
	// middle: cached chunk sits between two new ones
	write("middle.bin", randBytes(t, wfs.ChunkSize), shared, randBytes(t, wfs.ChunkSize))
	// last: the EOF chunk is cached while the chunk before it still needs data
	write("last.bin", randBytes(t, wfs.ChunkSize), shared)

	files, err := wfs.Discover(dir, []string{"*"}, nil)
	require.NoError(t, err)
	byName := map[string]wfs.FileInfo{}
	for _, f := range files {
		byName[filepath.Base(f.Path())] = f
	}

	ctx := jobContext("job-pipelined")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	_, err = backupOneFile(ctx, t, stream, byName["seed.bin"])
	require.NoError(t, err)

	for _, name := range []string{"middle.bin", "last.bin"} {
		serverHash, clientHash := pipelinedBackup(t, stream, byName[name])
		assert.Equal(t, clientHash, serverHash, name)
	}
	require.NoError(t, stream.CloseSend())
}
