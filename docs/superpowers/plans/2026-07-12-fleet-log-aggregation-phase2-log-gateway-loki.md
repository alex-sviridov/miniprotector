# Fleet Log Aggregation Phase 2: log-gateway & Loki Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `log-gateway` — the mTLS-terminating HTTP reverse proxy in front of Loki — as a standalone, fully-tested, independently-deployable component. This plan does **not** wire `agent`/Vector to call it yet (that's phase 3, a separate follow-up plan); it produces a working `log-gateway` you can push a real Loki-shaped request to directly (with a real mTLS client cert) and see the `hostname` label correctly enforced from the certificate, forwarded into a real Loki.

**Architecture:** `log-gateway` is a plain `net/http.Server` (not gRPC — Loki's push API is HTTP/JSON), terminating TLS via a new exported `mtls.ServerTLSConfig` (the same operating-tier verification `bwfs`/`catalog` already get from `mtls.LoadServerCredentials`, just not wrapped for gRPC). Its one handler, `POST /loki/api/v1/push`, derives the caller's `hostname` from the verified peer certificate (via a new `mtls.PeerHostnameFromConnState`, gRPC's `PeerHostname` extracted to share one hostname-extraction rule across both transports), force-overwrites the `hostname` label on every stream in the request body, and forwards the corrected body to Loki's real push endpoint.

**Tech Stack:** Go, `net/http`, `crypto/tls` (no gRPC, no protobuf — Loki's push API is plain JSON over HTTP), `common/mtls` (extended, not replaced), Loki (`grafana/loki` official image, pinned `3.7.3`), Docker Compose (for the e2e test and the control-plane deployment).

## Global Constraints

- `log-gateway` is deployed exactly like `catalog`/`policy-server`: an ordinary `agent`-managed enrolled node (bundles `agent`+`certclient`+`policyclient`, obtains its identity the standard bootstrap→operating-refresh way). It is **not** self-minting like `issuer` — no special-cased identity story to build or maintain.
- The `hostname` label is **always** derived from the verified mTLS peer certificate (via `mtls.PeerHostnameFromConnState`) — never trusted from the request body, even though a well-behaved caller (Vector, in phase 3) would send a value. This mirrors every other server in this project (`issuer`, `policy-server`, `catalog`): identity from cert, never a request field.
- Loki itself is never modified and never directly reachable from any agent-managed node in this plan's deployment wiring — only `log-gateway` talks to it.
- No gRPC, no `.proto` file, no generated code for this component — Loki's push API is plain JSON over HTTP, and there is exactly one caller shape to support (a `POST` with a JSON body), so a full RPC framework is unneeded machinery here.
- A failure to reach Loki must be surfaced to the caller (`502 Bad Gateway`, response body from Loki proxied through where possible) — never silently swallowed, since a silent failure here is invisible to Vector's own retry/buffer logic in phase 3.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/common/mtls/peer.go` (modify), `peer_test.go` (modify) | `hostnameFromCert` shared helper; new `PeerHostnameFromConnState` for plain-HTTP callers |
| `src/common/mtls/mtls.go` (modify), `mtls_test.go` (modify) | New exported `ServerTLSConfig` — the raw operating-tier `*tls.Config` for a `net/http.Server` |
| `src/common/config/config.go`, `config_test.go` (modify) | `LogGatewayHost`/`LogGatewayPort` (default `9400`) |
| `src/cmd/log-gateway/server.go` (new), `server_test.go` (new) | `logGatewayServer`: push-request parsing, hostname-label enforcement, forwarding |
| `src/cmd/log-gateway/arguments.go` (new) | `log-gateway` CLI flags (`--loki-url`, `--debug`) |
| `src/cmd/log-gateway/main.go` (new) | Wiring: config, TLS listener, graceful shutdown |
| `src/cmd/log-gateway/e2e_test.go` (new) | Real Loki + real mTLS handshake integration test |
| `Makefile` (modify) | `log-gateway` build target |
| `deploy/control-plane/log-gateway/{Dockerfile,entrypoint.sh,local.conf}` (new) | Deployment, mirroring `policy-server`'s exactly |
| `deploy/control-plane/loki/loki-config.yaml` (new) | Minimal single-binary, filesystem-storage Loki config |
| `deploy/control-plane/docker-compose.yml` (modify) | New `loki` and `log-gateway` services |
| `docs/components/log-gateway.md`, `docs/protocols/log-gateway.md` (new), `docs/SECURITY.md`, `docs/ARCHITECTURE.md` (modify) | Documentation |

---

### Task 1: `common/mtls` — HTTP-flavored peer identity and TLS config

**Files:**
- Modify: `src/common/mtls/peer.go`
- Modify: `src/common/mtls/peer_test.go`
- Modify: `src/common/mtls/mtls.go`
- Modify: `src/common/mtls/mtls_test.go`

**Interfaces:**
- Produces: `mtls.PeerHostnameFromConnState(state *tls.ConnectionState) (string, error)`, `mtls.ServerTLSConfig(certsDir string) (*tls.Config, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `src/common/mtls/peer_test.go`:

```go
func TestPeerHostnameFromConnState_ReturnsFirstSAN(t *testing.T) {
	cert := loadFixtureCert(t, fixtureCertsDir+"/client.crt")
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}

	host, err := PeerHostnameFromConnState(state)
	require.NoError(t, err)
	assert.Equal(t, "bwfs.internal", host)
}

func TestPeerHostnameFromConnState_FallsBackToCommonName(t *testing.T) {
	cert := selfSignedCertNoSAN(t, "cn-only-node")
	state := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}

	host, err := PeerHostnameFromConnState(state)
	require.NoError(t, err)
	assert.Equal(t, "cn-only-node", host)
}

func TestPeerHostnameFromConnState_NilState(t *testing.T) {
	_, err := PeerHostnameFromConnState(nil)
	assert.Error(t, err)
}

func TestPeerHostnameFromConnState_NoPeerCertificates(t *testing.T) {
	_, err := PeerHostnameFromConnState(&tls.ConnectionState{})
	assert.Error(t, err)
}
```

Append to `src/common/mtls/mtls_test.go`:

```go
func TestServerTLSConfig_AcceptsOperatingPeerCert(t *testing.T) {
	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "tier-test-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	dir := writeTestCertsDir(t, ca, serverIdentity)

	cfg, err := ServerTLSConfig(dir)
	require.NoError(t, err)
	addr := startListener(t, cfg)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	operatingLikeCert := generateTestLeaf(t, ca, caKey, "peer", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)

	err = dial(addr, peerConfig(caPool, operatingLikeCert))
	assert.NoError(t, err, "an operating-tier peer cert must be accepted")
}

func TestServerTLSConfig_RejectsIssuerCallerPeerCert(t *testing.T) {
	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "tier-test-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	dir := writeTestCertsDir(t, ca, serverIdentity)

	cfg, err := ServerTLSConfig(dir)
	require.NoError(t, err)
	addr := startListener(t, cfg)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	bootstrapLikeCert := generateTestLeaf(t, ca, caKey, "peer", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []asn1.ObjectIdentifier{oidEKUIssuerCaller})

	err = dial(addr, peerConfig(caPool, bootstrapLikeCert))
	assert.Error(t, err, "a peer cert carrying EKUIssuerCaller must be rejected by ServerTLSConfig, same as LoadServerCredentials")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/mtls/... -run 'TestPeerHostnameFromConnState|TestServerTLSConfig' -v`
Expected: FAIL — `PeerHostnameFromConnState`/`ServerTLSConfig` undefined (compile error).

- [ ] **Step 3: Implement**

In `src/common/mtls/peer.go`, add `"crypto/tls"` to the import block, then add (near the top, before `PeerHostname`):

```go
// hostnameFromCert extracts the verified hostname identity from cert: the
// first SAN entry, falling back to the Subject CommonName if no SAN is
// present. Shared by PeerHostname (gRPC) and PeerHostnameFromConnState
// (plain net/http, e.g. log-gateway) so both transports apply the exact
// same identity rule.
func hostnameFromCert(cert *x509.Certificate) (string, error) {
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0], nil
	}
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName, nil
	}
	return "", fmt.Errorf("peer certificate has no SAN or CommonName")
}
```

Replace `PeerHostname`'s body to use it:

```go
func PeerHostname(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", fmt.Errorf("peer connection is not authenticated via TLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", fmt.Errorf("no peer certificate presented")
	}
	return hostnameFromCert(tlsInfo.State.PeerCertificates[0])
}
```

Add, after `PeerHostname`:

```go
// PeerHostnameFromConnState is PeerHostname's plain-HTTP equivalent, for a
// server (like log-gateway) that terminates TLS via net/http.Server
// directly rather than gRPC. Same identity rule as PeerHostname: the first
// SAN entry, falling back to Subject CommonName. state is typically an
// *http.Request's own TLS field.
func PeerHostnameFromConnState(state *tls.ConnectionState) (string, error) {
	if state == nil || len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("no peer certificate presented")
	}
	return hostnameFromCert(state.PeerCertificates[0])
}
```

In `src/common/mtls/mtls.go`, add after `LoadServerCredentials`:

```go
// ServerTLSConfig returns the raw operating-tier *tls.Config
// LoadServerCredentials wraps into gRPC transport credentials -- for a
// server built directly on net/http.Server (like log-gateway) instead of
// gRPC. Same tier enforcement (rejects a bootstrap/issuer-caller peer
// cert) and the same per-handshake certificate reload every gRPC server's
// credentials already get from serverTLSConfigForTier.
func ServerTLSConfig(certsDir string) (*tls.Config, error) {
	return serverTLSConfigForTier(certsDir, requireOperatingTier)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/mtls/... -v 2>&1 | tail -60`
Expected: PASS (every test, including the 6 new ones — `ServerTLSConfig` reuses the exact same `serverTLSConfigForTier` internals `LoadServerCredentials` already does, so its behavior is already proven at that layer; these tests confirm the raw `*tls.Config` alone, without the gRPC wrap, behaves identically).

- [ ] **Step 5: Commit**

```bash
git add src/common/mtls/
git commit -m "feat(mtls): add PeerHostnameFromConnState and ServerTLSConfig for plain-HTTP servers"
```

---

### Task 2: `common/config` — `log_gateway_host`/`log_gateway_port`

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.LogGatewayHost string`, `Config.LogGatewayPort int` (default `9400`).

- [ ] **Step 1: Write the failing tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_LogGatewayHostParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\nlog_gateway_host=log-gateway.backup.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "log-gateway.backup.internal", conf.LogGatewayHost)
}

func TestParseConfig_LogGatewayPortDefaultsTo9400(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9400, conf.LogGatewayPort)
}

func TestParseConfig_LogGatewayPortParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\nlog_gateway_port=9500\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9500, conf.LogGatewayPort)
}
```

(Note: these use `log_dir=/tmp`, not `logfolder=/tmp` — Phase 1's rename has already landed by the time this plan runs.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run TestParseConfig_LogGateway -v`
Expected: FAIL — fields undefined (compile error).

- [ ] **Step 3: Implement**

In `src/common/config/config.go`, add two fields to the `Config` struct:

```go
	LogGatewayHost                    string
	LogGatewayPort                    int
```

Add the default to the literal in `ParseConfig`:

```go
		LogGatewayPort:                    9400,
```

Add two `case`s to the `switch key` block:

```go
		case "log_gateway_host":
			config.LogGatewayHost = value
			foundFields["log_gateway_host"] = true
		case "log_gateway_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid log_gateway_port value at line %d: %s", lineNum, value)
			}
			config.LogGatewayPort = port
			foundFields["log_gateway_port"] = true
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/common/config/
git commit -m "feat(config): add log_gateway_host, log_gateway_port"
```

---

### Task 3: `log-gateway`'s push handler

**Files:**
- Create: `src/cmd/log-gateway/server.go`
- Create: `src/cmd/log-gateway/server_test.go`

**Interfaces:**
- Consumes: `mtls.PeerHostnameFromConnState` (Task 1).
- Produces: `type pushRequest struct{ Streams []stream }`, `type stream struct{ Stream map[string]string; Values [][2]string }`, `newLogGatewayServer(lokiPushURL string, logger *slog.Logger) *logGatewayServer`, `(*logGatewayServer).ServeHTTP(http.ResponseWriter, *http.Request)`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/log-gateway/server_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/log-gateway/... -v`
Expected: FAIL — package `main` in `cmd/log-gateway` doesn't exist yet (compile error; no non-test file present).

- [ ] **Step 3: Implement**

Create `src/cmd/log-gateway/server.go`:

```go
// log-gateway is an mTLS-terminating HTTP reverse proxy in front of Loki.
// Loki's push API has no concept of mTLS peer identity, and this project
// never trusts a caller-asserted identity field (see docs/SECURITY.md) --
// so every proxied push has its hostname label force-overwritten from the
// verified peer certificate before being forwarded.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/alex-sviridov/miniprotector/common/mtls"
)

// pushRequest and stream mirror the subset of Loki's push API JSON body
// (POST /loki/api/v1/push) log-gateway needs to touch: the per-stream
// label set. Values (timestamp/line pairs) pass through completely
// unexamined and unmodified.
type pushRequest struct {
	Streams []stream `json:"streams"`
}

type stream struct {
	Stream map[string]string `json:"stream"`
	Values [][2]string       `json:"values"`
}

// logGatewayServer implements the sole HTTP handler an already-bootstrapped
// node's log shipper calls to push its logs toward Loki. The caller's
// identity is always the verified mTLS peer hostname -- never a field in
// the request body.
type logGatewayServer struct {
	lokiPushURL string
	httpClient  *http.Client
	logger      *slog.Logger
}

func newLogGatewayServer(lokiBaseURL string, logger *slog.Logger) *logGatewayServer {
	return &logGatewayServer{
		lokiPushURL: lokiBaseURL + "/loki/api/v1/push",
		httpClient:  &http.Client{},
		logger:      logger,
	}
}

func (s *logGatewayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hostname, err := mtls.PeerHostnameFromConnState(r.TLS)
	if err != nil {
		http.Error(w, "determine caller identity: "+err.Error(), http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var req pushRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "parse push request: "+err.Error(), http.StatusBadRequest)
		return
	}

	for i := range req.Streams {
		if req.Streams[i].Stream == nil {
			req.Streams[i].Stream = make(map[string]string)
		}
		req.Streams[i].Stream["hostname"] = hostname
	}

	rewritten, err := json.Marshal(req)
	if err != nil {
		http.Error(w, "re-marshal push request: "+err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := s.httpClient.Post(s.lokiPushURL, "application/json", bytes.NewReader(rewritten))
	if err != nil {
		s.logger.Error("forward to loki failed", "hostname", hostname, "error", err)
		http.Error(w, "forward to loki: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/log-gateway/... -v`
Expected: PASS (all 7 tests).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/log-gateway/server.go src/cmd/log-gateway/server_test.go
git commit -m "feat(log-gateway): add push handler enforcing hostname from verified peer cert"
```

---

### Task 4: `log-gateway`'s CLI and `main.go`

**Files:**
- Create: `src/cmd/log-gateway/arguments.go`
- Create: `src/cmd/log-gateway/main.go`

**Interfaces:**
- Consumes: `newLogGatewayServer` (Task 3), `mtls.ServerTLSConfig` (Task 1), `config.Config.LogGatewayPort` (Task 2).

- [ ] **Step 1: CLI arguments**

Create `src/cmd/log-gateway/arguments.go` (mirrors `issuer`'s `arguments.go` shape — a small, flag-only CLI, no subcommands):

```go
package main

import (
	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	LokiURL string
	Debug   bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "log-gateway",
		Short: "Verify agent-managed nodes' identity via mTLS and forward their logs to Loki, with the hostname label always derived from the verified peer certificate",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&args.LokiURL, "loki-url", "http://localhost:3100", "Base URL of the Loki instance to forward pushes to")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return args, nil
}
```

- [ ] **Step 2: `main.go`**

Create `src/cmd/log-gateway/main.go`:

```go
// log-gateway is an mTLS-terminating HTTP reverse proxy in front of Loki --
// the only new network-facing binary the fleet log aggregation design
// introduces. It shares no database and calls no other service besides
// Loki. See docs/components/log-gateway.md and
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/common/mtls"
)

func main() {
	const appName = "log-gateway"

	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	tlsConfig, err := mtls.ServerTLSConfig(certsDir)
	if err != nil {
		logger.Error("tls config failed", "error", err)
		os.Exit(1)
	}

	srv := newLogGatewayServer(args.LokiURL, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/push", srv.ServeHTTP)
	httpServer := &http.Server{Handler: mux, TLSConfig: tlsConfig}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", conf.LogGatewayPort))
	if err != nil {
		logger.Error("listen failed", "port", conf.LogGatewayPort, "error", err)
		os.Exit(1)
	}
	tlsListener := tls.NewListener(listener, tlsConfig)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-signalCtx.Done()
		logger.Info("shutting down log-gateway")
		_ = httpServer.Shutdown(context.Background())
	}()

	logger.Info("log-gateway started", "port", conf.LogGatewayPort, "loki_url", args.LokiURL)
	if err := httpServer.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Confirm it builds**

Run: `cd src && go build ./cmd/log-gateway/...`
Expected: no output, exit code 0.

- [ ] **Step 4: Commit**

```bash
git add src/cmd/log-gateway/arguments.go src/cmd/log-gateway/main.go
git commit -m "feat(log-gateway): CLI and main wiring"
```

---

### Task 5: Real-Loki, real-mTLS e2e test

**Files:**
- Create: `src/cmd/log-gateway/e2e_test.go`
- Create: `deploy/control-plane/loki/loki-config.yaml`

**Interfaces:**
- Consumes: `newLogGatewayServer` (Task 3), `mtls.ServerTLSConfig` (Task 1).

`log-gateway`'s tier enforcement is already proven generically at the `common/mtls` layer (Task 1) — this test doesn't need a real CA to re-prove that. What genuinely needs a real integration proof is Loki compatibility: that a real Loki instance accepts `log-gateway`'s forwarded push and actually stores it under the gateway-enforced `hostname` label, queryable back out. This test spins up `log-gateway`'s own HTTP+TLS server in-process (self-signed test CA, generated in Go — the same technique `common/mtls/mtls_test.go` already uses, no `step-ca` dependency needed since this isn't testing CA behavior) against a real, throwaway Loki container.

- [ ] **Step 1: Write the Loki config the e2e test (and later deployment) will use**

Create `deploy/control-plane/loki/loki-config.yaml`:

```yaml
auth_enabled: false

server:
  http_listen_port: 3100
  grpc_listen_port: 9096

common:
  path_prefix: /loki
  storage:
    filesystem:
      chunks_directory: /loki/chunks
      rules_directory: /loki/rules
  replication_factor: 1
  ring:
    instance_addr: 127.0.0.1
    kvstore:
      store: inmemory

schema_config:
  configs:
    - from: 2020-10-24
      store: tsdb
      object_store: filesystem
      schema: v13
      index:
        prefix: index_
        period: 24h

limits_config:
  retention_period: 720h
```

(Single-binary, filesystem-storage mode — no S3, no ring complexity, appropriate at this project's scale, per the design spec's Architecture section. `retention_period` is a starting default; operator-tunable, not a hardcoded ceiling.)

- [ ] **Step 2: Write the e2e test**

Create `src/cmd/log-gateway/e2e_test.go`:

```go
//go:build e2e

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/mtls"
)

// TestE2E_PushedLogIsQueryableUnderGatewayEnforcedHostname proves the full
// real pipeline this plan builds: a client presents a real (self-signed
// test CA, operating-tier-shaped) mTLS certificate for "node-real-hostname"
// but declares a spoofed hostname label in its push body; log-gateway,
// running its real TLS server construction (mtls.ServerTLSConfig) and real
// handler, forwards it to a genuine, throwaway Loki container; the pushed
// line is then queryable back out of Loki under the cert-derived hostname,
// never the spoofed one.
func TestE2E_PushedLogIsQueryableUnderGatewayEnforcedHostname(t *testing.T) {
	requireDocker(t)

	lokiURL, cleanup := startTestLoki(t)
	defer cleanup()

	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "log-gateway-e2e", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	certsDir := writeTestCertsDir(t, ca, serverIdentity)

	tlsConfig, err := mtls.ServerTLSConfig(certsDir)
	require.NoError(t, err)

	srv := newLogGatewayServer(lokiURL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/push", srv.ServeHTTP)
	httpServer := &http.Server{Handler: mux, TLSConfig: tlsConfig}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tlsListener := tls.NewListener(listener, tlsConfig)
	gatewayAddr := listener.Addr().String()

	go func() { _ = httpServer.Serve(tlsListener) }()
	defer httpServer.Close()

	clientCert := generateTestLeaf(t, ca, caKey, "node-real-hostname", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				Certificates: []tls.Certificate{clientCert},
				RootCAs:      caPool,
				ServerName:   "log-gateway-e2e",
			},
		},
	}

	nowNS := time.Now().UnixNano()
	pushBody := fmt.Sprintf(`{"streams":[{"stream":{"hostname":"spoofed-hostname","binary":"e2e-test"},"values":[["%d","this is the e2e test log line"]]}]}`, nowNS)
	resp, err := httpClient.Post(fmt.Sprintf("https://%s/loki/api/v1/push", gatewayAddr), "application/json", strings.NewReader(pushBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "push failed: %s", body)

	require.Eventually(t, func() bool {
		result, err := queryLoki(lokiURL, `{hostname="node-real-hostname"}`)
		if err != nil {
			return false
		}
		return strings.Contains(result, "this is the e2e test log line")
	}, 15*time.Second, 500*time.Millisecond, "pushed line never became queryable under the gateway-enforced hostname")

	result, err := queryLoki(lokiURL, `{hostname="spoofed-hostname"}`)
	require.NoError(t, err)
	assert.NotContains(t, result, "this is the e2e test log line", "the spoofed hostname label must never have been honored")
}

func queryLoki(lokiURL, query string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, lokiURL+"/loki/api/v1/query_range", nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(time.Now().Add(-time.Hour).UnixNano(), 10))
	q.Set("end", strconv.FormatInt(time.Now().Add(time.Hour).UnixNano(), 10))
	req.URL.RawQuery = q.Encode()

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("loki query returned %d: %s", resp.StatusCode, body)
	}
	return string(body), nil
}

// startTestLoki runs a real, throwaway Loki container directly (not via
// this repo's control-plane compose file, since Loki has no dependency on
// step-ca or any other project component) using the same config
// deploy/control-plane/loki/loki-config.yaml ships. Returns Loki's base
// URL and a cleanup func.
func startTestLoki(t *testing.T) (string, func()) {
	t.Helper()
	repoRoot := repoRootDir(t)
	configPath := filepath.Join(repoRoot, "deploy", "control-plane", "loki", "loki-config.yaml")

	name := fmt.Sprintf("log-gateway-e2e-loki-%d", time.Now().UnixNano())
	runCmd := exec.Command("docker", "run", "-d", "--rm",
		"--name", name,
		"-p", "0:3100",
		"-v", configPath+":/etc/loki/local-config.yaml:ro",
		"grafana/loki:3.7.3",
		"-config.file=/etc/loki/local-config.yaml",
	)
	out, err := runCmd.CombinedOutput()
	require.NoError(t, err, "docker run loki failed: %s", out)

	cleanup := func() {
		_ = exec.Command("docker", "stop", name).Run()
	}

	portCmd := exec.Command("docker", "port", name, "3100")
	portOut, err := portCmd.CombinedOutput()
	if err != nil {
		cleanup()
		require.NoError(t, err, "docker port failed: %s", portOut)
	}
	addr := strings.TrimSpace(strings.Split(string(portOut), "\n")[0])
	idx := strings.LastIndex(addr, ":")
	require.GreaterOrEqual(t, idx, 0, "unexpected `docker port` output: %q", addr)
	lokiURL := "http://localhost:" + addr[idx+1:]

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, waitForLoki(ctx, lokiURL), "loki never became ready")

	return lokiURL, cleanup
}

func waitForLoki(ctx context.Context, lokiURL string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s/ready: %w (last error: %v)", lokiURL, ctx.Err(), lastErr)
		case <-ticker.C:
			resp, err := http.Get(lokiURL + "/ready")
			if err != nil {
				lastErr = err
				continue
			}
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status %d", resp.StatusCode)
		}
	}
}

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not found in PATH, skipping e2e test: %v", err)
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon not reachable, skipping e2e test: %v\n%s", err, out)
	}
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// generateTestCA/generateTestLeaf/writeTestCertsDir mirror
// common/mtls/mtls_test.go's helpers of the same name exactly -- Go
// forbids importing another package's _test.go helpers, and this
// codebase's established convention (see cmd/issuer/e2e_test.go's own
// comment on this) is to duplicate small test fixtures per package rather
// than force a shared export.
func generateTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, key
}

func generateTestLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, hostname string, ekus []x509.ExtKeyUsage, unknownEKUs []asn1.ObjectIdentifier) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:       big.NewInt(2),
		Subject:            pkix.Name{CommonName: hostname},
		DNSNames:           []string{hostname},
		NotBefore:          time.Now().Add(-time.Hour),
		NotAfter:           time.Now().Add(time.Hour),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        ekus,
		UnknownExtKeyUsage: unknownEKUs,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	require.NoError(t, err)

	return tls.Certificate{
		Certificate: [][]byte{der, ca.Raw},
		PrivateKey:  key,
	}
}

func writeTestCertsDir(t *testing.T, ca *x509.Certificate, serverIdentity tls.Certificate) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), pemEncodeCert(ca.Raw), 0o600))

	var chainPEM []byte
	for _, der := range serverIdentity.Certificate {
		chainPEM = append(chainPEM, pemEncodeCert(der)...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), chainPEM, 0o600))

	ecKey, ok := serverIdentity.PrivateKey.(*ecdsa.PrivateKey)
	require.True(t, ok)
	keyDER, err := x509.MarshalECPrivateKey(ecKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), pemEncodeKey(keyDER), 0o600))

	return dir
}

func pemEncodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func pemEncodeKey(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}
```

- [ ] **Step 3: Run the e2e test**

Run: `cd src && go test -tags=e2e -timeout=120s ./cmd/log-gateway/... -run TestE2E_PushedLogIsQueryableUnderGatewayEnforcedHostname -v`
Expected: PASS (or a clear Docker-unavailable skip message). If `docker run grafana/loki:3.7.3` fails to pull, confirm the exact current stable tag on Docker Hub and update both this test and `deploy/control-plane/loki/loki-config.yaml`'s companion compose reference (Task 7) together — don't let the two drift apart.

- [ ] **Step 4: Confirm the non-e2e build and tests still pass**

Run: `cd src && go build ./... && go test ./cmd/log-gateway/... -v`
Expected: build succeeds; non-e2e tests (Task 3's) still PASS — the e2e file is behind a build tag and doesn't affect normal `go test ./...`.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/log-gateway/e2e_test.go deploy/control-plane/loki/loki-config.yaml
git commit -m "test(log-gateway): add real-Loki, real-mTLS e2e coverage"
```

---

### Task 6: Makefile build target

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add the command variable, `.PHONY` entry, and build target**

Add alongside the other `*_CMD` variables:

```makefile
LOG_GATEWAY_CMD := cmd/log-gateway
```

Add `log-gateway` to the `.PHONY` line (alongside `policyclient`).

Add, alongside the other build targets (after `policyclient`):

```makefile
log-gateway: $(BINARY_DIR) ## Build log-gateway binary
	@printf "$(BLUE)Building log-gateway...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/log-gateway ./$(LOG_GATEWAY_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/log-gateway"
```

- [ ] **Step 2: Confirm it builds**

Run: `make log-gateway`
Expected: `Built successfully:bin/log-gateway`, exit code 0.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "build: add log-gateway target"
```

---

### Task 7: Deployment — `deploy/control-plane/log-gateway/` and `docker-compose.yml`

**Files:**
- Create: `deploy/control-plane/log-gateway/Dockerfile`
- Create: `deploy/control-plane/log-gateway/entrypoint.sh`
- Create: `deploy/control-plane/log-gateway/local.conf`
- Modify: `deploy/control-plane/docker-compose.yml`

`log-gateway` is deployed exactly like `policy-server` — an ordinary `agent`-managed enrolled node, no special-cased identity story.

- [ ] **Step 1: `Dockerfile`**

Create `deploy/control-plane/log-gateway/Dockerfile`:

```dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make log-gateway certclient agent policyclient

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/log-gateway /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY deploy/control-plane/log-gateway/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: `entrypoint.sh`**

Create `deploy/control-plane/log-gateway/entrypoint.sh`:

```sh
#!/bin/sh
set -e

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart -- no expiry check, certclient always renews when an
# identity already exists) of the long-lived bootstrap credential.
if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent keeps both the bootstrap credential (daily) and the operating
# credential (every 15 min, talking to issuer) fresh continuously.
./agent serve &

# Wait for agent's first operating-refresh to produce client.crt/client.key
# before starting log-gateway -- a fresh volume only has the bootstrap
# credential until agent's reconcile loop completes its first cycle.
timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
	sleep 1
	timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
	echo "agent did not produce an operating certificate within 60s" >&2
	exit 1
fi

exec ./log-gateway --loki-url "${LOKI_URL:-http://loki:3100}" --debug="${DEBUG:-false}"
```

Set it executable: `chmod +x deploy/control-plane/log-gateway/entrypoint.sh`.

- [ ] **Step 3: `local.conf`**

Create `deploy/control-plane/log-gateway/local.conf`:

```
# default_port/default_streams/log_dir are required by every miniprotector
# binary's shared config parser, even though log-gateway itself only uses
# log_gateway_port and ca_host below. Harmless placeholders.
default_port=15722
default_streams=4
log_dir=/data/log

# The port log-gateway listens on.
log_gateway_port=9400

# Set to this deployment's CA host:port before first boot.
ca_host=ca.backup.internal:9000

# Where log-gateway's agent-managed operating-refresh policy dials issuer.
issuer_host=issuer
issuer_port=9200

# Where log-gateway's own agent-managed policy-update job dials
# policy-server. Every agent-managed node runs this job unconditionally,
# log-gateway included.
policy_server_host=policy-server

# agent's own reconcile-loop tick cadence.
ReconcileIntervalSec=30

# How often agent refreshes each credential tier -- see docs/SECURITY.md.
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
```

- [ ] **Step 4: Add `loki` and `log-gateway` services to `docker-compose.yml`**

In `deploy/control-plane/docker-compose.yml`, add after the existing `policy-server` service:

```yaml
  loki:
    image: grafana/loki:3.7.3
    volumes:
      - ./loki/loki-config.yaml:/etc/loki/local-config.yaml:ro
      - ./loki/data:/loki
    command: ["-config.file=/etc/loki/local-config.yaml"]
    restart: unless-stopped

  log-gateway:
    build:
      context: ../..
      dockerfile: deploy/control-plane/log-gateway/Dockerfile
    depends_on:
      - step-ca
      - issuer
      - loki
    volumes:
      - ./log-gateway/data:/data
      - ./log-gateway/local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
      - LOKI_URL=http://loki:3100
    ports:
      - "9400:9400"
    restart: unless-stopped
```

- [ ] **Step 5: Confirm the compose file is syntactically valid**

Run: `cd deploy/control-plane && docker compose config --quiet`
Expected: no output, exit code 0 (validates YAML syntax and variable interpolation without starting anything).

- [ ] **Step 6: Commit**

```bash
git add deploy/control-plane/log-gateway/ deploy/control-plane/docker-compose.yml
git commit -m "deploy: add log-gateway and loki to the control-plane stack"
```

---

### Task 8: Documentation

**Files:**
- Create: `docs/components/log-gateway.md`
- Create: `docs/protocols/log-gateway.md`
- Modify: `docs/SECURITY.md`
- Modify: `docs/ARCHITECTURE.md`

- [ ] **Step 1: `docs/components/log-gateway.md`**

```markdown
# log-gateway

An mTLS-terminating HTTP reverse proxy in front of Loki — the enforcement point for
[Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md).
Loki's push API has no concept of mTLS peer identity, and this project never trusts a
caller-asserted identity field (see [Security Model](../SECURITY.md)); `log-gateway` closes that
gap by deriving `hostname` from the verified peer certificate and overwriting whatever value the
caller sent, before forwarding to Loki.

Deployed exactly like [catalog](./catalog.md)/[policy-server](./policy-server.md): an ordinary
`agent`-managed enrolled node, not a self-minting one like `issuer`.

## Usage

```bash
log-gateway --loki-url http://localhost:3100
```

| Flag | Default | Description |
|------|---------|-------------|
| `--loki-url` | `http://localhost:3100` | Base URL of the Loki instance to forward pushes to |
| `--debug` | false | Enable debug logging |

## Behavior

`POST /loki/api/v1/push` (see [protocol](../protocols/log-gateway.md)) is `log-gateway`'s only
endpoint. The caller's hostname is always the verified mTLS peer identity, never a request field.
For every stream in the pushed body: the `hostname` label is force-overwritten with the verified
value (creating it if the caller omitted one), every other label passes through unchanged, and the
rewritten body is forwarded to Loki's own push endpoint. A caller presenting no verified peer
certificate, or malformed JSON, is rejected outright — nothing is forwarded. A Loki-side failure
(unreachable, or a non-2xx response) is surfaced back to the caller (`502` if unreachable, Loki's
own status/body proxied through otherwise) rather than swallowed.

`log-gateway`'s listener requires an operating-tier peer certificate — the same
`mtls.ServerTLSConfig`/`ServerTLSConfig`-equivalent tier check `bwfs`/`catalog` already enforce
(via `common/mtls.LoadServerCredentials`) rejects a bootstrap/issuer-caller credential outright.

## Building

```bash
make log-gateway
```

## See Also

- [Fleet Log Aggregation Protocol: log-gateway](../protocols/log-gateway.md)
- [Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md)
- [Security Model](../SECURITY.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 2: `docs/protocols/log-gateway.md`**

```markdown
# log-gateway Protocol

Already-bootstrapped node's log shipper → `log-gateway`'s `POST /loki/api/v1/push`, mTLS
(`common/mtls`'s operating-tier verification — the same transport check `bwfs`/`catalog` already
enforce, applied to a plain `net/http.Server` instead of gRPC since Loki's own push API is HTTP,
not gRPC).

## Request

Loki's own push API shape (a strict subset `log-gateway` cares about — everything else passes
through untouched):

```json
{
  "streams": [
    {
      "stream": { "<label>": "<value>", ... },
      "values": [["<unix-nano-timestamp-string>", "<line>"], ...]
    }
  ]
}
```

## Authorization

The caller's hostname is always derived from its verified mTLS peer identity
(`mtls.PeerHostnameFromConnState`) — never a field on the request. `log-gateway` overwrites the
`hostname` label on every stream in the body with that verified value before forwarding — a caller
cannot claim to be a different hostname than the one in its own certificate, in logs any more than
anywhere else in this project (see [Security Model](../SECURITY.md)).

## Response

Whatever Loki's own push endpoint returns, proxied through unchanged (`204 No Content` on success,
per Loki's own convention). `502 Bad Gateway` if Loki itself is unreachable. `401 Unauthorized` if
no verified peer certificate was presented. `400 Bad Request` for a malformed body or the wrong
HTTP method.

## See Also

- [log-gateway](../components/log-gateway.md)
- [Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md)
```

- [ ] **Step 3: Update `docs/SECURITY.md`**

In the "mTLS everywhere" section, after the existing paragraph beginning "Every gRPC connection
between components in this project", add a new paragraph:

```markdown
One exception to "gRPC": `log-gateway`'s push endpoint is plain HTTP, since it proxies to Loki's
own HTTP push API. The transport is still genuine mTLS (`common/mtls.ServerTLSConfig`, the same
operating-tier verification `LoadServerCredentials` gives every gRPC server, just not wrapped for
gRPC), and the same rule holds: caller identity is always the verified peer certificate
(`mtls.PeerHostnameFromConnState`), never a request field.
```

Update the two-tier credential model table's "Consumed by" row for the operating credential —
change:

```
| Consumed by | Only `certclient operating-refresh`'s connection to `issuer` | Every other component's mTLS transport (`common/mtls`'s hardcoded `client.crt`/`client.key`) — `bwfs`, `brfs`, `rwfs`, `catalogsync`, `catalog` |
```

to:

```
| Consumed by | Only `certclient operating-refresh`'s connection to `issuer` | Every other component's mTLS transport (`common/mtls`'s hardcoded `client.crt`/`client.key`) — `bwfs`, `brfs`, `rwfs`, `catalogsync`, `catalog`, `log-gateway` |
```

- [ ] **Step 4: Update `docs/ARCHITECTURE.md`**

Add a new components-table row after `policy-server`:

```markdown
| log-gateway | mTLS-terminating HTTP reverse proxy in front of Loki; enforces the hostname label from the verified peer cert | Implemented (agent/Vector integration is separate, later work) |
```

- [ ] **Step 5: Final verification**

Run: `cd src && go build ./... && go test ./... 2>&1 | tail -40` and `go vet ./...`
Expected: `ok` for every package; `go vet` shows only the pre-existing `cmd/brfs` warning (if any), not introduced by this task.

- [ ] **Step 6: Commit**

```bash
git add docs/components/log-gateway.md docs/protocols/log-gateway.md docs/SECURITY.md docs/ARCHITECTURE.md
git commit -m "docs: document log-gateway and its protocol"
```

---

## Self-Review

**Spec coverage** (against `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`):
- `log-gateway` component (mTLS-terminating proxy, hostname-label enforcement) → Tasks 1, 3, 4.
- Loki as internal-only backend, never directly reachable from agent nodes → Task 7's compose wiring (only `log-gateway` gets a route to `loki`; no other service depends on or is networked to it besides through the compose network itself, which is the same posture `issuer`/`policy-server` already have relative to each other).
- `log_gateway_host`/`log_gateway_port` config keys → Task 2 (`LogGatewayPort` consumed here; `LogGatewayHost` is added now since it's the matched pair, consumed by phase 3's Vector config generation, not this plan).
- Testing requirements (unit: tier verification, hostname derivation/overwrite, forwarding; integration: real Loki, spoofed-label rejection) → Tasks 1, 3, 5.
- Explicitly out of scope for this plan (correctly, per its own stated scope): `agent`/Vector integration, Vector bundling/config-generation, the demo wiring — deferred to phase 3.

**Placeholder scan:** every code block is complete and directly runnable. No `TODO`/`TBD` found. The one explicitly-flagged uncertainty (`grafana/loki:3.7.3`'s exact tag) is called out as something to verify against source at Task 5 Step 3 and Task 7, not silently assumed — consistent with this project's practice elsewhere (e.g. the original phase-2 design's "confirmed directly against source" discipline).

**Type consistency:** `pushRequest`/`stream` (Task 3) are used identically in `server.go` and `server_test.go`. `newLogGatewayServer(lokiBaseURL string, logger *slog.Logger) *logGatewayServer`'s signature matches every call site across Tasks 3, 4, and 5. `mtls.PeerHostnameFromConnState`/`mtls.ServerTLSConfig` (Task 1) are consumed with identical signatures in Tasks 3 (test), 4 (`main.go`), and 5 (e2e test).

**Sequencing:** Task 1 (mtls additions) must land before Task 3 (its consumer) — already in order. Task 3 (server logic) before Task 4 (wiring) and Task 5 (e2e, which also directly constructs `newLogGatewayServer`) — already in order. Task 7's `local.conf` uses `log_dir` (not `logfolder`), consistent with Phase 1 having already landed.

No gaps found.
