package main

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"testing"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// captureStdout redirects os.Stdout for the duration of fn and returns
// what was written -- listformat.RenderTable/RenderJSON both write
// directly to os.Stdout with no writer injection seam.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w
	t.Cleanup(func() {
		os.Stdout = old
	})
	fn()
	require.NoError(t, w.Close())
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(data)
}

func runListWithDialer(t *testing.T, lis *bufconn.Listener, serverName, pathFilter, filter, output string) error {
	t.Helper()
	conn, err := grpc.NewClient("passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer conn.Close()
	return runListWithConn(conn, serverName, pathFilter, filter, output, "test-job")
}

func TestRunList_StreamsMultipleRowsIntoTableOutput(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/b.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/b.txt:1000", []byte{5, 6, 7, 8}))

	listSrv := &testResolveServer{store: store}
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	out := captureStdout(t, func() {
		err := runListWithDialer(t, lis, "hosta", "", "", "table")
		require.NoError(t, err)
	})
	require.True(t, strings.Contains(out, "/data/a.txt") && strings.Contains(out, "/data/b.txt"),
		"expected both streamed rows in the rendered table, got:\n%s", out)
}

func TestRunList_MidStreamErrorDiscardsPartialOutputAndReturnsError(t *testing.T) {
	listSrv := &failingAfterFirstRowListServer{
		Row: &pb.FileRow{Source: "hosta", Path: "/data/a.txt", Type: "f", Size: 4},
	}
	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	var callErr error
	out := captureStdout(t, func() {
		callErr = runListWithDialer(t, lis, "", "", "", "table")
	})
	require.Error(t, callErr)
	require.Empty(t, out, "a mid-stream failure must never render partial output")
}
