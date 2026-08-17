# Live Job & Log Updates (WebSocket) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `/jobs` and `/jobs/:job_id` in `web` update live — a finished job's state and log
lines appear without a manual reload — by proxying Loki's native WebSocket tail endpoint through
`log-gateway` and `api-server`, with REST fetch-once behavior kept as a reconciliation backstop.

**Architecture:** `log-gateway` gains a third proxy route (`GET /loki/api/v1/tail`, WS upgrade,
same mTLS gate as its existing two routes). `api-server` gains a short-lived ticket mechanism for
WS auth, a stateless per-job log-tail WS proxy, and a stateful fleet-wide job aggregator (one
shared upstream tail, in-memory `job_id → summary` map, snapshot+upsert fan-out to every connected
browser) backed by the same start/finish pairing logic `GET /api/v1/jobs` already uses. `web` gets a
small reusable WS client (ticket fetch, jittered reconnect, permanent poll-fallback after repeated
failure) wired into both views alongside a periodic REST reconciliation call.

**Tech Stack:** Go (`net/http`, `github.com/gorilla/websocket`, new dependency this plan adds) for
`log-gateway`/`api-server`; Vue 3 + Pinia for `web`; Vitest for frontend unit tests; Playwright for
e2e.

## Global Constraints

- No proto changes — this plan touches no gRPC surface. (spec Architecture)
- No removal of the existing REST `/jobs` / `/jobs/{id}/logs` endpoints; they stay as the initial
  fetch and the reconciliation backstop. (spec Non-Goals)
- No change to which component emits `event`/`status` for which job kind — `brfs`/`bwfs` own it for
  `kind=backup`, `agent`'s wrapper log owns it for everything else. This plan's aggregator depends
  on that invariant (exactly one start line, one finish line, per `job_id`) holding unchanged. (spec
  "Backup vs. everything else")
- WS auth uses a single-use, 30s-TTL ticket minted by an already-bearer-authenticated REST call —
  the long-lived shared bearer token must never appear in a WS URL. (spec Architecture)
- Reconciliation cadence is 60s (both pages); overlap margin for history→tail stitching is 2s;
  reconnect gives up after 5 consecutive failures and falls back to REST polling at a fixed 10s
  interval. (spec Frontend)
- Go tests: run via `cd src && go test ./cmd/<pkg>/... -run <TestName> -v`.
- Web tests: run via `cd web && npx vitest run <path/to/spec.js>`.
- Full Go build check after any `cmd/api-server` or `cmd/log-gateway` change:
  `cd src && go build ./...`.

---

### Task 1: Add the `gorilla/websocket` dependency

**Files:**
- Modify: `src/go.mod`, `src/go.sum`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `github.com/gorilla/websocket` importable from `src/cmd/log-gateway` and
  `src/cmd/api-server` — `websocket.Upgrader`, `websocket.Dialer`, `websocket.Conn`,
  `websocket.DefaultDialer`, `websocket.TextMessage`, used by every later Go task in this plan.

- [ ] **Step 1: Add the dependency**

Run: `cd src && go get github.com/gorilla/websocket@v1.5.3`
Expected: `go.mod` gains a `require github.com/gorilla/websocket v1.5.3` line; `go.sum` gains
matching hash entries.

- [ ] **Step 2: Tidy and confirm the module still builds**

Run: `cd src && go mod tidy && go build ./...`
Expected: PASS, no errors. `git diff go.mod go.sum` shows only the new dependency.

- [ ] **Step 3: Commit**

```bash
git add src/go.mod src/go.sum
git commit -m "$(cat <<'EOF'
build: add gorilla/websocket dependency

Needed by log-gateway's new WS tail proxy route and api-server's new
per-job/fleet-wide live job-update endpoints (next several tasks).
EOF
)"
```

---

### Task 2: `log-gateway` — WebSocket tail proxy route

**Files:**
- Modify: `src/cmd/log-gateway/server.go`
- Modify: `src/cmd/log-gateway/main.go`
- Test: `src/cmd/log-gateway/server_test.go`

**Interfaces:**
- Consumes: Task 1's `gorilla/websocket`; `mtls.PeerHostnameFromConnState(r.TLS) (string, error)`
  (already imported in this file).
- Produces: `(*logGatewayServer).ServeTail(w http.ResponseWriter, r *http.Request)`, registered on
  `GET /loki/api/v1/tail` in `main.go`. Task 5 (`api-server`'s Loki tailer client) dials this route.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/log-gateway/server_test.go` (extend the existing `import` block with
`"github.com/gorilla/websocket"`):

```go
func TestServeTail_NoPeerCertificateRejected(t *testing.T) {
	srv := newLogGatewayServer("http://unused.invalid", testLogger())

	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/tail", nil)
	w := httptest.NewRecorder()

	srv.ServeTail(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
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
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/log-gateway/... -run TestServeTail -v`
Expected: FAIL — `srv.ServeTail undefined (type *logGatewayServer has no field or method ServeTail)`.

- [ ] **Step 3: Implement `ServeTail`**

In `src/cmd/log-gateway/server.go`, add `"strings"` to the import block and
`"github.com/gorilla/websocket"`. Add near the top (after the existing `passthroughHeaders` var):

```go
// tailUpgrader is shared across every caller's WS upgrade -- default
// buffer sizes are fine at this scale, mirroring the reasoning behind
// maxPushBodyBytes/maxQueryResponseBytes on the REST routes. CheckOrigin
// always returns true: the mTLS peer certificate check below is this
// route's real auth boundary, not browser origin -- log-gateway is never
// called directly from a browser (api-server proxies for browsers; see
// docs/SECURITY.md).
var tailUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// httpToWS rewrites an http(s):// base URL to its ws(s):// equivalent --
// Loki's tail endpoint is a WebSocket upgrade on the same host/port as its
// plain HTTP push/query_range endpoints.
func httpToWS(base string) string {
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimPrefix(base, "http://")
	default:
		return base
	}
}
```

Add the field and update the constructor:

```go
type logGatewayServer struct {
	lokiPushURL  string
	lokiQueryURL string
	lokiTailURL  string
	httpClient   *http.Client
	logger       *slog.Logger
}

func newLogGatewayServer(lokiBaseURL string, logger *slog.Logger) *logGatewayServer {
	return &logGatewayServer{
		lokiPushURL:  lokiBaseURL + "/loki/api/v1/push",
		lokiQueryURL: lokiBaseURL + "/loki/api/v1/query_range",
		lokiTailURL:  httpToWS(lokiBaseURL) + "/loki/api/v1/tail",
		httpClient:   &http.Client{},
		logger:       logger,
	}
}
```

Add the handler and its relay helper at the end of the file:

```go
// ServeTail proxies a caller's WebSocket tail connection to Loki's real
// tail endpoint -- the read-path live counterpart to ServeQuery's
// query_range proxying, gated by the same operating-tier mTLS check.
// Query parameters (query, start, delay_for, limit) are forwarded
// unmodified, same unexamined-passthrough philosophy as every other route
// here. Reachable by any operating-tier mesh node, same convention already
// accepted for the push/query routes.
func (s *logGatewayServer) ServeTail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, err := mtls.PeerHostnameFromConnState(r.TLS); err != nil {
		http.Error(w, "determine caller identity: "+err.Error(), http.StatusUnauthorized)
		return
	}

	lokiConn, _, err := websocket.DefaultDialer.DialContext(r.Context(), s.lokiTailURL+"?"+r.URL.RawQuery, nil)
	if err != nil {
		http.Error(w, "dial loki tail: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer lokiConn.Close()

	clientConn, err := tailUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("serveTail: client upgrade failed", "error", err)
		return
	}
	defer clientConn.Close()

	relayTail(clientConn, lokiConn)
}

// relayTail pumps every message from loki to client until either side
// closes or errors. A second goroutine drains client's own incoming
// frames (a tail client sends nothing but control frames -- pings, a
// close) purely to detect a client-initiated disconnect promptly and tear
// down the upstream Loki connection with it, since ReadMessage is the only
// way gorilla/websocket surfaces a close.
func relayTail(client, loki *websocket.Conn) {
	clientClosed := make(chan struct{})
	go func() {
		defer close(clientClosed)
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	lokiDone := make(chan struct{})
	go func() {
		defer close(lokiDone)
		for {
			msgType, msg, err := loki.ReadMessage()
			if err != nil {
				return
			}
			if err := client.WriteMessage(msgType, msg); err != nil {
				return
			}
		}
	}()

	select {
	case <-clientClosed:
	case <-lokiDone:
	}
}
```

- [ ] **Step 4: Register the route**

In `src/cmd/log-gateway/main.go`, add the route registration line after the existing two:

```go
	mux.HandleFunc("/loki/api/v1/push", srv.ServeHTTP)
	mux.HandleFunc("/loki/api/v1/query_range", srv.ServeQuery)
	mux.HandleFunc("/loki/api/v1/tail", srv.ServeTail)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/log-gateway/... -v`
Expected: PASS, including every pre-existing test in this package.

- [ ] **Step 6: Full build check**

Run: `cd src && go build ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/log-gateway/server.go src/cmd/log-gateway/main.go src/cmd/log-gateway/server_test.go
git commit -m "$(cat <<'EOF'
feat(log-gateway): add GET /loki/api/v1/tail WebSocket proxy

Third route alongside push/query_range, same operating-tier mTLS gate
and unexamined-passthrough philosophy -- proxies a caller's WS tail
connection straight through to Loki's own tail endpoint. Nothing calls
this yet; api-server's Loki tailer client (next task) is the first
caller.
EOF
)"
```

---

### Task 3: `api-server` — per-route auth (prerequisite for ticket-gated WS routes)

**Problem this task solves:** every `api-server` route today is authenticated by one blanket
`requireBearerToken` wrap around the whole mux (`main.go`). A browser's WS handshake can't carry an
`Authorization` header, so the two new WS routes (Tasks 6 and 10) need a *different* auth check
(a ticket, not a bearer token) applied *only* to them — which the blanket wrap can't express. This
task moves auth from "wrap the whole mux once" to "wrap each route at registration," with no
behavior change for any existing route, so later tasks can register the WS routes with a different
wrapper.

**Files:**
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/main.go`
- Test: `src/cmd/api-server/server_test.go` (new file)

**Interfaces:**
- Consumes: `requireBearerToken(token string, next http.Handler) http.Handler` (existing,
  `auth.go`, unchanged).
- Produces: `(*server).registerRoutes(mux *http.ServeMux, token string)` (signature changed — was
  `registerRoutes(mux *http.ServeMux)`). Every existing route's `http.HandlerFunc` is now wrapped
  individually in `requireBearerToken`. Task 4 registers `POST /api/v1/ws-tickets` the same way;
  Tasks 6 and 10 register their WS routes with a *different* wrapper (`requireWSTicket`) on this
  same mux.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/api-server/server_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRegisterRoutes_RejectsMissingBearerToken(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "correct-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRegisterRoutes_AcceptsCorrectBearerToken(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "correct-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	req.Header.Set("Authorization", "Bearer correct-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}
```

(Assumes a `testLogger()` helper already exists in this package's test files — if it doesn't yet,
add `func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }` to
this new file with the matching `log/slog`/`io` imports.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestRegisterRoutes -v`
Expected: FAIL — `not enough arguments in call to srv.registerRoutes` (signature mismatch).

- [ ] **Step 3: Change `registerRoutes` to wrap per-route**

Replace `registerRoutes` in `src/cmd/api-server/server.go`:

```go
// registerRoutes wires up every REST endpoint, each individually wrapped
// in requireBearerToken -- unlike main.go's previous single blanket wrap
// around the whole mux, this lets a route opt out (see requireWSTicket,
// ws_tickets.go, used by the WS routes Tasks 6/10 register on this same
// mux) without needing a second top-level handler/mux to compose.
func (s *server) registerRoutes(mux *http.ServeMux, token string) {
	bearer := func(h http.HandlerFunc) http.Handler { return requireBearerToken(token, h) }

	mux.Handle("GET /api/v1/clients", bearer(s.handleListClients))
	mux.Handle("GET /api/v1/clients/{hostname}", bearer(s.handleGetClient))
	mux.Handle("POST /api/v1/clients", bearer(s.handleAddClient))
	mux.Handle("POST /api/v1/clients/{hostname}/reenroll", bearer(s.handleReEnrollClient))
	mux.Handle("POST /api/v1/clients/{hostname}/revoke", bearer(s.handleRevokeClient))
	mux.Handle("POST /api/v1/clients/{hostname}/unrevoke", bearer(s.handleUnrevokeClient))
	mux.Handle("PATCH /api/v1/clients/{hostname}/description", bearer(s.handleUpdateDescription))
	mux.Handle("PATCH /api/v1/clients/{hostname}/attributes", bearer(s.handleUpdateAttributes))
	mux.Handle("PATCH /api/v1/clients/{hostname}/sans", bearer(s.handleUpdateSANs))
	mux.Handle("GET /api/v1/clients/{hostname}/cert-status", bearer(s.handleGetClientCertStatus))
	mux.Handle("GET /api/v1/catalog", bearer(s.handleListCatalog))
	mux.Handle("GET /api/v1/catalog/clients", bearer(s.handleListCatalogClients))
	mux.Handle("GET /api/v1/catalog/jobs", bearer(s.handleListCatalogJobs))
	mux.Handle("GET /api/v1/catalog/directories", bearer(s.handleListCatalogDirectories))
	mux.Handle("GET /api/v1/catalog/stores", bearer(s.handleListCatalogStores))
	mux.Handle("GET /api/v1/catalog/directories/children", bearer(s.handleListCatalogDirectoryChildren))
	mux.Handle("GET /api/v1/policies", bearer(s.handleListPolicies))
	mux.Handle("GET /api/v1/policies/{id}", bearer(s.handleGetPolicy))
	mux.Handle("POST /api/v1/policies", bearer(s.handleCreatePolicy))
	mux.Handle("POST /api/v1/policies/adhoc", bearer(s.handleCreateAdhocPolicy))
	mux.Handle("PUT /api/v1/policies/{id}", bearer(s.handleUpdatePolicy))
	mux.Handle("DELETE /api/v1/policies/{id}", bearer(s.handleDeletePolicy))
	mux.Handle("POST /api/v1/storage-policies", bearer(s.handleCreateStoragePolicy))
	mux.Handle("PUT /api/v1/storage-policies/{id}", bearer(s.handleUpdateStoragePolicy))
	mux.Handle("POST /api/v1/restore", bearer(s.handleCreateRestore))
	mux.Handle("GET /api/v1/jobs", bearer(s.handleListJobs))
	mux.Handle("GET /api/v1/jobs/{job_id}/logs", bearer(s.handleGetJobLogs))
}
```

- [ ] **Step 4: Update `main.go`'s call site**

In `src/cmd/api-server/main.go`, replace:

```go
	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	handler := requireBearerToken(arguments.Token, mux)
```

with:

```go
	mux := http.NewServeMux()
	srv.registerRoutes(mux, arguments.Token)
	handler := mux
```

and update the `httpServer` construction below it to use `handler` as before (no other change —
`handler` still flows into `&http.Server{Addr: ..., Handler: handler}` unchanged).

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS, including every pre-existing test in this package (each existing handler test calls
`srv.registerRoutes(mux, ...)` already, per the codebase's established per-test-file pattern seen in
`jobs_test.go` — confirm none of them still call the old one-argument form; if any do, add the
token argument, e.g. `"test-token"`, matching that test's existing `Authorization` header value or
adding one).

- [ ] **Step 6: Full build check**

Run: `cd src && go build ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/main.go src/cmd/api-server/server_test.go
git commit -m "$(cat <<'EOF'
refactor(api-server): move auth from one blanket wrap to per-route

registerRoutes now wraps each handler in requireBearerToken itself
instead of main.go wrapping the whole mux once -- no behavior change
for any existing route. Prerequisite for the WS ticket-gated routes
(next few tasks), which need a different auth check that a single
blanket wrap can't express.
EOF
)"
```

---

### Task 4: `api-server` — WS ticket store and issuance endpoint

**Files:**
- Create: `src/cmd/api-server/ws_tickets.go`
- Create: `src/cmd/api-server/ws_tickets_test.go`
- Modify: `src/cmd/api-server/server.go` (add field, register route)

**Interfaces:**
- Consumes: nothing new.
- Produces: `wsTicketStore` (`newWSTicketStore()`, `issue() (string, error)`,
  `consume(ticket string) bool`); `requireWSTicket(store *wsTicketStore, next http.Handler)
  http.Handler`; `(*server).handleIssueWSTicket`, registered as `POST /api/v1/ws-tickets`. Tasks 6
  and 10 wrap their WS handlers in `requireWSTicket(s.wsTickets, ...)`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/api-server/ws_tickets_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWSTicketStore_IssuedTicketConsumesOnce(t *testing.T) {
	store := newWSTicketStore()

	ticket, err := store.issue()
	require.NoError(t, err)
	assert.NotEmpty(t, ticket)

	assert.True(t, store.consume(ticket), "a freshly issued ticket must consume successfully")
	assert.False(t, store.consume(ticket), "a second consume of the same ticket must fail")
}

func TestWSTicketStore_UnknownTicketRejected(t *testing.T) {
	store := newWSTicketStore()
	assert.False(t, store.consume("never-issued"))
}

func TestWSTicketStore_ExpiredTicketRejected(t *testing.T) {
	store := newWSTicketStore()
	ticket, err := store.issue()
	require.NoError(t, err)

	store.mu.Lock()
	store.tickets[ticket] = time.Now().Add(-wsTicketTTL - time.Second)
	store.mu.Unlock()

	assert.False(t, store.consume(ticket))
}

func TestRequireWSTicket_MissingTicketRejected(t *testing.T) {
	store := newWSTicketStore()
	called := false
	h := requireWSTicket(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, called)
}

func TestRequireWSTicket_ValidTicketPassesThroughAndConsumes(t *testing.T) {
	store := newWSTicketStore()
	ticket, err := store.issue()
	require.NoError(t, err)
	called := false
	h := requireWSTicket(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream?ticket="+ticket, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.False(t, store.consume(ticket), "requireWSTicket must consume the ticket, not just check it")
}

func TestHandleIssueWSTicket_ReturnsConsumableTicket(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ws-tickets", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Ticket string `json:"ticket"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.NotEmpty(t, body.Ticket)
	assert.True(t, srv.wsTickets.consume(body.Ticket))
}
```

(Add `"encoding/json"` to this file's imports for the last test.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestWSTicketStore|TestRequireWSTicket|TestHandleIssueWSTicket' -v`
Expected: FAIL — `undefined: newWSTicketStore`.

- [ ] **Step 3: Implement**

Create `src/cmd/api-server/ws_tickets.go`:

```go
// src/cmd/api-server/ws_tickets.go
package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// wsTicketTTL bounds how long an issued-but-unused ticket stays valid.
// Short on purpose -- a ticket authenticates exactly one WS connection
// attempt, made immediately after it's issued, not a session.
const wsTicketTTL = 30 * time.Second

// wsTicketStore issues short-lived, single-use tickets that authenticate a
// browser's WebSocket upgrade. A WS handshake can't carry an Authorization
// header the way every other api-server call does (see
// docs/superpowers/specs/2026-08-17-live-job-updates-design.md), so a
// ticket -- minted only from an already-bearer-authenticated REST call,
// handleIssueWSTicket below -- stands in for it on the two WS routes
// (Tasks 6, 10) only. The long-lived shared bearer token itself never has
// to appear in a URL.
type wsTicketStore struct {
	mu      sync.Mutex
	tickets map[string]time.Time
}

func newWSTicketStore() *wsTicketStore {
	return &wsTicketStore{tickets: make(map[string]time.Time)}
}

func (s *wsTicketStore) issue() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	for t, at := range s.tickets {
		if time.Since(at) >= wsTicketTTL {
			delete(s.tickets, t) // opportunistic sweep, mirrors loki_cache.go's cachingLokiClient
		}
	}
	s.tickets[ticket] = time.Now()
	return ticket, nil
}

// consume reports whether ticket is a valid, unexpired, not-yet-used
// ticket -- and if so, invalidates it immediately, so a replayed URL
// (e.g. from browser history or a proxy log) can't open a second
// connection.
func (s *wsTicketStore) consume(ticket string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.tickets[ticket]
	if !ok {
		return false
	}
	delete(s.tickets, ticket)
	return time.Since(at) < wsTicketTTL
}

// requireWSTicket guards a WS-upgrade handler with a ticket passed as a
// query parameter, in place of requireBearerToken's Authorization-header
// check (auth.go), which a browser's WS handshake can't provide.
func requireWSTicket(store *wsTicketStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ticket := r.URL.Query().Get("ticket")
		if ticket == "" || !store.consume(ticket) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleIssueWSTicket(w http.ResponseWriter, r *http.Request) {
	ticket, err := s.wsTickets.issue()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "generate ticket: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket})
}
```

Note `subtle` is imported but unused in the snippet above — remove it; ticket comparison uses plain
map lookup (not a caller-presented shared secret compared against one fixed value the way the
bearer token is), so constant-time comparison isn't the relevant property here.

In `src/cmd/api-server/server.go`, add the field to the `server` struct:

```go
type server struct {
	clientManager      clientManagerClient
	clientManagerAdmin clientManagerAdminClient
	catalog            catalogQueryClient
	policy             policyServiceClient
	loki               lokiQuerier
	wsTickets          *wsTicketStore
	logger             *slog.Logger
	adhocPolicyTimeout time.Duration
}
```

and register the route inside `registerRoutes` (after the `POST /api/v1/restore` line, before the
jobs routes):

```go
	mux.Handle("POST /api/v1/ws-tickets", bearer(s.handleIssueWSTicket))
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 5: Full build check**

Run: `cd src && go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/ws_tickets.go src/cmd/api-server/ws_tickets_test.go src/cmd/api-server/server.go
git commit -m "$(cat <<'EOF'
feat(api-server): add short-lived single-use WS auth tickets

POST /api/v1/ws-tickets (bearer-authenticated, like every other route)
mints a 30s single-use ticket; requireWSTicket gates a WS-upgrade
handler on one instead of the Authorization header a browser's WS
handshake can't send. Nothing issues WS-upgrade routes yet -- Tasks 6
and 10 are the first callers of requireWSTicket.
EOF
)"
```

---

### Task 5: `api-server` — Loki tailer client

**Files:**
- Create: `src/cmd/api-server/loki_tail.go`
- Create: `src/cmd/api-server/loki_tail_test.go`

**Interfaces:**
- Consumes: Task 1's `gorilla/websocket`; `lokiStream`/`lokiValue` (existing, `loki.go`).
- Produces: `lokiTailMessage{Streams []lokiStream}`; `lokiTailer` interface with
  `Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error)
  error`; `httpLokiTailer` (`newHTTPLokiTailer(baseURL string, dialer *websocket.Dialer)
  *httpLokiTailer`) implementing it. Tasks 6 and 9 both consume `lokiTailer` (via the interface, so
  each can use a fake in tests).

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/api-server/loki_tail_test.go`:

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPLokiTailer_DeliversMessagesViaOnMessage(t *testing.T) {
	upgrader := websocket.Upgrader{}
	var gotQuery string
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		require.NoError(t, conn.WriteJSON(map[string]any{
			"streams": []map[string]any{
				{"stream": map[string]string{"hostname": "webserver"}, "values": [][]string{{"1752400500000000000", "line1"}}},
			},
		}))
	}))
	defer lokiStub.Close()

	wsBase := "ws" + strings.TrimPrefix(lokiStub.URL, "http")
	tailer := newHTTPLokiTailer(wsBase, websocket.DefaultDialer)

	received := make(chan lokiTailMessage, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = tailer.Tail(ctx, `{binary=~"agent"}`, time.Unix(1752400000, 0), func(msg lokiTailMessage) error {
		received <- msg
		cancel() // one message is enough for this test
		return nil
	})

	select {
	case msg := <-received:
		require.Len(t, msg.Streams, 1)
		assert.Equal(t, "webserver", msg.Streams[0].Stream["hostname"])
		require.Len(t, msg.Streams[0].Values, 1)
		assert.Equal(t, "line1", msg.Streams[0].Values[0].Line)
	case <-time.After(2 * time.Second):
		t.Fatal("no message received")
	}
	assert.Contains(t, gotQuery, "query=")
	assert.Contains(t, gotQuery, "start=1752400000000000000")
}

func TestHTTPLokiTailer_DialFailureReturnsError(t *testing.T) {
	tailer := newHTTPLokiTailer("ws://127.0.0.1:1", websocket.DefaultDialer) // nothing listens here
	err := tailer.Tail(context.Background(), `{}`, time.Now(), func(lokiTailMessage) error { return nil })
	assert.Error(t, err)
}

func TestHTTPLokiTailer_OnMessageErrorStopsTail(t *testing.T) {
	upgrader := websocket.Upgrader{}
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		require.NoError(t, err)
		defer conn.Close()
		for i := 0; i < 5; i++ {
			if err := conn.WriteJSON(map[string]any{"streams": []map[string]any{}}); err != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer lokiStub.Close()

	wsBase := "ws" + strings.TrimPrefix(lokiStub.URL, "http")
	tailer := newHTTPLokiTailer(wsBase, websocket.DefaultDialer)

	calls := 0
	err := tailer.Tail(context.Background(), `{}`, time.Now(), func(lokiTailMessage) error {
		calls++
		return assert.AnError
	})
	assert.ErrorIs(t, err, assert.AnError)
	assert.Equal(t, 1, calls, "onMessage's error must stop the tail after the first message, not be swallowed")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHTTPLokiTailer -v`
Expected: FAIL — `undefined: newHTTPLokiTailer`.

- [ ] **Step 3: Implement**

Create `src/cmd/api-server/loki_tail.go`:

```go
// src/cmd/api-server/loki_tail.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

// lokiTailMessage mirrors one batch of Loki's WS tail wire shape (see
// docs/protocols/log-gateway.md's new tail-proxy section) -- Loki batches
// however its own querier chooses to flush, so one message can carry
// several streams' worth of new lines.
type lokiTailMessage struct {
	Streams []lokiStream `json:"streams"`
}

// lokiTailer is the subset of tail behavior handleJobLogsStream (Task 6)
// and jobAggregator (Task 9) need -- satisfied by httpLokiTailer, and by a
// fake in tests.
type lokiTailer interface {
	// Tail opens a Loki tail connection for query starting at start and
	// calls onMessage for every batch received, until ctx is cancelled
	// (returns nil) or the connection errors or onMessage itself returns a
	// non-nil error (returns that error either way).
	Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error
}

// httpLokiTailer calls log-gateway's WS tail-proxy route (Task 2) rather
// than dialing Loki directly -- Loki is never directly reachable from any
// agent-managed node, api-server included (see docs/SECURITY.md).
type httpLokiTailer struct {
	baseURL string // log-gateway's ws(s):// base URL, e.g. "wss://log-gateway:9400"
	dialer  *websocket.Dialer
}

func newHTTPLokiTailer(baseURL string, dialer *websocket.Dialer) *httpLokiTailer {
	return &httpLokiTailer{baseURL: baseURL, dialer: dialer}
}

func (t *httpLokiTailer) Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error {
	u := t.baseURL + "/loki/api/v1/tail?query=" + url.QueryEscape(query) + "&start=" + strconv.FormatInt(start.UnixNano(), 10)

	conn, _, err := t.dialer.DialContext(ctx, u, nil)
	if err != nil {
		return fmt.Errorf("dial log-gateway tail: %w", err)
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read tail message: %w", err)
		}
		var msg lokiTailMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue // a malformed frame is dropped, not fatal to the tail
		}
		if err := onMessage(msg); err != nil {
			return err
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestHTTPLokiTailer -v`
Expected: PASS.

- [ ] **Step 5: Full build check**

Run: `cd src && go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/loki_tail.go src/cmd/api-server/loki_tail_test.go
git commit -m "$(cat <<'EOF'
feat(api-server): add lokiTailer client over log-gateway's WS proxy

httpLokiTailer dials log-gateway's new GET /loki/api/v1/tail route and
delivers decoded batches via a callback, stopping the tail on ctx
cancellation, a connection error, or the callback's own error. Nothing
calls this yet -- Tasks 6 and 9 are the first consumers.
EOF
)"
```

---

### Task 6: `api-server` — per-job log tail WS endpoint

**Files:**
- Create: `src/cmd/api-server/jobs_stream.go`
- Create: `src/cmd/api-server/jobs_stream_test.go`
- Modify: `src/cmd/api-server/server.go` (add field, register route)

**Interfaces:**
- Consumes: Task 5's `lokiTailer`; `jobIDPattern` (existing, `jobs.go`); `logLineDTO` (existing,
  `jobs.go`); Task 4's `requireWSTicket`.
- Produces: `(*server).handleJobLogsStream`, registered as `GET
  /api/v1/jobs/{job_id}/logs/stream`, ticket-gated. `web`'s `JobDetailView` (Task 13) is the
  consumer.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/api-server/jobs_stream_test.go`:

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLokiTailer struct {
	messages []lokiTailMessage
}

func (f *fakeLokiTailer) Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error {
	for _, m := range f.messages {
		if err := onMessage(m); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

func TestHandleJobLogsStream_RelaysMatchingLinesToClient(t *testing.T) {
	fake := &fakeLokiTailer{messages: []lokiTailMessage{{
		Streams: []lokiStream{{
			Stream: map[string]string{"hostname": "database", "binary": "brfs"},
			Values: []lokiValue{{Timestamp: 1752400000123456789, Line: `{"msg":"done","event":"finish"}`}},
		}},
	}}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.lokiTail = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	gatewayStub := httptest.NewServer(mux)
	defer gatewayStub.Close()

	ticket, err := srv.wsTickets.issue()
	require.NoError(t, err)

	wsURL := "ws" + strings.TrimPrefix(gatewayStub.URL, "http") + "/api/v1/jobs/backup%3Anightly%3A1/logs/stream?ticket=" + ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	var got logLineDTO
	require.NoError(t, conn.ReadJSON(&got))
	assert.Equal(t, "database", got.Hostname)
	assert.Equal(t, "brfs", got.Binary)
	assert.Contains(t, got.Line, "finish")
}

func TestHandleJobLogsStream_InvalidJobIDRejectedBeforeUpgrade(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.lokiTail = &fakeLokiTailer{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	ticket, err := srv.wsTickets.issue()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/bad%20id/logs/stream?ticket="+ticket, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleJobLogsStream_MissingTicketRejected(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.lokiTail = &fakeLokiTailer{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/backup%3Anightly%3A1/logs/stream", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleJobLogsStream -v`
Expected: FAIL — `srv.lokiTail` field undefined / `handleJobLogsStream` undefined.

- [ ] **Step 3: Implement**

Create `src/cmd/api-server/jobs_stream.go`:

```go
// src/cmd/api-server/jobs_stream.go
package main

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

// browserUpgrader is shared by every browser-facing WS endpoint this
// binary serves. CheckOrigin always returns true: the ticket
// (requireWSTicket) is the real auth boundary, and in production the
// browser only ever reaches this same-origin via web's nginx reverse
// proxy (web/nginx.conf) anyway.
var browserUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// handleJobLogsStream upgrades to a WebSocket and tails job_id's log lines
// live, in the same logLineDTO shape GET /api/v1/jobs/{job_id}/logs
// already returns (jobs.go) -- LogLine.vue's parser doesn't need a second
// format. A stateless per-connection proxy: each call dials its own
// job_id-filtered Loki tail (unlike handleJobsStream/jobAggregator, Tasks
// 9-10, which share one fleet-wide tail across every connected browser).
// Gated by requireWSTicket, registered in server.go.
func (s *server) handleJobLogsStream(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job_id")
	if !jobIDPattern.MatchString(jobID) {
		http.Error(w, "job_id contains invalid characters", http.StatusBadRequest)
		return
	}

	start := time.Now()
	if raw := r.URL.Query().Get("start"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			start = time.Unix(parsed, 0)
		}
	}

	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("handleJobLogsStream: upgrade failed", "job_id", jobID, "error", err)
		return
	}
	defer conn.Close()

	// Includes rwfs, same as handleGetJobLogs (jobs.go) -- this endpoint
	// returns every raw line for job_id verbatim, no start/finish pairing,
	// so rwfs's lines (which never carry event/status) are still useful
	// signal here.
	query := fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="%s"`, jobID)
	err = s.lokiTail.Tail(r.Context(), query, start, func(msg lokiTailMessage) error {
		for _, stream := range msg.Streams {
			for _, v := range stream.Values {
				line := logLineDTO{
					Timestamp: v.Timestamp,
					Hostname:  stream.Stream["hostname"],
					Binary:    stream.Stream["binary"],
					Line:      v.Line,
				}
				if err := conn.WriteJSON(line); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		s.logger.Error("handleJobLogsStream: tail ended", "job_id", jobID, "error", err)
	}
}
```

In `src/cmd/api-server/server.go`, add the field to `server`:

```go
type server struct {
	clientManager      clientManagerClient
	clientManagerAdmin clientManagerAdminClient
	catalog            catalogQueryClient
	policy             policyServiceClient
	loki               lokiQuerier
	lokiTail           lokiTailer
	wsTickets          *wsTicketStore
	logger             *slog.Logger
	adhocPolicyTimeout time.Duration
}
```

and register the route inside `registerRoutes` (after the `POST /api/v1/ws-tickets` line):

```go
	mux.Handle("GET /api/v1/jobs/{job_id}/logs/stream", requireWSTicket(s.wsTickets, http.HandlerFunc(s.handleJobLogsStream)))
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 5: Full build check**

Run: `cd src && go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs_stream.go src/cmd/api-server/jobs_stream_test.go src/cmd/api-server/server.go
git commit -m "$(cat <<'EOF'
feat(api-server): add GET /api/v1/jobs/{job_id}/logs/stream

Ticket-gated WS endpoint that live-tails one job's log lines, in the
same shape GET /api/v1/jobs/{job_id}/logs already returns. A thin
per-connection proxy over lokiTailer -- no shared state, unlike the
fleet-wide jobs-list stream (Tasks 9-10).
EOF
)"
```

---

### Task 7: `api-server` — extract shared start/finish accumulator

**Purpose:** `pairJobEvents` (`jobs.go`) currently builds every `jobDTO` in one batch pass over two
full slices. The upcoming job aggregator (Task 8) needs the *same* pairing logic but applied one
line at a time, as each arrives from a live tail — this task extracts that logic into a small
reusable type so both the REST handler and the aggregator call one implementation, not two that
could drift apart (spec: "not two independently maintained pairing logics"). Pure refactor: no
behavior change, and every existing `jobs_test.go` test must still pass unmodified.

**Files:**
- Modify: `src/cmd/api-server/jobs.go`
- Test: `src/cmd/api-server/jobs_test.go` (add tests for the new type; existing tests untouched)

**Interfaces:**
- Consumes: `jobDTO`, `jobEventLine`, `kindFromJobID` (existing).
- Produces: `jobEventAccumulator` (`newJobEventAccumulator()`,
  `newJobEventAccumulatorSeeded(jobID string, dto jobDTO) *jobEventAccumulator`,
  `(*jobEventAccumulator).ApplyStart(e jobEventLine) jobDTO`,
  `(*jobEventAccumulator).ApplyFinish(e jobEventLine) jobDTO`,
  `(*jobEventAccumulator).All() []jobDTO`); `queryEvent` changes from a `(*server)` method to a
  free function `queryEvent(ctx context.Context, loki lokiQuerier, labelSelector, event string,
  since, until time.Time) ([]jobEventLine, bool, error)`. Task 9's `jobAggregator` calls both
  directly.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/jobs_test.go`:

```go
func TestJobEventAccumulator_StartThenFinishProducesCompleteJob(t *testing.T) {
	acc := newJobEventAccumulator()
	acc.ApplyStart(jobEventLine{JobID: "operating-refresh:1", Hostname: "webserver", Timestamp: 100})
	got := acc.ApplyFinish(jobEventLine{JobID: "operating-refresh:1", Hostname: "webserver", Timestamp: 101, Status: "success"})

	assert.Equal(t, "operating-refresh:1", got.JobID)
	assert.Equal(t, "operating-refresh", got.Kind)
	assert.Equal(t, "webserver", got.SourceHost)
	assert.Nil(t, got.StoreHost)
	require.NotNil(t, got.StartedAt)
	assert.Equal(t, int64(100), *got.StartedAt)
	require.NotNil(t, got.FinishedAt)
	assert.Equal(t, int64(101), *got.FinishedAt)
	assert.Equal(t, "success", got.State)
}

func TestJobEventAccumulator_FinishOnlySetsBackupStoreHost(t *testing.T) {
	acc := newJobEventAccumulator()
	acc.ApplyStart(jobEventLine{JobID: "backup:nightly:1", Hostname: "source-host", Timestamp: 100})
	got := acc.ApplyFinish(jobEventLine{JobID: "backup:nightly:1", Hostname: "store-host", Timestamp: 101, Status: "success"})

	require.NotNil(t, got.StoreHost)
	assert.Equal(t, "store-host", *got.StoreHost)
	assert.Equal(t, "source-host", got.SourceHost)
}

func TestJobEventAccumulator_StartOnlyIsInProgress(t *testing.T) {
	acc := newJobEventAccumulator()
	got := acc.ApplyStart(jobEventLine{JobID: "policy-update:1", Hostname: "webserver", Timestamp: 100})

	assert.Equal(t, "in_progress", got.State)
	assert.Nil(t, got.FinishedAt)
}

func TestJobEventAccumulatorSeeded_AppliesOnTopOfExistingJob(t *testing.T) {
	existing := jobDTO{JobID: "restore:x:1", Kind: "restore", SourceHost: "webserver", State: "in_progress"}
	startedAt := int64(100)
	existing.StartedAt = &startedAt

	acc := newJobEventAccumulatorSeeded("restore:x:1", existing)
	got := acc.ApplyFinish(jobEventLine{JobID: "restore:x:1", Hostname: "webserver", Timestamp: 105, Status: "failure"})

	require.NotNil(t, got.StartedAt)
	assert.Equal(t, int64(100), *got.StartedAt, "seeding must preserve the job's prior state, not discard it")
	assert.Equal(t, "failure", got.State)
}

func TestPairJobEvents_StillMatchesPriorBehaviorViaAccumulator(t *testing.T) {
	starts := []jobEventLine{{JobID: "a", Hostname: "h1", Timestamp: 1}}
	finishes := []jobEventLine{{JobID: "a", Hostname: "h1", Timestamp: 2, Status: "success"}}

	got := pairJobEvents(starts, finishes)

	require.Len(t, got, 1)
	assert.Equal(t, "success", got[0].State)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestJobEventAccumulator -v`
Expected: FAIL — `undefined: newJobEventAccumulator`.

- [ ] **Step 3: Implement the accumulator and refactor `pairJobEvents`/`queryEvent`**

In `src/cmd/api-server/jobs.go`, replace the `pairJobEvents` function and the `get` closure inside
it with:

```go
// jobEventAccumulator incrementally builds jobDTOs from start/finish event
// lines -- the same logic pairJobEvents runs once per query (batch, below)
// and jobAggregator (Task 9) runs one line at a time as they arrive from
// Loki's live tail. Kept as one implementation so the two call sites can't
// drift apart.
type jobEventAccumulator struct {
	byJobID map[string]*jobDTO
	order   []string
}

func newJobEventAccumulator() *jobEventAccumulator {
	return &jobEventAccumulator{byJobID: make(map[string]*jobDTO)}
}

// newJobEventAccumulatorSeeded starts from an already-known job summary
// (the aggregator's in-memory state for jobID) instead of from scratch --
// used when folding in one more live event for a job the aggregator has
// already seen.
func newJobEventAccumulatorSeeded(jobID string, dto jobDTO) *jobEventAccumulator {
	acc := newJobEventAccumulator()
	acc.byJobID[jobID] = &dto
	acc.order = []string{jobID}
	return acc
}

func (a *jobEventAccumulator) get(jobID string) *jobDTO {
	j, ok := a.byJobID[jobID]
	if !ok {
		j = &jobDTO{JobID: jobID, Kind: kindFromJobID(jobID), State: "in_progress"}
		a.byJobID[jobID] = j
		a.order = append(a.order, jobID)
	}
	return j
}

// ApplyStart folds one event=start line in, returning the affected job's
// current (possibly still-incomplete) summary.
func (a *jobEventAccumulator) ApplyStart(e jobEventLine) jobDTO {
	j := a.get(e.JobID)
	ts := e.Timestamp
	j.SourceHost = e.Hostname
	j.StartedAt = &ts
	return *j
}

// ApplyFinish folds one event=finish line in, returning the affected job's
// current summary. For kind=backup, StoreHost comes from the finish
// line's hostname (bwfs, the destination) -- every other kind leaves it
// nil, same rule pairJobEvents always applied.
func (a *jobEventAccumulator) ApplyFinish(e jobEventLine) jobDTO {
	j := a.get(e.JobID)
	ts := e.Timestamp
	j.FinishedAt = &ts
	j.State = e.Status
	if j.Kind == "backup" {
		host := e.Hostname
		j.StoreHost = &host
	}
	return *j
}

func (a *jobEventAccumulator) All() []jobDTO {
	out := make([]jobDTO, 0, len(a.order))
	for _, id := range a.order {
		out = append(out, *a.byJobID[id])
	}
	return out
}

// pairJobEvents groups start/finish lines by job_id into one jobDTO each.
// A job_id with only a start line is in_progress; one with only a finish
// line (its start fell outside the queried window) gets a nil StartedAt --
// never guessed.
func pairJobEvents(starts, finishes []jobEventLine) []jobDTO {
	acc := newJobEventAccumulator()
	for _, e := range starts {
		acc.ApplyStart(e)
	}
	for _, e := range finishes {
		acc.ApplyFinish(e)
	}
	return acc.All()
}
```

Change `queryEvent` from a method to a free function — replace:

```go
func (s *server) queryEvent(ctx context.Context, labelSelector, event string, since, until time.Time) ([]jobEventLine, bool, error) {
	query := fmt.Sprintf(`%s | event="%s"`, labelSelector, event)
	streams, err := s.loki.QueryRange(ctx, query, since, until, jobsQueryLineLimit)
```

with:

```go
// queryEvent runs one Loki query scoped to labelSelector and the given
// event value against loki, returning every matching (job_id, hostname,
// timestamp, status) line and whether the query hit its own line cap. A
// free function (not a *server method) so jobAggregator (Task 9) can call
// it against its own loki field too, not just handleListJobs.
func queryEvent(ctx context.Context, loki lokiQuerier, labelSelector, event string, since, until time.Time) ([]jobEventLine, bool, error) {
	query := fmt.Sprintf(`%s | event="%s"`, labelSelector, event)
	streams, err := loki.QueryRange(ctx, query, since, until, jobsQueryLineLimit)
```

(the rest of the function body is unchanged). Update its two call sites inside `handleListJobs`:

```go
	starts, startsTruncated, err := queryEvent(r.Context(), s.loki, startLabelSelector, "start", since, until)
	...
	finishes, finishesTruncated, err := queryEvent(r.Context(), s.loki, finishLabelSelector, "finish", since, until)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS, including every pre-existing `TestHandleListJobs*`/`TestPairJobEvents*`/
`TestKindFromJobID`/`TestBinariesForKind` test unmodified — this step is the proof the refactor
changed no behavior.

- [ ] **Step 5: Full build check**

Run: `cd src && go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go
git commit -m "$(cat <<'EOF'
refactor(api-server): extract jobEventAccumulator from pairJobEvents

pairJobEvents now delegates to a small incremental accumulator type
instead of inlining its own map/order bookkeeping. No behavior change
-- every existing /api/v1/jobs test still passes unmodified. Also
turns queryEvent from a *server method into a free function taking
loki explicitly. Prerequisite for jobAggregator (next task), which
needs the exact same pairing logic applied one line at a time instead
of batch, and must not reimplement it separately.
EOF
)"
```

---

### Task 8: `api-server` — job aggregator core (state, subscribe, ingest)

**Files:**
- Create: `src/cmd/api-server/jobs_aggregator.go`
- Create: `src/cmd/api-server/jobs_aggregator_test.go`

**Interfaces:**
- Consumes: Task 7's `jobEventAccumulator`/`newJobEventAccumulatorSeeded`/`jobDTO`; Task 5's
  `lokiTailMessage`.
- Produces: `jobsStreamMsg{Type string, Jobs []jobDTO, Job *jobDTO}`; `jobAggregator`
  (`newJobAggregator(loki lokiQuerier, tailer lokiTailer, logger *slog.Logger) *jobAggregator`,
  `(*jobAggregator).Subscribe() (snapshot []jobDTO, ch chan jobsStreamMsg, unsubscribe func())`,
  `(*jobAggregator).ingestTailMessage(msg lokiTailMessage)`). Task 9 adds `Start`/`reconcile` to
  this same type; Task 10's `handleJobsStream` calls `Subscribe`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/api-server/jobs_aggregator_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobAggregator_SubscribeReturnsCurrentSnapshot(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	agg.jobs["a"] = jobDTO{JobID: "a", Kind: "backup", State: "success"}

	snapshot, _, unsubscribe := agg.Subscribe()
	defer unsubscribe()

	require.Len(t, snapshot, 1)
	assert.Equal(t, "a", snapshot[0].JobID)
}

func TestJobAggregator_IngestTailMessageUpsertsAndBroadcasts(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	_, ch, unsubscribe := agg.Subscribe()
	defer unsubscribe()

	agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
		Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "start"},
		Values: []lokiValue{{Timestamp: 1752400500000000000}},
	}}})

	select {
	case msg := <-ch:
		assert.Equal(t, "upsert", msg.Type)
		require.NotNil(t, msg.Job)
		assert.Equal(t, "operating-refresh:1", msg.Job.JobID)
		assert.Equal(t, "in_progress", msg.Job.State)
	case <-time.After(time.Second):
		t.Fatal("no upsert broadcast")
	}

	agg.mu.Lock()
	stored, ok := agg.jobs["operating-refresh:1"]
	agg.mu.Unlock()
	require.True(t, ok, "ingested job must be stored in the aggregator's own state")
	assert.Equal(t, "in_progress", stored.State)
}

func TestJobAggregator_IngestTailMessageAppliesFinishOnTopOfStart(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	_, ch, unsubscribe := agg.Subscribe()
	defer unsubscribe()

	agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
		Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "start"},
		Values: []lokiValue{{Timestamp: 1752400500000000000}},
	}}})
	<-ch

	agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
		Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "finish", "status": "success"},
		Values: []lokiValue{{Timestamp: 1752400501000000000}},
	}}})

	select {
	case msg := <-ch:
		require.NotNil(t, msg.Job)
		assert.Equal(t, "success", msg.Job.State)
		require.NotNil(t, msg.Job.StartedAt, "the finish upsert must not lose the start line already ingested")
	case <-time.After(time.Second):
		t.Fatal("no upsert broadcast")
	}
}

func TestJobAggregator_SlowSubscriberDoesNotBlockBroadcast(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	_, ch, unsubscribe := agg.Subscribe() // never read from -- simulates a slow/stuck browser
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		for i := 0; i < jobsAggregatorSubscriberBuffer+5; i++ {
			agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
				Stream: map[string]string{"hostname": "h", "job_id": "policy-update:1", "event": "start"},
				Values: []lokiValue{{Timestamp: int64(i) * 1_000_000_000}},
			}}})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a full, unread subscriber channel")
	}
	_ = ch
	_ = context.Background()
}
```

(The unused `context`/`ch` references in the last test are there only to keep imports tidy if you
trim the test; remove the two `_ = ...` lines and the `"context"` import if unused once written —
keep whichever compiles cleanly.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestJobAggregator -v`
Expected: FAIL — `undefined: newJobAggregator`.

- [ ] **Step 3: Implement**

Create `src/cmd/api-server/jobs_aggregator.go`:

```go
// src/cmd/api-server/jobs_aggregator.go
package main

import (
	"log/slog"
	"sync"
)

// jobsAggregatorSubscriberBuffer bounds how many pending messages one
// connected browser can be behind before broadcast starts dropping
// updates for it -- a slow/stuck subscriber must never block delivery to
// every other one. A dropped upsert is never a permanent miss: the
// periodic reconciliation reconcile() runs (Task 9) resyncs every
// subscriber with a fresh full snapshot regardless.
const jobsAggregatorSubscriberBuffer = 32

// jobsStreamMsg is GET /api/v1/jobs/stream's wire message (Task 10):
// "snapshot" carries the full current job list (sent once, right after a
// browser connects, and again on every periodic reconcile), "upsert"
// carries one job whose summary just changed.
type jobsStreamMsg struct {
	Type string   `json:"type"`
	Jobs []jobDTO `json:"jobs,omitempty"`
	Job  *jobDTO  `json:"job,omitempty"`
}

// jobAggregator maintains one fleet-wide, in-memory job_id -> summary map
// fed by a single shared Loki tail (Task 9 adds the tail/reconcile
// lifecycle), fanning out snapshot+upsert messages to every connected
// browser (Task 10) -- rather than each browser tab opening its own
// fleet-wide tail, which would multiply Loki-side query cost with no
// benefit (spec Architecture).
type jobAggregator struct {
	loki   lokiQuerier
	tailer lokiTailer
	logger *slog.Logger

	mu   sync.Mutex
	jobs map[string]jobDTO
	subs map[chan jobsStreamMsg]struct{}
}

func newJobAggregator(loki lokiQuerier, tailer lokiTailer, logger *slog.Logger) *jobAggregator {
	return &jobAggregator{
		loki:   loki,
		tailer: tailer,
		logger: logger,
		jobs:   make(map[string]jobDTO),
		subs:   make(map[chan jobsStreamMsg]struct{}),
	}
}

// Subscribe registers a new listener and returns the current state as a
// snapshot, alongside the channel future upserts (and future full
// snapshots, from reconcile) will arrive on. Callers must call unsubscribe
// exactly once, typically via defer, when they stop reading.
func (a *jobAggregator) Subscribe() (snapshot []jobDTO, ch chan jobsStreamMsg, unsubscribe func()) {
	ch = make(chan jobsStreamMsg, jobsAggregatorSubscriberBuffer)

	a.mu.Lock()
	a.subs[ch] = struct{}{}
	snapshot = make([]jobDTO, 0, len(a.jobs))
	for _, j := range a.jobs {
		snapshot = append(snapshot, j)
	}
	a.mu.Unlock()

	unsubscribe = func() {
		a.mu.Lock()
		delete(a.subs, ch)
		a.mu.Unlock()
	}
	return snapshot, ch, unsubscribe
}

func (a *jobAggregator) broadcast(msg jobsStreamMsg) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- msg:
		default:
			// slow subscriber: drop rather than block every other one --
			// see jobsAggregatorSubscriberBuffer's comment above.
		}
	}
}

// ingestTailMessage folds one batch of live tail lines into the
// aggregator's state, using the exact same jobEventAccumulator logic (Task
// 7) GET /api/v1/jobs runs in batch -- applied here one job at a time,
// seeded from whatever this job_id's prior state already was (if any), so
// a finish line doesn't clobber the start line ingested moments earlier.
func (a *jobAggregator) ingestTailMessage(msg lokiTailMessage) {
	for _, stream := range msg.Streams {
		streamEvent := stream.Stream["event"]
		streamJobID := stream.Stream["job_id"]
		streamStatus := stream.Stream["status"]
		hostname := stream.Stream["hostname"]

		for _, v := range stream.Values {
			jobID := v.Metadata["job_id"]
			if jobID == "" {
				jobID = streamJobID
			}
			event := v.Metadata["event"]
			if event == "" {
				event = streamEvent
			}
			if jobID == "" || (event != "start" && event != "finish") {
				continue
			}
			status := v.Metadata["status"]
			if status == "" {
				status = streamStatus
			}
			line := jobEventLine{JobID: jobID, Hostname: hostname, Timestamp: v.Timestamp / 1_000_000_000, Status: status}

			a.mu.Lock()
			existing, ok := a.jobs[jobID]
			var acc *jobEventAccumulator
			if ok {
				acc = newJobEventAccumulatorSeeded(jobID, existing)
			} else {
				acc = newJobEventAccumulator()
			}
			var updated jobDTO
			if event == "start" {
				updated = acc.ApplyStart(line)
			} else {
				updated = acc.ApplyFinish(line)
			}
			a.jobs[jobID] = updated
			a.mu.Unlock()

			a.broadcast(jobsStreamMsg{Type: "upsert", Job: &updated})
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestJobAggregator -v`
Expected: PASS.

- [ ] **Step 5: Full build check**

Run: `cd src && go build ./...`
Expected: PASS (nothing constructs a real `jobAggregator` in `main.go` yet — that's Task 10).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs_aggregator.go src/cmd/api-server/jobs_aggregator_test.go
git commit -m "$(cat <<'EOF'
feat(api-server): add jobAggregator core (state, subscribe, ingest)

In-memory job_id -> summary map fed by ingestTailMessage, fanned out
to subscribers via snapshot-on-connect + upsert-on-change, using
jobEventAccumulator (Task 7) so this is the same pairing logic
GET /api/v1/jobs already runs, just incremental. A slow subscriber's
full channel drops an update rather than blocking every other
listener -- Task 9's periodic reconciliation is the backstop that
makes a dropped upsert non-permanent. No tail/reconcile lifecycle yet
(next task) and nothing wires this into main.go yet (Task 10).
EOF
)"
```

---

### Task 9: `api-server` — job aggregator reconcile + supervised tail loop

**Files:**
- Modify: `src/cmd/api-server/jobs_aggregator.go`
- Modify: `src/cmd/api-server/jobs_aggregator_test.go`

**Interfaces:**
- Consumes: Task 7's `queryEvent`/`pairJobEvents`; Task 8's `jobAggregator`.
- Produces: `(*jobAggregator).Start(ctx context.Context)`, `(*jobAggregator).reconcile(ctx
  context.Context) error`. Task 10's `main.go` wiring calls `go agg.Start(signalCtx)`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/jobs_aggregator_test.go` (add `"errors"`, `"sync/atomic"` to imports as
needed):

```go
func TestJobAggregator_ReconcileReplacesStateFromLoki(t *testing.T) {
	fakeLoki := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "start"},
				Values: []lokiValue{{Timestamp: 1752400500000000000}}},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "finish", "status": "success"},
				Values: []lokiValue{{Timestamp: 1752400501000000000}}},
		},
	}}
	agg := newJobAggregator(fakeLoki, &fakeLokiTailer{}, testLogger())

	require.NoError(t, agg.reconcile(context.Background()))

	agg.mu.Lock()
	job, ok := agg.jobs["operating-refresh:1"]
	agg.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, "success", job.State)
}

func TestJobAggregator_ReconcileBroadcastsSnapshot(t *testing.T) {
	fakeLoki := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}
	agg := newJobAggregator(fakeLoki, &fakeLokiTailer{}, testLogger())
	_, ch, unsubscribe := agg.Subscribe()
	defer unsubscribe()

	require.NoError(t, agg.reconcile(context.Background()))

	select {
	case msg := <-ch:
		assert.Equal(t, "snapshot", msg.Type)
	case <-time.After(time.Second):
		t.Fatal("reconcile must broadcast a snapshot even when nothing changed")
	}
}

// blockingTailer's Tail call fails errCount times, then succeeds
// (delivering nothing, just blocking on ctx) -- lets the test observe
// jobAggregator.Start reconnecting with backoff and re-reconciling before
// each attempt, without a real Loki.
type blockingTailer struct {
	failuresBeforeSuccess int32
	attempts              atomic.Int32
}

func (b *blockingTailer) Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error {
	n := b.attempts.Add(1)
	if n <= b.failuresBeforeSuccess {
		return errors.New("simulated tail failure")
	}
	<-ctx.Done()
	return nil
}

func TestJobAggregator_StartReconnectsAfterTailFailure(t *testing.T) {
	fakeLoki := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}
	tailer := &blockingTailer{failuresBeforeSuccess: 2}
	agg := newJobAggregator(fakeLoki, tailer, testLogger())
	agg.backoffBase = time.Millisecond // keep the test fast

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go agg.Start(ctx)

	require.Eventually(t, func() bool {
		return tailer.attempts.Load() >= 3
	}, 2*time.Second, 10*time.Millisecond, "expected at least 2 failed attempts + 1 successful reconnect")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestJobAggregator_Reconcile|TestJobAggregator_StartReconnects' -v`
Expected: FAIL — `agg.reconcile undefined`, `agg.Start undefined`, `agg.backoffBase undefined`.

- [ ] **Step 3: Implement**

Add to `src/cmd/api-server/jobs_aggregator.go` (extend the `import` block with `"context"`,
`"math/rand/v2"`, `"time"`; add the two new fields to the `jobAggregator` struct and the two new
constructor defaults):

```go
const (
	jobsAggregatorWindow         = 24 * time.Hour
	jobsAggregatorReconcileEvery = 60 * time.Second
)

type jobAggregator struct {
	loki   lokiQuerier
	tailer lokiTailer
	logger *slog.Logger

	// backoffBase/backoffMax are fields (not consts) so tests can shrink
	// backoffBase -- mirrors cmd/agent/reconcile.go's backoffBase/backoffMax
	// vars for the same reason. Same jittered-exponential idiom as that
	// file's backoff(), reimplemented here since this is a separate
	// package with no shared code between the two.
	backoffBase time.Duration
	backoffMax  time.Duration

	mu   sync.Mutex
	jobs map[string]jobDTO
	subs map[chan jobsStreamMsg]struct{}
}

func newJobAggregator(loki lokiQuerier, tailer lokiTailer, logger *slog.Logger) *jobAggregator {
	return &jobAggregator{
		loki:        loki,
		tailer:      tailer,
		logger:      logger,
		backoffBase: time.Second,
		backoffMax:  30 * time.Second,
		jobs:        make(map[string]jobDTO),
		subs:        make(map[chan jobsStreamMsg]struct{}),
	}
}

func (a *jobAggregator) backoff(failures int) time.Duration {
	exp := min(max(failures-1, 0), 8)
	d := a.backoffBase * time.Duration(1<<exp)
	if d > a.backoffMax {
		d = a.backoffMax
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// reconcile re-runs the same 24h fleet-wide query GET /api/v1/jobs already
// runs (via queryEvent/pairJobEvents, Task 7) and wholesale-replaces the
// aggregator's in-memory state, broadcasting the result as a fresh
// snapshot to every subscriber. Called on startup, every
// jobsAggregatorReconcileEvery, and once before every tail
// (re)attachment -- a correctness backstop independent of tail health,
// since Loki's tail is explicitly best-effort on delivery (spec
// Architecture), not a substitute for it.
func (a *jobAggregator) reconcile(ctx context.Context) error {
	until := time.Now()
	since := until.Add(-jobsAggregatorWindow)
	const selector = `{binary=~"agent|brfs|bwfs"}`

	starts, _, err := queryEvent(ctx, a.loki, selector, "start", since, until)
	if err != nil {
		return err
	}
	finishes, _, err := queryEvent(ctx, a.loki, selector, "finish", since, until)
	if err != nil {
		return err
	}
	jobs := pairJobEvents(starts, finishes)

	a.mu.Lock()
	a.jobs = make(map[string]jobDTO, len(jobs))
	for _, j := range jobs {
		a.jobs[j.JobID] = j
	}
	a.mu.Unlock()

	a.broadcast(jobsStreamMsg{Type: "snapshot", Jobs: jobs})
	return nil
}

// Start runs until ctx is cancelled: an initial reconcile, a background
// loop re-reconciling every jobsAggregatorReconcileEvery, and a
// foreground supervised tail that reconnects with jittered backoff on any
// unexpected error -- re-reconciling before every (re)attach so a dropped
// connection can never silently lose a job (spec Architecture). Intended
// to be run in its own goroutine by main.go (Task 10): `go agg.Start(ctx)`.
func (a *jobAggregator) Start(ctx context.Context) {
	if err := a.reconcile(ctx); err != nil {
		a.logger.Error("jobAggregator: initial reconcile failed", "error", err)
	}

	go a.reconcileLoop(ctx)
	a.tailLoop(ctx)
}

func (a *jobAggregator) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(jobsAggregatorReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.reconcile(ctx); err != nil {
				a.logger.Error("jobAggregator: periodic reconcile failed", "error", err)
			}
		}
	}
}

func (a *jobAggregator) tailLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		err := a.tailer.Tail(ctx, `{binary=~"agent|brfs|bwfs"}`, time.Now(), func(msg lokiTailMessage) error {
			a.ingestTailMessage(msg)
			return nil
		})
		if ctx.Err() != nil {
			return
		}

		failures++
		a.logger.Error("jobAggregator: tail ended unexpectedly, reconnecting", "failures", failures, "error", err)
		if rerr := a.reconcile(ctx); rerr != nil {
			a.logger.Error("jobAggregator: reconnect reconcile failed", "error", rerr)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.backoff(failures)):
		}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestJobAggregator -v`
Expected: PASS.

- [ ] **Step 5: Full build check**

Run: `cd src && go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs_aggregator.go src/cmd/api-server/jobs_aggregator_test.go
git commit -m "$(cat <<'EOF'
feat(api-server): add jobAggregator reconcile + supervised tail loop

reconcile() re-runs the existing 24h /jobs query and wholesale-
replaces state, broadcasting a fresh snapshot -- the correctness
backstop independent of tail health. Start() runs it on a 60s ticker
plus once before every tail (re)attachment, with jittered
exponential backoff on unexpected tail errors, mirroring
cmd/agent/reconcile.go's backoff() idiom (reimplemented, not shared --
separate package). Still not wired into main.go -- Task 10.
EOF
)"
```

---

### Task 10: `api-server` — `GET /api/v1/jobs/stream` endpoint + `main.go` wiring

**Files:**
- Create: `src/cmd/api-server/jobs_stream_list.go`
- Create: `src/cmd/api-server/jobs_stream_list_test.go`
- Modify: `src/cmd/api-server/server.go` (add field, register route)
- Modify: `src/cmd/api-server/main.go` (construct + start the aggregator, ticket store, tailer)

**Interfaces:**
- Consumes: Task 8/9's `jobAggregator`; Task 6's `browserUpgrader`.
- Produces: `(*server).handleJobsStream`, registered as `GET /api/v1/jobs/stream`, ticket-gated.
  `web`'s `JobsListView` (Task 14) is the consumer. `main.go` now runs the aggregator for the
  process's lifetime.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/api-server/jobs_stream_list_test.go`:

```go
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleJobsStream_SendsSnapshotThenUpsert(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	agg.jobs["a"] = jobDTO{JobID: "a", Kind: "backup", State: "success"}

	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.aggregator = agg
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	gatewayStub := httptest.NewServer(mux)
	defer gatewayStub.Close()

	ticket, err := srv.wsTickets.issue()
	require.NoError(t, err)

	wsURL := "ws" + strings.TrimPrefix(gatewayStub.URL, "http") + "/api/v1/jobs/stream?ticket=" + ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	var snapshot jobsStreamMsg
	require.NoError(t, conn.ReadJSON(&snapshot))
	assert.Equal(t, "snapshot", snapshot.Type)
	require.Len(t, snapshot.Jobs, 1)
	assert.Equal(t, "a", snapshot.Jobs[0].JobID)

	agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
		Stream: map[string]string{"hostname": "h", "job_id": "b", "event": "start"},
		Values: []lokiValue{{Timestamp: time.Now().UnixNano()}},
	}}})

	var upsert jobsStreamMsg
	require.NoError(t, conn.ReadJSON(&upsert))
	assert.Equal(t, "upsert", upsert.Type)
	require.NotNil(t, upsert.Job)
	assert.Equal(t, "b", upsert.Job.JobID)
}

func TestHandleJobsStream_MissingTicketRejected(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.aggregator = newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleJobsStream -v`
Expected: FAIL — `srv.aggregator` undefined / `handleJobsStream` undefined.

- [ ] **Step 3: Implement the handler**

Create `src/cmd/api-server/jobs_stream_list.go`:

```go
// src/cmd/api-server/jobs_stream_list.go
package main

import "net/http"

// handleJobsStream upgrades to a WebSocket, sends the aggregator's current
// state as one "snapshot" message, then relays every subsequent "upsert"/
// "snapshot" (from a periodic reconcile) until the client disconnects.
// Gated by requireWSTicket, registered in server.go. Unlike
// handleJobLogsStream (Task 6), this never dials Loki itself -- it only
// subscribes to the one shared aggregator every connected browser reads
// from (Tasks 8-9).
func (s *server) handleJobsStream(w http.ResponseWriter, r *http.Request) {
	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("handleJobsStream: upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	snapshot, ch, unsubscribe := s.aggregator.Subscribe()
	defer unsubscribe()

	if err := conn.WriteJSON(jobsStreamMsg{Type: "snapshot", Jobs: snapshot}); err != nil {
		return
	}

	clientClosed := make(chan struct{})
	go func() {
		defer close(clientClosed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-clientClosed:
			return
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}
}
```

In `src/cmd/api-server/server.go`, add the field:

```go
type server struct {
	clientManager      clientManagerClient
	clientManagerAdmin clientManagerAdminClient
	catalog            catalogQueryClient
	policy             policyServiceClient
	loki               lokiQuerier
	lokiTail           lokiTailer
	wsTickets          *wsTicketStore
	aggregator         *jobAggregator
	logger             *slog.Logger
	adhocPolicyTimeout time.Duration
}
```

and register the route inside `registerRoutes` (after the `logs/stream` line from Task 6):

```go
	mux.Handle("GET /api/v1/jobs/stream", requireWSTicket(s.wsTickets, http.HandlerFunc(s.handleJobsStream)))
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS, including every pre-existing test.

- [ ] **Step 5: Wire everything into `main.go`**

In `src/cmd/api-server/main.go`, after the existing `srv.loki = newCachingLokiClient(...)` line,
add:

```go
	srv.wsTickets = newWSTicketStore()

	lokiTailDialer := &websocket.Dialer{TLSClientConfig: lokiTLSConfig}
	lokiTailBaseURL := fmt.Sprintf("wss://%s:%d", conf.LogGatewayHost, conf.LogGatewayPort)
	srv.lokiTail = newHTTPLokiTailer(lokiTailBaseURL, lokiTailDialer)

	srv.aggregator = newJobAggregator(srv.loki, srv.lokiTail, logger)
```

Add `"github.com/gorilla/websocket"` to the import block. Then, after `signalCtx, stop :=
signal.NotifyContext(...)` and before the `httpServer := &http.Server{...}` line, add:

```go
	go srv.aggregator.Start(signalCtx)
```

(`signalCtx` is already cancelled on shutdown by the existing `defer stop()`/signal-handling code
just above it, so the aggregator's background goroutines stop cleanly alongside the HTTP server —
no separate shutdown wiring needed.)

- [ ] **Step 6: Full build check**

Run: `cd src && go build ./...`
Expected: PASS.

- [ ] **Step 7: Manual smoke check against the demo lab**

Run: `make demo-up` (repo root), then from another shell:

```bash
curl -sS -X POST -H "Authorization: Bearer $(cat demo/.token 2>/dev/null || echo changeme)" \
  http://localhost:8090/api/v1/ws-tickets
```

Expected: a JSON body like `{"ticket":"<64 hex chars>"}` — confirms `api-server` started cleanly
with the aggregator running and the route reachable. (Exact token retrieval depends on how
`demo/docker-compose.yml` provisions `api-server`'s bearer token — check `demo/README.md` if the
literal `changeme` guess above doesn't work; this step's purpose is just confirming the process
didn't fail to start, not exhaustively validating the ticket flow, which Task 6/10's automated
tests already cover.)

Run: `make demo-down` when done.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/api-server/jobs_stream_list.go src/cmd/api-server/jobs_stream_list_test.go src/cmd/api-server/server.go src/cmd/api-server/main.go
git commit -m "$(cat <<'EOF'
feat(api-server): add GET /api/v1/jobs/stream, wire up the aggregator

Ticket-gated WS endpoint fanning out the shared jobAggregator's
snapshot/upsert stream to every connected browser. main.go now
constructs the ticket store, the Loki tailer (dialed over
log-gateway's WS proxy with the same mTLS credential already used for
query_range), and starts the aggregator's background loop alongside
the HTTP server, stopped by the same signalCtx cancellation that
already shuts everything else down.

This is the last api-server piece -- the backend half of live job
updates is now complete end to end. Remaining work is the web frontend
(next several tasks).
EOF
)"
```

---

### Task 11: `web` — WS client utility

**Files:**
- Create: `web/src/utils/wsClient.js`
- Create: `web/src/utils/wsClient.spec.js`

**Interfaces:**
- Consumes: `apiFetch` (existing, `web/src/api/client.js`).
- Produces: `openTicketedSocket(path) => Promise<WebSocket>`; `createLiveStream(path, { onMessage,
  onStatus, onFallback }) => { close() }`. Tasks 13-14's `jobs.js` store consume
  `createLiveStream`.

- [ ] **Step 1: Write the failing tests**

Create `web/src/utils/wsClient.spec.js`:

```js
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({ apiFetch: vi.fn() }))

class FakeWebSocket {
  static instances = []
  constructor(url) {
    this.url = url
    this.sent = []
    FakeWebSocket.instances.push(this)
  }
  send(data) { this.sent.push(data) }
  close() { this.onclose && this.onclose({}) }
  triggerOpen() { this.onopen && this.onopen({}) }
  triggerMessage(data) { this.onmessage && this.onmessage({ data: JSON.stringify(data) }) }
  triggerClose() { this.onclose && this.onclose({}) }
}

describe('wsClient', () => {
  let wsClient

  beforeEach(async () => {
    vi.resetModules()
    apiFetch.mockReset()
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket)
    wsClient = await import('./wsClient')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('openTicketedSocket fetches a ticket and includes it in the WS URL', async () => {
    apiFetch.mockResolvedValue({ ticket: 'abc123' })

    const socket = await wsClient.openTicketedSocket('/jobs/stream')

    expect(apiFetch).toHaveBeenCalledWith('/ws-tickets', { method: 'POST' })
    expect(socket.url).toContain('/api/v1/jobs/stream')
    expect(socket.url).toContain('ticket=abc123')
  })

  it('createLiveStream reports "live" once the socket opens and forwards messages', async () => {
    apiFetch.mockResolvedValue({ ticket: 'abc123' })
    const onMessage = vi.fn()
    const onStatus = vi.fn()

    wsClient.createLiveStream('/jobs/stream', { onMessage, onStatus, onFallback: vi.fn() })
    await Promise.resolve()
    await Promise.resolve()

    const socket = FakeWebSocket.instances[0]
    socket.triggerOpen()
    expect(onStatus).toHaveBeenCalledWith('live')

    socket.triggerMessage({ type: 'snapshot', jobs: [] })
    expect(onMessage).toHaveBeenCalledWith({ type: 'snapshot', jobs: [] })
  })

  it('reconnects with backoff on an unexpected close, reporting "reconnecting"', async () => {
    vi.useFakeTimers()
    apiFetch.mockResolvedValue({ ticket: 'abc123' })
    const onStatus = vi.fn()

    wsClient.createLiveStream('/jobs/stream', { onMessage: vi.fn(), onStatus, onFallback: vi.fn() })
    await vi.advanceTimersByTimeAsync(0)

    const first = FakeWebSocket.instances[0]
    first.triggerClose()
    expect(onStatus).toHaveBeenCalledWith('reconnecting')

    await vi.advanceTimersByTimeAsync(10000)
    expect(FakeWebSocket.instances.length).toBeGreaterThan(1)
  })

  it('falls back to polling after repeated reconnect failures', async () => {
    vi.useFakeTimers()
    apiFetch.mockResolvedValue({ ticket: 'abc123' })
    const onStatus = vi.fn()
    const onFallback = vi.fn()

    wsClient.createLiveStream('/jobs/stream', { onMessage: vi.fn(), onStatus, onFallback })

    for (let i = 0; i < 6; i++) {
      await vi.advanceTimersByTimeAsync(0)
      const socket = FakeWebSocket.instances[FakeWebSocket.instances.length - 1]
      socket.triggerClose()
      await vi.advanceTimersByTimeAsync(10000)
    }

    expect(onStatus).toHaveBeenCalledWith('polling')
    expect(onFallback).toHaveBeenCalled()
  })

  it('close() prevents further reconnect attempts', async () => {
    vi.useFakeTimers()
    apiFetch.mockResolvedValue({ ticket: 'abc123' })

    const stream = wsClient.createLiveStream('/jobs/stream', { onMessage: vi.fn(), onStatus: vi.fn(), onFallback: vi.fn() })
    await vi.advanceTimersByTimeAsync(0)
    const countBeforeClose = FakeWebSocket.instances.length

    stream.close()
    await vi.advanceTimersByTimeAsync(20000)

    expect(FakeWebSocket.instances.length).toBe(countBeforeClose)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/utils/wsClient.spec.js`
Expected: FAIL — `Failed to resolve import "./wsClient"`.

- [ ] **Step 3: Implement**

Create `web/src/utils/wsClient.js`:

```js
// web/src/utils/wsClient.js
import { apiFetch } from '../api/client'

const MAX_RECONNECT_ATTEMPTS = 5
const BASE_BACKOFF_MS = 500
const MAX_BACKOFF_MS = 8000
export const FALLBACK_POLL_MS = 10000

// backoff mirrors src/cmd/agent/reconcile.go's jittered backoff() idiom --
// exponential, capped, half-jittered -- reimplemented here since this is a
// separate language/runtime, not shared code.
function backoff(attempt) {
  const base = Math.min(BASE_BACKOFF_MS * 2 ** attempt, MAX_BACKOFF_MS)
  return base / 2 + Math.random() * (base / 2)
}

// openTicketedSocket mints a fresh single-use ticket (POST /ws-tickets --
// required on every call, since a ticket authenticates exactly one
// connection attempt, see src/cmd/api-server/ws_tickets.go) and opens the
// WebSocket with it as a query param -- a WS handshake can't carry an
// Authorization header the way every other apiFetch call does.
export async function openTicketedSocket(path) {
  const { ticket } = await apiFetch('/ws-tickets', { method: 'POST' })
  const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const separator = path.includes('?') ? '&' : '?'
  const url = `${proto}//${window.location.host}/api/v1${path}${separator}ticket=${encodeURIComponent(ticket)}`
  return new WebSocket(url)
}

// createLiveStream manages one logical live connection: opens a ticketed
// socket, calls onMessage for each parsed JSON frame, and reconnects with
// jittered backoff on an unexpected close -- up to MAX_RECONNECT_ATTEMPTS,
// after which it calls onFallback(FALLBACK_POLL_MS) once and stops
// retrying, so the caller can switch to REST polling instead of retrying
// forever silently (see jobs.js). onStatus reports 'live' | 'reconnecting'
// | 'polling', so the page can never look current while actually stalled.
export function createLiveStream(path, { onMessage, onStatus, onFallback }) {
  let attempt = 0
  let closedByCaller = false
  let socket = null

  async function connect() {
    if (closedByCaller) return
    try {
      socket = await openTicketedSocket(path)
    } catch {
      scheduleReconnect()
      return
    }
    socket.onopen = () => {
      attempt = 0
      onStatus('live')
    }
    socket.onmessage = (event) => {
      try {
        onMessage(JSON.parse(event.data))
      } catch {
        // a malformed frame is dropped, not fatal to the stream
      }
    }
    socket.onclose = () => {
      if (closedByCaller) return
      scheduleReconnect()
    }
    socket.onerror = () => socket.close()
  }

  function scheduleReconnect() {
    attempt += 1
    if (attempt > MAX_RECONNECT_ATTEMPTS) {
      onStatus('polling')
      onFallback(FALLBACK_POLL_MS)
      return
    }
    onStatus('reconnecting')
    setTimeout(connect, backoff(attempt))
  }

  connect()

  return {
    close() {
      closedByCaller = true
      if (socket) socket.close()
    },
  }
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/utils/wsClient.spec.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/wsClient.js web/src/utils/wsClient.spec.js
git commit -m "$(cat <<'EOF'
feat(web): add wsClient — ticketed WS connect + reconnect/fallback

createLiveStream opens a ticketed WebSocket, reconnects with jittered
backoff on an unexpected close, and falls back to REST polling after 5
failed attempts rather than retrying forever silently. Nothing uses
this yet -- Tasks 13-14 wire it into the jobs store.
EOF
)"
```

---

### Task 12: `web` — `ConnectionStatus` component

**Files:**
- Create: `web/src/components/ui/ConnectionStatus.vue`
- Create: `web/src/components/ui/ConnectionStatus.spec.js`

**Interfaces:**
- Consumes: nothing.
- Produces: `<ConnectionStatus :status="...">` where `status` is one of `'live' |
  'connecting' | 'reconnecting' | 'polling' | 'finished'`. Tasks 13-14's views render it.

- [ ] **Step 1: Write the failing test**

Create `web/src/components/ui/ConnectionStatus.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import ConnectionStatus from './ConnectionStatus.vue'

describe('ConnectionStatus', () => {
  it('renders the label for each known status', () => {
    for (const [status, label] of [
      ['live', 'Live'],
      ['connecting', 'Connecting…'],
      ['reconnecting', 'Reconnecting…'],
      ['polling', 'Live updates unavailable — refreshing every 10s'],
      ['finished', 'Finished'],
    ]) {
      const wrapper = mount(ConnectionStatus, { props: { status } })
      expect(wrapper.text()).toContain(label)
    }
  })
})
```

(Check `web/src/components/ui/Badge.spec.js` or a similar existing component spec for this
project's exact `@vue/test-utils` import/mount convention before writing this — match it if it
differs, e.g. a different test-id query helper.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd web && npx vitest run src/components/ui/ConnectionStatus.spec.js`
Expected: FAIL — `Failed to resolve import "./ConnectionStatus.vue"`.

- [ ] **Step 3: Implement**

Create `web/src/components/ui/ConnectionStatus.vue`:

```vue
<!-- web/src/components/ui/ConnectionStatus.vue -->
<script setup>
defineProps({
  status: { type: String, required: true }, // 'live' | 'connecting' | 'reconnecting' | 'polling' | 'finished'
})

const LABELS = {
  live: 'Live',
  connecting: 'Connecting…',
  reconnecting: 'Reconnecting…',
  polling: 'Live updates unavailable — refreshing every 10s',
  finished: 'Finished',
}

const CLASSES = {
  live: 'bg-green-100 text-green-700',
  connecting: 'bg-gray-100 text-gray-500',
  reconnecting: 'bg-amber-100 text-amber-700',
  polling: 'bg-amber-100 text-amber-700',
  finished: 'bg-gray-100 text-gray-600',
}
</script>

<template>
  <span
    :class="['inline-block rounded px-2 py-0.5 text-xs font-semibold', CLASSES[status] || CLASSES.connecting]"
    data-test="connection-status"
  >
    {{ LABELS[status] || status }}
  </span>
</template>
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd web && npx vitest run src/components/ui/ConnectionStatus.spec.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/ui/ConnectionStatus.vue web/src/components/ui/ConnectionStatus.spec.js
git commit -m "$(cat <<'EOF'
feat(web): add ConnectionStatus component

Small status badge for the two live-updating job pages -- never lets
either page look current while its connection is actually stalled.
Not wired into a view yet (Tasks 13-14).
EOF
)"
```

---

### Task 13: `web` — `jobs.js` store: live log-tail stream

**Files:**
- Modify: `web/src/stores/jobs.js`
- Modify: `web/src/stores/jobs.spec.js`
- Modify: `web/src/views/JobDetailView.vue`
- Modify: `web/src/views/JobDetailView.spec.js` (adjust for the new mount/unmount calls if it
  asserts on them; otherwise unchanged)

**Interfaces:**
- Consumes: Task 11's `createLiveStream`; `parseLogLine` (existing, `web/src/utils/logLine.js`).
- Produces: `jobsStore.logsStatus` (state, one of the `ConnectionStatus` values),
  `jobsStore.connectLogsStream(jobId)`, `jobsStore.disconnectLogsStream()`.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/stores/jobs.spec.js` (add `vi.mock('../utils/wsClient', () => ({ createLiveStream:
vi.fn() }))` near the top, alongside the existing `apiFetch` mock, and import
`{ createLiveStream } from '../utils/wsClient'`):

```js
import { createLiveStream } from '../utils/wsClient'

// ... inside the existing describe block, alongside the pre-existing tests:

describe('connectLogsStream', () => {
  let liveStreamHandlers

  beforeEach(() => {
    createLiveStream.mockReset()
    createLiveStream.mockImplementation((path, handlers) => {
      liveStreamHandlers = handlers
      return { close: vi.fn() }
    })
  })

  it('fetches history first, then opens a live stream from a cursor near "now"', async () => {
    apiFetch.mockResolvedValue({ data: [] })
    const jobs = useJobsStore()

    await jobs.connectLogsStream('restore:x:1')

    expect(apiFetch).toHaveBeenCalledWith('/jobs/restore%3Ax%3A1/logs')
    expect(createLiveStream).toHaveBeenCalledWith(
      expect.stringMatching(/^\/jobs\/restore%3Ax%3A1\/logs\/stream\?start=\d+$/),
      expect.objectContaining({ onMessage: expect.any(Function), onStatus: expect.any(Function), onFallback: expect.any(Function) })
    )
  })

  it('merges a new line delivered over the stream, deduping by timestamp/hostname/binary', async () => {
    apiFetch.mockResolvedValue({
      data: [{ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }],
    })
    const jobs = useJobsStore()
    await jobs.connectLogsStream('restore:x:1')

    liveStreamHandlers.onMessage({ timestamp: 100, hostname: 'h', binary: 'brfs', line: '{}' }) // duplicate of history
    liveStreamHandlers.onMessage({ timestamp: 200, hostname: 'h', binary: 'brfs', line: '{"msg":"new"}' })

    expect(jobs.logs).toHaveLength(2)
    expect(jobs.logs.map((l) => l.timestamp)).toEqual([100, 200])
  })

  it('flips status to "finished" and closes the stream on an event=finish line', async () => {
    apiFetch.mockResolvedValue({ data: [] })
    const jobs = useJobsStore()
    await jobs.connectLogsStream('restore:x:1')

    liveStreamHandlers.onMessage({ timestamp: 100, hostname: 'h', binary: 'agent', line: '{"event":"finish","status":"success"}' })

    expect(jobs.logsStatus).toBe('finished')
  })

  it('onStatus updates logsStatus, except once finished it stays finished', async () => {
    apiFetch.mockResolvedValue({ data: [] })
    const jobs = useJobsStore()
    await jobs.connectLogsStream('restore:x:1')

    liveStreamHandlers.onStatus('live')
    expect(jobs.logsStatus).toBe('live')

    jobs.logsStatus = 'finished'
    liveStreamHandlers.onStatus('reconnecting')
    expect(jobs.logsStatus).toBe('finished')
  })

  it('disconnectLogsStream closes the stream and clears reconciliation timers', async () => {
    apiFetch.mockResolvedValue({ data: [] })
    const jobs = useJobsStore()
    await jobs.connectLogsStream('restore:x:1')
    const closeSpy = createLiveStream.mock.results[0].value.close

    jobs.disconnectLogsStream()

    expect(closeSpy).toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/stores/jobs.spec.js`
Expected: FAIL — `jobs.connectLogsStream is not a function`.

- [ ] **Step 3: Implement**

Replace the full content of `web/src/stores/jobs.js`:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'
import { createLiveStream } from '../utils/wsClient'
import { parseLogLine } from '../utils/logLine'

const OVERLAP_MARGIN_SEC = 2
const RECONCILE_INTERVAL_MS = 60000

function logKey(line) {
  return `${line.timestamp}|${line.hostname}|${line.binary}`
}

function isFinishLine(line) {
  return parseLogLine(line.line).fields.event === 'finish'
}

export const useJobsStore = defineStore('jobs', {
  state: () => ({
    list: [],
    loading: false,
    error: null,
    logs: [],
    logsLoading: false,
    logsError: null,
    logsStatus: 'connecting',
    _logsStream: null,
    _logsSeen: new Set(),
    _logsReconcileTimer: null,
  }),
  actions: {
    async fetchAll() {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch('/jobs')
          this.list = body.data
        },
        { rethrow: false }
      )
    },

    async fetchLogs(jobId) {
      await withRequest(
        this,
        async () => {
          const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs`)
          this.logs = body.data ?? []
          this._logsSeen = new Set(this.logs.map(logKey))
        },
        { rethrow: false, loadingKey: 'logsLoading', errorKey: 'logsError' }
      )
    },

    _mergeLogLine(line) {
      const key = logKey(line)
      if (this._logsSeen.has(key)) return
      this._logsSeen.add(key)
      this.logs.push(line)
      this.logs.sort((a, b) => a.timestamp - b.timestamp)
      if (isFinishLine(line)) {
        this.logsStatus = 'finished'
        this.disconnectLogsStream()
      }
    },

    async connectLogsStream(jobId) {
      await this.fetchLogs(jobId)
      const startSec = Math.floor(Date.now() / 1000) - OVERLAP_MARGIN_SEC
      this._logsStream = createLiveStream(`/jobs/${encodeURIComponent(jobId)}/logs/stream?start=${startSec}`, {
        onMessage: (line) => this._mergeLogLine(line),
        onStatus: (status) => {
          if (this.logsStatus !== 'finished') this.logsStatus = status
        },
        onFallback: (intervalMs) => {
          if (this._logsReconcileTimer) clearInterval(this._logsReconcileTimer)
          this._logsReconcileTimer = setInterval(() => this._reconcileLogs(jobId), intervalMs)
        },
      })
      this._logsReconcileTimer = setInterval(() => this._reconcileLogs(jobId), RECONCILE_INTERVAL_MS)
    },

    async _reconcileLogs(jobId) {
      const body = await apiFetch(`/jobs/${encodeURIComponent(jobId)}/logs`)
      ;(body.data ?? []).forEach((line) => this._mergeLogLine(line))
    },

    disconnectLogsStream() {
      if (this._logsStream) {
        this._logsStream.close()
        this._logsStream = null
      }
      if (this._logsReconcileTimer) {
        clearInterval(this._logsReconcileTimer)
        this._logsReconcileTimer = null
      }
    },
  },
})
```

In `web/src/views/JobDetailView.vue`, replace the full content:

```vue
<script setup>
import { onMounted, onUnmounted, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useJobsStore } from '../stores/jobs'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import ConnectionStatus from '../components/ui/ConnectionStatus.vue'
import LogLine from '../components/LogLine.vue'

const route = useRoute()
const jobs = useJobsStore()
const jobId = computed(() => route.params.job_id)

onMounted(async () => {
  await jobs.connectLogsStream(jobId.value)
})

onUnmounted(() => {
  jobs.disconnectLogsStream()
})
</script>

<template>
  <div>
    <PageHeader :title="jobId" :crumbs="[{ label: 'Jobs', to: { name: 'jobs' } }, { label: jobId }]">
      <template #actions>
        <ConnectionStatus :status="jobs.logsStatus" />
      </template>
    </PageHeader>
    <StatusMessage
      :loading="jobs.logsLoading"
      :error="jobs.logsError"
      :empty="jobs.logs.length === 0"
      empty-text="No log lines found for this job in the last 24h."
    >
      <ul>
        <LogLine v-for="(line, index) in jobs.logs" :key="index" :line="line" />
      </ul>
    </StatusMessage>
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/stores/jobs.spec.js src/views/JobDetailView.spec.js`
Expected: PASS. If `JobDetailView.spec.js` asserts on `jobs.fetchLogs` being called directly, update
that assertion to `jobs.connectLogsStream` instead (same call site, new store action name) — inspect
the existing spec first and adjust minimally, keeping its other assertions intact.

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/jobs.js web/src/stores/jobs.spec.js web/src/views/JobDetailView.vue web/src/views/JobDetailView.spec.js
git commit -m "$(cat <<'EOF'
feat(web): live-update /jobs/:job_id via WS, REST as backstop

connectLogsStream fetches history first (unchanged REST call), then
opens a live tail from a 2s-overlap cursor, deduping incoming lines by
(timestamp, hostname, binary) against what's already rendered. A 60s
periodic re-fetch reconciles against the same endpoint regardless of
stream health. Observing this job's event=finish line flips the
connection-status indicator to "Finished" and closes the stream --
matches the "stop polling once a job completes" decision from the
design spec.
EOF
)"
```

---

### Task 14: `web` — `jobs.js` store: live jobs-list stream + view wiring

**Files:**
- Modify: `web/src/stores/jobs.js`
- Modify: `web/src/stores/jobs.spec.js`
- Modify: `web/src/views/JobsListView.vue`
- Modify: `web/src/views/JobsListView.spec.js` (adjust only if it asserts on mount behavior)

**Interfaces:**
- Consumes: Task 11's `createLiveStream`; Task 13's store shape.
- Produces: `jobsStore.listStatus`, `jobsStore.connectJobsStream()`,
  `jobsStore.disconnectJobsStream()`.

- [ ] **Step 1: Write the failing tests**

Add to `web/src/stores/jobs.spec.js`:

```js
describe('connectJobsStream', () => {
  let liveStreamHandlers

  beforeEach(() => {
    createLiveStream.mockReset()
    createLiveStream.mockImplementation((path, handlers) => {
      liveStreamHandlers = handlers
      return { close: vi.fn() }
    })
  })

  it('opens a stream at /jobs/stream', () => {
    const jobs = useJobsStore()
    jobs.connectJobsStream()
    expect(createLiveStream).toHaveBeenCalledWith(
      '/jobs/stream',
      expect.objectContaining({ onMessage: expect.any(Function), onStatus: expect.any(Function), onFallback: expect.any(Function) })
    )
  })

  it('a "snapshot" message replaces the whole list', () => {
    const jobs = useJobsStore()
    jobs.connectJobsStream()
    liveStreamHandlers.onMessage({ type: 'snapshot', jobs: [{ job_id: 'a' }, { job_id: 'b' }] })
    expect(jobs.list).toEqual([{ job_id: 'a' }, { job_id: 'b' }])
  })

  it('an "upsert" message updates an existing job in place', () => {
    const jobs = useJobsStore()
    jobs.list = [{ job_id: 'a', state: 'in_progress' }]
    jobs.connectJobsStream()
    liveStreamHandlers.onMessage({ type: 'upsert', job: { job_id: 'a', state: 'success' } })
    expect(jobs.list).toEqual([{ job_id: 'a', state: 'success' }])
  })

  it('an "upsert" message for an unseen job_id appends it', () => {
    const jobs = useJobsStore()
    jobs.list = [{ job_id: 'a' }]
    jobs.connectJobsStream()
    liveStreamHandlers.onMessage({ type: 'upsert', job: { job_id: 'b' } })
    expect(jobs.list).toEqual([{ job_id: 'a' }, { job_id: 'b' }])
  })

  it('disconnectJobsStream closes the stream and clears the reconciliation timer', () => {
    const jobs = useJobsStore()
    jobs.connectJobsStream()
    const closeSpy = createLiveStream.mock.results[0].value.close

    jobs.disconnectJobsStream()

    expect(closeSpy).toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/stores/jobs.spec.js`
Expected: FAIL — `jobs.connectJobsStream is not a function`.

- [ ] **Step 3: Implement**

In `web/src/stores/jobs.js`, add two more state fields to the `state()` object:

```js
    listStatus: 'connecting',
    _listStream: null,
    _listReconcileTimer: null,
```

and add these actions (alongside `fetchAll`, etc.):

```js
    _mergeJobsSnapshot(jobs) {
      this.list = jobs
    },

    _mergeJobUpsert(job) {
      const idx = this.list.findIndex((j) => j.job_id === job.job_id)
      if (idx === -1) this.list.push(job)
      else this.list[idx] = job
    },

    connectJobsStream() {
      this._listStream = createLiveStream('/jobs/stream', {
        onMessage: (msg) => {
          if (msg.type === 'snapshot') this._mergeJobsSnapshot(msg.jobs ?? [])
          else if (msg.type === 'upsert' && msg.job) this._mergeJobUpsert(msg.job)
        },
        onStatus: (status) => {
          this.listStatus = status
        },
        onFallback: (intervalMs) => {
          if (this._listReconcileTimer) clearInterval(this._listReconcileTimer)
          this._listReconcileTimer = setInterval(() => this.fetchAll(), intervalMs)
        },
      })
      this._listReconcileTimer = setInterval(() => this.fetchAll(), RECONCILE_INTERVAL_MS)
    },

    disconnectJobsStream() {
      if (this._listStream) {
        this._listStream.close()
        this._listStream = null
      }
      if (this._listReconcileTimer) {
        clearInterval(this._listReconcileTimer)
        this._listReconcileTimer = null
      }
    },
```

Replace the full content of `web/src/views/JobsListView.vue`:

```vue
<script setup>
import { onMounted, onUnmounted } from 'vue'
import { useJobsStore } from '../stores/jobs'
import { formatTimestamp } from '../utils/format'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import Badge from '../components/ui/Badge.vue'
import ConnectionStatus from '../components/ui/ConnectionStatus.vue'

const jobs = useJobsStore()

onMounted(() => {
  jobs.connectJobsStream()
})

onUnmounted(() => {
  jobs.disconnectJobsStream()
})

function stateVariant(state) {
  if (state === 'success') return 'ok'
  if (state === 'failure') return 'bad'
  return 'neutral'
}

const columns = [
  { label: 'Job ID', field: 'job_id', sortable: true },
  { label: 'Kind', field: 'kind', sortable: true },
  { label: 'Source Host', field: 'source_host', sortable: true },
  { label: 'Store Host', field: 'store_host', sortable: true, formatFn: (v) => v || '—' },
  { label: 'Started At', field: 'started_at', sortable: true, type: 'number', formatFn: (v) => formatTimestamp(v) || '—' },
  { label: 'Finished At', field: 'finished_at', sortable: true, type: 'number', formatFn: (v) => formatTimestamp(v) || '—' },
  { label: 'State', field: 'state', sortable: true },
]
</script>

<template>
  <div>
    <PageHeader title="Jobs" :crumbs="[{ label: 'Jobs' }]">
      <template #actions>
        <ConnectionStatus :status="jobs.listStatus" />
      </template>
    </PageHeader>
    <StatusMessage
      :loading="jobs.loading"
      :error="jobs.error"
      :empty="jobs.list.length === 0"
      empty-text="No jobs in the last 24h."
    >
      <DataTable :columns="columns" :rows="jobs.list">
        <template #table-row="{ column, row, formattedRow }">
          <router-link
            v-if="column.field === 'job_id'"
            :to="{ name: 'job-detail', params: { job_id: row.job_id } }"
            class="text-blue-600 hover:underline"
          >
            {{ row.job_id }}
          </router-link>
          <Badge v-else-if="column.field === 'state'" :variant="stateVariant(row.state)">
            {{ row.state }}
          </Badge>
          <span v-else>{{ formattedRow[column.field] }}</span>
        </template>
      </DataTable>
    </StatusMessage>
  </div>
</template>
```

Note `jobs.loading`/`jobs.error` (from `fetchAll`, still called by the reconciliation timer and by
the fallback-to-polling path) still drive `StatusMessage`'s loading/error display — the initial
mount no longer calls `fetchAll` directly, but `connectJobsStream`'s live `snapshot` message
populates `jobs.list` immediately once the WS opens, and the reconciliation timer's `fetchAll`
calls continue to exercise the same loading/error state as before.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/stores/jobs.spec.js src/views/JobsListView.spec.js`
Expected: PASS. If `JobsListView.spec.js` asserts `jobs.fetchAll` was called on mount, update that
assertion to `jobs.connectJobsStream` instead, keeping the rest of the spec intact.

- [ ] **Step 5: Full frontend test suite**

Run: `cd web && npx vitest run`
Expected: PASS across the whole suite — confirms nothing else in `web` broke.

- [ ] **Step 6: Commit**

```bash
git add web/src/stores/jobs.js web/src/stores/jobs.spec.js web/src/views/JobsListView.vue web/src/views/JobsListView.spec.js
git commit -m "$(cat <<'EOF'
feat(web): live-update /jobs via WS, REST as backstop

connectJobsStream opens the fleet-wide WS stream, merging "snapshot"
(full replace) and "upsert" (by job_id) messages into the list, with a
60s periodic fetchAll() reconciling regardless of stream health. Same
ConnectionStatus indicator pattern as the job-detail page (previous
task). This is the last piece of the live-update feature itself --
remaining tasks are infra config, docs, and e2e coverage.
EOF
)"
```

---

### Task 15: Infra — nginx and Vite dev-server WebSocket proxying

**Files:**
- Modify: `web/nginx.conf`
- Modify: `web/vite.config.js`

**Interfaces:**
- Consumes: nothing.
- Produces: no code interface — this task makes Tasks 13-14's WS connections actually reach
  `api-server` in both the Docker demo (`web/nginx.conf`) and local dev (`vite.config.js`), neither
  of which is exercised by the Vitest unit suite.

- [ ] **Step 1: Update `nginx.conf`**

Replace the full content of `web/nginx.conf`:

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 80;
    root /usr/share/nginx/html;
    index index.html;

    location /api/ {
        proxy_pass http://api-server:8090/api/;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host $host;
        # A tail with no new lines for a while would otherwise be killed by
        # nginx's default 60s proxy_read_timeout, well inside how long a
        # quiet job or an idle /jobs list page is expected to sit open.
        proxy_read_timeout 3600s;
    }

    location / {
        try_files $uri $uri/ /index.html;
    }
}
```

(The `map` block must sit outside the `server {}` block, at the file's top level — this file is
included inside nginx's `http {}` context via `/etc/nginx/conf.d/*.conf` in the base `nginx:1.27-alpine`
image, per `web/Dockerfile`, so a top-level `map` here is valid.)

- [ ] **Step 2: Update `vite.config.js`**

Replace the `server.proxy` block in `web/vite.config.js`:

```js
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8090', ws: true },
    },
  },
```

- [ ] **Step 3: Manual verification against the demo lab**

Run: `make demo-up` (repo root, waits for every service to be healthy).

From another shell, confirm the WS upgrade actually reaches `api-server` through nginx (not just
directly): open `http://localhost:8091/jobs` in a browser, open the browser's Network tab, and
confirm a `wss://localhost:8091/api/v1/jobs/stream?ticket=...` (or `ws://` if not using TLS
locally) entry shows status `101 Switching Protocols`, not a failed/red request. Then open any job's
detail page and confirm the same for `.../logs/stream`.

Run: `make demo-down` when done.

- [ ] **Step 4: Commit**

```bash
git add web/nginx.conf web/vite.config.js
git commit -m "$(cat <<'EOF'
fix(web): proxy WebSocket upgrades through nginx and the Vite dev server

Both /api/v1/jobs/stream and /api/v1/jobs/{id}/logs/stream (Tasks
10/6) need the Upgrade/Connection headers forwarded and a long enough
read timeout to survive a quiet job or an idle jobs-list tab -- nginx
doesn't do this by default. Vite's dev proxy needs `ws: true` for the
same reason during local development.
EOF
)"
```

---

### Task 16: Documentation

Per this repo's `.claude/CLAUDE.md` rules: any feature change updates the affected
`docs/components/*.md` files and `README.md`/`docs/ARCHITECTURE.md` if topology/data-flow changed,
and any feature branch gets a `CHANGELOG.md` entry before merging to `main`. This plan also touches
`log-gateway`'s protocol surface (a new route), which `docs/protocols/log-gateway.md` covers per the
same file's existing convention.

**Files:**
- Modify: `docs/protocols/log-gateway.md`
- Modify: `docs/components/log-gateway.md`
- Modify: `docs/components/api-server.md`
- Modify: `docs/components/web.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing — reads back the endpoints/routes built in Tasks 2-14 to describe accurately.
- Produces: nothing code-facing; this is the plan's documentation-impact closure, per spec
  "Documentation Impact".

- [ ] **Step 1: `docs/protocols/log-gateway.md`**

Read the existing file first, then add a new section (matching its existing style for the push/
query_range routes) documenting `GET /loki/api/v1/tail`: request (WS upgrade, query params `query`/
`start`/`delay_for`/`limit` forwarded unmodified to Loki), auth (same operating-tier mTLS check as
the other two routes), and response (WS frames relayed byte-for-byte from Loki's own tail wire
format — reference `lokiTailMessage`'s shape from `src/cmd/api-server/loki_tail.go`, Task 5).

- [ ] **Step 2: `docs/components/log-gateway.md`**

Add a sentence to the `## Behavior` section (after the existing `query_range` paragraph) describing
the new tail route, and add a cross-link to
`docs/superpowers/specs/2026-08-17-live-job-updates-design.md` in `## See Also`.

- [ ] **Step 3: `docs/components/api-server.md`**

Read the existing file first (it should already document `GET /api/v1/jobs` and `GET
/api/v1/jobs/{job_id}/logs` as the one documented exception to the one-RPC-per-call rule, per the
`2026-07-19-jobs-endpoint-design.md` spec). Add:
- `POST /api/v1/ws-tickets` — bearer-authenticated, issues a 30s single-use ticket for the two WS
  routes below.
- `GET /api/v1/jobs/{job_id}/logs/stream` — ticket-gated WS, live-tails one job's log lines.
- `GET /api/v1/jobs/stream` — ticket-gated WS, live job-state updates from the shared in-memory
  aggregator; note this as a *second* documented exception to the one-RPC-per-call rule, since the
  aggregator holds state across calls rather than translating one REST call to one backend call.

Add a cross-link to `docs/superpowers/specs/2026-08-17-live-job-updates-design.md` in `## See Also`.

- [ ] **Step 4: `docs/components/web.md`**

Update the `/jobs` and `/jobs/:job_id` bullets (currently ending in "...client-side search, sort,
and pagination..." and "...fetched once on page load (no live-tail/polling)..." respectively) to
describe: both pages now connect over WebSocket for live updates, with a `ConnectionStatus`
indicator (`live`/`reconnecting`/`polling`/`finished`) and a 60s periodic REST reconciliation
running underneath regardless of stream health; after 5 failed reconnects a page falls back to
plain 10s REST polling. Remove the now-inaccurate "no live-tail/polling" clause. Add a cross-link to
`docs/superpowers/specs/2026-08-17-live-job-updates-design.md` in `## See Also`.

- [ ] **Step 5: `docs/api/rest-v1.md`**

Read the existing file first (find the `GET /api/v1/jobs` / `GET /api/v1/jobs/{job_id}/logs`
sections for the established format). Add sections for `POST /api/v1/ws-tickets` (request: none;
response: `{"ticket": "<hex string>"}`), and, in whatever style this doc uses for non-REST routes (or
a short new "WebSocket Endpoints" subsection if it has none), document `GET
/api/v1/jobs/stream` and `GET /api/v1/jobs/{job_id}/logs/stream`'s message shapes (`jobsStreamMsg`
and `logLineDTO` respectively, per Tasks 8 and 6).

- [ ] **Step 6: `CHANGELOG.md`**

Read the existing file's most-recent entries first to match its exact heading/date format, then add
a new dated entry at the top:

```markdown
## 2026-08-17 — Live job & log updates

`/jobs` and `/jobs/:job_id` in the web UI now update live instead of only on page load: a
WebSocket, proxied through `log-gateway` to Loki's native tail endpoint, pushes new log lines and
job-state transitions as they happen, with the existing REST endpoints kept as both the initial
fetch and a periodic correctness backstop. A connection-status indicator always shows whether a page
is live, reconnecting, or has fallen back to plain polling, so a stalled page never looks
up to date. See `docs/superpowers/specs/2026-08-17-live-job-updates-design.md`.
```

(Adjust wording/format only as needed to match the file's actual established convention once read.)

- [ ] **Step 7: Commit**

```bash
git add docs/protocols/log-gateway.md docs/components/log-gateway.md docs/components/api-server.md docs/components/web.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: document live job & log updates (WS tail proxy, tickets, aggregator)

Updates log-gateway's protocol doc and component doc for the new tail
route, api-server's component doc for the three new endpoints (ticket
issuance, per-job stream, fleet-wide stream), web's component doc to
replace the now-inaccurate "no live-tail/polling" description, the
REST API reference, and CHANGELOG.md, per this repo's
.claude/CLAUDE.md documentation rules.
EOF
)"
```

---

### Task 17: End-to-end coverage

**Files:**
- Create: `web/e2e/live-job-updates.spec.js`

**Interfaces:**
- Consumes: the full stack built by Tasks 1-15, running via `make demo-up`; the existing e2e
  fixture/seeding pattern from `web/e2e/restore-verify.spec.js` (an ad-hoc backup policy created
  through the real `/policies` form, per `docs/superpowers/specs/2026-08-09-restore-cart-e2e-design.md`'s
  "seeding is itself UI-driven" convention).
- Produces: one Playwright spec proving the feature end to end against the real stack — no fakes,
  no mocks.

- [ ] **Step 1: Read the existing pattern**

Read `web/e2e/restore-verify.spec.js` in full first, to match its exact fixture-creation flow (how
it drives the `/policies` form to create and run a fast ad-hoc policy, and how it waits for and
opens the resulting job's detail page) before writing this new spec — do not invent a different
flow.

- [ ] **Step 2: Write the test**

Create `web/e2e/live-job-updates.spec.js`, following the structure just read, with this scenario:

```js
import { test, expect } from '@playwright/test'
// Import whatever fixture/seeding helper restore-verify.spec.js uses (e.g.
// a shared `createAdhocBackupPolicy(page)` helper, or inline steps if that
// spec doesn't factor one out -- match exactly what Step 1 found).

test('job detail page flips to Finished live, with no manual reload', async ({ page }) => {
  await page.goto('/policies')
  // ... drive the same "New backup" -> fill form -> "Run now" flow
  // restore-verify.spec.js uses, ending on the resulting job's /jobs/:job_id
  // page. Use a policy targeting a tiny, fast object filter so the job
  // finishes in well under this test's timeout.

  // At this point the page has just navigated to /jobs/:job_id for a job
  // that may still be in_progress -- the whole point of this test is that
  // we do NOT reload and DO NOT poll manually from here on.
  await expect(page.getByTestId('connection-status')).toHaveText(/Live|Connecting/)

  // Wait for the connection-status badge to flip to Finished purely from
  // the WS push -- Playwright's built-in auto-retrying expect() polls the
  // DOM without any reload, which is exactly what a real user would see.
  await expect(page.getByTestId('connection-status')).toHaveText('Finished', { timeout: 30000 })

  // The finish log line itself must be visible in the rendered list too,
  // not just the status badge.
  await expect(page.locator('[data-test="log-line-summary"]', { hasText: /completed/i })).toBeVisible()
})

test('jobs list page shows a new job appear and transition to success live', async ({ page }) => {
  await page.goto('/jobs')
  await expect(page.getByTestId('connection-status')).toHaveText(/Live|Connecting/)

  const rowCountBefore = await page.locator('table tbody tr').count()

  // Open a second tab/context to trigger a job while the first stays on
  // /jobs -- or, if restore-verify.spec.js's helper takes a page/context
  // param, reuse it here the same way. The key assertion is what happens
  // on the ALREADY-OPEN /jobs page below, not how the job gets triggered.
  const policyPage = await page.context().newPage()
  await policyPage.goto('/policies')
  // ... same "Run now" flow as above, on policyPage
  await policyPage.close()

  await expect(page.locator('table tbody tr')).toHaveCount(rowCountBefore + 1, { timeout: 15000 })
  await expect(page.locator('table tbody tr').first().locator('.badge, [class*="bg-green"]')).toBeVisible({ timeout: 30000 })
})
```

Adjust selectors (`getByTestId('connection-status')` matches `ConnectionStatus.vue`'s
`data-test="connection-status"` from Task 12; the job-row/state-badge selector should match
whatever `Badge`/`DataTable` actually render — check those components' existing `data-test`
attributes, used elsewhere in this suite, rather than guessing a CSS class selector) once Step 1's
read confirms this suite's real conventions.

- [ ] **Step 3: Run it against the real demo lab**

```bash
make demo-up
cd web && npx playwright test live-job-updates.spec.js
```

Expected: both tests PASS. If a test times out waiting for `Finished`/the new row, check (in order):
`make demo-up`'s services are all healthy; `web/nginx.conf`'s WS proxy (Task 15) is actually in the
built image (`docker compose build web` if testing a stale image); and the aggregator/tail path
itself (Tasks 2-10) via the manual smoke check described in Task 10 Step 7 and Task 15 Step 3.

Run: `make demo-down` when done.

- [ ] **Step 4: Commit**

```bash
git add web/e2e/live-job-updates.spec.js
git commit -m "$(cat <<'EOF'
test(e2e): verify live job & log updates via the real UI, no reload

Runs an ad-hoc backup policy through the real /policies form (same
UI-driven seeding convention as restore-verify.spec.js) and asserts
both /jobs/:job_id and /jobs flip to their finished/updated state
purely from the WebSocket push -- no manual reload, no polling from
the test itself. This is the scenario the whole feature exists for.
EOF
)"
```

---

## Self-Review Notes

- **Spec coverage:** every architecture piece from
  `docs/superpowers/specs/2026-08-17-live-job-updates-design.md` maps to a task —
  `log-gateway` tail proxy (Task 2), WS ticket auth (Task 4), per-job tail (Task 6), shared pairing
  logic (Task 7), aggregator state/ingest (Task 8), aggregator reconcile/backoff (Task 9), jobs-list
  WS endpoint + wiring (Task 10), frontend WS client with reconnect/fallback (Task 11), connection
  status UI (Task 12), both pages' store wiring with dedup/overlap/stop-on-finish/reconciliation
  (Tasks 13-14), infra WS proxying (Task 15), documentation impact (Task 16), and the real e2e
  scenario (Task 17).
- **Type/name consistency checked across tasks:** `lokiTailMessage`/`lokiTailer` (Task 5) used
  identically in Tasks 6, 8, 9; `jobEventAccumulator`/`newJobEventAccumulatorSeeded` (Task 7) used
  identically in Task 8; `jobsStreamMsg{Type, Jobs, Job}` (Task 8) used identically in Tasks 10 and
  14's frontend merge logic (`msg.type`/`msg.jobs`/`msg.job`); `server.wsTickets`/`server.lokiTail`/
  `server.aggregator` fields introduced exactly once each (Tasks 4/6/10) and referenced, never
  redeclared, thereafter; `createLiveStream(path, { onMessage, onStatus, onFallback })`'s signature
  (Task 11) matches its call sites in Tasks 13 and 14 exactly.
- **No placeholders:** every step above contains real code, exact file paths, and runnable commands
  — the two spots that say "match the existing convention" (Task 12 Step 1's component-test import
  style, Task 17's fixture-creation flow) are deliberate, since they depend on reading a sibling
  file this plan doesn't reproduce wholesale, not unresolved design decisions.
