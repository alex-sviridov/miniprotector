package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
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
)

// runRestoreWithDialer mirrors verify_test.go's runVerifyWithDialer --
// runRestore always dials via connection.Connect (host/port plus mTLS
// certs), which has no bufconn injection seam, so this duplicates just the
// dial step against lis, then calls runRestoreWithConn, the exact same
// package-level resolution/dispatch logic runRestore itself calls after
// dialing.
func runRestoreWithDialer(t *testing.T, logger *slog.Logger, lis *bufconn.Listener, rulesJSON string, overwrite bool, streams int) error {
	t.Helper()

	rules, err := parseRulesStdin(strings.NewReader(rulesJSON))
	require.NoError(t, err)

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	return runRestoreWithConn(logger, conn, overwrite, rules, false, streams, "test-job")
}

func TestRunRestore_LogsResolvedFileWithRenamedDestPath(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	// Phase 2 (this task) means restore now actually writes file content, so
	// this needs a directory row (for phase 1 to create the renamed
	// destination directory) and a genuinely restorable file (real chunk
	// data realRestoreServer can serve back), not just a bare
	// file_version_records row -- unlike before phase 2 existed, when
	// restore only resolved and logged and this fixture never had to
	// survive an actual write.
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("vacation photo bytes"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destDir := t.TempDir() + "/photos_recovered"
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destDir)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4)
	require.NoError(t, err)

	out := logBuf.String()
	assert.Contains(t, out, `source=hosta`)
	assert.Contains(t, out, `path=/data/photos/vacation.jpg`)
	assert.Contains(t, out, fmt.Sprintf("dest_path=%s", destDir+"/vacation.jpg"))
	assert.Contains(t, out, `overwrite=true`)

	got, readErr := os.ReadFile(destDir + "/vacation.jpg")
	require.NoError(t, readErr)
	assert.Equal(t, "vacation photo bytes", string(got))
}

func TestRunRestore_FileLevelRuleMatchingNothingFails(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	rulesJSON := `{"rules":[{"host":"hosta","path":"/etc/never-backed-up.conf","include":true}]}`

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 file(s) failed resolution")
	assert.Contains(t, logBuf.String(), `reason="not found on this store"`)
}

func TestRunRestore_FolderLevelRuleMatchingNothingSucceeds(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	rulesJSON := `{"rules":[{"host":"","path":"/empty","include":true}]}`

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	assert.NoError(t, err)
}

// seedDirectory writes a file_version_records row shaped like a directory
// bwfs actually backed up, for driving testResolveServer's new directory
// query (Step 1 above) -- no file_data_records row, since directories
// never get one.
func seedDirectory(t *testing.T, store *wfs.Store, source, path, jobID string, createdAtUnix int64) {
	t.Helper()
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{
		ObjectID:   fmt.Sprintf("fs://%s:d:%s:%d", source, path, createdAtUnix),
		JobID:      jobID,
		SourceHost: source,
		Path:       path,
		Type:       "d",
		CreatedAt:  time.Unix(createdAtUnix, 0),
	}).Error)
}

func TestRunRestore_CreatesDirectoryStructureForFolderSelection(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	destDir := destBase + "/nested_recovered"
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/tmp/nested","include":true,"dest_path":%q}]}`, destDir)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	info, statErr := os.Stat(destDir)
	require.NoError(t, statErr, "the directory must actually exist on disk now")
	assert.True(t, info.IsDir())

	out := logBuf.String()
	assert.Contains(t, out, "creating restored directory structure")
	assert.Contains(t, out, "restored directory structure created")
	assert.Contains(t, out, "created=1")
	assert.Contains(t, out, "reused=0")
}

func TestRunRestore_ReusesExistingDirectory(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	destDir := destBase + "/already-here"
	require.NoError(t, os.Mkdir(destDir, 0o755))
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/tmp/nested","include":true,"dest_path":%q}]}`, destDir)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	out := logBuf.String()
	assert.Contains(t, out, "created=0")
	assert.Contains(t, out, "reused=1")
}

func TestRunRestore_AbortsOnDirectoryCreationFailureBeforeSummary(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	// A plain file sits where the directory needs to go.
	destDir := destBase + "/blocked"
	require.NoError(t, os.WriteFile(destDir, []byte("data"), 0o644))
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/tmp/nested","include":true,"dest_path":%q}]}`, destDir)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")

	out := logBuf.String()
	assert.Contains(t, out, "failed to create restored directory")
	assert.NotContains(t, out, "restored directory structure created",
		"the summary line must never be logged when phase 1 aborts")
}

func TestRunRestore_ParentBeforeChildOrdering(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	// Three levels deep, seeded in a deliberately non-hierarchical order --
	// if phase 1 didn't sort parent-first, os.Mkdir would fail on whichever
	// child streams in before its parent exists.
	seedDirectory(t, store, "hosta", "/tmp/a/b/c", "job1", 5000)
	seedDirectory(t, store, "hosta", "/tmp/a", "job1", 5000)
	seedDirectory(t, store, "hosta", "/tmp/a/b", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	destRoot := destBase + "/a"
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/tmp/a","include":true,"dest_path":%q}]}`, destRoot)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	for _, p := range []string{destRoot, destRoot + "/b", destRoot + "/b/c"} {
		info, statErr := os.Stat(p)
		require.NoError(t, statErr, p)
		assert.True(t, info.IsDir(), p)
	}
}

func TestRunRestore_NotFoundAbortsBeforePhase1(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	// A directory that WOULD be creatable, but a file-level rule elsewhere
	// in the same rule set matches nothing -- phase 1 must never run.
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	rulesJSON := fmt.Sprintf(`{"rules":[
		{"host":"","path":"/tmp/nested","include":true,"dest_path":%q},
		{"host":"hosta","path":"/etc/never-backed-up.conf","include":true}
	]}`, destBase+"/nested")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 file(s) failed resolution")

	out := logBuf.String()
	assert.NotContains(t, out, "creating restored directory structure",
		"phase 1 must never start when resolution already has a not-found failure")
	_, statErr := os.Stat(destBase + "/nested")
	assert.True(t, os.IsNotExist(statErr), "the directory must not have been created")
}

func TestRunRestore_WritesFileContent(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("vacation photo bytes"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destBase+"/recovered")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	got, readErr := os.ReadFile(destBase + "/recovered/vacation.jpg")
	require.NoError(t, readErr)
	assert.Equal(t, "vacation photo bytes", string(got))

	out := logBuf.String()
	assert.Contains(t, out, "restoring file content")
	assert.Contains(t, out, "restore complete")
	assert.Contains(t, out, "files_written=1")
	assert.NotContains(t, out, "file written",
		"the per-file success line must not appear at the default (Info) log level")
}

// TestRunRestore_DebugLogsPerFileSuccessLine is
// TestRunRestore_WritesFileContent's counterpart at Debug level -- proves
// the per-file "file written" line exists and is gated purely by the
// logger's level (slog.LevelDebug), not by a separate --quiet-style flag.
func TestRunRestore_DebugLogsPerFileSuccessLine(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("vacation photo bytes"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destBase+"/recovered")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	assert.Contains(t, logBuf.String(), "file written")
}

func TestRunRestore_OverwriteFalseSkipsExistingFile(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("new content from bwfs"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	require.NoError(t, os.Mkdir(destBase+"/recovered", 0o755))
	require.NoError(t, os.WriteFile(destBase+"/recovered/vacation.jpg", []byte("original content on disk"), 0o644))
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destBase+"/recovered")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.NoError(t, err)

	got, readErr := os.ReadFile(destBase + "/recovered/vacation.jpg")
	require.NoError(t, readErr)
	assert.Equal(t, "original content on disk", string(got), "overwrite=false must leave the existing file untouched")

	out := logBuf.String()
	assert.Contains(t, out, "files_written=0")
	assert.Contains(t, out, "skipped=1")
}

func TestRunRestore_OverwriteTrueReplacesExistingFile(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/data/photos", "job1", 5000)
	seedRestorableFile(t, store, "hosta", "/data/photos/vacation.jpg", "job1", 5000, []byte("new content from bwfs"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	require.NoError(t, os.Mkdir(destBase+"/recovered", 0o755))
	require.NoError(t, os.WriteFile(destBase+"/recovered/vacation.jpg", []byte("stale content on disk"), 0o644))
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":%q}]}`, destBase+"/recovered")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, true, 4)
	require.NoError(t, err)

	got, readErr := os.ReadFile(destBase + "/recovered/vacation.jpg")
	require.NoError(t, readErr)
	assert.Equal(t, "new content from bwfs", string(got))

	out := logBuf.String()
	assert.Contains(t, out, "files_written=1")
	assert.Contains(t, out, "skipped=0")
}

func TestRunRestore_FileWriteFailureAbortsWithoutSummary(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedRestorableFile(t, store, "hosta", "/data/a.txt", "job1", 5000, []byte("content"))

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &realRestoreServer{store: store}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	// A file-level rule has no accompanying folder rule, so phase 1 never
	// creates any directory -- dest_path's parent is never created, so
	// phase 2's write must fail with the parent missing.
	destBase := t.TempDir()
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"hosta","path":"/data/a.txt","include":true,"dest_path":%q}]}`, destBase+"/missing-parent/a.txt")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false, 4)
	require.Error(t, err)

	out := logBuf.String()
	assert.Contains(t, out, "restoring file content")
	assert.Contains(t, out, "failed to restore file")
	assert.NotContains(t, out, "restore complete",
		"the summary line must never be logged when phase 2 aborts")
}

// cancelDetectingRestoreServer serves file_uuid "slow" by sending Meta
// then blocking until its stream context is cancelled (recording that on
// cancelled) or a generous safety timeout elapses -- proving
// restoreFileContent's cancel-on-first-failure contract deterministically,
// without a wall-clock race. Any other file_uuid ("fail") blocks until
// "slow" has actually reached that blocked-and-listening state (via
// slowBlocked) before failing -- without this gate, "fail" racing ahead
// of "slow" establishing its stream would let restoreFileContent's cancel
// land before "slow"'s handler ever starts listening for it, so the
// server would sit in its 5s fallback instead of ever observing the
// cancellation, flaking this test under scheduling pressure (observed
// empirically on a 4-core sandbox: roughly 1 run in 10).
type cancelDetectingRestoreServer struct {
	pb.UnimplementedRestoreServiceServer
	cancelled   chan struct{}
	slowBlocked chan struct{}
}

func (s *cancelDetectingRestoreServer) RestoreFile(req *pb.RestoreRequest, stream pb.RestoreService_RestoreFileServer) error {
	if req.GetFileUuid() != "slow" {
		<-s.slowBlocked
		return status.Error(codes.Internal, "simulated failure")
	}
	if err := stream.Send(&pb.RestoreEvent{
		Payload: &pb.RestoreEvent_Meta{Meta: &pb.RestoreFileMeta{Size: 4, ChunkCount: 1, ExpectedChecksum: []byte{0, 0, 0, 0}}},
	}); err != nil {
		return err
	}
	close(s.slowBlocked)
	select {
	case <-stream.Context().Done():
		close(s.cancelled)
		return stream.Context().Err()
	case <-time.After(5 * time.Second):
		return fmt.Errorf("test timeout: stream was never cancelled")
	}
}

func TestRestoreFileContent_FirstFailureCancelsOtherInFlightTransfers(t *testing.T) {
	srv := &cancelDetectingRestoreServer{cancelled: make(chan struct{}), slowBlocked: make(chan struct{})}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterRestoreServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()
	client := pb.NewRestoreServiceClient(conn)

	destBase := t.TempDir()
	files := []restoreFile{
		{FileUUID: "slow", Source: "hosta", Path: "/data/slow.bin", DestPath: destBase + "/slow.bin"},
		{FileUUID: "fail", Source: "hosta", Path: "/data/fail.bin", DestPath: destBase + "/fail.bin"},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	err = restoreFileContent(context.Background(), logger, client, files, false, 2)
	require.Error(t, err)

	select {
	case <-srv.cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal(`the in-flight "slow" transfer was never cancelled after "fail" failed`)
	}
}
