package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func newTestListServer(t *testing.T) (*listServer, *wfs.Store) {
	t.Helper()
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewListServer(store, logger), store
}

func TestListFiles_EmptyStoreReturnsEmptyRows(t *testing.T) {
	srv, _ := newTestListServer(t)

	resp, err := srv.ListFiles(context.Background(), &pb.ListRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Rows)
}

func TestListFiles_FiltersByServerName(t *testing.T) {
	srv, store := newTestListServer(t)

	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hostb:f:/data/b.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hostb:f:/data/b.txt:1000", []byte{5, 6, 7, 8}))

	resp, err := srv.ListFiles(context.Background(), &pb.ListRequest{ServerName: "hosta"})
	require.NoError(t, err)
	require.Len(t, resp.Rows, 1)
	assert.Equal(t, "hosta", resp.Rows[0].Source)
	assert.Equal(t, "/data/a.txt", resp.Rows[0].Path)
}

func TestListFiles_FiltersByPathPrefix(t *testing.T) {
	srv, store := newTestListServer(t)

	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hosta:f:/other/c.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/other/c.txt:1000", []byte{5, 6, 7, 8}))

	resp, err := srv.ListFiles(context.Background(), &pb.ListRequest{Path: "/data"})
	require.NoError(t, err)
	require.Len(t, resp.Rows, 1)
	assert.Equal(t, "/data/a.txt", resp.Rows[0].Path)
}

func TestListFiles_GRPCRoundTrip(t *testing.T) {
	srv, store := newTestListServer(t)
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewListServiceClient(conn)
	resp, err := client.ListFiles(context.Background(), &pb.ListRequest{ServerName: "hosta"})
	require.NoError(t, err)
	require.Len(t, resp.Rows, 1)
	assert.Equal(t, "/data/a.txt", resp.Rows[0].Path)
}
