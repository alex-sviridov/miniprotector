package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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

func TestHandlePush_BodyForwardedUnmodifiedToLoki(t *testing.T) {
	// log-gateway never parses the push body -- JSON, snappy-compressed
	// protobuf (Vector's own default wire format), or anything else --
	// it only gates on mTLS identity. A caller-set hostname label is
	// trusted as sent, not rewritten; the security boundary here is
	// "must present a valid, non-revoked operating certificate," not
	// body inspection.
	var capturedBody []byte
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	reqBody := `{"streams":[{"stream":{"hostname":"whatever-the-client-says","binary":"brfs"},"values":[["1699999999000000000","line1",{"trace_id":"abc123"}]]}]}`
	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader(reqBody))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Result().StatusCode)
	assert.Equal(t, reqBody, string(capturedBody), "body must reach Loki byte-for-byte unmodified")
}

func TestHandlePush_ContentTypeAndEncodingHeadersForwarded(t *testing.T) {
	// Vector's loki sink sends application/x-protobuf with
	// Content-Encoding: snappy by default -- log-gateway must preserve
	// both headers verbatim (it never decodes the body, so Loki has to
	// see the same encoding the caller used) rather than assuming JSON.
	var gotContentType, gotContentEncoding string
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotContentEncoding = r.Header.Get("Content-Encoding")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader("opaque-protobuf-bytes"))
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Result().StatusCode)
	assert.Equal(t, "application/x-protobuf", gotContentType)
	assert.Equal(t, "snappy", gotContentEncoding)
}

func TestHandlePush_NoPeerCertificateRejected(t *testing.T) {
	srv := newLogGatewayServer("http://unused.invalid", testLogger())

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader(`{}`))
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
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
