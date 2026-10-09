# Logging Flow Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix three independent reliability/performance issues found by auditing the client-push and
server-delivery logging pipeline: Vector's buffer drops the wrong data under backpressure,
`jobAggregator` polls Loki with nobody watching, and `log-gateway` fully buffers push bodies in
memory before forwarding them.

**Architecture:** Three surgical, independent changes to existing files — no new components, no
schema change, no new configuration. Each task is self-contained and independently testable.

**Tech Stack:** Go 1.x, `log/slog`, `net/http`, `gopkg.in/natefinch/lumberjack.v2`, Vector (YAML
config, not modified as a binary — only the generated config template changes), `testify`
(`assert`/`require`).

## Global Constraints

- Test command for a single package: `cd src && go test ./cmd/<pkg>/... -run <TestName> -v`. Full
  suite: `cd src && go test ./...` (per `Makefile`'s `test` target).
- Every commit message follows this repo's existing convention: `type(scope): summary` (see `git log`
  for examples), no `Co-Authored-By`/session trailers unless the harness adds them automatically.
- Per `.claude/CLAUDE.md`'s Changelog rule: a `CHANGELOG.md` entry (dated heading, prose paragraph) is
  added before this work is considered mergeable — Task 4, below, after the other three land.
- Spec: `docs/superpowers/specs/2026-08-22-logging-flow-hardening-design.md`. Read it before starting
  if anything here seems to assume context — every design decision and its rationale lives there.
- Run all commands from the repository root (the worktree root) — no `cd` to any other checkout.

---

### Task 1: Vector buffer — stop dropping the freshest logs

**Files:**
- Modify: `src/cmd/agent/vector.go:90-93` (the `buffer:` block inside `vectorConfigTemplate`)
- Modify: `src/cmd/agent/vector_test.go` (no test currently asserts on `when_full` — add one)
- Modify: `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md` (correct the stale
  "oldest entries are dropped" claim)

**Interfaces:**
- Consumes: nothing new — `renderVectorConfig(logDir, varDir, certsDir, logGatewayHost string,
  logGatewayPort int, hostname string) (string, error)` already exists and is unchanged in signature.
- Produces: nothing new for later tasks — this task is self-contained.

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/agent/vector_test.go`, near the other `TestRenderVectorConfig_*` tests:

```go
func TestRenderVectorConfig_BufferBlocksInsteadOfDroppingFreshLogs(t *testing.T) {
	// Vector's disk buffer only supports "block" or "drop_newest" -- there
	// is no "drop oldest" mode. drop_newest would discard the freshest,
	// most operationally relevant lines once the buffer fills during an
	// outage; block instead pauses the file source until the buffer
	// drains, so nothing is lost (see docs/superpowers/specs/
	// 2026-08-22-logging-flow-hardening-design.md).
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "when_full: block")
	assert.NotContains(t, got, "when_full: drop_newest")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./cmd/agent/... -run TestRenderVectorConfig_BufferBlocksInsteadOfDroppingFreshLogs -v`
Expected: FAIL — the rendered config still contains `when_full: drop_newest`.

- [ ] **Step 3: Change the template**

In `src/cmd/agent/vector.go`, inside `vectorConfigTemplate`'s `sinks.loki_gateway.buffer` block,
change:

```yaml
    buffer:
      type: disk
      max_size: 268435488
      when_full: drop_newest
```

to:

```yaml
    buffer:
      type: disk
      max_size: 268435488
      when_full: block
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./cmd/agent/... -run TestRenderVectorConfig_BufferBlocksInsteadOfDroppingFreshLogs -v`
Expected: PASS. Also run the full package to confirm nothing else broke:
`cd src && go test ./cmd/agent/... -v`
Expected: all tests PASS.

- [ ] **Step 5: Correct the design doc's stale claim**

In `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`:

- In the Architecture section's Vector bullet (the paragraph ending "...once its configured bound is
  exceeded, oldest entries are dropped rather than blocking or growing without limit."), replace that
  sentence with: "...once its configured bound is exceeded, the file source pauses (Vector's disk
  buffer only supports `block` or `drop_newest` — there is no 'drop oldest' mode) rather than
  discarding newly-arriving lines; see `docs/superpowers/specs/
  2026-08-22-logging-flow-hardening-design.md` for why `block` was chosen over the originally-picked
  `drop_newest`."
- In the Data Flow diagram's Vector block, replace "buffered to its own disk buffer at
  `<var_dir>/vector-buffer` if log-gateway/Loki is unreachable; oldest entries drop only once the
  buffer's configured bound is exceeded" with "buffered to its own disk buffer at
  `<var_dir>/vector-buffer` if log-gateway/Loki is unreachable; the file source pauses (not drops)
  once the buffer's configured bound is exceeded, resuming once space frees up."

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/vector.go src/cmd/agent/vector_test.go docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md
git commit -m "fix(agent): stop Vector's buffer from dropping the freshest logs

when_full: drop_newest discarded new lines once the 256MB disk buffer
filled during a log-gateway/Loki outage -- the opposite of what an
operator debugging a live incident needs. Vector's disk buffer has no
'drop oldest' mode, so when_full: block is the only way to actually
avoid losing data: the file source pauses instead, resuming once the
buffer drains."
```

---

### Task 2: `jobAggregator` — no periodic reconcile with zero subscribers

**Files:**
- Modify: `src/cmd/api-server/jobs_aggregator.go`
- Modify: `src/cmd/api-server/jobs_aggregator_test.go`
- Modify: `src/cmd/api-server/jobs_stream_list.go:21` (update the one production call site)

**Interfaces:**
- Consumes: `jobAggregator.reconcile(ctx context.Context) error` (existing, unchanged),
  `jobAggregator.broadcast(msg jobsStreamMsg)` (existing, unchanged), `jobAggregator.mu`/`a.subs`
  (existing fields, unchanged types).
- Produces:
  - `func (a *jobAggregator) subscriberCount() int` — new, returns `len(a.subs)` under `a.mu`.
  - `func (a *jobAggregator) reconcileIfSubscribed(ctx context.Context) error` — new; returns `nil`
    immediately if `subscriberCount() == 0`, else delegates to `a.reconcile(ctx)`.
  - `func (a *jobAggregator) Subscribe(ctx context.Context) (snapshot []jobDTO, ch chan
    jobsStreamMsg, unsubscribe func())` — **signature changed**, now takes a `context.Context` as its
    first argument. Every other caller in this codebase (`handleJobsStream` in
    `jobs_stream_list.go`, and every test in `jobs_aggregator_test.go`) must be updated to pass one.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/jobs_aggregator_test.go` (needs `"sync/atomic"` — already imported):

```go
// countingLokiClient wraps a fakeLokiClient and counts QueryRange calls --
// lets these tests assert whether Loki was queried at all, not just what
// it returned.
type countingLokiClient struct {
	fakeLokiClient
	calls atomic.Int32
}

func (c *countingLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	c.calls.Add(1)
	return c.fakeLokiClient.QueryRange(ctx, query, start, end, limit)
}

func TestJobAggregator_ReconcileIfSubscribedSkipsWhenNoSubscribers(t *testing.T) {
	counting := &countingLokiClient{fakeLokiClient: fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}}
	agg := newJobAggregator(counting, &fakeLokiTailer{}, testLogger())

	require.NoError(t, agg.reconcileIfSubscribed(context.Background()))

	assert.EqualValues(t, 0, counting.calls.Load(), "must not query Loki when no subscriber is connected")
}

func TestJobAggregator_ReconcileIfSubscribedRunsWithSubscriber(t *testing.T) {
	counting := &countingLokiClient{fakeLokiClient: fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}}
	agg := newJobAggregator(counting, &fakeLokiTailer{}, testLogger())
	_, _, unsubscribe := agg.Subscribe(context.Background())
	defer unsubscribe()
	counting.calls.Store(0) // reset: Subscribe's own first-subscriber reconcile already ran once

	require.NoError(t, agg.reconcileIfSubscribed(context.Background()))

	assert.EqualValues(t, 1, counting.calls.Load(), "must query Loki when at least one subscriber is connected")
}

func TestJobAggregator_SubscribeTriggersReconcileOnFirstSubscriber(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "start"},
				Values: []lokiValue{{Timestamp: 1752400500000000000}}},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}
	agg := newJobAggregator(fake, &fakeLokiTailer{}, testLogger())

	snapshot, _, unsubscribe := agg.Subscribe(context.Background())
	defer unsubscribe()

	require.Len(t, snapshot, 1, "the first subscriber must see freshly reconciled state")
	assert.Equal(t, "operating-refresh:1", snapshot[0].JobID)
}

func TestJobAggregator_SecondSubscriberDoesNotTriggerExtraReconcile(t *testing.T) {
	counting := &countingLokiClient{fakeLokiClient: fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}}
	agg := newJobAggregator(counting, &fakeLokiTailer{}, testLogger())

	_, _, unsubscribe1 := agg.Subscribe(context.Background())
	defer unsubscribe1()
	require.EqualValues(t, 1, counting.calls.Load())

	_, _, unsubscribe2 := agg.Subscribe(context.Background())
	defer unsubscribe2()

	assert.EqualValues(t, 1, counting.calls.Load(), "a second concurrent subscriber must not trigger another reconcile")
}
```

Now update every *existing* call to `agg.Subscribe()` in the same file to `agg.Subscribe(context.Background())`:
- `TestJobAggregator_IngestTailMessageUpsertsAndBroadcasts` (line ~27)
- `TestJobAggregator_IngestTailMessageAppliesFinishOnTopOfStart` (line ~54)
- `TestJobAggregator_SlowSubscriberDoesNotBlockBroadcast` (line ~80)
- `TestJobAggregator_ReconcileBroadcastsSnapshot` (line ~129)

And rewrite `TestJobAggregator_SubscribeReturnsCurrentSnapshot` (line ~14), whose old premise — that
`Subscribe` returns whatever was already sitting in `agg.jobs` untouched — is exactly the behavior
this task changes (the first subscriber now always gets a freshly-reconciled snapshot, not stale
manually-seeded state):

```go
func TestJobAggregator_SubscribeReturnsCurrentSnapshot(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "a", "event": "start"},
				Values: []lokiValue{{Timestamp: 1752400500000000000}}},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "a", "event": "finish", "status": "success"},
				Values: []lokiValue{{Timestamp: 1752400501000000000}}},
		},
	}}
	agg := newJobAggregator(fake, &fakeLokiTailer{}, testLogger())

	snapshot, _, unsubscribe := agg.Subscribe(context.Background())
	defer unsubscribe()

	require.Len(t, snapshot, 1)
	assert.Equal(t, "a", snapshot[0].JobID)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestJobAggregator -v`
Expected: FAIL to compile — `reconcileIfSubscribed`, `countingLokiClient` undefined, and
`agg.Subscribe()` call-count mismatches (`Subscribe` still takes zero arguments).

- [ ] **Step 3: Implement `subscriberCount`, `reconcileIfSubscribed`, and the new `Subscribe` signature**

In `src/cmd/api-server/jobs_aggregator.go`, add below `broadcast`:

```go
// subscriberCount returns how many browsers are currently subscribed --
// used to decide whether a periodic reconcile is worth running at all.
func (a *jobAggregator) subscriberCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.subs)
}

// reconcileIfSubscribed runs reconcile only when at least one browser is
// currently subscribed -- the periodic 24h, fleet-wide double query costs
// real Loki load for a value nobody is watching when nothing is.
func (a *jobAggregator) reconcileIfSubscribed(ctx context.Context) error {
	if a.subscriberCount() == 0 {
		return nil
	}
	return a.reconcile(ctx)
}
```

Replace the existing `Subscribe` method with:

```go
// Subscribe registers a new listener and returns the current state as a
// snapshot, alongside the channel future upserts (and future full
// snapshots, from reconcile) will arrive on. Callers must call unsubscribe
// exactly once, typically via defer, when they stop reading.
//
// On the 0->1 subscriber transition, Subscribe runs one synchronous
// reconcile before computing the snapshot -- the periodic reconcileLoop
// ticker skips work while unsubscribed (reconcileIfSubscribed, above), so
// without this the first browser to connect after an idle stretch could
// see a snapshot arbitrarily stale.
func (a *jobAggregator) Subscribe(ctx context.Context) (snapshot []jobDTO, ch chan jobsStreamMsg, unsubscribe func()) {
	if a.subscriberCount() == 0 {
		if err := a.reconcile(ctx); err != nil {
			a.logger.Error("jobAggregator: reconcile on first subscriber failed", "error", err)
		}
	}

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
```

Update `reconcileLoop`'s ticker branch to call the new gated method:

```go
func (a *jobAggregator) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(jobsAggregatorReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.reconcileIfSubscribed(ctx); err != nil {
				a.logger.Error("jobAggregator: periodic reconcile failed", "error", err)
			}
		}
	}
}
```

(`Start`'s one-time startup reconcile and `tailLoop`'s reconcile-before-reattach are both left
exactly as they are — see the design doc's Non-Goals.)

- [ ] **Step 4: Update the production call site**

In `src/cmd/api-server/jobs_stream_list.go`, change:

```go
	snapshot, ch, unsubscribe := s.aggregator.Subscribe()
```

to:

```go
	snapshot, ch, unsubscribe := s.aggregator.Subscribe(r.Context())
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestJobAggregator -v`
Expected: PASS. Also run the full package: `cd src && go test ./cmd/api-server/... -v`
Expected: all tests PASS (confirms `jobs_stream_list.go`'s call-site update didn't break any
`handleJobsStream`-adjacent test).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs_aggregator.go src/cmd/api-server/jobs_aggregator_test.go src/cmd/api-server/jobs_stream_list.go
git commit -m "perf(api-server): skip jobAggregator's periodic reconcile with no subscribers

reconcileLoop ran two full 24h, fleet-wide Loki queries every 60s
forever, even with zero browsers on /api/v1/jobs/stream. Gate the
ticker-driven reconcile on subscriber count, and run one synchronous
reconcile on the 0->1 subscriber transition so the first browser to
connect after an idle stretch still gets a fresh snapshot rather than
stale state."
```

---

### Task 3: `log-gateway` — stream the push body instead of buffering it

**Files:**
- Modify: `src/cmd/log-gateway/server.go` (`ServeHTTP`, plus its import block)
- Modify: `src/cmd/log-gateway/server_test.go`
- Modify: `docs/components/log-gateway.md`
- Modify: `docs/protocols/log-gateway.md`

**Interfaces:**
- Consumes: `s.lokiPushURL string`, `s.httpClient *http.Client`, `mtls.PeerHostnameFromConnState` —
  all existing, unchanged.
- Produces: nothing new for later tasks — this task is self-contained. `ServeHTTP`'s external
  behavior (status codes, forwarded body/headers) is unchanged for a well-formed request; only the
  one edge case in Step 1's second test changes (`413` → `502` for an oversized body with no/lying
  `Content-Length`).

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/log-gateway/server_test.go`:

```go
// contentLengthlessReader hides strings.Reader's Len() method so
// httptest.NewRequest can't infer a Content-Length from it -- simulates a
// push whose declared Content-Length doesn't reflect its actual size
// (absent or understated), the case the fast Content-Length check can't
// catch and MaxBytesReader must still guard mid-stream.
type contentLengthlessReader struct{ io.Reader }

func TestHandlePush_OversizedBodyWithNoContentLengthSurfacesAsBadGateway(t *testing.T) {
	lokiStub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer lokiStub.Close()

	srv := newLogGatewayServer(lokiStub.URL, testLogger())

	oversized := contentLengthlessReader{strings.NewReader(strings.Repeat("a", maxPushBodyBytes+1))}
	req := httptest.NewRequest(http.MethodPost, "/loki/api/v1/push", oversized)
	req.ContentLength = -1 // simulates an inbound request with no declared Content-Length
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{fakePeerCert(t, "node-1")}}
	w := httptest.NewRecorder()

	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Result().StatusCode, "MaxBytesReader tripping mid-stream (no declared Content-Length) surfaces as a failed forward, not a clean 413")
}
```

Also update the existing `TestHandlePush_OversizedBodyRejected` test's comment (its assertions and
behavior are unchanged — `httptest.NewRequest` auto-derives `Content-Length` from a `strings.Reader`,
so it will hit the new fast pre-check — only the *reason* it never reaches Loki changes):

```go
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
```

- [ ] **Step 2: Run tests to verify the new one fails**

Run: `cd src && go test ./cmd/log-gateway/... -run TestHandlePush_OversizedBodyWithNoContentLengthSurfacesAsBadGateway -v`
Expected: FAIL — today's code buffers the whole body via `io.ReadAll` before ever building `lokiReq`,
so `MaxBytesReader` trips *before* any outbound call is attempted, producing `413`, not `502`.

- [ ] **Step 3: Rewrite `ServeHTTP` to stream**

In `src/cmd/log-gateway/server.go`, replace the whole `ServeHTTP` method with:

```go
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

	// Fast, cheap rejection for the common case (Vector's loki sink always
	// sets Content-Length for a single batched POST) -- no read, no dial to
	// Loki. MaxBytesReader below remains the hard safety net for a caller
	// that omits or understates Content-Length.
	if r.ContentLength > maxPushBodyBytes {
		http.Error(w, "request body exceeds size cap", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPushBodyBytes)

	ctx, cancel := context.WithTimeout(r.Context(), lokiForwardTimeout)
	defer cancel()

	// Streams r.Body straight through to Loki instead of buffering the
	// whole request in memory first -- see docs/superpowers/specs/
	// 2026-08-22-logging-flow-hardening-design.md. If MaxBytesReader trips
	// mid-stream (a caller that lied about or omitted Content-Length), the
	// read error surfaces as a failed Do() below, i.e. 502, not the clean
	// 413 a pre-buffered read would give -- accepted for this internal,
	// mTLS-authenticated route, where the cap is an OOM guard, not a
	// caller-facing validation contract.
	lokiReq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.lokiPushURL, r.Body)
	if err != nil {
		http.Error(w, "build loki request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if r.ContentLength > 0 {
		lokiReq.ContentLength = r.ContentLength
	}
	for _, h := range passthroughHeaders {
		if v := r.Header.Get(h); v != "" {
			lokiReq.Header.Set(h, v)
		}
	}

	resp, err := s.httpClient.Do(lokiReq)
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

Remove the now-unused `"bytes"` and `"errors"` entries from the import block at the top of the file
(both were only used by the `io.ReadAll`/`bytes.NewReader`/`errors.As` code just removed — `io` and
every other import stay, since `io.Copy`/`io.LimitReader`/`io.ReadAll` are all still used elsewhere in
this file for `ServeQuery`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/log-gateway/... -v`
Expected: all tests PASS, including both `TestHandlePush_OversizedBodyRejected` (now via the
Content-Length fast path) and the new
`TestHandlePush_OversizedBodyWithNoContentLengthSurfacesAsBadGateway` (via the `MaxBytesReader`
safety net). Also confirm the build is clean: `cd src && go build ./cmd/log-gateway/...`

- [ ] **Step 5: Update documentation**

`docs/protocols/log-gateway.md`'s `## Response` section — the line currently reading `413 Request
Entity Too Large if the body exceeds log-gateway's 10MB cap.` becomes:

```markdown
`413 Request Entity Too Large` if the body's declared `Content-Length` exceeds `log-gateway`'s 10MB
cap (checked before any read). If a caller omits or understates `Content-Length` and the actual body
exceeds the cap, the streamed forward to Loki fails partway through instead, surfacing as `502 Bad
Gateway` rather than `413` — see
[Design: Logging Flow Hardening](../superpowers/specs/2026-08-22-logging-flow-hardening-design.md).
```

`docs/components/log-gateway.md`'s Behavior section — after the sentence ending "...the request body
is forwarded to Loki's own push endpoint completely unexamined and byte-for-byte unmodified", add:

```markdown
The request body streams straight through to Loki rather than being buffered in memory first; a
declared `Content-Length` over the 10MB cap is rejected before any read, and an actual body exceeding
the cap despite a smaller/absent declared length fails the forward mid-stream (`502`, not `413`).
```

- [ ] **Step 6: Commit**

```bash
git add src/cmd/log-gateway/server.go src/cmd/log-gateway/server_test.go docs/components/log-gateway.md docs/protocols/log-gateway.md
git commit -m "perf(log-gateway): stream the push body instead of buffering it in memory

ServeHTTP io.ReadAll'd the whole request body into a byte slice before
building the outbound request to Loki, adding latency and peak-memory
cost per push on the one process every node's logs pass through.
Stream r.Body straight through instead, with a fast Content-Length
pre-check for the common (declared-length) case and MaxBytesReader
kept as the safety net for the rest -- which now surfaces as 502
instead of 413 if it trips, an accepted trade for an internal,
mTLS-authenticated route where the cap is an OOM guard, not a
caller-facing validation contract."
```

---

### Task 4: Changelog entry

**Files:**
- Modify: `CHANGELOG.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: Add the entry**

Insert a new section at the top of `CHANGELOG.md`, directly under the `# Changelog` header/intro
paragraph and above the existing `## 2026-08-17 — Live job & log updates` entry:

```markdown
## 2026-08-22 — Logging flow hardening

Three fixes found by auditing the client-push and server-delivery logging paths end to end. Vector's
disk buffer now blocks (pausing local log shipping) instead of dropping the newest lines once full --
its old `drop_newest` setting meant a prolonged `log-gateway`/Loki outage discarded exactly the
freshest, most operationally relevant log lines while stale ones sat queued. `api-server`'s
`jobAggregator` no longer runs its periodic 24h, fleet-wide Loki reconcile query while no browser has
`/api/v1/jobs/stream` open, cutting steady-state Loki load to zero when nobody's watching; the first
browser to reconnect after an idle stretch now triggers one synchronous reconcile so it never sees
stale data. `log-gateway`'s push route now streams request bodies straight through to Loki instead of
fully buffering them in memory first, cutting per-push latency and peak memory on the one process
every node's logs pass through. See
`docs/superpowers/specs/2026-08-22-logging-flow-hardening-design.md`.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs(changelog): note logging flow hardening fixes"
```

---

## Self-Review Notes

- **Spec coverage:** Task 1 covers Architecture §1 (Vector buffer). Task 2 covers Architecture §2
  (`jobAggregator` gating). Task 3 covers Architecture §3 (`log-gateway` streaming). The design doc's
  Testing section's four bullet groups are each represented by at least one concrete test above; the
  Documentation Impact section's four doc updates are each a step above (design-doc amendment in Task
  1, `log-gateway` component/protocol docs in Task 3, changelog in Task 4).
- **Type consistency:** `Subscribe(ctx context.Context) (snapshot []jobDTO, ch chan jobsStreamMsg,
  unsubscribe func())` is the same signature used consistently across Task 2's test rewrites and the
  `jobs_stream_list.go` call-site update. `reconcileIfSubscribed(ctx context.Context) error` and
  `subscriberCount() int` are each defined once (Task 2 Step 3) and used only within that same task.
- **No placeholders:** every step above contains literal, complete code — nothing deferred to "add
  appropriate tests" or similar.
