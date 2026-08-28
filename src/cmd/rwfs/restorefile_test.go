package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"lukechampine.com/blake3"
)

// dialRestoreClient starts srv on an in-memory bufconn listener and
// returns a RestoreServiceClient dialed against it -- this file only ever
// needs RestoreServiceServer (not ListServiceServer), unlike
// restore_test.go's full end-to-end fixtures, so it gets its own minimal
// dial helper rather than reusing runRestoreWithDialer.
func dialRestoreClient(t *testing.T, srv pb.RestoreServiceServer) pb.RestoreServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterRestoreServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	t.Cleanup(grpcSrv.GracefulStop)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	return pb.NewRestoreServiceClient(conn)
}

// hashMismatchRestoreServer serves a valid Meta event followed by one
// chunk whose Hash field doesn't match its Data -- drives
// writeRestoreFile's BLAKE3 mismatch abort path without needing a real
// corrupted store.
type hashMismatchRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
}

func (s *hashMismatchRestoreServer) RestoreFile(_ *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	data := []byte("some file content")
	if err := stream.Send(&pb.RestoreEvent{Payload: &pb.RestoreEvent_Meta{Meta: &pb.RestoreFileMeta{
		Size:             int64(len(data)),
		ChunkCount:       1,
		ExpectedChecksum: []byte{0, 0, 0, 0},
	}}}); err != nil {
		return err
	}
	return stream.Send(&pb.RestoreEvent{Payload: &pb.RestoreEvent_Chunk{Chunk: &pb.RestoreChunk{
		Index: 0,
		Hash:  []byte{0x00}, // deliberately wrong
		Data:  data,
		Eof:   true,
	}}})
}

// crcMismatchRestoreServer serves a Meta event with a deliberately wrong
// ExpectedChecksum, followed by one chunk whose Hash correctly matches its
// Data -- drives writeRestoreFile's whole-file CRC32 mismatch path, distinct
// from the per-chunk BLAKE3 path above.
type crcMismatchRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
}

func (s *crcMismatchRestoreServer) RestoreFile(_ *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	data := []byte("some file content")
	hash := blake3Sum(data)
	if err := stream.Send(&pb.RestoreEvent{Payload: &pb.RestoreEvent_Meta{Meta: &pb.RestoreFileMeta{
		Size:             int64(len(data)),
		ChunkCount:       1,
		ExpectedChecksum: []byte{0xDE, 0xAD, 0xBE, 0xEF}, // deliberately wrong
	}}}); err != nil {
		return err
	}
	return stream.Send(&pb.RestoreEvent{Payload: &pb.RestoreEvent_Chunk{Chunk: &pb.RestoreChunk{
		Index: 0,
		Hash:  hash,
		Data:  data,
		Eof:   true,
	}}})
}

func TestWriteRestoreFile_WritesFileContent(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	chunks := [][]byte{[]byte("hello "), []byte("world!")}
	fileUUID := seedRestorableFileChunks(t, store, "hosta", "/data/a.txt", "job1", 1000, chunks)

	client := dialRestoreClient(t, &realRestoreServer{store: store})

	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.NoError(t, result.Err)
	assert.False(t, result.Skipped)
	assert.EqualValues(t, 12, result.Bytes)

	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "hello world!", string(got))
}

func TestWriteRestoreFile_SkipsWhenExistsAndNotOverwrite(t *testing.T) {
	restoreSrv := &recordingRestoreServer{}
	client := dialRestoreClient(t, restoreSrv)

	destPath := t.TempDir() + "/a.txt"
	require.NoError(t, os.WriteFile(destPath, []byte("original"), 0o644))

	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "does-not-matter", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.NoError(t, result.Err)
	assert.True(t, result.Skipped)
	assert.Empty(t, restoreSrv.Requested(), "RestoreFile must never be called for a skipped file")

	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "original", string(got), "a skipped file must be left untouched")
}

func TestWriteRestoreFile_OverwritesWhenExistsAndOverwriteTrue(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte("new content"))

	client := dialRestoreClient(t, &realRestoreServer{store: store})

	destPath := t.TempDir() + "/a.txt"
	require.NoError(t, os.WriteFile(destPath, []byte("stale content, longer than the replacement"), 0o644))

	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, true)

	require.NoError(t, result.Err)
	assert.False(t, result.Skipped)

	got, err := os.ReadFile(destPath)
	require.NoError(t, err)
	assert.Equal(t, "new content", string(got))
}

func TestWriteRestoreFile_DirectoryAtDestinationIsHardError(t *testing.T) {
	restoreSrv := &recordingRestoreServer{}
	client := dialRestoreClient(t, restoreSrv)

	destPath := t.TempDir() + "/a-directory"
	require.NoError(t, os.Mkdir(destPath, 0o755))

	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "does-not-matter", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "directory")
	assert.Empty(t, restoreSrv.Requested(), "RestoreFile must never be called when the destination is a directory")
	assert.False(t, result.Retryable)
}

func TestWriteRestoreFile_BlakeMismatchAbortsAndRemovesPartialFile(t *testing.T) {
	client := dialRestoreClient(t, &hashMismatchRestoreServer{})

	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "blake3_mismatch")
	assert.False(t, result.Retryable)
	_, statErr := os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr), "a BLAKE3 mismatch must remove the partial file")
}

func TestWriteRestoreFile_CRCMismatchAbortsAndRemovesPartialFile(t *testing.T) {
	client := dialRestoreClient(t, &crcMismatchRestoreServer{})

	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "crc_mismatch")
	assert.False(t, result.Retryable)
	_, statErr := os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr), "a CRC32 mismatch must remove the partial file")
}

func TestWriteRestoreFile_StreamErrorReturnsErrorAndCreatesNoFile(t *testing.T) {
	restoreSrv := &recordingRestoreServer{} // always fails RestoreFile with codes.Unimplemented
	client := dialRestoreClient(t, restoreSrv)

	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	assert.True(t, result.Retryable)
	_, statErr := os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr), "a stream error before any chunk arrives must never create a file")
}

func TestWriteRestoreFile_MissingParentDirectoryIsHardError(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte("content"))

	client := dialRestoreClient(t, &realRestoreServer{store: store})

	destPath := t.TempDir() + "/missing-parent/a.txt"
	result := writeRestoreFile(context.Background(), client, restoreFile{
		FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false)

	require.Error(t, result.Err)
	assert.False(t, result.Retryable)
}

// flakyRestoreServer fails RestoreFile with a stream-level error the
// first failuresBeforeSuccess calls, then delegates to wrapped -- proving
// writeRestoreFileWithRetry actually recovers a transient failure rather
// than merely detecting one.
type flakyRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
	mu                    sync.Mutex
	calls                 int
	failuresBeforeSuccess int
	wrapped               pb.RestoreServiceServer
}

func (s *flakyRestoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	s.mu.Lock()
	s.calls++
	shouldFail := s.calls <= s.failuresBeforeSuccess
	s.mu.Unlock()
	if shouldFail {
		return status.Error(codes.Unavailable, "simulated transient failure")
	}
	return s.wrapped.RestoreFile(req, stream)
}

func (s *flakyRestoreServer) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestWriteRestoreFileWithRetry_RecoversAfterTransientFailures(t *testing.T) {
	original := retryBackoffInitial
	retryBackoffInitial = time.Millisecond
	t.Cleanup(func() { retryBackoffInitial = original })

	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	fileUUID := seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 1000, []byte("recovered content"))

	flakySrv := &flakyRestoreServer{failuresBeforeSuccess: 2, wrapped: &realRestoreServer{store: store}}
	client := dialRestoreClient(t, flakySrv)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFileWithRetry(context.Background(), logger, client, restoreFile{
		FileUUID: fileUUID, Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false, 3)

	require.NoError(t, result.Err)
	assert.Equal(t, 3, flakySrv.Calls(), "must have failed twice then succeeded on the third attempt")
	got, readErr := os.ReadFile(destPath)
	require.NoError(t, readErr)
	assert.Equal(t, "recovered content", string(got))
}

func TestWriteRestoreFileWithRetry_ExhaustsRetriesAndReturnsFinalError(t *testing.T) {
	original := retryBackoffInitial
	retryBackoffInitial = time.Millisecond
	t.Cleanup(func() { retryBackoffInitial = original })

	restoreSrv := &recordingRestoreServer{} // always fails RestoreFile with codes.Unimplemented
	client := dialRestoreClient(t, restoreSrv)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	destPath := t.TempDir() + "/a.txt"
	result := writeRestoreFileWithRetry(context.Background(), logger, client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false, 3)

	require.Error(t, result.Err)
	assert.True(t, result.Retryable, "a stream error stays marked Retryable even once retries are exhausted")
	assert.Len(t, restoreSrv.Requested(), 3, "must attempt exactly maxRetries times")
	_, statErr := os.Stat(destPath)
	assert.True(t, os.IsNotExist(statErr))
}

func TestWriteRestoreFileWithRetry_IntegrityMismatchNeverRetries(t *testing.T) {
	client := dialRestoreClient(t, &hashMismatchRestoreServer{})

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	destPath := t.TempDir() + "/a.txt"

	start := time.Now()
	result := writeRestoreFileWithRetry(context.Background(), logger, client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false, 3)
	elapsed := time.Since(start)

	require.Error(t, result.Err)
	assert.Contains(t, result.Err.Error(), "blake3_mismatch")
	assert.False(t, result.Retryable)
	assert.Less(t, elapsed, 200*time.Millisecond, "an integrity mismatch must fail on the first attempt with no backoff wait")
}

func TestWriteRestoreFileWithRetry_BacksOffBetweenAttempts(t *testing.T) {
	restoreSrv := &recordingRestoreServer{}
	client := dialRestoreClient(t, restoreSrv)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	destPath := t.TempDir() + "/a.txt"

	start := time.Now()
	writeRestoreFileWithRetry(context.Background(), logger, client, restoreFile{
		FileUUID: "x", Source: "hosta", Path: "/data/a.txt", DestPath: destPath,
	}, false, 3)
	elapsed := time.Since(start)

	if elapsed < 1300*time.Millisecond {
		t.Fatalf("expected at least ~1.5s of backoff across 2 waits, took %v", elapsed)
	}
}

// blake3Sum is a tiny local wrapper so crcMismatchRestoreServer above
// doesn't need its own top-level blake3 import alias collision with
// anything else in this file.
func blake3Sum(data []byte) []byte {
	sum := blake3.Sum256(data)
	return sum[:]
}
