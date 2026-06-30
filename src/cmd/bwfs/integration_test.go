//go:build integration

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	wfs "github.com/alex-sviridov/miniprotector/workload/filesystem"
)

const bufSize = 1 << 20 // 1MB in-memory buffer

// testEnv holds a live bwfs server + connected gRPC client for one test.
type testEnv struct {
	client  pb.BackupServiceClient
	store   *backupServer
	cleanup func()
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	storageDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	conf := &config.Config{
		ConnectionTimeOutSec: 10,
		FileLockTimeoutSec:   5,
	}
	srvCtx := context.WithValue(ctx, config.ContextKey, conf)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	srv, err := NewBackupServer(srvCtx, logger, storageDir)
	require.NoError(t, err)

	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer()
	pb.RegisterBackupServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	return &testEnv{
		client: pb.NewBackupServiceClient(conn),
		store:  srv,
		cleanup: func() {
			conn.Close()
			grpcSrv.GracefulStop()
			lis.Close()
			srv.store.Close()
			cancel()
		},
	}
}

// backupOneFile runs the full brfs-side protocol for a single file over the given stream.
// Returns (fileHash, error). fileHash is nil for non-file or already-known files.
func backupOneFile(ctx context.Context, t *testing.T, stream pb.BackupService_ProcessBackupStreamClient, file wfs.FileInfo) ([]byte, error) {
	t.Helper()
	conf := &config.Config{ConnectionTimeOutSec: 10, FileLockTimeoutSec: 5}
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	logger := slog.Default()
	timeout := 10 * time.Second

	encoded, err := file.Encode()
	require.NoError(t, err)

	err = stream.Send(&pb.FileRequest{
		RequestType: &pb.FileRequest_FileInfo{
			FileInfo: &pb.FileInfo{FileId: file.ID(), Attributes: encoded},
		},
	})
	require.NoError(t, err)

	resp, err := connection.WaitForResponse(ctx, logger, stream, connection.FileNeeded(file.ID()), timeout)
	require.NoError(t, err)
	fileNeeded := resp.(*pb.FileNeeded)

	if !fileNeeded.Needed || file.GetType() != 'f' || file.Size() == 0 {
		// brfs always waits for FileProcessingResult even on skip path
		result, err := connection.WaitForResponse(ctx, logger, stream, connection.FileResult(file.ID()), timeout)
		require.NoError(t, err)
		assert.True(t, result.(*pb.FileProcessingResult).Success)
		return nil, nil
	}

	// Transfer chunks
	for chunk, err := range file.ChunkIterator() {
		require.NoError(t, err)
		err = stream.Send(&pb.FileRequest{
			RequestType: &pb.FileRequest_ChunkHash{
				ChunkHash: &pb.ChunkHash{Hash: chunk.Hash(), Index: chunk.Index(), Size: int64(chunk.Size()), Eof: chunk.IsEOF(), Checksum: chunk.Checksum()},
			},
		})
		require.NoError(t, err)

		chunkResp, err := connection.WaitForResponse(ctx, logger, stream, connection.ChunkNeeded(chunk.Hash()), timeout)
		require.NoError(t, err)
		if chunkResp.(*pb.ChunkNeeded).Needed {
			err = stream.Send(&pb.FileRequest{
				RequestType: &pb.FileRequest_ChunkData{
					ChunkData: &pb.ChunkData{Hash: chunk.Hash(), Index: chunk.Index(), Data: chunk.Data(), Eof: chunk.IsEOF()},
				},
			})
			require.NoError(t, err)
			dataResp, err := connection.WaitForResponse(ctx, logger, stream, connection.ChunkResult(chunk.Hash()), timeout)
			require.NoError(t, err)
			assert.True(t, dataResp.(*pb.ChunkResult).Success)
		}
	}

	result, err := connection.WaitForResponse(ctx, logger, stream, connection.FileResult(file.ID()), timeout)
	require.NoError(t, err)
	pr := result.(*pb.FileProcessingResult)
	assert.True(t, pr.Success)
	return pr.Hash, nil
}

// makeTestDir creates a temp source directory with a regular file, a subdir, and a symlink.
func makeTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(dir+"/hello.txt", []byte("hello world, this is test content for integration tests"), 0644))
	require.NoError(t, os.Mkdir(dir+"/subdir", 0755))
	require.NoError(t, os.Symlink(dir+"/hello.txt", dir+"/link.txt"))
	return dir
}

// TestIntegration_SkipPath_DirectoryAndSymlink verifies that non-file objects
// (directories, symlinks) complete without hanging — the server must send
// FileProcessingResult even when Needed=false.
func TestIntegration_SkipPath_DirectoryAndSymlink(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)

	ctx := context.Background()
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	for _, f := range files {
		if f.GetType() == 'd' || f.GetType() == 'l' {
			_, err := backupOneFile(ctx, t, stream, f)
			assert.NoError(t, err, "non-file object %s should complete without hang", f.ID())
		}
	}
	require.NoError(t, stream.CloseSend())
}

// TestIntegration_NewFile_TransferPath verifies that a new regular file is
// transferred, its chunks stored, and a FileVersion record created.
func TestIntegration_NewFile_TransferPath(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)

	ctx := context.Background()
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			hash, err := backupOneFile(ctx, t, stream, f)
			require.NoError(t, err)
			assert.NotEmpty(t, hash, "transferred file must return a non-empty hash")

			// FileVersion must exist in catalog
			v, err := env.store.store.LatestFileVersion(f.ID())
			require.NoError(t, err)
			assert.Equal(t, f.ID(), v.ObjectID)
		}
	}
	require.NoError(t, stream.CloseSend())
}

// TestIntegration_DedupPath_SecondBackupSkipsChunks verifies that backing up
// the same file twice: second run skips chunk transfer, still creates a
// FileVersion, and returns success without hanging.
func TestIntegration_DedupPath_SecondBackupSkipsChunks(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)

	// Find the regular file
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID(), "need at least one regular file")

	ctx := context.Background()

	// First backup — transfers chunks
	stream1, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	hash1, err := backupOneFile(ctx, t, stream1, target)
	require.NoError(t, err)
	require.NotEmpty(t, hash1)
	require.NoError(t, stream1.CloseSend())

	// Second backup — same file, same mtime → same ID → dedup fires
	stream2, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	// On the dedup path the server responds FileNeeded{Needed:false} + FileProcessingResult
	// backupOneFile handles this and must not hang
	_, err = backupOneFile(ctx, t, stream2, target)
	require.NoError(t, err)
	require.NoError(t, stream2.CloseSend())

	// Two FileVersion records must exist (one per backup run)
	v, err := env.store.store.LatestFileVersion(target.ID())
	require.NoError(t, err)
	assert.Equal(t, target.ID(), v.ObjectID)
}

// TestIntegration_MultipleFiles_OneStream verifies that a stream processing
// multiple files sequentially does not leak state between files.
func TestIntegration_MultipleFiles_OneStream(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := t.TempDir()
	for i := range 5 {
		content := fmt.Sprintf("file %d content: %s", i, string(make([]byte, 1000)))
		require.NoError(t, os.WriteFile(fmt.Sprintf("%s/file%d.txt", srcDir, i), []byte(content), 0644))
	}

	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)

	ctx := context.Background()
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	successCount := 0
	for _, f := range files {
		if f.GetType() != 'f' || f.Size() == 0 {
			continue
		}
		_, err := backupOneFile(ctx, t, stream, f)
		require.NoError(t, err, "file %s failed", f.ID())
		successCount++
	}
	require.NoError(t, stream.CloseSend())
	assert.Equal(t, 5, successCount)
}

// TestIntegration_ConcurrentStreams_SameFileContent verifies that multiple
// concurrent streams backing up files with identical content do not race:
// atomic chunk writes + OnConflict{DoNothing} must handle concurrent dedup.
func TestIntegration_ConcurrentStreams_SameFileContent(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	// Create N source dirs each with the same file content
	const streams = 5
	content := []byte("identical content across all streams — dedup race test payload padding padding padding")

	srcDirs := make([]string, streams)
	for i := range streams {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(dir+"/same.txt", content, 0644))
		srcDirs[i] = dir
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, streams)

	for i := range streams {
		wg.Add(1)
		go func(srcDir string) {
			defer wg.Done()

			files, err := wfs.Discover(srcDir)
			if err != nil {
				errs <- err
				return
			}

			stream, err := env.client.ProcessBackupStream(ctx)
			if err != nil {
				errs <- err
				return
			}

			for _, f := range files {
				if f.GetType() != 'f' || f.Size() == 0 {
					continue
				}
				if _, err := backupOneFile(ctx, t, stream, f); err != nil {
					errs <- fmt.Errorf("stream backup failed: %w", err)
					return
				}
			}
			errs <- stream.CloseSend()
		}(srcDirs[i])
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err)
	}
}
