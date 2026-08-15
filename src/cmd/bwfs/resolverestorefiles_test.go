package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func collectResolved(t *testing.T, store *wfs.Store, filter *pb.RestoreFileFilter) []resolvedCandidate {
	t.Helper()
	var got []resolvedCandidate
	err := resolveRestoreFilter(store, filter, func(c resolvedCandidate) bool {
		got = append(got, c)
		return true
	})
	require.NoError(t, err)
	return got
}

func seedFile(t *testing.T, store *wfs.Store, fileID string, size int64, checksum []byte, jobID string, versionCreatedAtUnix int64) {
	t.Helper()
	require.NoError(t, store.CreateFileData(fileID, size))
	require.NoError(t, store.FinalizeFileData(fileID, checksum))
	require.NoError(t, store.RawDB().Model(&wfs.FileVersionRecord{}).
		Create(&wfs.FileVersionRecord{
			ObjectID:  fileID,
			JobID:     jobID,
			CreatedAt: unixTime(versionCreatedAtUnix),
		}).Error)
}

func TestResolveRestoreFilter_ExactFileMatch(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/etc/nginx.conf:1000", 10, []byte{1}, "job1", 5000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/etc/nginx.conf"})
	require.Len(t, got, 1)
	assert.Equal(t, "hosta", got[0].Source)
	assert.Equal(t, "/etc/nginx.conf", got[0].Path)
}

func TestResolveRestoreFilter_HostAgnosticFolderMatchesEveryHost(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/etc/a.conf:1000", 10, []byte{1}, "job1", 5000)
	seedFile(t, store, "fs://hostb:f:/etc/sub/b.conf:1000", 10, []byte{1}, "job1", 5000)
	seedFile(t, store, "fs://hosta:f:/etc2/other.conf:1000", 10, []byte{1}, "job1", 5000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Path: "/etc", PathIsPrefix: true})
	require.Len(t, got, 2)
	paths := []string{got[0].Path, got[1].Path}
	assert.ElementsMatch(t, []string{"/etc/a.conf", "/etc/sub/b.conf"}, paths)
}

func TestResolveRestoreFilter_PicksLatestVersionInsideWindow(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	// Two distinct mtimes (content versions) of the same path.
	seedFile(t, store, "fs://hosta:f:/data/f.txt:1000", 10, []byte{1}, "job1", 1000)
	seedFile(t, store, "fs://hosta:f:/data/f.txt:2000", 20, []byte{2}, "job2", 2000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/data/f.txt", NotBefore: 1, NotAfter: 1500})
	require.Len(t, got, 1)
	assert.Equal(t, int64(10), got[0].Size) // the mtime=1000 version, whose version is inside the window
}

func TestResolveRestoreFilter_UnchangedFileStaysFoundAcrossManyReattestations(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	// Content created in January (created_at=1000), never changes, but is
	// re-attested (re-backed-up unchanged) through August.
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/stable.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/stable.txt:1000", []byte{1}))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/data/stable.txt:1000", JobID: "jan", CreatedAt: unixTime(1000)}).Error)
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/data/stable.txt:1000", JobID: "jul", CreatedAt: unixTime(7000)}).Error)

	// A window around July, long after the content's original upload.
	got := collectResolved(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/data/stable.txt", NotBefore: 6000, NotAfter: 8000})
	require.Len(t, got, 1, "the July re-attestation must satisfy the window even though FileDataRecord.CreatedAt is January")
}

func TestResolveRestoreFilter_NoVersionInWindowReturnsNothing(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/data/f.txt:1000", 10, []byte{1}, "job1", 1000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/data/f.txt", NotBefore: 5000, NotAfter: 6000})
	assert.Empty(t, got)
}

func TestResolveRestoreFilter_FolderPrefixDoesNotOverMatchSiblingPath(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/etc/a.conf:1000", 10, []byte{1}, "job1", 5000)
	seedFile(t, store, "fs://hosta:f:/etc2/b.conf:1000", 10, []byte{1}, "job1", 5000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Path: "/etc", PathIsPrefix: true})
	require.Len(t, got, 1)
	assert.Equal(t, "/etc/a.conf", got[0].Path)
}

func unixTime(sec int64) time.Time { return time.Unix(sec, 0) }

func TestResolveRestoreFiles_GRPCRoundTrip(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedFile(t, store, "fs://hosta:f:/etc/a.conf:1000", 10, []byte{1}, "job1", 5000)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewListServer(store, logger)

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewListServiceClient(conn)
	stream, err := client.ResolveRestoreFiles(context.Background(), &pb.ResolveRestoreFilesRequest{
		Filters: []*pb.RestoreFileFilter{
			{Host: "hosta", Path: "/etc/a.conf"},
			{Path: "/nonexistent", PathIsPrefix: true},
		},
	})
	require.NoError(t, err)

	var got []*pb.ResolveRestoreFilesResponse
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		got = append(got, resp)
	}

	require.Len(t, got, 1)
	assert.Equal(t, "/etc/a.conf", got[0].GetRow().GetPath())
	assert.Equal(t, int32(0), got[0].GetFilterIndex())
}

// failingStream is a minimal mock implementing pb.ListService_ResolveRestoreFilesServer
// that fails on the second Send call.
type failingStream struct {
	sendCount int
}

func (f *failingStream) Send(*pb.ResolveRestoreFilesResponse) error {
	f.sendCount++
	if f.sendCount > 1 {
		return io.EOF // simulate send failure
	}
	return nil
}

func (f *failingStream) SetHeader(metadata.MD) error     { return nil }
func (f *failingStream) SendHeader(metadata.MD) error    { return nil }
func (f *failingStream) SetTrailer(metadata.MD)          {}
func (f *failingStream) Context() context.Context        { return context.Background() }
func (f *failingStream) RecvMsg(interface{}) error       { return nil }
func (f *failingStream) SendMsg(interface{}) error       { return nil }

func TestResolveRestoreFiles_SendErrorIsReturned(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	// Seed two files so the handler tries to send twice
	seedFile(t, store, "fs://hosta:f:/etc/a.conf:1000", 10, []byte{1}, "job1", 5000)
	seedFile(t, store, "fs://hosta:f:/etc/b.conf:2000", 20, []byte{2}, "job1", 5000)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewListServer(store, logger)

	stream := &failingStream{}

	// Call ResolveRestoreFiles with a filter that matches both files
	err = srv.ResolveRestoreFiles(&pb.ResolveRestoreFilesRequest{
		Filters: []*pb.RestoreFileFilter{
			{Path: "/etc", PathIsPrefix: true},
		},
	}, stream)

	// The handler should return the send error, not nil or the query error
	require.Error(t, err)
	assert.Equal(t, io.EOF, err, "should return the stream.Send error, not query error or nil")
}
