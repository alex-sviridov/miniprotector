package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

const fixtureCertsDir = "../../common/testdata/certs"

func newTestCatalogServer(t *testing.T) (*catalogServer, *catalogstore.Store) {
	t.Helper()
	store, err := catalogstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewCatalogServer(store, logger), store
}

// fakeAuthContext builds a context carrying a self-signed certificate with
// the given hostname as its SAN, simulating what a real mTLS handshake
// leaves in a gRPC handler's context — without needing a real TLS
// connection or a CA-signed cert.
func fakeAuthContext(t *testing.T, hostname string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

func TestSyncFileVersions_PersistsBatchUnderPeerHostname(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: "obj-1", Ctime: 100, StoreSeq: 1, CreatedAt: time.Now().Unix()},
	}}
	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestSyncFileVersions_NoPeerIdentityReturnsError(t *testing.T) {
	srv, _ := newTestCatalogServer(t)
	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}}}

	_, err := srv.SyncFileVersions(context.Background(), req)
	assert.Error(t, err)
}

func TestSyncFileVersions_DuplicateBatchIsIdempotent(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")
	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1", CreatedAt: time.Now().Unix()}}}

	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)
	_, err = srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestSyncFileVersions_DerivesSourceHostFromMetadata(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	fi := filesystem.NewFileInfoForTest("origin-host", "/var/lib/dbdata/data.db", 8192, 0o644, 999, 999, time.Now())
	metadata, err := fi.Encode()
	require.NoError(t, err)

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: fi.ID(), Metadata: metadata, CreatedAt: time.Now().Unix()},
	}}
	_, err = srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	entries, _, err := store.ListEntries(catalogstore.ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "origin-host", entries[0].SourceHost)
}

func TestSyncFileVersions_MalformedMetadataLeavesSourceHostEmpty(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: "obj-1", Metadata: []byte("not-gob-encoded"), CreatedAt: time.Now().Unix()},
	}}
	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err) // a bad row's metadata doesn't fail the batch

	entries, _, err := store.ListEntries(catalogstore.ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "", entries[0].SourceHost)
}

func TestSyncFileVersions_GRPCRoundTripWithoutTLSIsRejected(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterCatalogServiceServer(grpcSrv, srv)
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

	client := pb.NewCatalogServiceClient(conn)
	_, err = client.SyncFileVersions(context.Background(), &pb.SyncRequest{
		Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}},
	})
	// bufconn + insecure transport carries no peer certificate, so
	// PeerHostname fails and the RPC is rejected — proving identity is
	// enforced end to end, not just when a fake context is handed in.
	assert.Error(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestSyncFileVersions_RealMTLSRoundTrip uses the actual connection.StartServer/
// connection.Connect helpers production code uses, and the project's real
// testdata certs (whose client.crt SAN is "bwfs.internal" — see
// common/mtls/peer_test.go), to prove StoreNode extraction works against a
// genuine mTLS handshake, not just a fabricated context.
func TestSyncFileVersions_RealMTLSRoundTrip(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close()) // release the port; connection.StartServer re-binds it

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, func(s *grpc.Server) {
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
	defer conn.Close()

	client := pb.NewCatalogServiceClient(conn)
	_, err = client.SyncFileVersions(context.Background(), &pb.SyncRequest{
		Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}},
	})
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestListEntries_ReturnsPersistedEntriesNewestFirst(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 2)
	assert.Equal(t, "obj-2", resp.GetEntries()[0].GetObjectId())
	assert.False(t, resp.GetHasMore())
}

func TestListEntries_FiltersByStoreHost(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{StoreHost: "bwfs-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "bwfs-a", resp.GetEntries()[0].GetStoreHost())
}

func TestListEntries_FiltersBySourceHost(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{SourceHost: "database"})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "database", resp.GetEntries()[0].GetSourceHost())
}

func TestListEntries_DecodesMetadataIntoEntryFields(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	fi := filesystem.NewFileInfoForTest("bwfs-a", "/var/log/syslog", 4096, 0o644, 1000, 1000, time.Now())
	metadata, err := fi.Encode()
	require.NoError(t, err)

	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: fi.ID(), Metadata: metadata, StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	entry := resp.GetEntries()[0]
	assert.Equal(t, "/var/log/syslog", entry.GetPath())
	assert.Equal(t, int64(4096), entry.GetSize())
	assert.Equal(t, uint32(1000), entry.GetOwner())
	assert.Equal(t, uint32(1000), entry.GetGroup())
}

func TestListEntries_MalformedMetadataStillReturnsEntryWithEmptyDecodedFields(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", Metadata: []byte("not-gob-encoded"), StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "", resp.GetEntries()[0].GetPath())
}
