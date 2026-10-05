package main

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

type fakeChunk struct {
	size int64
	crc  uint32
	eof  bool
}

// fakeBwfs mimics bwfs's wire behaviour: it answers each stream's requests
// strictly in the order received, accounts for chunks as they arrive (a stored
// chunk at hash time, a new one at data time) and folds the file CRC in index
// order. It also records how many requests were received but not yet answered,
// which is what a sliding window changes.
type fakeBwfs struct {
	pb.UnimplementedBackupServiceServer
	delay    time.Duration
	failData bool

	mu     sync.Mutex
	stored map[string]bool

	outstanding    atomic.Int32
	maxOutstanding atomic.Int32
}

func (f *fakeBwfs) ProcessBackupStream(stream pb.BackupService_ProcessBackupStreamServer) error {
	reqs := make(chan *pb.FileRequest, 4096)
	go func() {
		defer close(reqs)
		for {
			r, err := stream.Recv()
			if err != nil {
				return
			}
			n := f.outstanding.Add(1)
			for {
				m := f.maxOutstanding.Load()
				if n <= m || f.maxOutstanding.CompareAndSwap(m, n) {
					break
				}
			}
			reqs <- r
		}
	}()

	var chunks map[int64]fakeChunk
	// A request stops being outstanding the moment its first reply is sent;
	// count it down before sending, or a fast client's next request would be
	// counted while this one still looks unanswered.
	answered := false
	send := func(r *pb.FileResponse) {
		if !answered {
			answered = true
			f.outstanding.Add(-1)
		}
		_ = stream.Send(r)
	}
	finish := func(fileID string) {
		var eofEnd int64 = -1
		for idx, c := range chunks {
			if c.eof {
				eofEnd = idx + c.size
			}
		}
		if eofEnd < 0 {
			return
		}
		idxs := make([]int64, 0, len(chunks))
		for idx := range chunks {
			idxs = append(idxs, idx)
		}
		sort.Slice(idxs, func(i, j int) bool { return idxs[i] < idxs[j] })
		var next int64
		h := crc32.NewIEEE()
		for _, idx := range idxs {
			if idx != next {
				return
			}
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], chunks[idx].crc)
			h.Write(b[:])
			next += chunks[idx].size
		}
		if next != eofEnd {
			return
		}
		var sum [4]byte
		binary.BigEndian.PutUint32(sum[:], h.Sum32())
		send(&pb.FileResponse{ResponseType: &pb.FileResponse_Result{Result: &pb.FileProcessingResult{FileId: fileID, Success: true, Hash: sum[:]}}})
	}

	var fileID string
	for r := range reqs {
		time.Sleep(f.delay) // give the client time to run ahead
		answered = false
		switch {
		case r.GetFileInfo() != nil:
			fileID = r.GetFileInfo().FileId
			chunks = map[int64]fakeChunk{}
			info, err := filesystem.DecodeFileInfo(r.GetFileInfo().Attributes)
			if err != nil || info.GetType() != 'f' || info.Size() == 0 {
				// like bwfs: nothing to transfer, answer both replies at once
				send(&pb.FileResponse{ResponseType: &pb.FileResponse_FileNeeded{FileNeeded: &pb.FileNeeded{FileId: fileID, Needed: false}}})
				send(&pb.FileResponse{ResponseType: &pb.FileResponse_Result{Result: &pb.FileProcessingResult{FileId: fileID, Success: true}}})
				break
			}
			send(&pb.FileResponse{ResponseType: &pb.FileResponse_FileNeeded{FileNeeded: &pb.FileNeeded{FileId: fileID, Needed: true}}})
		case r.GetChunkHash() != nil:
			h := r.GetChunkHash()
			f.mu.Lock()
			have := f.stored[hex.EncodeToString(h.Hash)]
			f.mu.Unlock()
			if have {
				chunks[h.Index] = fakeChunk{h.Size, h.Checksum, h.Eof}
			}
			send(&pb.FileResponse{ResponseType: &pb.FileResponse_ChunkNeeded{ChunkNeeded: &pb.ChunkNeeded{Hash: h.Hash, Needed: !have}}})
			if have {
				finish(fileID)
			}
		case r.GetChunkData() != nil:
			d := r.GetChunkData()
			if f.failData {
				send(&pb.FileResponse{ResponseType: &pb.FileResponse_ChunkResult{ChunkResult: &pb.ChunkResult{Hash: d.Hash, Success: false}}})
				break
			}
			f.mu.Lock()
			f.stored[hex.EncodeToString(d.Hash)] = true
			f.mu.Unlock()
			chunks[d.Index] = fakeChunk{int64(len(d.Data)), crc32.ChecksumIEEE(d.Data), d.Eof}
			send(&pb.FileResponse{ResponseType: &pb.FileResponse_ChunkResult{ChunkResult: &pb.ChunkResult{Hash: d.Hash, Success: true}}})
			finish(fileID)
		}
	}
	return nil
}

func startFake(t *testing.T, f *fakeBwfs) pb.BackupServiceClient {
	t.Helper()
	f.stored = map[string]bool{}
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterBackupServiceServer(srv, f)
	go srv.Serve(lis)
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(); srv.Stop(); lis.Close() })
	return pb.NewBackupServiceClient(conn)
}

func randChunks(t *testing.T, n int) [][]byte {
	t.Helper()
	out := make([][]byte, n)
	for i := range out {
		out[i] = make([]byte, filesystem.ChunkSize)
		_, err := rand.Read(out[i])
		require.NoError(t, err)
	}
	return out
}

func writeParts(t *testing.T, path string, parts ...[]byte) {
	t.Helper()
	var all []byte
	for _, p := range parts {
		all = append(all, p...)
	}
	require.NoError(t, os.WriteFile(path, all, 0o644))
}

// sourceTree builds files that share chunks, so a run mixes chunks the server
// asks for with chunks it already stores, at varying positions (including the
// EOF chunk), plus a file whose last chunk is partial.
func sourceTree(t *testing.T) []filesystem.FileInfo {
	t.Helper()
	dir := t.TempDir()
	c := randChunks(t, 12)
	writeParts(t, filepath.Join(dir, "a.bin"), c[0], c[1], c[2], c[3], c[4], c[5])
	writeParts(t, filepath.Join(dir, "b.bin"), c[1], c[6], c[7], c[4])               // cached, new, new, cached(eof)
	writeParts(t, filepath.Join(dir, "c.bin"), c[8], c[0], c[9], c[10], c[11][:100]) // partial eof chunk
	writeParts(t, filepath.Join(dir, "d.bin"), c[3])                                 // single cached chunk
	writeParts(t, filepath.Join(dir, "e.txt"), []byte("small"))
	files, err := filesystem.Discover(dir, []string{"*"}, nil)
	require.NoError(t, err)
	return files
}

func testCtx() context.Context {
	conf := &config.Config{FileLockTimeoutSec: 5, ConnectionTimeOutSec: 10}
	return context.WithValue(context.Background(), config.ContextKey, conf)
}

func runBackup(t *testing.T, client pb.BackupServiceClient, files []filesystem.FileInfo, streams, window int) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(testCtx(), 30*time.Second)
	defer cancel()
	results := map[string]bool{}
	for r := range processFilesList(ctx, slog.New(slog.DiscardHandler), client, files, streams, window, nil) {
		if r.FileID != "" {
			results[r.FileID] = r.Success
		}
	}
	return results
}

func TestProcessFilesList_WindowTransfersEveryFile(t *testing.T) {
	for _, tc := range []struct{ streams, window int }{{1, 1}, {1, 2}, {1, 4}, {1, 8}, {3, 4}, {1, 100}} {
		fake := &fakeBwfs{delay: time.Millisecond}
		client := startFake(t, fake)
		files := sourceTree(t)

		results := runBackup(t, client, files, tc.streams, tc.window)

		require.Len(t, results, len(files))
		for _, f := range files {
			// Success means the file CRC bwfs folded in index order equals the
			// one brfs computed, so chunks accounted for out of order still
			// reassemble correctly.
			assert.True(t, results[f.ID()], "streams=%d window=%d file %s", tc.streams, tc.window, f.Path())
		}
	}
}

func TestProcessFilesList_WindowBoundsAndUsesInFlightRequests(t *testing.T) {
	for _, window := range []int{1, 4} {
		fake := &fakeBwfs{delay: 3 * time.Millisecond}
		client := startFake(t, fake)
		runBackup(t, client, sourceTree(t), 1, window)

		got := int(fake.maxOutstanding.Load())
		assert.LessOrEqual(t, got, window, "window=%d must bound in-flight requests", window)
		if window == 1 {
			assert.Equal(t, 1, got, "window 1 must be stop-and-wait")
		} else {
			assert.Greater(t, got, 1, "window=%d never had more than one request in flight", window)
		}
	}
}

func TestProcessFilesList_NonPositiveWindowActsAsStopAndWait(t *testing.T) {
	fake := &fakeBwfs{delay: time.Millisecond}
	client := startFake(t, fake)
	files := sourceTree(t)

	results := runBackup(t, client, files, 1, 0)

	for _, f := range files {
		assert.True(t, results[f.ID()], f.Path())
	}
	assert.Equal(t, int32(1), fake.maxOutstanding.Load())
}

func TestProcessFilesList_FailedChunkFailsFileWithoutHangingWindow(t *testing.T) {
	fake := &fakeBwfs{failData: true}
	client := startFake(t, fake)
	files := sourceTree(t)

	results := runBackup(t, client, files, 1, 4)

	require.Len(t, results, len(files))
	failed := 0
	for _, ok := range results {
		if !ok {
			failed++
		}
	}
	assert.Positive(t, failed, "files with chunk data to send must be reported as failed")
}

// rejectingBwfs refuses every stream the way bwfs does for a client without
// the required role.
type rejectingBwfs struct {
	pb.UnimplementedBackupServiceServer
}

func (rejectingBwfs) ProcessBackupStream(pb.BackupService_ProcessBackupStreamServer) error {
	return status.Error(codes.PermissionDenied, "requires role in [client]")
}

// A stream that fails must fail every file queued on it, not hang the ones
// after the first: the receive error has to stay readable once the reader
// goroutine has stopped.
func TestProcessFilesList_StreamError_FailsAllFilesWithoutHanging(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterBackupServiceServer(srv, rejectingBwfs{})
	go srv.Serve(lis)
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(); srv.Stop(); lis.Close() })

	files := sourceTree(t)
	for _, window := range []int{1, 4} {
		results := runBackup(t, pb.NewBackupServiceClient(conn), files, 1, window)
		require.Len(t, results, len(files), "window=%d: every file must be reported", window)
		for _, ok := range results {
			assert.False(t, ok)
		}
	}
}
