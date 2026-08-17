package main

import (
	"context"
	"fmt"
	"io"
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

// collectingStream is a grpc.ServerStreamingServer[pb.FileRow] test double
// that records every row Send is called with, letting a unit test call
// listServer.ListFiles directly without a real network round trip.
type collectingStream struct {
	grpc.ServerStream
	rows []*pb.FileRow
}

func (s *collectingStream) Send(row *pb.FileRow) error {
	s.rows = append(s.rows, row)
	return nil
}

// erroringStream is a grpc.ServerStreamingServer[pb.FileRow] test double
// that fails every Send call after the first, proving ListFiles surfaces
// a mid-stream send error from the handler rather than swallowing it.
type erroringStream struct {
	grpc.ServerStream
	sent int
}

func (s *erroringStream) Send(row *pb.FileRow) error {
	s.sent++
	if s.sent > 1 {
		return fmt.Errorf("simulated send failure")
	}
	return nil
}

func TestListFiles_EmptyStoreReturnsEmptyRows(t *testing.T) {
	srv, _ := newTestListServer(t)

	stream := &collectingStream{}
	err := srv.ListFiles(&pb.ListRequest{}, stream)
	require.NoError(t, err)
	assert.Empty(t, stream.rows)
}

func TestListFiles_FiltersByServerName(t *testing.T) {
	srv, store := newTestListServer(t)

	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hostb:f:/data/b.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hostb:f:/data/b.txt:1000", []byte{5, 6, 7, 8}))

	stream := &collectingStream{}
	err := srv.ListFiles(&pb.ListRequest{ServerName: "hosta"}, stream)
	require.NoError(t, err)
	require.Len(t, stream.rows, 1)
	assert.Equal(t, "hosta", stream.rows[0].Source)
	assert.Equal(t, "/data/a.txt", stream.rows[0].Path)
}

func TestListFiles_FiltersByPathPrefix(t *testing.T) {
	srv, store := newTestListServer(t)

	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hosta:f:/other/c.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/other/c.txt:1000", []byte{5, 6, 7, 8}))

	stream := &collectingStream{}
	err := srv.ListFiles(&pb.ListRequest{Path: "/data"}, stream)
	require.NoError(t, err)
	require.Len(t, stream.rows, 1)
	assert.Equal(t, "/data/a.txt", stream.rows[0].Path)
}

func TestListFiles_MidStreamSendErrorSurfaces(t *testing.T) {
	srv, store := newTestListServer(t)
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/a.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/a.txt:1000", []byte{1, 2, 3, 4}))
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/b.txt:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/b.txt:1000", []byte{5, 6, 7, 8}))

	stream := &erroringStream{}
	err := srv.ListFiles(&pb.ListRequest{}, stream)
	require.Error(t, err, "the handler must propagate Send's error instead of swallowing it")
	assert.Equal(t, 2, stream.sent, "the second Send call is where erroringStream fails")
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
	stream, err := client.ListFiles(context.Background(), &pb.ListRequest{ServerName: "hosta"})
	require.NoError(t, err)

	var rows []*pb.FileRow
	for {
		row, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		rows = append(rows, row)
	}
	require.Len(t, rows, 1)
	assert.Equal(t, "/data/a.txt", rows[0].Path)
}
