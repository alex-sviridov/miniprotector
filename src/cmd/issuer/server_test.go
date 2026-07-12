package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log/slog"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"

	pb "github.com/alex-sviridov/miniprotector/api"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func newTestIssuerStore(t *testing.T) *clientmanagerstore.Store {
	t.Helper()
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

// peerCertContext builds a context carrying only a verified mTLS peer
// certificate for hostname, with no gRPC metadata attached. fakeAuthContext
// (below) layers job-id metadata on top for the common case; this is used
// directly by TestRequestOperatingCert_MissingJobIDRejectedWithoutMinting
// to exercise the "no job-id metadata at all" path.
func peerCertContext(t *testing.T, hostname string) context.Context {
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

// fakeAuthContext mirrors cmd/catalog/server_test.go's helper of the same
// name: a self-signed cert with the given hostname as its SAN, plus job-id
// metadata every RequestOperatingCert test needs by default now that it's
// required -- simulating a verified mTLS peer identity and an already-
// job-id-tagged call, without a real handshake.
func fakeAuthContext(t *testing.T, hostname string) context.Context {
	t.Helper()
	return metadata.NewIncomingContext(peerCertContext(t, hostname), metadata.Pairs("job-id", "test-job-id"))
}

// testCSR builds a minimal, validly-signed CSR for use as request payload
// -- the server never inspects its subject/SANs (those come from the
// database, keyed by the verified peer hostname), only forwards it.
func testCSR(t *testing.T) *x509.CertificateRequest {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(der)
	require.NoError(t, err)
	return csr
}

func TestRequestOperatingCert_KnownNotRevokedHostSucceeds(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"node-1.internal"}, time.Now()))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindAttribute, "role", "prod-db"))

	var gotHostname string
	var gotSANs []string
	var gotAttrs map[string]string
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		gotHostname = hostname
		gotSANs = sans
		gotAttrs = attributes
		return []byte("fake-cert-chain"), nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	resp, err := srv.RequestOperatingCert(fakeAuthContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-cert-chain"), resp.CertChainPem)
	assert.Equal(t, "node-1", gotHostname)
	assert.Equal(t, []string{"node-1.internal"}, gotSANs)
	assert.Equal(t, map[string]string{"role": "prod-db"}, gotAttrs)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	require.NotNil(t, got.LastSeenAt, "last_seen should be stamped on success")
}

func TestRequestOperatingCert_RevokedHostRejectedWithoutMinting(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	require.NoError(t, store.SetRevoked("node-1", true, time.Now()))

	called := false
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		called = true
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(fakeAuthContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)
	assert.False(t, called, "mintSign must not be called for a revoked host")

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Nil(t, got.LastSeenAt, "last_seen must not be stamped when the request was refused")
}

func TestRequestOperatingCert_UnknownHostRejectedWithoutMinting(t *testing.T) {
	store := newTestIssuerStore(t)

	called := false
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		called = true
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(fakeAuthContext(t, "ghost"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)
	assert.False(t, called, "mintSign must not be called for a hostname not tracked at all")
}

func TestRequestOperatingCert_NoPeerIdentityRejected(t *testing.T) {
	store := newTestIssuerStore(t)
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		t.Fatal("mintSign must not be called without a peer identity")
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(context.Background(), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)
}

func TestRequestOperatingCert_MissingJobIDRejectedWithoutMinting(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	called := false
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		called = true
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(peerCertContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)
	assert.False(t, called, "mintSign must not be called when job-id metadata is missing")
}

func TestRequestOperatingCert_MalformedCSRRejected(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		t.Fatal("mintSign must not be called for an unparseable CSR")
		return nil, nil
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(fakeAuthContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: []byte("not a csr"),
	})
	assert.Error(t, err)
}

func TestRequestOperatingCert_MintSignFailurePropagatesAndSkipsLastSeen(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return nil, assert.AnError
	}

	srv := newIssuerServer(store, mintSign, testLogger())
	_, err := srv.RequestOperatingCert(fakeAuthContext(t, "node-1"), &pb.RequestOperatingCertRequest{
		CsrDer: testCSR(t).Raw,
	})
	assert.Error(t, err)

	got, err := store.GetClient("node-1")
	require.NoError(t, err)
	assert.Nil(t, got.LastSeenAt)
}

func TestDescribeSANs_KnownHostReturnsCurrentSANs(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"node-1.internal", "node-1.alt"}, time.Now()))

	srv := newIssuerServer(store, nil, testLogger())
	resp, err := srv.DescribeSANs(fakeAuthContext(t, "node-1"), &pb.DescribeSANsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1.internal", "node-1.alt"}, resp.Sans)
}

func TestDescribeSANs_HostWithNoSANsReturnsEmpty(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	srv := newIssuerServer(store, nil, testLogger())
	resp, err := srv.DescribeSANs(fakeAuthContext(t, "node-1"), &pb.DescribeSANsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Sans)
}

func TestDescribeSANs_RevokedHostStillReturnsSANs(t *testing.T) {
	// DescribeSANs reveals nothing the caller isn't already entitled to know
	// about itself and issues nothing -- only RequestOperatingCert enforces
	// revocation.
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"node-1.internal"}, time.Now()))
	require.NoError(t, store.SetRevoked("node-1", true, time.Now()))

	srv := newIssuerServer(store, nil, testLogger())
	resp, err := srv.DescribeSANs(fakeAuthContext(t, "node-1"), &pb.DescribeSANsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1.internal"}, resp.Sans)
}

func TestDescribeSANs_UnknownHostErrors(t *testing.T) {
	store := newTestIssuerStore(t)

	srv := newIssuerServer(store, nil, testLogger())
	_, err := srv.DescribeSANs(fakeAuthContext(t, "ghost"), &pb.DescribeSANsRequest{})
	assert.Error(t, err)
}

func TestDescribeSANs_NoPeerIdentityRejected(t *testing.T) {
	store := newTestIssuerStore(t)

	srv := newIssuerServer(store, nil, testLogger())
	_, err := srv.DescribeSANs(context.Background(), &pb.DescribeSANsRequest{})
	assert.Error(t, err)
}
