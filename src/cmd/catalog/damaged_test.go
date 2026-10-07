package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
)

// fakeDamagedStream is the server side of a ReportDamagedFiles client stream:
// it hands out chunks in order, then ends with endErr (io.EOF for a clean end
// of stream). beforeRecv, when set, runs before each Recv with the chunk index
// about to be returned, so a test can look at the store mid-stream.
type fakeDamagedStream struct {
	grpc.ServerStream
	ctx        context.Context
	chunks     [][]string
	endErr     error
	next       int
	beforeRecv func(i int)
	closedWith *pb.ReportDamagedFilesResponse
}

func (f *fakeDamagedStream) Context() context.Context { return f.ctx }

func (f *fakeDamagedStream) Recv() (*pb.DamagedFilesChunk, error) {
	if f.beforeRecv != nil {
		f.beforeRecv(f.next)
	}
	if f.next >= len(f.chunks) {
		return nil, f.endErr
	}
	ids := f.chunks[f.next]
	f.next++
	return &pb.DamagedFilesChunk{ObjectIds: ids}, nil
}

func (f *fakeDamagedStream) SendAndClose(resp *pb.ReportDamagedFilesResponse) error {
	f.closedWith = resp
	return nil
}

// seedVersions stores one version per object id under node, so the damaged
// flag can be read back through ListEntries.
func seedVersions(t *testing.T, store *catalogstore.Store, node string, objectIDs ...string) {
	t.Helper()
	batch := make([]catalogstore.Entry, len(objectIDs))
	for i, id := range objectIDs {
		batch[i] = catalogstore.Entry{StoreNode: node, JobID: "job-1", ObjectID: id, StoreCreatedAt: time.Now()}
	}
	require.NoError(t, store.EnsureEntries(t.Context(), batch))
}

// damagedSet returns "node/object" for every entry ListEntries marks damaged.
func damagedSet(t *testing.T, store *catalogstore.Store) []string {
	t.Helper()
	recs, _, err := store.ListEntries(t.Context(), catalogstore.ListEntriesFilter{})
	require.NoError(t, err)
	out := []string{}
	for _, r := range recs {
		if r.Damaged {
			out = append(out, r.StoreNode+"/"+r.ObjectID)
		}
	}
	return out
}

func TestReportDamagedFiles_ReplacesPeerSetOnlyAfterWholeStream(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	seedVersions(t, store, "bwfs-a.internal", "old", "o1", "o2", "o3")
	seedVersions(t, store, "bwfs-b.internal", "o1")
	require.NoError(t, store.ReplaceDamagedFiles(t.Context(), "bwfs-a.internal", []string{"old"}))
	require.NoError(t, store.ReplaceDamagedFiles(t.Context(), "bwfs-b.internal", []string{"o1"}))

	stream := &fakeDamagedStream{
		ctx:    fakeAuthContext(t, "bwfs-a.internal"),
		chunks: [][]string{{"o1", "o2"}, {"o3"}},
		endErr: io.EOF,
		beforeRecv: func(i int) {
			if i > 0 { // mid-stream: nothing may have been replaced yet
				assert.ElementsMatch(t, []string{"bwfs-a.internal/old", "bwfs-b.internal/o1"}, damagedSet(t, store))
			}
		},
	}
	require.NoError(t, srv.ReportDamagedFiles(stream))

	assert.NotNil(t, stream.closedWith, "a clean end of stream is acknowledged")
	assert.ElementsMatch(t,
		[]string{"bwfs-a.internal/o1", "bwfs-a.internal/o2", "bwfs-a.internal/o3", "bwfs-b.internal/o1"},
		damagedSet(t, store), "the peer's set is replaced; another node's set is untouched")
}

func TestReportDamagedFiles_EmptyStreamClearsPeerSet(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	seedVersions(t, store, "bwfs-a.internal", "o1")
	require.NoError(t, store.ReplaceDamagedFiles(t.Context(), "bwfs-a.internal", []string{"o1"}))

	stream := &fakeDamagedStream{ctx: fakeAuthContext(t, "bwfs-a.internal"), endErr: io.EOF}
	require.NoError(t, srv.ReportDamagedFiles(stream))

	assert.Empty(t, damagedSet(t, store))
}

// A stream that breaks off (client cancelled, connection lost) is a partial
// set; applying it would clear damage that is still real.
func TestReportDamagedFiles_AbortedStreamChangesNothing(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	seedVersions(t, store, "bwfs-a.internal", "old", "o1")
	require.NoError(t, store.ReplaceDamagedFiles(t.Context(), "bwfs-a.internal", []string{"old"}))

	stream := &fakeDamagedStream{
		ctx:    fakeAuthContext(t, "bwfs-a.internal"),
		chunks: [][]string{{"o1"}},
		endErr: status.Error(codes.Canceled, "client cancelled"),
	}
	err := srv.ReportDamagedFiles(stream)

	require.Error(t, err)
	assert.Equal(t, codes.Canceled, status.Code(err))
	assert.Nil(t, stream.closedWith)
	assert.ElementsMatch(t, []string{"bwfs-a.internal/old"}, damagedSet(t, store))
}

func TestReportDamagedFiles_NoPeerIdentityReturnsErrorAndChangesNothing(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	seedVersions(t, store, "bwfs-a.internal", "old")
	require.NoError(t, store.ReplaceDamagedFiles(t.Context(), "bwfs-a.internal", []string{"old"}))

	stream := &fakeDamagedStream{ctx: context.Background(), endErr: io.EOF}
	require.Error(t, srv.ReportDamagedFiles(stream))

	assert.ElementsMatch(t, []string{"bwfs-a.internal/old"}, damagedSet(t, store))
}

// startCatalogMTLS serves srv over a real mTLS listener using the project's
// fixture certs (client SAN "bwfs.internal", no authz-role attribute), with
// the given role matrix, and returns a connected client.
func startCatalogMTLS(t *testing.T, srv *catalogServer, roles map[string][]string) pb.CatalogServiceClient {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close()) // release the port; connection.StartServer re-binds it

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, roles, func(s *grpc.Server) {
			pb.RegisterCatalogServiceServer(s, srv)
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "server did not start listening")

	conn, err := connection.Connect("localhost", port, 5, fixtureCertsDir)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	return pb.NewCatalogServiceClient(conn)
}

func sendDamaged(t *testing.T, client pb.CatalogServiceClient, chunks ...[]string) error {
	t.Helper()
	stream, err := client.ReportDamagedFiles(t.Context())
	require.NoError(t, err)
	for _, ids := range chunks {
		if err := stream.Send(&pb.DamagedFilesChunk{ObjectIds: ids}); err != nil {
			break // the real error surfaces from CloseAndRecv
		}
	}
	_, err = stream.CloseAndRecv()
	return err
}

// The node is the CA-verified certificate's hostname, proven over a genuine
// mTLS handshake rather than a fabricated context.
func TestReportDamagedFiles_RealMTLSRoundTripUsesCertHostname(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	seedVersions(t, store, "bwfs.internal", "o1", "o2")
	client := startCatalogMTLS(t, srv, nil)

	require.NoError(t, sendDamaged(t, client, []string{"o1"}))

	assert.ElementsMatch(t, []string{"bwfs.internal/o1"}, damagedSet(t, store))
}

func TestReportDamagedFiles_RealMTLSRoundTrip_NonStoreRoleDenied(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	seedVersions(t, store, "bwfs.internal", "o1")
	client := startCatalogMTLS(t, srv, roleRequirements())

	err := sendDamaged(t, client, []string{"o1"})

	require.Error(t, err)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
	assert.Empty(t, damagedSet(t, store), "the denied report must not have written anything")
}

func TestListEntries_CarriesDamagedFlag(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	seedVersions(t, store, "bwfs-a", "bad", "good")
	require.NoError(t, store.ReplaceDamagedFiles(t.Context(), "bwfs-a", []string{"bad"}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)

	got := map[string]bool{}
	for _, e := range resp.GetEntries() {
		got[e.GetObjectId()] = e.GetDamaged()
	}
	assert.Equal(t, map[string]bool{"bad": true, "good": false}, got)
}
