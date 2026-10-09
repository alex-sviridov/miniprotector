package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

// fakeRolePeerCert is fakePeerCert carrying the attribute extension issuer
// embeds, with the given authz-role ("" for no role attribute at all).
func fakeRolePeerCert(t *testing.T, hostname, role string) *x509.Certificate {
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
	if role != "" {
		template.ExtraExtensions = []pkix.Extension{{
			Id:    asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1},
			Value: []byte(`{"authz-role":"` + role + `"}`),
		}}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

// The read routes are control-plane only; push stays open to any role.
func TestRoutes_ReadRoutesRequireControlPlaneRole(t *testing.T) {
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer lokiStub.Close()
	handler := newLogGatewayServer(lokiStub.URL, testLogger()).routes()

	do := func(method, path, role string, withCert bool) int {
		req := httptest.NewRequest(method, path, strings.NewReader("{}"))
		if withCert {
			req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakeRolePeerCert(t, "node-1", role)}}
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Result().StatusCode
	}

	for _, path := range []string{"/loki/api/v1/query_range", "/loki/api/v1/tail"} {
		assert.Equal(t, http.StatusUnauthorized, do(http.MethodGet, path, "", false), "%s: no peer certificate", path)
		assert.Equal(t, http.StatusForbidden, do(http.MethodGet, path, "", true), "%s: no role attribute", path)
		assert.Equal(t, http.StatusForbidden, do(http.MethodGet, path, "client", true), "%s: client role", path)
		assert.Equal(t, http.StatusForbidden, do(http.MethodGet, path, "store", true), "%s: store role", path)
	}
	assert.Equal(t, http.StatusOK, do(http.MethodGet, "/loki/api/v1/query_range", "control-plane", true))

	for _, role := range []string{"", "client", "store", "control-plane"} {
		assert.Equal(t, http.StatusOK, do(http.MethodPost, "/loki/api/v1/push", role, true), "push must stay open to role %q", role)
	}
}

func TestHandlePush_BodyForwardedUnmodifiedToLoki(t *testing.T) {
	// log-gateway never parses the push body -- JSON, snappy-compressed
	// protobuf (Vector's own default wire format), or anything else --
	// it only gates on mTLS identity. A caller-set hostname label is
	// trusted as sent, not rewritten; the security boundary here is
	// "must present a valid, non-revoked operating certificate," not
	// body inspection.
	var capturedBody []byte
	var gotContentLength int64
	var gotTransferEncoding []string
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		capturedBody, err = io.ReadAll(r.Body)
		require.NoError(t, err)
		gotContentLength = r.ContentLength
		gotTransferEncoding = r.TransferEncoding
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
	// Pins the lokiReq.ContentLength = r.ContentLength propagation in
	// ServeHTTP: a within-cap push must reach Loki with a declared
	// Content-Length (not chunked transfer encoding).
	assert.Equal(t, int64(len(reqBody)), gotContentLength, "loki must see a declared Content-Length matching the request body's length")
	assert.Empty(t, gotTransferEncoding, "loki must not see the push forwarded chunked")
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

func TestHandlePush_OversizedBodyWithNoContentLengthSurfacesAsBadGateway(t *testing.T) {
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", strings.NewReader(strings.Repeat("a", maxPushBodyBytes+1)))
	req.ContentLength = -1 // simulates an inbound request with no declared Content-Length
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Result().StatusCode, "MaxBytesReader tripping mid-stream (no declared Content-Length) surfaces as a failed forward, not a clean 413")
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

	// httptest.NewRequest infers Content-Length from strings.Reader, so
	// this hits the fast Content-Length>maxPushBodyBytes pre-check below --
	// no read, no dial to Loki.
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

func TestHandleQuery_ForwardsToLokiAndReturnsBody(t *testing.T) {
	var gotQuery string
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/query_range?query=%7B%7D&start=1&end=2", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "api-server-1")}}
	w := httptest.NewRecorder()

	srv.ServeQuery(w, req)

	assert.Equal(t, http.StatusOK, w.Result().StatusCode)
	assert.Contains(t, gotQuery, "query=%7B%7D")
	assert.Contains(t, gotQuery, "start=1")
	assert.Contains(t, gotQuery, "end=2")
	assert.Contains(t, w.Body.String(), `"status":"success"`)
}

func TestHandleQuery_NonGetMethodRejected(t *testing.T) {
	srv := newLogGatewayServer("http://unused.invalid", testLogger())

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/query_range", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "api-server-1")}}
	w := httptest.NewRecorder()

	srv.ServeQuery(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Result().StatusCode)
}

func TestHandleQuery_LokiUnreachablePropagatesBadGateway(t *testing.T) {
	srv := newLogGatewayServer("http://127.0.0.1:1", testLogger())

	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/query_range", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "api-server-1")}}
	w := httptest.NewRecorder()

	srv.ServeQuery(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Result().StatusCode)
}

func TestServeTail_NonGetMethodRejected(t *testing.T) {
	srv := newLogGatewayServer("http://unused.invalid", testLogger())

	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/tail", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "api-server-1")}}
	w := httptest.NewRecorder()

	srv.ServeTail(w, req)

	assert.Equal(t, http.StatusMethodNotAllowed, w.Result().StatusCode)
}

// TestServeTail_RelaysMessagesFromLokiToClient proves the full relay: a
// caller with a verified peer cert connects, log-gateway dials Loki's own
// tail endpoint and pumps every message straight through unmodified.
// ServeTail needs a real WS upgrade (an http.Hijacker), which
// httptest.NewRecorder can't provide, so this test runs log-gateway behind
// a real httptest.NewServer with r.TLS forced by a thin middleware --
// ServeTail itself only ever reads r.TLS, never the transport's real TLS
// state, so this faithfully exercises the same code path the mTLS
// listener would in production (see main.go).
func TestServeTail_RelaysMessagesFromLokiToClient(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var gotQuery string
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(`{"streams":[]}`)))
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	gatewayStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "api-server-1")}}
		srv.ServeTail(w, r)
	}))
	defer gatewayStub.Close()

	wsURL := "ws" + strings.TrimPrefix(gatewayStub.URL, "http") + "/loki/api/v1/tail?query=%7B%7D&start=1"
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer clientConn.Close()

	_, msg, err := clientConn.ReadMessage()
	require.NoError(t, err)
	assert.JSONEq(t, `{"streams":[]}`, string(msg))
	assert.Contains(t, gotQuery, "query=%7B%7D")
	assert.Contains(t, gotQuery, "start=1")
}

// TestServeTail_ClientDisconnectClosesUpstream proves the client side of
// the relay is watched too -- not just the Loki->client direction -- so a
// browser closing its tab doesn't leak the upstream Loki connection.
func TestServeTail_ClientDisconnectClosesUpstream(t *testing.T) {
	upgrader := websocket.Upgrader{}
	upstreamClosed := make(chan struct{})
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				close(upstreamClosed)
				return
			}
		}
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	gatewayStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "api-server-1")}}
		srv.ServeTail(w, r)
	}))
	defer gatewayStub.Close()

	wsURL := "ws" + strings.TrimPrefix(gatewayStub.URL, "http") + "/loki/api/v1/tail?query=%7B%7D"
	clientConn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	require.NoError(t, clientConn.Close())

	select {
	case <-upstreamClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream loki connection was never closed after the client disconnected")
	}
}

func TestHandleQuery_OversizedResponseRejected(t *testing.T) {
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("a", maxQueryResponseBytes+1)))
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/query_range", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "api-server-1")}}
	w := httptest.NewRecorder()

	srv.ServeQuery(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Result().StatusCode)
}
