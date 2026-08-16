package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
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
