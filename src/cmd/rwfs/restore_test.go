package main

import (
	"bytes"
	"context"
	"fmt"
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
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// runRestoreWithDialer mirrors verify_test.go's runVerifyWithDialer --
// runRestore always dials via connection.Connect (host/port plus mTLS
// certs), which has no bufconn injection seam, so this duplicates just the
// dial step against lis, then calls runRestoreWithConn, the exact same
// package-level resolution/dispatch logic runRestore itself calls after
// dialing.
func runRestoreWithDialer(t *testing.T, logger *slog.Logger, lis *bufconn.Listener, rulesJSON string, overwrite bool) error {
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

	return runRestoreWithConn(logger, conn, overwrite, rules, false, "test-job")
}

func TestRunRestore_LogsResolvedFileWithRenamedDestPath(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/photos/vacation.jpg:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/photos/vacation.jpg:1000", expectedCRC32(t, [][]byte{{1, 2, 3, 4}})))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/data/photos/vacation.jpg:1000", JobID: "job1", CreatedAt: time.Unix(5000, 0)}).Error)

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

	rulesJSON := `{"rules":[{"host":"","path":"/data/photos","include":true,"dest_path":"/data/photos_recovered"}]}`

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, true)
	require.NoError(t, err)

	out := logBuf.String()
	assert.Contains(t, out, `source=hosta`)
	assert.Contains(t, out, `path=/data/photos/vacation.jpg`)
	assert.Contains(t, out, `dest_path=/data/photos_recovered/vacation.jpg`)
	assert.Contains(t, out, `overwrite=true`)
	assert.Empty(t, restoreSrv.Requested(),
		"rwfs restore must never call RestoreFile in this round -- it only resolves and logs")
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

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
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

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
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

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
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

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
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

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
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

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
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

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 file(s) failed resolution")

	out := logBuf.String()
	assert.NotContains(t, out, "creating restored directory structure",
		"phase 1 must never start when resolution already has a not-found failure")
	_, statErr := os.Stat(destBase + "/nested")
	assert.True(t, os.IsNotExist(statErr), "the directory must not have been created")
}
