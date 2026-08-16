package main

import (
	"context"
	"fmt"
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

// A folder rule on a Windows-style path must match its children. The
// separator there is a backslash, so a range built only around '/' never
// matched anything and the rule silently verified zero files.
func TestResolveRestoreFilter_WindowsBackslashFolderMatchesChildren(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, `fs://winhost:f:C:\Users\foo\bar.txt:1000`, 10, []byte{1}, "job1", 5000)
	seedFile(t, store, `fs://winhost:f:C:\Users2\other.txt:1000`, 10, []byte{1}, "job1", 5000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Path: `C:\Users`, PathIsPrefix: true})
	require.Len(t, got, 1, "a backslash folder rule must match its backslash children, and only those")
	assert.Equal(t, `C:\Users\foo\bar.txt`, got[0].Path)
}

// A folder rule at a filesystem root already ends in a separator.
// Appending another one produced an upper bound ("/0") that sorts below
// every real child, so a root rule matched nothing at all.
func TestResolveRestoreFilter_RootFolderMatchesChildren(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rulePath string
		fileID   string
		wantPath string
		otherID  string
	}{
		{
			name:     "unix root",
			rulePath: "/",
			fileID:   "fs://hosta:f:/etc/nginx.conf:1000",
			wantPath: "/etc/nginx.conf",
			otherID:  `fs://winhost:f:C:\Users\x.txt:1000`,
		},
		{
			name:     "windows drive root",
			rulePath: `C:\`,
			fileID:   `fs://winhost:f:C:\Users\anything.txt:1000`,
			wantPath: `C:\Users\anything.txt`,
			otherID:  "fs://hosta:f:/etc/nginx.conf:1000",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := wfs.New(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { store.Close() })

			seedFile(t, store, tc.fileID, 10, []byte{1}, "job1", 5000)
			// A file under the *other* root, proving the range stays scoped
			// to the requested root rather than degenerating into match-all.
			seedFile(t, store, tc.otherID, 10, []byte{1}, "job1", 5000)

			got := collectResolved(t, store, &pb.RestoreFileFilter{Path: tc.rulePath, PathIsPrefix: true})
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantPath, got[0].Path)
		})
	}
}

// One host-agnostic folder rule can span source hosts of both conventions,
// so both separator ranges have to be live in the same query -- picking
// one convention per query would silently drop the other host's files.
func TestResolveRestoreFilter_FolderMatchesBothSeparatorConventionsInOneQuery(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://nixhost:f:/srv/data/a.txt:1000", 10, []byte{1}, "job1", 5000)
	seedFile(t, store, `fs://winhost:f:/srv/data\b.txt:1000`, 10, []byte{1}, "job1", 5000)
	seedFile(t, store, "fs://nixhost:f:/srv/database/c.txt:1000", 10, []byte{1}, "job1", 5000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Path: "/srv/data", PathIsPrefix: true})
	require.Len(t, got, 2)
	assert.ElementsMatch(t, []string{"/srv/data/a.txt", `/srv/data\b.txt`},
		[]string{got[0].Path, got[1].Path},
		"both separators must match, and the /srv/database sibling must not")
}

func TestRestoreChildRanges(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		path                         string
		wantUnixLo, wantUnixHi       string
		wantWindowsLo, wantWindowsHi string
	}{
		{"plain unix path", "/etc", "/etc/", "/etc0", `/etc\`, "/etc]"},
		{"trailing slash stripped", "/etc/", "/etc/", "/etc0", `/etc\`, "/etc]"},
		{"unix root", "/", "/", "0", `\`, "]"},
		{"windows path", `C:\Users`, `C:\Users/`, `C:\Users0`, `C:\Users\`, `C:\Users]`},
		{"windows drive root", `C:\`, "C:/", "C:0", `C:\`, "C:]"},
		{"windows drive root without separator", "C:", "C:/", "C:0", `C:\`, "C:]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := restoreChildRanges(tc.path)
			assert.Equal(t, tc.wantUnixLo, got.Unix.Lower)
			assert.Equal(t, tc.wantUnixHi, got.Unix.Upper)
			assert.Equal(t, tc.wantWindowsLo, got.Windows.Lower)
			assert.Equal(t, tc.wantWindowsHi, got.Windows.Upper)
			// The upper bound must be the immediate successor byte of the
			// separator, or the range would leak into sibling paths.
			assert.Equal(t, got.Unix.Lower[len(got.Unix.Lower)-1]+1, got.Unix.Upper[len(got.Unix.Upper)-1])
			assert.Equal(t, got.Windows.Lower[len(got.Windows.Lower)-1]+1, got.Windows.Upper[len(got.Windows.Upper)-1])
		})
	}
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

func (f *failingStream) SetHeader(metadata.MD) error  { return nil }
func (f *failingStream) SendHeader(metadata.MD) error { return nil }
func (f *failingStream) SetTrailer(metadata.MD)       {}
func (f *failingStream) Context() context.Context     { return context.Background() }
func (f *failingStream) RecvMsg(interface{}) error    { return nil }
func (f *failingStream) SendMsg(interface{}) error    { return nil }

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

// seedDirectory writes a file_version_records row shaped like a directory
// bwfs actually backed up -- no file_data_records row (directories never
// get one), real source_host/path/type columns (Task 1), no checksum
// concept. Mirrors seedFile's shape for the parts that apply.
func seedDirectory(t *testing.T, store *wfs.Store, source, path, jobID string, createdAtUnix int64) {
	t.Helper()
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{
		ObjectID:   fmt.Sprintf("fs://%s:d:%s:%d", source, path, createdAtUnix),
		JobID:      jobID,
		SourceHost: source,
		Path:       path,
		Type:       "d",
		CreatedAt:  unixTime(createdAtUnix),
	}).Error)
}

func collectResolvedDirectories(t *testing.T, store *wfs.Store, filter *pb.RestoreFileFilter) [][2]string {
	t.Helper()
	var got [][2]string
	err := resolveRestoreDirectoryFilter(store, filter, func(source, path string) bool {
		got = append(got, [2]string{source, path})
		return true
	})
	require.NoError(t, err)
	return got
}

func TestResolveRestoreDirectoryFilter_HostSpecificMatch(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)
	seedDirectory(t, store, "hostb", "/tmp/nested", "job1", 5000)

	got := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/tmp", PathIsPrefix: true})
	require.Len(t, got, 1)
	assert.Equal(t, [2]string{"hosta", "/tmp/nested"}, got[0])
}

func TestResolveRestoreDirectoryFilter_HostAgnosticMatchesEveryHost(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)
	seedDirectory(t, store, "hostb", "/tmp/nested/sub", "job1", 5000)
	seedDirectory(t, store, "hosta", "/tmp2/other", "job1", 5000)

	got := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Path: "/tmp", PathIsPrefix: true})
	require.Len(t, got, 2)
	assert.ElementsMatch(t, [][2]string{{"hosta", "/tmp/nested"}, {"hostb", "/tmp/nested/sub"}}, got)
}

func TestResolveRestoreDirectoryFilter_ExactPathFilterNeverMatchesDirectories(t *testing.T) {
	// A non-prefix filter is what a host-specific FILE rule builds
	// (buildRestoreFilters: PathIsPrefix = rule.Host == ""). This test
	// pins that resolveRestoreDirectoryFilter itself doesn't need the
	// caller to gate it -- an exact-path filter is forced to match nothing,
	// via the query's unconditional "1 = 0" branch for the non-prefix case
	// (not a "path = ?" equality check, which would wrongly admit a
	// directory row that happens to share the filter's own literal path).
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	got := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/tmp/nested", PathIsPrefix: false})
	assert.Empty(t, got)
}

func TestResolveRestoreDirectoryFilter_TimeframeWindowing(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 1000)

	inWindow := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Path: "/tmp", PathIsPrefix: true, NotBefore: 500, NotAfter: 1500})
	assert.Len(t, inWindow, 1)

	outOfWindow := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Path: "/tmp", PathIsPrefix: true, NotBefore: 5000, NotAfter: 6000})
	assert.Empty(t, outOfWindow)
}

func TestResolveRestoreFiles_GRPCRoundTrip_IncludesDirectoryRows(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedFile(t, store, "fs://hosta:f:/tmp/nested/a.txt:1000", 10, []byte{1}, "job1", 5000)
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

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
		Filters: []*pb.RestoreFileFilter{{Path: "/tmp/nested", PathIsPrefix: true}},
	})
	require.NoError(t, err)

	var gotFile, gotDir bool
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		switch resp.GetRow().GetType() {
		case "f":
			gotFile = true
			assert.Equal(t, "/tmp/nested/a.txt", resp.GetRow().GetPath())
		case "d":
			gotDir = true
			assert.Equal(t, "/tmp/nested", resp.GetRow().GetPath())
			assert.Empty(t, resp.GetRow().GetFileUuid(), "a directory row must never carry a file_uuid")
		}
	}
	assert.True(t, gotFile, "the file row must still be streamed")
	assert.True(t, gotDir, "the directory row must now also be streamed")
}
