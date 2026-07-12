package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// fakePeerCert mirrors this codebase's established "fabricated peer
// identity" pattern (see cmd/issuer/server_test.go's fakeAuthContext),
// adapted for plain net/http: a self-signed cert with the given hostname
// as its SAN, attached directly to an *http.Request's TLS field rather
// than a gRPC peer context, simulating a verified mTLS handshake without
// a real one.
func fakePeerCert(t *testing.T, hostname string) *x509.Certificate {
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
	return cert
}

func TestHandlePush_RewritesHostnameLabelFromVerifiedPeerCert(t *testing.T) {
	var capturedBody []byte
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	reqBody := `{"streams":[{"stream":{"hostname":"spoofed-by-client","binary":"brfs"},"values":[["1699999999000000000","line1"]]}]}`
	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader(reqBody))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Result().StatusCode)
	var got pushRequest
	require.NoError(t, json.Unmarshal(capturedBody, &got))
	require.Len(t, got.Streams, 1)
	assert.Equal(t, "node-1", got.Streams[0].Stream["hostname"], "hostname label must be overwritten from the verified peer cert, not trusted from the client")
	assert.Equal(t, "brfs", got.Streams[0].Stream["binary"], "other labels must pass through unchanged")
}

func TestHandlePush_MultipleStreamsAllGetHostnameRewritten(t *testing.T) {
	var capturedBody []byte
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	reqBody := `{"streams":[
		{"stream":{"binary":"brfs"},"values":[["1699999999000000000","line1"]]},
		{"stream":{"binary":"certclient"},"values":[["1699999999000000001","line2"]]}
	]}`
	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader(reqBody))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-2")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	var got pushRequest
	require.NoError(t, json.Unmarshal(capturedBody, &got))
	require.Len(t, got.Streams, 2)
	for _, s := range got.Streams {
		assert.Equal(t, "node-2", s.Stream["hostname"])
	}
}

func TestHandlePush_NoPeerCertificateRejected(t *testing.T) {
	srv := newLogGatewayServer("http://unused.invalid", testLogger())

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
}

func TestHandlePush_MalformedJSONRejected(t *testing.T) {
	srv := newLogGatewayServer("http://unused.invalid", testLogger())

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader("not json"))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Result().StatusCode)
}

func TestHandlePush_NonPostMethodRejected(t *testing.T) {
	srv := newLogGatewayServer("http://unused.invalid", testLogger())

	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/push", nil)
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Result().StatusCode)
}

func TestHandlePush_LokiUnreachablePropagatesBadGateway(t *testing.T) {
	srv := newLogGatewayServer("http://127.0.0.1:1", testLogger()) // port 1: nothing listens here

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader(`{"streams":[]}`))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Result().StatusCode)
}

func TestHandlePush_OversizedBodyRejected(t *testing.T) {
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("loki must not be contacted when the inbound body exceeds the size cap")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	// One byte over the cap is enough to prove MaxBytesReader is wired in;
	// no need to actually allocate/send a multi-MB body for this to be a
	// meaningful assertion.
	oversized := strings.NewReader(strings.Repeat("a", maxPushBodyBytes+1))
	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", oversized)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Result().StatusCode)
}

func TestHandlePush_LokiErrorStatusPropagated(t *testing.T) {
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte("entry too far behind"))
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader(`{"streams":[{"stream":{},"values":[["1699999999000000000","line1"]]}]}`))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnprocessableEntity, w.Result().StatusCode)
	assert.Contains(t, w.Body.String(), "entry too far behind")
}
