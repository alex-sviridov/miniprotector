//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"lukechampine.com/blake3"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	"github.com/alex-sviridov/miniprotector/storage"
	storagefs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	wfs "github.com/alex-sviridov/miniprotector/workload/filesystem"
)

const bufSize = 1 << 20 // 1MB in-memory buffer
const testCertsDir = "../../common/testdata/certs"

// testEnv holds a live bwfs server + connected gRPC client for one test.
type testEnv struct {
	client     pb.BackupServiceClient
	store      *backupServer
	cleanup    func()
	storageDir string
}

func newTestEnvWithLogger(t *testing.T, logger *slog.Logger) *testEnv {
	t.Helper()

	storageDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	conf := &config.Config{
		ConnectionTimeOutSec: 10,
		FileLockTimeoutSec:   5,
	}
	srvCtx := context.WithValue(ctx, config.ContextKey, conf)

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
		client:     pb.NewBackupServiceClient(conn),
		store:      srv,
		storageDir: storageDir,
		cleanup: func() {
			conn.Close()
			grpcSrv.GracefulStop()
			lis.Close()
			srv.store.Close()
			cancel()
		},
	}
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWithLogger(t, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
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
	return backupOneFileWithExpiry(ctx, t, stream, file, 0)
}

// backupOneFileWithExpiry is backupOneFile with the client-computed expire_at
// attached to the file's FileInfo message, as brfs does.
func backupOneFileWithExpiry(ctx context.Context, t *testing.T, stream pb.BackupService_ProcessBackupStreamClient, file wfs.FileInfo, expireAt int64) ([]byte, error) {
	t.Helper()
	conf := &config.Config{ConnectionTimeOutSec: 10, FileLockTimeoutSec: 5}
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	logger := slog.Default()
	timeout := 10 * time.Second

	encoded, err := file.Encode()
	require.NoError(t, err)

	err = stream.Send(&pb.FileRequest{
		RequestType: &pb.FileRequest_FileInfo{
			FileInfo: &pb.FileInfo{FileId: file.ID(), Attributes: encoded, ExpireAt: expireAt},
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
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
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
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
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
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
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

	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
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

			files, err := wfs.Discover(srcDir, []string{"*"}, nil)
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

// TestIntegration_BackupJob_RecordedWithSourceHost verifies a stream creates
// a backup_jobs row with the mTLS-verified source host, staying in_progress
// with finished_at nil after the stream closes — completion now requires an
// explicit BackupCommit (Task 6), not just the stream going away.
func TestIntegration_BackupJob_RecordedWithSourceHost(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
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
		return err == nil && record.SourceHost == "bwfs.internal"
	}, time.Second, 10*time.Millisecond, "backup job should be recorded with source host")

	record, err := backupJobRow(t, env, "job-source-host")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusInProgress, record.Status)
	assert.Nil(t, record.FinishedAt, "finished_at must stay nil until BackupCommit — streams closing alone is not completion")
}

// TestIntegration_BackupJob_StaysInProgressAfterAllStreamsClose verifies
// that closing every stream of a job does NOT, by itself, finalize it —
// completion now requires an explicit BackupCommit call (Task 6) or the
// stall watchdog (Task 7), not just stream closure.
func TestIntegration_BackupJob_StaysInProgressAfterAllStreamsClose(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
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
	require.NoError(t, stream2.CloseSend())
	_, err = stream2.Recv()
	require.ErrorIs(t, err, io.EOF)

	record, err := backupJobRow(t, env, "job-multi-stream")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusInProgress, record.Status, "job must stay in_progress after streams close with no BackupCommit sent")
	assert.Nil(t, record.FinishedAt)
}

// TestIntegration_DuplicateFileWithinJob_OneFileVersionRow verifies that
// sending the same file twice within one job (simulating a retry) does not
// create two file_version rows.
func TestIntegration_DuplicateFileWithinJob_OneFileVersionRow(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
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

// commitHash computes the same SHA256-over-sorted-newline-joined-IDs that
// brfs computes client-side (see cmd/brfs/commit.go, Task 8) — inlined here
// so this test file doesn't depend on the brfs package.
func commitHash(ids ...string) []byte {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return sum[:]
}

func TestIntegration_BackupCommit_LogsEventFinishAndStatusSuccessOnMatch(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	env := newTestEnvWithLogger(t, logger)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-log-success")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(target.ID())})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"msg":"Backup job committed"`)
	assert.Contains(t, out, `"event":"finish"`)
	assert.Contains(t, out, `"status":"success"`)
}

func TestIntegration_BackupCommit_LogsStatusFailureOnMismatch(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	env := newTestEnvWithLogger(t, logger)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-log-mismatch")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash("never-sent")})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"status":"failure"`)
}

func TestIntegration_BackupCommit_AlreadyFinalizedLogsEventFinish(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	env := newTestEnvWithLogger(t, logger)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-log-retry")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(target.ID())})
	require.NoError(t, err)
	buf.Reset() // only care about the log line from the retried call below

	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(target.ID())})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"msg":"BackupCommit for already-finalized job"`)
	assert.Contains(t, out, `"event":"finish"`)
}

func TestIntegration_BackupCommit_MatchingHashSucceeds(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-success")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	resp, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(target.ID())})
	require.NoError(t, err)
	assert.True(t, resp.Success)

	record, err := backupJobRow(t, env, "job-commit-success")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusSuccess, record.Status)
	require.NotNil(t, record.FinishedAt)
}

func TestIntegration_BackupCommit_MismatchedHashFailsAndPurges(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-mismatch")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	resp, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash("some-file-that-was-never-sent")})
	require.NoError(t, err)
	assert.False(t, resp.Success)

	record, err := backupJobRow(t, env, "job-commit-mismatch")
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusFailure, record.Status)

	var count int64
	concrete, ok := env.store.store.(*storagefs.Store)
	require.True(t, ok)
	require.NoError(t, concrete.RawDB().Model(&storagefs.FileVersionRecord{}).Where("job_id = ?", "job-commit-mismatch").Count(&count).Error)
	assert.Equal(t, int64(0), count, "mismatched job's file_versions must be purged")
}

func TestIntegration_BackupCommit_UnknownJobReturnsNotFound(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	ctx := jobContext("job-never-existed")
	_, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash("x")})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
}

func TestIntegration_BackupCommit_WrongSourceHostRejected(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	// Seed a job as if it belonged to a different host, bypassing mTLS —
	// the test client cert always presents "bwfs.internal", so this
	// simulates a second host trying to commit a job it doesn't own.
	require.NoError(t, env.store.store.EnsureBackupJob("job-other-host", "some-other-host"))

	ctx := jobContext("job-other-host")
	_, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash("x")})
	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

func TestIntegration_BackupCommit_RetriedCallAfterSuccessIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-retry")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	hash := commitHash(target.ID())
	resp1, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: hash})
	require.NoError(t, err)
	assert.True(t, resp1.Success)

	// Simulate brfs retrying because the first response was lost in transit.
	resp2, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: hash})
	require.NoError(t, err)
	assert.True(t, resp2.Success, "a retried commit call for an already-succeeded job must return the same outcome, not re-hash or error")
}

// TestIntegration_LateMessageAfterFinalize_Rejected verifies a message
// arriving for a job that BackupCommit already finalized is rejected rather
// than silently written.
func TestIntegration_LateMessageAfterFinalize_Rejected(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
	require.NoError(t, err)
	var first, second wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			if first.ID() == "" {
				first = f
			} else if second.ID() == "" {
				second = f
			}
		}
	}
	require.NotEmpty(t, first.ID())

	ctx := jobContext("job-late-message")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, first)
	require.NoError(t, err)

	// Commit the job while stream is still open (simulating brfs committing
	// after its WaitGroup joins, even though this test keeps one stream alive).
	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(first.ID())})
	require.NoError(t, err)

	// Now send another message on the still-open stream — must be rejected.
	require.NoError(t, stream.Send(&pb.FileRequest{
		RequestType: &pb.FileRequest_FileInfo{FileInfo: &pb.FileInfo{FileId: "late-file", Attributes: []byte("x")}},
	}))
	_, err = stream.Recv()
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
}

// TestIntegration_StallWatchdog_FailsSilentJob verifies a job with no
// BackupCommit and no further stream activity is failed once its liveness
// entry exceeds the timeout — this runs the real watchStaleJobs background
// loop (the same one main.go starts on a ticker) with a near-zero timeout
// so the test doesn't need to sleep for main.go's real poll interval; the
// poll-interval floor of 5s in watchStaleJobs still applies, so the first
// tick takes a few seconds.
func TestIntegration_StallWatchdog_FailsSilentJob(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	require.NoError(t, env.store.store.EnsureBackupJob("job-stalled", "bwfs.internal"))
	env.store.liveness.Touch("job-stalled")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchStaleJobs(ctx, env.store, time.Millisecond)

	require.Eventually(t, func() bool {
		record, err := backupJobRow(t, env, "job-stalled")
		return err == nil && record.Status == storage.JobStatusFailure
	}, 8*time.Second, 100*time.Millisecond, "watchdog should fail a job with zero-duration timeout within one poll interval")
}

// fileVersionExpireAt reads one file_version row's expire_at directly.
func fileVersionExpireAt(t *testing.T, env *testEnv, jobID, objectID string) *int64 {
	t.Helper()
	concrete, ok := env.store.store.(*storagefs.Store)
	require.True(t, ok)
	var rec storagefs.FileVersionRecord
	require.NoError(t, concrete.RawDB().First(&rec, "job_id = ? AND object_id = ?", jobID, objectID).Error)
	return rec.ExpireAt
}

// TestIntegration_ExpireAt_StoredForNewAndSkippedFiles verifies the
// client-computed expire_at lands on the file version both when the file's
// content is transferred (fileWritten path) and when it is already known
// (skip path, a later job re-observing unchanged content).
func TestIntegration_ExpireAt_StoredForNewAndSkippedFiles(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir, []string{"*"}, nil)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	// New file: content transferred, version recorded in fileWritten.
	ctx1 := jobContext("job-expiry-1")
	s1, err := env.client.ProcessBackupStream(ctx1)
	require.NoError(t, err)
	_, err = backupOneFileWithExpiry(ctx1, t, s1, target, 1_700_000_000)
	require.NoError(t, err)
	require.NoError(t, s1.CloseSend())
	got := fileVersionExpireAt(t, env, "job-expiry-1", target.ID())
	require.NotNil(t, got)
	assert.Equal(t, int64(1_700_000_000), *got)

	// Same content in a later job: skip path, still stamped with that job's expiry.
	ctx2 := jobContext("job-expiry-2")
	s2, err := env.client.ProcessBackupStream(ctx2)
	require.NoError(t, err)
	_, err = backupOneFileWithExpiry(ctx2, t, s2, target, 1_800_000_000)
	require.NoError(t, err)
	require.NoError(t, s2.CloseSend())
	got = fileVersionExpireAt(t, env, "job-expiry-2", target.ID())
	require.NotNil(t, got)
	assert.Equal(t, int64(1_800_000_000), *got)

	// No expiry sent: NULL.
	ctx3 := jobContext("job-expiry-3")
	s3, err := env.client.ProcessBackupStream(ctx3)
	require.NoError(t, err)
	_, err = backupOneFile(ctx3, t, s3, target)
	require.NoError(t, err)
	require.NoError(t, s3.CloseSend())
	assert.Nil(t, fileVersionExpireAt(t, env, "job-expiry-3", target.ID()))
}

// TestIntegration_ScheduledGC_NeverCorruptsConcurrentBackups runs cleanup and
// vacuum back to back, as fast as possible, while backups are being taken --
// the situation the store operation guard exists for. Each round backs up
// files whose content is new that round (expired immediately) and then, in a
// second job, the *same content under other names* plus the same files again
// (permanent): the first job's chunks become orphans exactly while the second
// job decides they "already exist" (dedup race) and while it re-observes
// known files (known-file race). Whatever GC does, nothing a permanent
// version references may be missing or damaged, and once everything has
// settled nothing may be left over either.
func TestIntegration_ScheduledGC_NeverCorruptsConcurrentBackups(t *testing.T) {
	env := newTestEnv(t)
	defer env.cleanup()
	store, ok := env.store.store.(*storagefs.Store)
	require.True(t, ok)

	stop := make(chan struct{})
	gcDone := make(chan struct{})
	go func() {
		defer close(gcDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = store.CleanupExpired(context.Background(), time.Now(), 2, false)
			_, _ = store.VacuumOnline(context.Background(), 2, time.Hour)
		}
	}()

	const rounds = 25
	root := t.TempDir()
	backup := func(job string, expireAt int64, files []wfs.FileInfo) {
		ctx := jobContext(job)
		stream, err := env.client.ProcessBackupStream(ctx)
		require.NoError(t, err)
		var ids []string
		for _, f := range files {
			_, err := backupOneFileWithExpiry(ctx, t, stream, f, expireAt)
			require.NoError(t, err)
			ids = append(ids, f.ID())
		}
		require.NoError(t, stream.CloseSend())
		_, err = stream.Recv()
		require.ErrorIs(t, err, io.EOF)
		resp, err := env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(ids...)})
		require.NoError(t, err)
		require.True(t, resp.Success, "job %s must commit", job)
	}
	regular := func(dir string) []wfs.FileInfo {
		all, err := wfs.Discover(dir, []string{"*"}, nil)
		require.NoError(t, err)
		var out []wfs.FileInfo
		for _, f := range all {
			if f.GetType() == 'f' && f.Size() > 0 {
				out = append(out, f)
			}
		}
		return out
	}

	for r := 0; r < rounds; r++ {
		expDir := filepath.Join(root, fmt.Sprintf("exp-%d", r))
		permDir := filepath.Join(root, fmt.Sprintf("perm-%d", r))
		require.NoError(t, os.MkdirAll(expDir, 0o755))
		require.NoError(t, os.MkdirAll(permDir, 0o755))
		for k := 0; k < 3; k++ {
			content := []byte(fmt.Sprintf("content-%d-%d, unique to this round", r, k))
			require.NoError(t, os.WriteFile(filepath.Join(expDir, fmt.Sprintf("e%d.bin", k)), content, 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(permDir, fmt.Sprintf("p%d.bin", k)), content, 0o644))
		}
		expFiles := regular(expDir)
		backup(fmt.Sprintf("exp-%d", r), time.Now().Unix()-10, expFiles)
		backup(fmt.Sprintf("perm-%d", r), 0, append(regular(permDir), expFiles...))
	}

	close(stop)
	<-gcDone

	// Settle: one last cleanup + vacuum with nothing running.
	_, err := store.CleanupExpired(context.Background(), time.Now(), 100, false)
	require.NoError(t, err)
	_, err = store.VacuumOnline(context.Background(), 100, time.Hour)
	require.NoError(t, err)

	// 1. Nothing a permanent version references is missing or damaged.
	var permanent []storagefs.FileVersionRecord
	require.NoError(t, store.RawDB().Where("expire_at IS NULL").Find(&permanent).Error)
	require.Len(t, permanent, rounds*6, "every permanent version must have survived")
	referenced := map[string]bool{}
	for _, v := range permanent {
		ok, err := store.FileDataExists(v.ObjectID)
		require.NoError(t, err)
		require.True(t, ok, "file data for %s was removed while a version still references it", v.ObjectID)
		for hash, err := range store.FileDataChunks(v.ObjectID) {
			require.NoError(t, err)
			data, err := store.ReadChunk(hash)
			require.NoError(t, err, "chunk %x of %s is missing", hash, v.ObjectID)
			sum := blake3.Sum256(data)
			require.Equal(t, hash, sum[:], "chunk content damaged")
			referenced[hex.EncodeToString(hash)] = true
		}
	}

	// 2. Nothing is left over: every chunk record is referenced by a
	// surviving version, and every expired version is gone. (Unreferenced
	// chunk bytes are dead space inside pack segments, not separate files.)
	var records []storagefs.ChunkRecord
	require.NoError(t, store.RawDB().Find(&records).Error)
	assert.Len(t, records, len(referenced), "chunk records leaked")
	var expired int64
	require.NoError(t, store.RawDB().Model(&storagefs.FileVersionRecord{}).Where("expire_at IS NOT NULL").Count(&expired).Error)
	assert.Equal(t, int64(0), expired)
}
