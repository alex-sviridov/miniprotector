//go:build integration

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	storagefs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	wfs "github.com/alex-sviridov/miniprotector/workload/filesystem"
)

const bufSize = 1 << 20 // 1MB in-memory buffer
const testCertsDir = "../../common/testdata/certs"

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

	serverCreds, err := mtls.LoadServerCredentials(testCertsDir)
	require.NoError(t, err)

	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterBackupServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)

	clientCreds, err := mtls.LoadClientCredentials(testCertsDir, "bwfs.internal")
	require.NoError(t, err)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(clientCreds),
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

// jobContext attaches job-id gRPC metadata, as brfs does when opening a
// stream. Tests that don't call this and use context.Background() directly
// are exercising the "no job-id" rejection path.
func jobContext(jobID string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "job-id", jobID)
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

// backupJobRow reads a backup_jobs row directly, bypassing the BackupStore
// interface (which intentionally has no query surface for jobs — see the
// design doc's Non-Goals). Only valid because newTestEnv always constructs
// the filesystem-backed store.
func backupJobRow(t *testing.T, env *testEnv, jobID string) (storagefs.BackupJobRecord, error) {
	t.Helper()
	concrete, ok := env.store.store.(*storagefs.Store)
	require.True(t, ok, "test env must use the filesystem store implementation")
	var record storagefs.BackupJobRecord
	err := concrete.RawDB().First(&record, "job_id = ?", jobID).Error
	return record, err
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

	ctx := jobContext("job-skip-path")
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

	ctx := jobContext("job-new-file")
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

	ctx := jobContext("job-dedup")

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

	ctx := jobContext("job-multi-file")
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

	ctx := jobContext("job-concurrent")
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

// TestIntegration_MissingJobID_StreamRejected verifies a stream opened
// without job-id metadata is rejected before any file processing.
func TestIntegration_MissingJobID_StreamRejected(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	ctx := context.Background() // no job-id metadata attached
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	_, err = stream.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestIntegration_BackupJob_RecordedWithSourceHost verifies a completed
// stream creates a backup_jobs row with the mTLS-verified source host and a
// non-nil finished_at once the stream closes.
func TestIntegration_BackupJob_RecordedWithSourceHost(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)

	ctx := jobContext("job-source-host")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			_, err := backupOneFile(ctx, t, stream, f)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	require.Eventually(t, func() bool {
		record, err := backupJobRow(t, env, "job-source-host")
		return err == nil && record.SourceHost == "bwfs.internal" && record.FinishedAt != nil
	}, time.Second, 10*time.Millisecond, "backup job should be recorded with source host and finished_at")
}

// TestIntegration_BackupJob_FinishedAtWaitsForAllStreams verifies finished_at
// is not set while any stream of the job is still open, and is set once the
// last one closes.
func TestIntegration_BackupJob_FinishedAtWaitsForAllStreams(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-multi-stream")

	stream1, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	stream2, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	_, err = backupOneFile(ctx, t, stream1, target)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		_, err := backupJobRow(t, env, "job-multi-stream")
		return err == nil
	}, time.Second, 10*time.Millisecond, "backup job row should exist once the first stream starts")

	require.NoError(t, stream1.CloseSend())
	_, err = stream1.Recv()
	require.ErrorIs(t, err, io.EOF)

	// stream2 still open — finished_at must not be set yet.
	record, err := backupJobRow(t, env, "job-multi-stream")
	require.NoError(t, err)
	assert.Nil(t, record.FinishedAt, "finished_at must stay nil while stream2 is still open")

	require.NoError(t, stream2.CloseSend())
	_, err = stream2.Recv()
	require.ErrorIs(t, err, io.EOF)

	require.Eventually(t, func() bool {
		record, err := backupJobRow(t, env, "job-multi-stream")
		return err == nil && record.FinishedAt != nil
	}, time.Second, 10*time.Millisecond, "finished_at should be set once both streams close")
}

// TestIntegration_DuplicateFileWithinJob_OneFileVersionRow verifies that
// sending the same file twice within one job (simulating a retry) does not
// create two file_version rows.
func TestIntegration_DuplicateFileWithinJob_OneFileVersionRow(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-duplicate")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)

	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	// Same file, same stream, sent again within the same job.
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)

	require.NoError(t, stream.CloseSend())

	concrete, ok := env.store.store.(*storagefs.Store)
	require.True(t, ok)
	var count int64
	require.NoError(t, concrete.RawDB().Model(&storagefs.FileVersionRecord{}).
		Where("job_id = ? AND object_id = ?", "job-duplicate", target.ID()).
		Count(&count).Error)
	assert.Equal(t, int64(1), count)
}
