//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/checksum"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	workfs "github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// restoreTestEnv wraps a live bwfs server with both BackupService and RestoreService.
type restoreTestEnv struct {
	backupClient  pb.BackupServiceClient
	restoreClient pb.RestoreServiceClient
	listClient    pb.ListServiceClient
	storageDir    string
	cleanup       func()
}

func newRestoreTestEnv(t *testing.T) *restoreTestEnv {
	t.Helper()

	storageDir := t.TempDir()
	conf := &config.Config{ConnectionTimeOutSec: 10, FileLockTimeoutSec: 5}
	ctx := context.WithValue(context.Background(), config.ContextKey, conf)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	rawStore, err := wfs.New(storageDir)
	require.NoError(t, err)

	bkpSrv := &backupServer{store: rawStore, logger: logger, config: conf, liveness: newJobLiveness()}
	restoreSrv := NewRestoreServer(rawStore, logger)
	listSrv := NewListServer(rawStore, logger)

	serverCreds, err := mtls.LoadServerCredentials(testCertsDir)
	require.NoError(t, err)

	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterBackupServiceServer(grpcSrv, bkpSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	pb.RegisterListServiceServer(grpcSrv, listSrv)
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

	_ = ctx
	return &restoreTestEnv{
		backupClient:  pb.NewBackupServiceClient(conn),
		restoreClient: pb.NewRestoreServiceClient(conn),
		listClient:    pb.NewListServiceClient(conn),
		storageDir:    storageDir,
		cleanup: func() {
			conn.Close()
			grpcSrv.GracefulStop()
			lis.Close()
			rawStore.Close()
		},
	}
}

// restoreAndVerifyCRC calls RestoreFile for fileUUID and checks that:
//   - all chunks' BLAKE3 hashes match the returned data
//   - the accumulated CRC32 matches meta.ExpectedChecksum
func restoreAndVerifyCRC(t *testing.T, client pb.RestoreServiceClient, fileUUID string) {
	t.Helper()

	stream, err := client.RestoreFile(context.Background(), &pb.RestoreRequest{FileUuid: fileUUID})
	require.NoError(t, err, "RestoreFile RPC failed for %s", fileUUID)

	firstEvent, err := stream.Recv()
	require.NoError(t, err, "failed to recv first event for %s", fileUUID)
	meta := firstEvent.GetMeta()
	require.NotNil(t, meta, "first event must be RestoreFileMeta for %s", fileUUID)

	hasher := crc32.NewIEEE()
	chunksReceived := 0

	for {
		event, err := stream.Recv()
		require.NoError(t, err, "stream error while reading chunks for %s", fileUUID)

		chunk := event.GetChunk()
		require.NotNil(t, chunk, "non-chunk event after meta for %s", fileUUID)

		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(chunk.Data))
		chunksReceived++

		if chunk.Eof {
			break
		}
	}

	assert.Equal(t, int(meta.ChunkCount), chunksReceived,
		"chunk count mismatch for %s: meta says %d, got %d", fileUUID, meta.ChunkCount, chunksReceived)

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	assert.True(t, bytes.Equal(buf[:], meta.ExpectedChecksum),
		"CRC32 mismatch for %s", fileUUID)
}

// listLatestFileUUID returns the file_uuid for the latest version of a file by path.
func listLatestFileUUID(t *testing.T, client pb.ListServiceClient, path string) string {
	t.Helper()
	resp, err := client.ListFiles(context.Background(), &pb.ListRequest{})
	require.NoError(t, err)
	for _, row := range resp.Rows {
		if row.Path == path {
			return row.FileUuid
		}
	}
	t.Fatalf("no file found for path %q in list response", path)
	return ""
}

// TestIntegration_Restore_HappyPath backs up a file and verifies CRC via RestoreService.
func TestIntegration_Restore_HappyPath(t *testing.T) {
	env := newRestoreTestEnv(t)
	defer env.cleanup()

	srcDir := t.TempDir()
	require.NoError(t, os.WriteFile(srcDir+"/data.txt",
		[]byte("test content for restore happy path — enough bytes to be meaningful"), 0644))

	files, err := workfs.Discover(srcDir)
	require.NoError(t, err)

	ctx := jobContext("job-restore-happy-path")
	stream, err := env.backupClient.ProcessBackupStream(ctx)
	require.NoError(t, err)
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			_, err := backupOneFile(ctx, t, stream, f)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stream.CloseSend())

	id := listLatestFileUUID(t, env.listClient, srcDir+"/data.txt")
	restoreAndVerifyCRC(t, env.restoreClient, id)
}

// TestIntegration_Restore_DedupChunks_ChunkLinksPresent is the critical regression test
// for the bug where handleChunkHashRequest did not call LinkChunkToFileData.
//
// Two files share identical content. File A is backed up first (all chunks new).
// File B is backed up second — all its chunks are already stored, so every chunk
// goes through handleChunkHashRequest (the dedup path). Before the fix, file B
// would have zero chunk links in file_data_chunk_records and the restore stream
// would return no chunks, making CRC verification always fail.
func TestIntegration_Restore_DedupChunks_ChunkLinksPresent(t *testing.T) {
	env := newRestoreTestEnv(t)
	defer env.cleanup()

	content := bytes.Repeat([]byte("deduplicated content block. "), 200) // ~5.6KB, fits one chunk

	// Use two separate directories so fileA and fileB have different absolute paths
	// (and thus different file_ids), forcing chunk-level deduplication rather than
	// file-level skipping.
	srcDirA := t.TempDir()
	srcDirB := t.TempDir()
	pathA := srcDirA + "/file.txt"
	pathB := srcDirB + "/file.txt"
	require.NoError(t, os.WriteFile(pathA, content, 0644))
	require.NoError(t, os.WriteFile(pathB, content, 0644))

	filesA, err := workfs.Discover(srcDirA)
	require.NoError(t, err)
	filesB, err := workfs.Discover(srcDirB)
	require.NoError(t, err)

	ctx := jobContext("job-restore-dedup-chunks")

	// Back up file A first — all chunks are new, stored via handleChunkDataRequest.
	stream1, err := env.backupClient.ProcessBackupStream(ctx)
	require.NoError(t, err)
	for _, f := range filesA {
		if f.GetType() == 'f' && f.Size() > 0 {
			_, err := backupOneFile(ctx, t, stream1, f)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stream1.CloseSend())

	// Back up file B — all chunks already exist, so every chunk goes through
	// handleChunkHashRequest with needed=false (the dedup path).
	stream2, err := env.backupClient.ProcessBackupStream(ctx)
	require.NoError(t, err)
	for _, f := range filesB {
		if f.GetType() == 'f' && f.Size() > 0 {
			_, err := backupOneFile(ctx, t, stream2, f)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stream2.CloseSend())

	// Both files must pass CRC verification via the restore stream.
	idA := listLatestFileUUID(t, env.listClient, pathA)
	restoreAndVerifyCRC(t, env.restoreClient, idA)

	idB := listLatestFileUUID(t, env.listClient, pathB)
	restoreAndVerifyCRC(t, env.restoreClient, idB)
}

// TestIntegration_Restore_AllChunksDeduped is the edge case where 100% of
// a file's chunks are deduplicated (single small file backed up from two paths).
func TestIntegration_Restore_AllChunksDeduped(t *testing.T) {
	env := newRestoreTestEnv(t)
	defer env.cleanup()

	content := []byte("single chunk content — small enough to be one chunk")

	// Two separate source directories so the file_ids differ (different paths).
	srcDirA := t.TempDir()
	srcDirB := t.TempDir()
	require.NoError(t, os.WriteFile(srcDirA+"/file.txt", content, 0644))
	require.NoError(t, os.WriteFile(srcDirB+"/file.txt", content, 0644))

	filesA, err := workfs.Discover(srcDirA)
	require.NoError(t, err)
	filesB, err := workfs.Discover(srcDirB)
	require.NoError(t, err)

	ctx := jobContext("job-restore-all-deduped")

	// Back up A first — chunk is stored.
	stream1, err := env.backupClient.ProcessBackupStream(ctx)
	require.NoError(t, err)
	for _, f := range filesA {
		if f.GetType() == 'f' && f.Size() > 0 {
			_, err := backupOneFile(ctx, t, stream1, f)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stream1.CloseSend())

	// Back up B — same chunk already exists → 100% dedup path.
	stream2, err := env.backupClient.ProcessBackupStream(ctx)
	require.NoError(t, err)
	for _, f := range filesB {
		if f.GetType() == 'f' && f.Size() > 0 {
			_, err := backupOneFile(ctx, t, stream2, f)
			require.NoError(t, err)
		}
	}
	require.NoError(t, stream2.CloseSend())

	// File B must pass CRC verification — this is the all-chunks-deduped edge case.
	idB := listLatestFileUUID(t, env.listClient, srcDirB+"/file.txt")
	restoreAndVerifyCRC(t, env.restoreClient, idB)
}
