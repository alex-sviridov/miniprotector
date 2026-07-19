# Jobs Endpoint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `GET /api/v1/jobs` (fleet-wide job list: start/end/source/state, all job kinds) and `GET /api/v1/jobs/{job_id}/logs` (per-job log tail) to `api-server`, backed by Loki.

**Architecture:** Three small upstream logging fixes (`bwfs`, `brfs`, `agent`) tag each job's lifecycle boundary lines with `event`/`status`; `agent`'s bundled Vector lifts `job_id`/`event`/`status` into Loki **structured metadata** (not labels — avoids the per-job stream-cardinality explosion the original fleet-log design deliberately avoided) instead of leaving them buried in JSON line content. `log-gateway` gains a second, read-only proxy route onto Loki's `query_range` API (mirroring its existing push-proxy). `api-server` queries through that route, pairs `event=start`/`event=finish` lines by `job_id`, and serves both endpoints.

**Tech Stack:** Go, `log/slog` (JSON file logging, already in place), Vector (VRL `remap` transform + `loki` sink), Loki 3.7.3 (structured metadata, schema v13/TSDB), `net/http` (both new endpoints and the `log-gateway` proxy route).

## Global Constraints

- `GET /api/v1/jobs` default window: `since` = 24h before `until` (`until` defaults to now). **Product decision — do not shrink this default.**
- `until - since` capped at 168h (7 days) — `400` if exceeded.
- `job_id` path segment on `/jobs/{job_id}/logs` validated against `^[a-zA-Z0-9:_-]+$` before use in a LogQL query — `400` on anything else.
- `event` ∈ `{"start", "finish"}`; `status` ∈ `{"success", "failure"}`, present only on `event=finish` lines.
- `agent`'s wrapper logging (`logExecStart`/`logExecCompletion`) must **never** emit `event`/`status` for a backup-dispatch policy (`p.ID` prefixed `backup:`) — only `brfs`/`bwfs` mark backup lifecycle boundaries. This is load-bearing for `GET /api/v1/jobs`'s unfiltered (no `kind` filter) query, which would otherwise see two competing `event=start`/`event=finish` sources for the same `job_id`.
- No new `local.conf` keys — `log_gateway_host`/`log_gateway_port` already exist in every relevant `local.conf` and are reused.
- `api-server`'s Loki query cache TTL: 10s.
- `log-gateway`'s new query route response cap: `10 << 20` bytes (mirrors the existing `maxPushBodyBytes`).

---

## File Structure

| File | Responsibility |
|---|---|
| `src/common/mtls/mtls.go` | + `ClientTLSConfig` (exported): raw `*tls.Config` for an HTTP mTLS client |
| `src/cmd/bwfs/commit.go` | Modify: add `event`/`status` to both `BackupCommit` log lines |
| `src/cmd/bwfs/integration_test.go` | Modify: add `newTestEnvWithLogger`, new logging assertions |
| `src/cmd/brfs/main.go` | Modify: add `event=start`, remove duplicate `jobId` key |
| `src/cmd/agent/reconcile.go` | Modify: `logExecStart`/`logExecCompletion` gain `event`/`status`, gated off `backup:`-prefixed policies |
| `src/cmd/agent/reconcile_test.go` | Modify: new assertions for the above |
| `src/cmd/agent/vector.go` | Modify: `add_binary_label` remap gains a JSON-decode step; `loki_gateway` sink gains `structured_metadata` |
| `src/cmd/agent/vector_test.go` | Modify: new template-content assertions |
| `src/cmd/log-gateway/server.go` | Modify: new `ServeQuery` handler + `maxQueryResponseBytes` |
| `src/cmd/log-gateway/main.go` | Modify: register the new route |
| `src/cmd/log-gateway/server_test.go` | Modify: new tests for `ServeQuery` |
| `src/cmd/log-gateway/e2e_test.go` | Modify: extend with a structured-metadata round trip |
| `demo/loki/loki-config.yaml`, `deploy/control-plane/loki/loki-config.yaml` | Modify: add query-cost bounds to `limits_config` |
| `src/cmd/api-server/loki.go` | New: `lokiQuerier` interface, `lokiStream`/`lokiValue` response types, `httpLokiClient` |
| `src/cmd/api-server/loki_test.go` | New |
| `src/cmd/api-server/loki_cache.go` | New: `cachingLokiClient` (10s TTL wrapper) |
| `src/cmd/api-server/loki_cache_test.go` | New |
| `src/cmd/api-server/jobs.go` | New: `handleListJobs`, `handleGetJobLogs`, pairing/aggregation logic |
| `src/cmd/api-server/jobs_test.go` | New |
| `src/cmd/api-server/server.go` | Modify: add `loki lokiQuerier` field, register two new routes |
| `src/cmd/api-server/main.go` | Modify: build the mTLS Loki HTTP client, wire it in |
| `docs/api/rest-v1.md`, `docs/components/api-server.md`, `docs/components/log-gateway.md`, `docs/protocols/log-gateway.md`, `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md` | Modify: document the two new endpoints and the query-proxy route |
| `CHANGELOG.md` | Modify: dated entry |

---

## Task 1: `common/mtls` — export `ClientTLSConfig`

**Files:**
- Modify: `src/common/mtls/mtls.go:250-258` (immediately after `ServerTLSConfig`)
- Test: `src/common/mtls/mtls_test.go`

**Interfaces:**
- Produces: `func ClientTLSConfig(certsDir, host string) (*tls.Config, error)` — the raw `*tls.Config` an `http.Client` needs to present the standard `client.crt`/`client.key` identity when dialing another mTLS-terminating HTTP server (used by Task 13 to reach `log-gateway`'s new query route).

- [ ] **Step 1: Write the failing test**

Add to `src/common/mtls/mtls_test.go` (mirrors `TestLoadClientCredentials_Success` immediately below it):

```go
func TestClientTLSConfig_Success(t *testing.T) {
	cfg, err := ClientTLSConfig(fixtureCertsDir, "bwfs.internal")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.NotNil(t, cfg.GetClientCertificate, "must present this node's identity via GetClientCertificate for cert-reload-on-handshake, same as clientTLSConfig")
}

func TestClientTLSConfig_MissingCAFile(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, fixtureCertsDir+"/client.crt", dir+"/client.crt")
	copyFile(t, fixtureCertsDir+"/client.key", dir+"/client.key")
	// ca.crt intentionally omitted

	_, err := ClientTLSConfig(dir, "bwfs.internal")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./common/mtls/... -run TestClientTLSConfig -v`
Expected: FAIL with `undefined: ClientTLSConfig`

- [ ] **Step 3: Add the exported function**

In `src/common/mtls/mtls.go`, immediately after the existing `ServerTLSConfig` function (ends at line 258):

```go
// ClientTLSConfig returns the raw operating-tier *tls.Config
// LoadClientCredentials wraps into gRPC transport credentials -- for an
// HTTP client built directly on net/http (e.g. api-server dialing
// log-gateway's query_range proxy route) instead of gRPC. Presents the
// standard client.crt/client.key identity; same hostname/chain
// verification rules as LoadClientCredentials, including per-handshake
// certificate reload via GetClientCertificate.
func ClientTLSConfig(certsDir, host string) (*tls.Config, error) {
	return clientTLSConfig(certsDir, host)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./common/mtls/... -run TestClientTLSConfig -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/common/mtls/mtls.go src/common/mtls/mtls_test.go
git commit -m "feat(mtls): export ClientTLSConfig for HTTP mTLS clients"
```

---

## Task 2: `bwfs` — tag backup commit lines with `event`/`status`

**Files:**
- Modify: `src/cmd/bwfs/commit.go:47,64`
- Modify: `src/cmd/bwfs/integration_test.go` (new `newTestEnvWithLogger` + 3 new tests)

**Interfaces:**
- Consumes: `storage.JobStatusSuccess` / `storage.JobStatusFailure` (already imported in `commit.go`)
- Produces: no new symbols — only log line content changes, asserted directly in tests via a captured `*slog.Logger`.

- [ ] **Step 1: Write the failing tests**

In `src/cmd/bwfs/integration_test.go`, replace the existing `newTestEnv` (the block at lines 49-95) with:

```go
func newTestEnvWithLogger(t *testing.T, logger *slog.Logger) *testEnv {
	t.Helper()

	storageDir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())

	conf := &config.Config{
		ConnectionTimeOutSec: 10,
		FileLockTimeoutSec:   5,
	}
	srvCtx := context.WithValue(ctx, config.ContextKey, conf)

	srv, err := NewBackupServer(srvCtx, logger, storageDir)
	require.NoError(t, err)

	serverCreds, err := mtls.LoadServerCredentials(testCertsDir)
	require.NoError(t, err)

	lis := bufconn.Listen(bufSize)
	grpcSrv := grpc.NewServer(grpc.Creds(serverCreds))
	pb.RegisterBackupServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)

	clientCreds, err := mtls.LoadClientCredentials(testCertsDir, "bwfs.internal")
	require.NoError(t, err)

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(clientCreds),
	)
	require.NoError(t, err)

	return &testEnv{
		client: pb.NewBackupServiceClient(conn),
		store:  srv,
		cleanup: func() {
			conn.Close()
			grpcSrv.GracefulStop()
			lis.Close()
			srv.store.Close()
			cancel()
		},
	}
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWithLogger(t, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
}
```

Then add these new tests near `TestIntegration_BackupCommit_MatchingHashSucceeds`:

```go
func TestIntegration_BackupCommit_LogsEventFinishAndStatusSuccessOnMatch(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	env := newTestEnvWithLogger(t, logger)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-log-success")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(target.ID())})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"msg":"Backup job committed"`)
	assert.Contains(t, out, `"event":"finish"`)
	assert.Contains(t, out, `"status":"success"`)
}

func TestIntegration_BackupCommit_LogsStatusFailureOnMismatch(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	env := newTestEnvWithLogger(t, logger)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-log-mismatch")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash("never-sent")})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"status":"failure"`)
}

func TestIntegration_BackupCommit_AlreadyFinalizedLogsEventFinish(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	env := newTestEnvWithLogger(t, logger)
	defer env.cleanup()

	srcDir := makeTestDir(t)
	files, err := wfs.Discover(srcDir)
	require.NoError(t, err)
	var target wfs.FileInfo
	for _, f := range files {
		if f.GetType() == 'f' && f.Size() > 0 {
			target = f
			break
		}
	}
	require.NotEmpty(t, target.ID())

	ctx := jobContext("job-commit-log-retry")
	stream, err := env.client.ProcessBackupStream(ctx)
	require.NoError(t, err)
	_, err = backupOneFile(ctx, t, stream, target)
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(target.ID())})
	require.NoError(t, err)
	buf.Reset() // only care about the log line from the retried call below

	_, err = env.client.BackupCommit(ctx, &pb.BackupCommitRequest{FileListHash: commitHash(target.ID())})
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, `"msg":"BackupCommit for already-finalized job"`)
	assert.Contains(t, out, `"event":"finish"`)
}
```

Add `"bytes"` to the import block at the top of `src/cmd/bwfs/integration_test.go` (not already imported there).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test -tags integration ./cmd/bwfs/... -run 'TestIntegration_BackupCommit_LogsEvent|TestIntegration_BackupCommit_LogsStatus|TestIntegration_BackupCommit_AlreadyFinalizedLogsEventFinish' -v`
Expected: FAIL — output missing `"event":"finish"` (and `"status"` on the already-finalized case originally used `job.Status`, which will still be present, but `event` will not)

- [ ] **Step 3: Modify `commit.go`**

In `src/cmd/bwfs/commit.go`, change line 47:

```go
		server.logger.Info("BackupCommit for already-finalized job", "job_id", jobID, "status", job.Status)
```
to:
```go
		server.logger.Info("BackupCommit for already-finalized job", "job_id", jobID, "event", "finish", "status", job.Status)
```

And change lines 56-65 (the `matched`/`FinalizeBackupJob`/log block) from:

```go
	computed := sha256.Sum256([]byte(strings.Join(objectIDs, "\n")))
	matched := bytes.Equal(computed[:], req.FileListHash)

	if _, err := server.store.FinalizeBackupJob(jobID, matched); err != nil {
		return nil, status.Errorf(codes.Internal, "finalize backup job: %v", err)
	}
	server.liveness.Complete(jobID)

	server.logger.Info("Backup job committed", "job_id", jobID, "matched", matched)
	return &pb.BackupCommitResponse{Success: matched}, nil
```
to:
```go
	computed := sha256.Sum256([]byte(strings.Join(objectIDs, "\n")))
	matched := bytes.Equal(computed[:], req.FileListHash)
	commitStatus := storage.JobStatusFailure
	if matched {
		commitStatus = storage.JobStatusSuccess
	}

	if _, err := server.store.FinalizeBackupJob(jobID, matched); err != nil {
		return nil, status.Errorf(codes.Internal, "finalize backup job: %v", err)
	}
	server.liveness.Complete(jobID)

	server.logger.Info("Backup job committed", "job_id", jobID, "event", "finish", "status", commitStatus, "matched", matched)
	return &pb.BackupCommitResponse{Success: matched}, nil
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test -tags integration ./cmd/bwfs/... -v`
Expected: PASS (all tests, including the pre-existing ones — this confirms nothing else broke)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/bwfs/commit.go src/cmd/bwfs/integration_test.go
git commit -m "feat(bwfs): tag BackupCommit log lines with event/status"
```

---

## Task 3: `brfs` — tag start line with `event`, drop duplicate `jobId`

**Files:**
- Modify: `src/cmd/brfs/main.go:70-76`

**Interfaces:** none — log content only.

- [ ] **Step 1: Make the edit**

In `src/cmd/brfs/main.go`, change:

```go
	logger.Info("Backup reader started",
		"sourceFolder", arguments.SourceFolder,
		"writerHost", arguments.WriterHost,
		"writerPort", arguments.WriterPort,
		"streamsCount", arguments.Streams,
		"jobId", jobID,
	)
```
to:
```go
	logger.Info("Backup reader started",
		"sourceFolder", arguments.SourceFolder,
		"writerHost", arguments.WriterHost,
		"writerPort", arguments.WriterPort,
		"streamsCount", arguments.Streams,
		"event", "start",
	)
```

(`job_id` is already attached to every line by `common/logging.NewLogger` via the `jobId` context value set two lines above — the removed `"jobId", jobID` was a redundant, differently-cased duplicate of that same value.)

- [ ] **Step 2: Verify it builds and existing tests still pass**

Run: `cd src && go build ./cmd/brfs/... && go test ./cmd/brfs/... -v`
Expected: build succeeds, all existing tests PASS (this repo has no test asserting on this particular log line's content today, so there is nothing to update)

- [ ] **Step 3: Commit**

```bash
git add src/cmd/brfs/main.go
git commit -m "fix(brfs): tag start line with event=start, drop duplicate jobId key"
```

---

## Task 4: `agent` — tag static-policy wrapper lines with `event`/`status`, excluding backups

**Files:**
- Modify: `src/cmd/agent/reconcile.go:134-160`
- Modify: `src/cmd/agent/reconcile_test.go` (extend existing `TestLogExecOutcome_*` tests, add new ones)

**Interfaces:**
- Produces: `logExecStart`/`logExecCompletion` keep their existing signatures — behavior changes only.

- [ ] **Step 1: Write the failing tests**

In `src/cmd/agent/reconcile_test.go`, replace `TestLogExecOutcome_SuccessLogsStartAndCompletionWithJobID` with:

```go
func TestLogExecOutcome_SuccessLogsStartAndCompletionWithJobID(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	logExecStart(logger, p)
	logExecCompletion(logger, p, nil, 250*time.Millisecond)

	out := buf.String()
	assert.Contains(t, out, "policy execution started")
	assert.Contains(t, out, "policy execution completed")
	assert.Contains(t, out, "test-policy:123")
	assert.Contains(t, out, `"event":"start"`)
	assert.Contains(t, out, `"event":"finish"`)
	assert.Contains(t, out, `"status":"success"`)
}
```

Add two new tests directly below it:

```go
func TestLogExecOutcome_FailureLogsStatusFailure(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	logExecCompletion(logger, p, errors.New("simulated failure"), time.Second)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "failure", entry["status"])
	assert.Equal(t, "finish", entry["event"])
}

func TestLogExecOutcome_BackupPolicyOmitsEventAndStatus(t *testing.T) {
	// agent must never tag a scheduled backup dispatch with event/status --
	// brfs (start) and bwfs (finish) are the sole lifecycle sources for
	// kind=backup, otherwise GET /api/v1/jobs' unfiltered query would see
	// two competing event=start/event=finish lines for the same job_id.
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "backup:nightly:var-www:abcd1234", JobID: "backup:nightly:var-www:abcd1234:1752400000"}

	logExecStart(logger, p)
	logExecCompletion(logger, p, nil, 250*time.Millisecond)

	out := buf.String()
	assert.NotContains(t, out, `"event"`)
	assert.NotContains(t, out, `"status"`)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run 'TestLogExecOutcome' -v`
Expected: FAIL — no `event`/`status` keys present yet; the new `BackupPolicyOmitsEventAndStatus` test passes vacuously today (nothing emits these fields yet) but will start actually exercising the gate once Step 3 lands, so re-run after Step 3 too.

- [ ] **Step 3: Modify `reconcile.go`**

Add `"strings"` to the import block (already imported — confirm; it is, per the existing `import` block). Replace `logExecStart`/`logExecCompletion`:

```go
// isBackupPolicy reports whether p is a scheduled backup dispatch (see
// backup.go's backupTaskID) rather than one of agent's three static
// policies. Backup jobs' event/status lifecycle markers come solely from
// brfs (start) and bwfs (finish) -- see logExecStart/logExecCompletion.
func isBackupPolicy(p Policy) bool {
	return strings.HasPrefix(p.ID, "backup:")
}

// logExecStart logs that agent is about to dispatch p's exec. Called
// immediately before execute for both the synchronous and background
// dispatch paths in run(), so agent's own log always shows an exec
// starting even if it never finishes (e.g. agent is killed mid-exec).
// event=start is added for every policy except scheduled backups --
// brfs's own "Backup reader started" line is that job kind's sole
// event=start source, so this line staying untagged for backups is
// deliberate, not an oversight (see isBackupPolicy).
func logExecStart(logger *slog.Logger, p Policy) {
	if isBackupPolicy(p) {
		logger.Info("policy execution started", "policy", p.ID, "binary", p.Binary, "job_id", p.JobID)
		return
	}
	logger.Info("policy execution started", "policy", p.ID, "binary", p.Binary, "job_id", p.JobID, "event", "start")
}

// logExecCompletion logs the outcome of one exec attempt at Info level, on
// both success and failure -- unlike recordOutcome's existing Error-level
// line (which only fires on failure, for operators grepping specifically
// for errors), this gives agent's own log a complete start/end timeline
// for every dispatched exec. exit_code is included only when attemptErr is
// a real *exec.ExitError -- fabricating one for any other error type would
// be misleading. event/status are omitted for scheduled backups, same
// reasoning as logExecStart.
func logExecCompletion(logger *slog.Logger, p Policy, attemptErr error, duration time.Duration) {
	backup := isBackupPolicy(p)
	status := "success"
	if attemptErr != nil {
		status = "failure"
	}

	if attemptErr == nil {
		if backup {
			logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration)
			return
		}
		logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "event", "finish", "status", status)
		return
	}

	var exitErr *exec.ExitError
	if errors.As(attemptErr, &exitErr) {
		if backup {
			logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "exit_code", exitErr.ExitCode(), "error", attemptErr)
			return
		}
		logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "exit_code", exitErr.ExitCode(), "error", attemptErr, "event", "finish", "status", status)
		return
	}
	if backup {
		logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "error", attemptErr)
		return
	}
	logger.Info("policy execution completed", "policy", p.ID, "job_id", p.JobID, "duration", duration, "error", attemptErr, "event", "finish", "status", status)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS (full package — confirms `TestRun_LogsStartAndCompletionForEveryDispatchedExec` and every other existing reconcile test still passes unchanged)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go
git commit -m "feat(agent): tag static-policy exec lines with event/status, excluding backups"
```

---

## Task 5: `agent` — lift `job_id`/`event`/`status` into Vector structured metadata

**Files:**
- Modify: `src/cmd/agent/vector.go:49-83` (the `vectorConfigTemplate` constant)
- Modify: `src/cmd/agent/vector_test.go`

**Interfaces:** `renderVectorConfig`'s signature is unchanged.

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/agent/vector_test.go`, near the other `TestRenderVectorConfig_*` tests:

```go
func TestRenderVectorConfig_LiftsJobLifecycleFieldsIntoStructuredMetadata(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "parse_json(.message)")
	assert.Contains(t, got, "structured_metadata:")
	assert.Contains(t, got, `job_id: "{{ job_id }}"`)
	assert.Contains(t, got, `event: "{{ event }}"`)
	assert.Contains(t, got, `status: "{{ status }}"`)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./cmd/agent/... -run TestRenderVectorConfig_LiftsJobLifecycleFieldsIntoStructuredMetadata -v`
Expected: FAIL — none of these strings present yet

- [ ] **Step 3: Modify the template**

In `src/cmd/agent/vector.go`, replace the `transforms`/`sinks` section of `vectorConfigTemplate` (currently lines 62-83) with:

```go
transforms:
  add_binary_label:
    type: remap
    inputs: ["local_logs"]
    source: |
      parts = split!(.file, "/")
      .binary = replace!(parts[-1], ".log", "")
      parsed, err = parse_json(.message)
      if err == null {
        .job_id = parsed.job_id
        .event = parsed.event
        .status = parsed.status
      }

sinks:
  loki_gateway:
    type: loki
    inputs: ["add_binary_label"]
    endpoint: "https://{{ .LogGatewayHost }}:{{ .LogGatewayPort }}"
    encoding:
      codec: json
    labels:
      binary: "{{"{{ binary }}"}}"
      hostname: "{{ .Hostname }}"
    structured_metadata:
      job_id: "{{"{{ job_id }}"}}"
      event: "{{"{{ event }}"}}"
      status: "{{"{{ status }}"}}"
    tls:
      ca_file: "{{ .CertsDir }}/ca.crt"
      crt_file: "{{ .CertsDir }}/client.crt"
      key_file: "{{ .CertsDir }}/client.key"
    buffer:
      type: disk
      max_size: 268435488
      when_full: drop_newest
`
```

(Only the `transforms`/`sinks` block changes — `sources`, `data_dir`, and everything above/below stay as-is. The trailing backtick that closes the Go raw-string constant stays where it already is.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS (full package, including every pre-existing `TestRenderVectorConfig_*` test — confirms the log-dir glob, endpoint, TLS paths, var-dir, no-API-listener, and hostname-label assertions all still hold)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/vector.go src/cmd/agent/vector_test.go
git commit -m "feat(agent): lift job_id/event/status into Vector structured metadata"
```

---

## Task 6: `log-gateway` — add the `query_range` read-proxy route

**Files:**
- Modify: `src/cmd/log-gateway/server.go`
- Modify: `src/cmd/log-gateway/main.go:64-65`
- Modify: `src/cmd/log-gateway/server_test.go`

**Interfaces:**
- Produces: `(*logGatewayServer).ServeQuery(w http.ResponseWriter, r *http.Request)` — registered at `GET /loki/api/v1/query_range`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/log-gateway/server_test.go`:

```go
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

func TestHandleQuery_NoPeerCertificateRejected(t *testing.T) {
	srv := newLogGatewayServer("http://unused.invalid", testLogger())

	req := httptest.NewRequest(http.MethodGet, "/loki/api/v1/query_range", nil)
	w := httptest.NewRecorder()

	srv.ServeQuery(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Result().StatusCode)
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/log-gateway/... -run TestHandleQuery -v`
Expected: FAIL with `srv.ServeQuery undefined` / `maxQueryResponseBytes undefined`

- [ ] **Step 3: Implement `ServeQuery`**

In `src/cmd/log-gateway/server.go`, add after `maxPushBodyBytes`'s declaration:

```go
// maxQueryResponseBytes bounds how much of a query_range response
// log-gateway will buffer in memory -- the read-path mirror of
// maxPushBodyBytes: an unusually broad query returning a huge result must
// not OOM the sole path to Loki for the whole fleet.
const maxQueryResponseBytes = 10 << 20 // 10MB
```

Add `lokiQueryURL` to the struct and constructor:

```go
type logGatewayServer struct {
	lokiPushURL  string
	lokiQueryURL string
	httpClient   *http.Client
	logger       *slog.Logger
}

func newLogGatewayServer(lokiBaseURL string, logger *slog.Logger) *logGatewayServer {
	return &logGatewayServer{
		lokiPushURL:  lokiBaseURL + "/loki/api/v1/push",
		lokiQueryURL: lokiBaseURL + "/loki/api/v1/query_range",
		httpClient:   &http.Client{},
		logger:       logger,
	}
}
```

Add the new handler at the end of the file:

```go
// ServeQuery proxies a caller's query_range parameters to Loki's real
// query_range endpoint unmodified -- the read-path counterpart to
// ServeHTTP's push forwarding, gated by the same operating-tier mTLS
// check. Reachable by any operating-tier mesh node, not just api-server --
// the same "any operating-tier cert may call any RPC it can reach"
// convention already accepted for clientmanager-api/catalog/policy-server.
func (s *logGatewayServer) ServeQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if _, err := mtls.PeerHostnameFromConnState(r.TLS); err != nil {
		http.Error(w, "determine caller identity: "+err.Error(), http.StatusUnauthorized)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), lokiForwardTimeout)
	defer cancel()

	lokiReq, err := http.NewRequestWithContext(ctx, http.MethodGet, s.lokiQueryURL, nil)
	if err != nil {
		http.Error(w, "build loki request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	lokiReq.URL.RawQuery = r.URL.RawQuery

	resp, err := s.httpClient.Do(lokiReq)
	if err != nil {
		s.logger.Error("forward query to loki failed", "error", err)
		http.Error(w, "forward to loki: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQueryResponseBytes+1))
	if err != nil {
		http.Error(w, "read loki response: "+err.Error(), http.StatusBadGateway)
		return
	}
	if len(body) > maxQueryResponseBytes {
		http.Error(w, "loki response exceeds size cap", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
```

Add `"strings"` to `server_test.go`'s imports (used by the new oversized-response test; check it isn't already imported before adding).

- [ ] **Step 4: Register the route in `main.go`**

In `src/cmd/log-gateway/main.go`, change:

```go
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/push", srv.ServeHTTP)
```
to:
```go
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/push", srv.ServeHTTP)
	mux.HandleFunc("/loki/api/v1/query_range", srv.ServeQuery)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/log-gateway/... -v`
Expected: PASS (full package, including all pre-existing push tests)

- [ ] **Step 6: Commit**

```bash
git add src/cmd/log-gateway/server.go src/cmd/log-gateway/main.go src/cmd/log-gateway/server_test.go
git commit -m "feat(log-gateway): add GET /loki/api/v1/query_range read-proxy route"
```

---

## Task 7: `log-gateway` e2e — structured-metadata round trip

**Files:**
- Modify: `src/cmd/log-gateway/e2e_test.go`

**Interfaces:** none new — reuses `startTestLoki`, `queryLoki`, cert-fixture helpers already in the file.

- [ ] **Step 1: Add the test**

Add to `src/cmd/log-gateway/e2e_test.go`, after `TestE2E_AuthenticatedPushReachesLokiUnderClientDeclaredHostname`:

```go
// TestE2E_QueryRouteReturnsStructuredMetadataPushedThroughLogGateway proves
// the read path this design adds: a push carrying Loki structured metadata
// (job_id/event/status -- see cmd/agent/vector.go) round-trips through
// log-gateway's new /loki/api/v1/query_range proxy and is queryable by
// filtering on that metadata, not just on labels or line content.
func TestE2E_QueryRouteReturnsStructuredMetadataPushedThroughLogGateway(t *testing.T) {
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
	mux.HandleFunc("/loki/api/v1/query_range", srv.ServeQuery)
	httpServer := &http.Server{Handler: mux, TLSConfig: tlsConfig}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tlsListener := tls.NewListener(listener, tlsConfig)
	gatewayAddr := listener.Addr().String()

	go func() { _ = httpServer.Serve(tlsListener) }()
	defer httpServer.Close()

	clientCert := generateTestLeaf(t, ca, caKey, "node-e2e-metadata", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)
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
	pushBody := fmt.Sprintf(`{"streams":[{"stream":{"hostname":"node-e2e-metadata","binary":"agent"},"values":[["%d","policy execution completed",{"job_id":"operating-refresh:1752400500","event":"finish","status":"success"}]]}]}`, nowNS)
	resp, err := httpClient.Post(fmt.Sprintf("https://%s/loki/api/v1/push", gatewayAddr), "application/json", strings.NewReader(pushBody))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "push failed: %s", body)

	require.Eventually(t, func() bool {
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("https://%s/loki/api/v1/query_range", gatewayAddr), nil)
		if err != nil {
			return false
		}
		q := req.URL.Query()
		q.Set("query", `{hostname="node-e2e-metadata"} | job_id="operating-refresh:1752400500"`)
		q.Set("start", strconv.FormatInt(time.Now().Add(-time.Hour).UnixNano(), 10))
		q.Set("end", strconv.FormatInt(time.Now().Add(time.Hour).UnixNano(), 10))
		req.URL.RawQuery = q.Encode()

		resp, err := httpClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		result, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			return false
		}
		return strings.Contains(string(result), `"event":"finish"`) || strings.Contains(string(result), "policy execution completed")
	}, 15*time.Second, 500*time.Millisecond, "pushed structured-metadata line never became queryable through the new query route")
}
```

- [ ] **Step 2: Run it**

Run: `cd src && go test -tags e2e ./cmd/log-gateway/... -run TestE2E_QueryRouteReturnsStructuredMetadataPushedThroughLogGateway -v`
Expected: PASS if Docker is available (skips with a clear message otherwise, per `requireDocker`)

- [ ] **Step 3: Commit**

```bash
git add src/cmd/log-gateway/e2e_test.go
git commit -m "test(log-gateway): e2e-cover the structured-metadata query round trip"
```

---

## Task 8: Loki config — add query-cost bounds

**Files:**
- Modify: `demo/loki/loki-config.yaml`
- Modify: `deploy/control-plane/loki/loki-config.yaml`

**Interfaces:** none — config only.

- [ ] **Step 1: Edit both files identically**

In both `demo/loki/loki-config.yaml` and `deploy/control-plane/loki/loki-config.yaml`, change:

```yaml
limits_config:
  retention_period: 720h
```
to:
```yaml
limits_config:
  retention_period: 720h
  # Query-cost bounds for GET /api/v1/jobs and /api/v1/jobs/{job_id}/logs
  # (docs/superpowers/specs/2026-07-19-jobs-endpoint-design.md) -- defense
  # in depth so a broad or misbehaving query can't peg this single Loki
  # instance, the same "generous but bounded" philosophy log-gateway
  # already applies to push size/timeout.
  max_query_length: 168h
  max_entries_limit_per_query: 5000
  query_timeout: 30s
```

- [ ] **Step 2: Verify both configs are valid**

Run:
```bash
docker run --rm -v "$(pwd)/demo/loki/loki-config.yaml:/etc/loki/local-config.yaml:ro" grafana/loki:3.7.3 -config.file=/etc/loki/local-config.yaml -verify-config
docker run --rm -v "$(pwd)/deploy/control-plane/loki/loki-config.yaml:/etc/loki/local-config.yaml:ro" grafana/loki:3.7.3 -config.file=/etc/loki/local-config.yaml -verify-config
```
Expected: both exit 0 with no config error printed (skip this step if Docker isn't available locally — it will still be exercised by Task 7's e2e test using `deploy/control-plane/loki/loki-config.yaml` directly)

- [ ] **Step 3: Commit**

```bash
git add demo/loki/loki-config.yaml deploy/control-plane/loki/loki-config.yaml
git commit -m "chore(loki): bound query cost for the new jobs endpoints"
```

---

## Task 9: `api-server` — Loki HTTP query client

**Files:**
- Create: `src/cmd/api-server/loki.go`
- Create: `src/cmd/api-server/loki_test.go`

**Interfaces:**
- Produces:
  - `type lokiQuerier interface { QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) }`
  - `type lokiStream struct { Stream map[string]string; Values []lokiValue }`
  - `type lokiValue struct { Timestamp int64; Line string; Metadata map[string]string }` (`Timestamp` in unix nanoseconds, matching Loki's own wire format)
  - `func newHTTPLokiClient(baseURL string, httpClient *http.Client) *httpLokiClient`, implementing `lokiQuerier`

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/api-server/loki_test.go`:

```go
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPLokiClient_QueryRange_ParsesStreamsAndStructuredMetadata(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, `{binary=~"agent"} | event="finish"`, r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"status": "success",
			"data": {
				"resultType": "streams",
				"result": [
					{
						"stream": {"hostname": "webserver", "binary": "agent"},
						"values": [
							["1752400500000000000", "policy execution completed", {"job_id": "operating-refresh:1752400500", "event": "finish", "status": "success"}]
						]
					}
				]
			}
		}`))
	}))
	defer stub.Close()

	client := newHTTPLokiClient(stub.URL, stub.Client())
	streams, err := client.QueryRange(context.Background(), `{binary=~"agent"} | event="finish"`, time.Unix(0, 0), time.Unix(1, 0), 100)
	require.NoError(t, err)
	require.Len(t, streams, 1)
	assert.Equal(t, "webserver", streams[0].Stream["hostname"])
	require.Len(t, streams[0].Values, 1)
	assert.Equal(t, int64(1752400500000000000), streams[0].Values[0].Timestamp)
	assert.Equal(t, "policy execution completed", streams[0].Values[0].Line)
	assert.Equal(t, "operating-refresh:1752400500", streams[0].Values[0].Metadata["job_id"])
	assert.Equal(t, "finish", streams[0].Values[0].Metadata["event"])
	assert.Equal(t, "success", streams[0].Values[0].Metadata["status"])
}

func TestHTTPLokiClient_QueryRange_HandlesValuesWithNoStructuredMetadata(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"stream":{},"values":[["1","line with no metadata"]]}]}}`))
	}))
	defer stub.Close()

	client := newHTTPLokiClient(stub.URL, stub.Client())
	streams, err := client.QueryRange(context.Background(), `{}`, time.Unix(0, 0), time.Unix(1, 0), 100)
	require.NoError(t, err)
	require.Len(t, streams, 1)
	require.Len(t, streams[0].Values, 1)
	assert.Nil(t, streams[0].Values[0].Metadata)
}

func TestHTTPLokiClient_QueryRange_NonOKStatusReturnsError(t *testing.T) {
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("loki unreachable"))
	}))
	defer stub.Close()

	client := newHTTPLokiClient(stub.URL, stub.Client())
	_, err := client.QueryRange(context.Background(), `{}`, time.Unix(0, 0), time.Unix(1, 0), 100)
	assert.Error(t, err)
}

func TestHTTPLokiClient_QueryRange_SendsStartEndAsUnixNanoAndLimit(t *testing.T) {
	var gotQuery, gotStart, gotEnd, gotLimit string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotStart = r.URL.Query().Get("start")
		gotEnd = r.URL.Query().Get("end")
		gotLimit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[]}}`))
	}))
	defer stub.Close()

	client := newHTTPLokiClient(stub.URL, stub.Client())
	start := time.Unix(1000, 0)
	end := time.Unix(2000, 0)
	_, err := client.QueryRange(context.Background(), `{binary="agent"}`, start, end, 42)
	require.NoError(t, err)

	assert.Equal(t, `{binary="agent"}`, gotQuery)
	assert.Equal(t, "1000000000000", gotStart)
	assert.Equal(t, "2000000000000", gotEnd)
	assert.Equal(t, "42", gotLimit)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHTTPLokiClient -v`
Expected: FAIL with `undefined: newHTTPLokiClient`

- [ ] **Step 3: Implement**

Create `src/cmd/api-server/loki.go`:

```go
// src/cmd/api-server/loki.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// lokiQuerier is the subset of a Loki-query-capable client GET /api/v1/jobs
// and GET /api/v1/jobs/{job_id}/logs need -- satisfied by httpLokiClient
// (Task 9), cachingLokiClient (Task 10), and a fake in tests.
type lokiQuerier interface {
	QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error)
}

// lokiStream is one label-set's worth of matching log lines, as returned by
// Loki's query_range API.
type lokiStream struct {
	Stream map[string]string `json:"stream"`
	Values []lokiValue        `json:"values"`
}

// lokiValue is one matched log line: Loki always returns [timestamp, line],
// and -- when the queried stream carries Loki structured metadata (see
// cmd/agent/vector.go's sink config) -- a third element holding it, which
// custom UnmarshalJSON below decodes into Metadata.
type lokiValue struct {
	Timestamp int64 // unix nanoseconds, Loki's own wire unit
	Line      string
	Metadata  map[string]string
}

func (v *lokiValue) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if len(raw) < 2 {
		return fmt.Errorf("loki value entry has fewer than 2 elements")
	}

	var tsStr string
	if err := json.Unmarshal(raw[0], &tsStr); err != nil {
		return fmt.Errorf("parse loki value timestamp: %w", err)
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return fmt.Errorf("parse loki value timestamp: %w", err)
	}
	v.Timestamp = ts

	if err := json.Unmarshal(raw[1], &v.Line); err != nil {
		return fmt.Errorf("parse loki value line: %w", err)
	}

	if len(raw) >= 3 {
		v.Metadata = map[string]string{}
		if err := json.Unmarshal(raw[2], &v.Metadata); err != nil {
			return fmt.Errorf("parse loki value structured metadata: %w", err)
		}
	}
	return nil
}

type lokiQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []lokiStream `json:"result"`
	} `json:"data"`
}

// httpLokiClient calls Loki's query_range API through log-gateway's
// read-proxy route (Task 6) rather than dialing Loki directly -- Loki is
// never directly reachable from any agent-managed node, api-server
// included (see docs/SECURITY.md).
type httpLokiClient struct {
	baseURL    string // log-gateway's base URL, e.g. "https://log-gateway:9400"
	httpClient *http.Client
}

func newHTTPLokiClient(baseURL string, httpClient *http.Client) *httpLokiClient {
	return &httpLokiClient{baseURL: baseURL, httpClient: httpClient}
}

func (c *httpLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/loki/api/v1/query_range", nil)
	if err != nil {
		return nil, fmt.Errorf("build loki query request: %w", err)
	}
	q := req.URL.Query()
	q.Set("query", query)
	q.Set("start", strconv.FormatInt(start.UnixNano(), 10))
	q.Set("end", strconv.FormatInt(end.UnixNano(), 10))
	q.Set("limit", strconv.Itoa(limit))
	req.URL.RawQuery = q.Encode()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query loki: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read loki response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("loki query returned %d: %s", resp.StatusCode, body)
	}

	var parsed lokiQueryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode loki response: %w", err)
	}
	return parsed.Data.Result, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestHTTPLokiClient -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/loki.go src/cmd/api-server/loki_test.go
git commit -m "feat(api-server): add Loki query_range HTTP client"
```

---

## Task 10: `api-server` — TTL cache around the Loki client

**Files:**
- Create: `src/cmd/api-server/loki_cache.go`
- Create: `src/cmd/api-server/loki_cache_test.go`

**Interfaces:**
- Consumes: `lokiQuerier` (Task 9)
- Produces: `func newCachingLokiClient(inner lokiQuerier, ttl time.Duration) *cachingLokiClient`, implementing `lokiQuerier`

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/api-server/loki_cache_test.go`:

```go
package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type countingLokiClient struct {
	calls   atomic.Int32
	streams []lokiStream
}

func (c *countingLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	c.calls.Add(1)
	return c.streams, nil
}

func TestCachingLokiClient_ReturnsCachedResultWithinTTL(t *testing.T) {
	inner := &countingLokiClient{streams: []lokiStream{{Stream: map[string]string{"hostname": "a"}}}}
	cache := newCachingLokiClient(inner, time.Minute)

	now := time.Now()
	_, err := cache.QueryRange(context.Background(), `{}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)
	_, err = cache.QueryRange(context.Background(), `{}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)

	assert.Equal(t, int32(1), inner.calls.Load(), "second call within TTL and the same time bucket must be served from cache")
}

func TestCachingLokiClient_RequeriesAfterTTLExpires(t *testing.T) {
	inner := &countingLokiClient{}
	cache := newCachingLokiClient(inner, 10*time.Millisecond)

	now := time.Now()
	_, err := cache.QueryRange(context.Background(), `{}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)

	time.Sleep(20 * time.Millisecond)

	_, err = cache.QueryRange(context.Background(), `{}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)

	assert.Equal(t, int32(2), inner.calls.Load())
}

func TestCachingLokiClient_DifferentQueriesNotConflated(t *testing.T) {
	inner := &countingLokiClient{}
	cache := newCachingLokiClient(inner, time.Minute)

	now := time.Now()
	_, err := cache.QueryRange(context.Background(), `{binary="agent"}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)
	_, err = cache.QueryRange(context.Background(), `{binary="brfs"}`, now.Add(-time.Hour), now, 100)
	require.NoError(t, err)

	assert.Equal(t, int32(2), inner.calls.Load())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestCachingLokiClient -v`
Expected: FAIL with `undefined: newCachingLokiClient`

- [ ] **Step 3: Implement**

Create `src/cmd/api-server/loki_cache.go`:

```go
// src/cmd/api-server/loki_cache.go
package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// cachingLokiClient wraps a lokiQuerier with a short in-memory TTL cache --
// a UI polling GET /api/v1/jobs or GET /api/v1/jobs/{job_id}/logs every few
// seconds would otherwise re-run a near-identical Loki query on every poll.
//
// start/end are rounded (Truncate) to a ttl-sized bucket for cache-key
// purposes only, not for the actual query sent to inner: callers typically
// derive start/end from time.Now(), which differs by milliseconds between
// otherwise-identical requests -- keying on exact timestamps would mean the
// cache never hits at all. The bucketed key still lets rapid repeated polls
// within the same window share a cache entry.
type cachingLokiClient struct {
	inner lokiQuerier
	ttl   time.Duration

	mu    sync.Mutex
	cache map[string]cachedLokiResult
}

type cachedLokiResult struct {
	streams []lokiStream
	err     error
	at      time.Time
}

func newCachingLokiClient(inner lokiQuerier, ttl time.Duration) *cachingLokiClient {
	return &cachingLokiClient{inner: inner, ttl: ttl, cache: make(map[string]cachedLokiResult)}
}

func (c *cachingLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	key := fmt.Sprintf("%s|%d|%d|%d", query, start.Truncate(c.ttl).UnixNano(), end.Truncate(c.ttl).UnixNano(), limit)

	c.mu.Lock()
	if cached, ok := c.cache[key]; ok && time.Since(cached.at) < c.ttl {
		c.mu.Unlock()
		return cached.streams, cached.err
	}
	c.mu.Unlock()

	streams, err := c.inner.QueryRange(ctx, query, start, end, limit)

	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range c.cache {
		if time.Since(v.at) >= c.ttl {
			delete(c.cache, k) // opportunistic sweep -- bounds the map without a background goroutine
		}
	}
	c.cache[key] = cachedLokiResult{streams: streams, err: err, at: time.Now()}

	return streams, err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestCachingLokiClient -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/loki_cache.go src/cmd/api-server/loki_cache_test.go
git commit -m "feat(api-server): add TTL cache wrapper for Loki queries"
```

---

## Task 11: `api-server` — `GET /api/v1/jobs`

**Files:**
- Create: `src/cmd/api-server/jobs.go`
- Create: `src/cmd/api-server/jobs_test.go`

**Interfaces:**
- Consumes: `lokiQuerier` (Task 9/10), `writeJSON`/`writeJSONError` (`errors.go`)
- Produces: `(*server).handleListJobs(w http.ResponseWriter, r *http.Request)`, `kindFromJobID(jobID string) string`, `jobDTO` (consumed by Task 12's tests for shared helpers)

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/api-server/jobs_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLokiClient struct {
	byQuery map[string][]lokiStream
	err     error
}

func (f *fakeLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byQuery[query], nil
}

func TestKindFromJobID(t *testing.T) {
	assert.Equal(t, "backup", kindFromJobID("backup:nightly:var-www:abcd1234:1752400000"))
	assert.Equal(t, "operating-refresh", kindFromJobID("operating-refresh:1752400500"))
	assert.Equal(t, "", kindFromJobID("no-colon-here"))
}

func TestHandleListJobs_PairsStartAndFinishByJobID(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver"}, Values: []lokiValue{
				{Timestamp: 1752400500000000000, Metadata: map[string]string{"job_id": "operating-refresh:1752400500"}},
			}},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "webserver"}, Values: []lokiValue{
				{Timestamp: 1752400501000000000, Metadata: map[string]string{"job_id": "operating-refresh:1752400500", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	job := data[0].(map[string]any)
	assert.Equal(t, "operating-refresh:1752400500", job["job_id"])
	assert.Equal(t, "operating-refresh", job["kind"])
	assert.Equal(t, "webserver", job["source_host"])
	assert.Nil(t, job["store_host"])
	assert.Equal(t, float64(1752400500), job["started_at"])
	assert.Equal(t, float64(1752400501), job["finished_at"])
	assert.Equal(t, "success", job["state"])
}

func TestHandleListJobs_NoFinishLineMeansInProgress(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver"}, Values: []lokiValue{
				{Timestamp: 1752400500000000000, Metadata: map[string]string{"job_id": "policy-update:1752400500"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	job := body["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "in_progress", job["state"])
	assert.Nil(t, job["finished_at"])
}

func TestHandleListJobs_BackupJobUsesFinishLineHostAsStoreHost(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "database"}, Values: []lokiValue{
				{Timestamp: 1752400000000000000, Metadata: map[string]string{"job_id": "backup:nightly:var-www:abcd1234:1752400000"}},
			}},
		},
		`{binary=~"brfs|bwfs"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "bwfs-east"}, Values: []lokiValue{
				{Timestamp: 1752400010000000000, Metadata: map[string]string{"job_id": "backup:nightly:var-www:abcd1234:1752400000", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=backup", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	job := body["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "database", job["source_host"])
	assert.Equal(t, "bwfs-east", job["store_host"])
}

func TestHandleListJobs_InvalidKindReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=not-a-real-kind", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListJobs_WindowExceeding168hReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	now := time.Now()
	since := now.Add(-200 * time.Hour).Unix()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?since="+itoa(since), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListJobs_LokiErrorReturns502(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{err: assert.AnError}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func itoa(n int64) string {
	return time.Unix(n, 0).Format("") + fmtInt(n) // placeholder replaced below
}
```

Replace the placeholder `itoa` helper above with a real one — Go's `strconv.FormatInt` — by adding `"strconv"` to the imports and defining:

```go
func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
```

(Remove the bogus `time.Unix(n, 0).Format("") + fmtInt(n)` line entirely; it was a placeholder to flag — the real body is the one-line `strconv.FormatInt` version above.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleListJobs -v`
Expected: FAIL — `handleListJobs`/`kindFromJobID`/`srv.loki` undefined

- [ ] **Step 3: Add the `loki` field to `server`**

In `src/cmd/api-server/server.go`, add the field (not a constructor parameter — see Task 13 for why) and register the route:

```go
type server struct {
	clientManager clientManagerClient
	catalog       catalogQueryClient
	policy        policyServiceClient
	loki          lokiQuerier
	logger        *slog.Logger
}
```

And in `registerRoutes`:

```go
func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clients", s.handleListClients)
	mux.HandleFunc("GET /api/v1/clients/{hostname}", s.handleGetClient)
	mux.HandleFunc("GET /api/v1/catalog", s.handleListCatalog)
	mux.HandleFunc("GET /api/v1/policies", s.handleListPolicies)
	mux.HandleFunc("GET /api/v1/policies/{id}", s.handleGetPolicy)
	mux.HandleFunc("POST /api/v1/policies", s.handleCreatePolicy)
	mux.HandleFunc("PUT /api/v1/policies/{id}", s.handleUpdatePolicy)
	mux.HandleFunc("DELETE /api/v1/policies/{id}", s.handleDeletePolicy)
	mux.HandleFunc("GET /api/v1/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/v1/jobs/{job_id}/logs", s.handleGetJobLogs)
}
```

(`handleGetJobLogs` is implemented in Task 12; declaring its route here now means this file's route table is complete in one place, matching the existing convention noted in `registerRoutes`'s own comment — `jobs.go` will define both handlers by the end of Task 12.)

- [ ] **Step 4: Implement `jobs.go`**

Create `src/cmd/api-server/jobs.go`:

```go
// src/cmd/api-server/jobs.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultJobsWindow = 24 * time.Hour
	maxJobsWindow      = 168 * time.Hour
	defaultJobsLimit   = 100
	maxJobsLimit       = 500
	jobsQueryLineLimit = 5000
)

var validJobKinds = map[string]bool{
	"backup":            true,
	"bootstrap-refresh": true,
	"operating-refresh": true,
	"policy-update":     true,
}

// kindFromJobID derives a job's kind from its own id, per the prefix
// convention agent/policy.go and agent/backup.go already established
// (e.g. "backup:nightly:var-www:abcd1234:1752400000",
// "operating-refresh:1752400500") -- no separate field needed anywhere.
func kindFromJobID(jobID string) string {
	if idx := strings.Index(jobID, ":"); idx >= 0 {
		return jobID[:idx]
	}
	return ""
}

// binariesForKind returns the Loki `binary` label regex to scope a query
// to, for a given (possibly empty) kind filter. kind=backup deliberately
// excludes "agent" -- see reconcile.go's isBackupPolicy and this repo's
// design doc for why agent never tags a scheduled backup dispatch with
// event/status.
func binariesForKind(kind string) string {
	switch kind {
	case "backup":
		return "brfs|bwfs"
	case "bootstrap-refresh", "operating-refresh", "policy-update":
		return "agent"
	default:
		return "agent|brfs|bwfs"
	}
}

type jobDTO struct {
	JobID      string  `json:"job_id"`
	Kind       string  `json:"kind"`
	SourceHost string  `json:"source_host"`
	StoreHost  *string `json:"store_host"`
	StartedAt  *int64  `json:"started_at"`
	FinishedAt *int64  `json:"finished_at"`
	State      string  `json:"state"`
}

// jobEventLine is one event=start or event=finish line, reduced to the
// fields pairJobEvents needs. Timestamp is unix seconds (Loki's own
// nanosecond wire unit, truncated -- sub-second precision is not
// meaningful for a job's started_at/finished_at).
type jobEventLine struct {
	JobID     string
	Hostname  string
	Timestamp int64
	Status    string
}

// queryEvent runs one Loki query scoped to labelSelector and the given
// event value, returning every matching (job_id, hostname, timestamp,
// status) line and whether the query hit its own line cap (in which case
// the window should be narrowed -- see the truncated flag on
// handleListJobs' response).
func (s *server) queryEvent(ctx context.Context, labelSelector, event string, since, until time.Time) ([]jobEventLine, bool, error) {
	query := fmt.Sprintf(`%s | event="%s"`, labelSelector, event)
	streams, err := s.loki.QueryRange(ctx, query, since, until, jobsQueryLineLimit)
	if err != nil {
		return nil, false, err
	}

	var lines []jobEventLine
	count := 0
	for _, stream := range streams {
		hostname := stream.Stream["hostname"]
		for _, v := range stream.Values {
			count++
			jobID := v.Metadata["job_id"]
			if jobID == "" {
				continue
			}
			lines = append(lines, jobEventLine{
				JobID:     jobID,
				Hostname:  hostname,
				Timestamp: v.Timestamp / 1_000_000_000,
				Status:    v.Metadata["status"],
			})
		}
	}
	return lines, count >= jobsQueryLineLimit, nil
}

// pairJobEvents groups start/finish lines by job_id into one jobDTO each.
// A job_id with only a start line is in_progress; one with only a finish
// line (its start fell outside the queried window) gets a nil StartedAt --
// never guessed. For kind=backup, StoreHost comes from the finish line's
// hostname (bwfs, the destination) while SourceHost comes from the start
// line's hostname (brfs, the real source) -- every other kind has a single
// SourceHost and a nil StoreHost.
func pairJobEvents(starts, finishes []jobEventLine) []jobDTO {
	byJobID := make(map[string]*jobDTO)
	var order []string

	get := func(jobID string) *jobDTO {
		j, ok := byJobID[jobID]
		if !ok {
			j = &jobDTO{JobID: jobID, Kind: kindFromJobID(jobID), State: "in_progress"}
			byJobID[jobID] = j
			order = append(order, jobID)
		}
		return j
	}

	for _, e := range starts {
		j := get(e.JobID)
		ts := e.Timestamp
		j.SourceHost = e.Hostname
		j.StartedAt = &ts
	}
	for _, e := range finishes {
		j := get(e.JobID)
		ts := e.Timestamp
		j.FinishedAt = &ts
		j.State = e.Status
		if j.Kind == "backup" {
			host := e.Hostname
			j.StoreHost = &host
		}
	}

	out := make([]jobDTO, 0, len(order))
	for _, id := range order {
		out = append(out, *byJobID[id])
	}
	return out
}

func sortKey(j jobDTO) int64 {
	if j.StartedAt != nil {
		return *j.StartedAt
	}
	if j.FinishedAt != nil {
		return *j.FinishedAt
	}
	return 0
}

func (s *server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	until := time.Now()
	if raw := q.Get("until"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "until must be a unix-second integer")
			return
		}
		until = time.Unix(parsed, 0)
	}
	since := until.Add(-defaultJobsWindow)
	if raw := q.Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "since must be a unix-second integer")
			return
		}
		since = time.Unix(parsed, 0)
	}
	if until.Before(since) {
		writeJSONError(w, http.StatusBadRequest, "until must not be before since")
		return
	}
	if until.Sub(since) > maxJobsWindow {
		writeJSONError(w, http.StatusBadRequest, "until-since must not exceed 168h")
		return
	}

	kind := q.Get("kind")
	if kind != "" && !validJobKinds[kind] {
		writeJSONError(w, http.StatusBadRequest, "kind must be one of backup, bootstrap-refresh, operating-refresh, policy-update")
		return
	}
	sourceHost := q.Get("source_host")
	stateFilter := q.Get("state")

	limit := defaultJobsLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxJobsLimit {
			writeJSONError(w, http.StatusBadRequest, "limit must be an integer between 1 and 500")
			return
		}
		limit = parsed
	}

	labelSelector := fmt.Sprintf(`{binary=~"%s"}`, binariesForKind(kind))
	if sourceHost != "" {
		labelSelector = fmt.Sprintf(`{binary=~"%s", hostname="%s"}`, binariesForKind(kind), sourceHost)
	}

	starts, startsTruncated, err := s.queryEvent(r.Context(), labelSelector, "start", since, until)
	if err != nil {
		s.logger.Error("handleListJobs: query start events failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}
	finishes, finishesTruncated, err := s.queryEvent(r.Context(), labelSelector, "finish", since, until)
	if err != nil {
		s.logger.Error("handleListJobs: query finish events failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}

	jobs := pairJobEvents(starts, finishes)

	filtered := make([]jobDTO, 0, len(jobs))
	for _, j := range jobs {
		if kind != "" && j.Kind != kind {
			continue
		}
		if sourceHost != "" && j.SourceHost != sourceHost {
			continue
		}
		if stateFilter != "" && j.State != stateFilter {
			continue
		}
		filtered = append(filtered, j)
	}
	sort.Slice(filtered, func(i, k int) bool { return sortKey(filtered[i]) > sortKey(filtered[k]) })
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": filtered, "truncated": startsTruncated || finishesTruncated})
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS (full package — confirms every pre-existing `newServer(...)` call site in `catalog_test.go`/`clients_test.go`/`policies_test.go` still compiles unchanged, since `loki` was added as a struct field, not a constructor parameter)

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go src/cmd/api-server/server.go
git commit -m "feat(api-server): add GET /api/v1/jobs"
```

---

## Task 12: `api-server` — `GET /api/v1/jobs/{job_id}/logs`

**Files:**
- Modify: `src/cmd/api-server/jobs.go` (append)
- Modify: `src/cmd/api-server/jobs_test.go` (append)

**Interfaces:**
- Consumes: `lokiQuerier` (Task 9/10), `jobEventLine`-adjacent helpers not needed — this handler queries directly by `job_id`.
- Produces: `(*server).handleGetJobLogs(w http.ResponseWriter, r *http.Request)` (already registered in Task 11's `registerRoutes` edit)

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/api-server/jobs_test.go`:

```go
func TestHandleGetJobLogs_ReturnsLinesSortedByTimestamp(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | job_id="operating-refresh:1752400500"`: {
			{Stream: map[string]string{"hostname": "webserver", "binary": "agent"}, Values: []lokiValue{
				{Timestamp: 1752400501000000000, Line: "policy execution completed"},
				{Timestamp: 1752400500000000000, Line: "policy execution started"},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/operating-refresh:1752400500/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 2)
	assert.Equal(t, "policy execution started", data[0].(map[string]any)["line"])
	assert.Equal(t, "policy execution completed", data[1].(map[string]any)["line"])
	assert.Equal(t, "webserver", data[0].(map[string]any)["hostname"])
	assert.Equal(t, "agent", data[0].(map[string]any)["binary"])
}

func TestHandleGetJobLogs_InvalidJobIDCharacterReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/not%20valid;job/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleGetJobLogs_SourceAndStoreHostNarrowLabelSelector(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs", hostname=~"database|bwfs-east"} | job_id="backup:nightly:var-www:abcd1234:1752400000"`: {
			{Stream: map[string]string{"hostname": "database", "binary": "brfs"}, Values: []lokiValue{
				{Timestamp: 1752400000000000000, Line: "Backup reader started"},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/backup:nightly:var-www:abcd1234:1752400000/logs?source_host=database&store_host=bwfs-east", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Len(t, body["data"].([]any), 1)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleGetJobLogs -v`
Expected: FAIL — `handleGetJobLogs` route currently 404s (registered but undefined) / build fails

- [ ] **Step 3: Implement**

Append to `src/cmd/api-server/jobs.go` — add `"regexp"` to the import block, then:

```go
var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9:_-]+$`)

type logLineDTO struct {
	Timestamp int64  `json:"timestamp"`
	Hostname  string `json:"hostname"`
	Binary    string `json:"binary"`
	Line      string `json:"line"`
}

func (s *server) handleGetJobLogs(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job_id")
	if !jobIDPattern.MatchString(jobID) {
		writeJSONError(w, http.StatusBadRequest, "job_id contains invalid characters")
		return
	}

	q := r.URL.Query()
	until := time.Now()
	since := until.Add(-defaultJobsWindow)
	if raw := q.Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "since must be a unix-second integer")
			return
		}
		since = time.Unix(parsed, 0)
	}

	sourceHost := q.Get("source_host")
	storeHost := q.Get("store_host")
	labelSelector := `{binary=~"agent|brfs|bwfs"}`
	switch {
	case sourceHost != "" && storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs", hostname=~"%s|%s"}`, sourceHost, storeHost)
	case sourceHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs", hostname="%s"}`, sourceHost)
	case storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs", hostname="%s"}`, storeHost)
	}

	query := fmt.Sprintf(`%s | job_id="%s"`, labelSelector, jobID)
	streams, err := s.loki.QueryRange(r.Context(), query, since, until, jobsQueryLineLimit)
	if err != nil {
		s.logger.Error("handleGetJobLogs: query failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}

	var lines []logLineDTO
	for _, stream := range streams {
		for _, v := range stream.Values {
			lines = append(lines, logLineDTO{
				Timestamp: v.Timestamp,
				Hostname:  stream.Stream["hostname"],
				Binary:    stream.Stream["binary"],
				Line:      v.Line,
			})
		}
	}
	sort.Slice(lines, func(i, k int) bool { return lines[i].Timestamp < lines[k].Timestamp })

	writeJSON(w, http.StatusOK, map[string]any{"data": lines})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS (full package)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go
git commit -m "feat(api-server): add GET /api/v1/jobs/{job_id}/logs"
```

---

## Task 13: `api-server` — wire the Loki client into `main.go`

**Files:**
- Modify: `src/cmd/api-server/main.go`

**Interfaces:**
- Consumes: `mtls.ClientTLSConfig` (Task 1), `newHTTPLokiClient` (Task 9), `newCachingLokiClient` (Task 10), `server.loki` field (Task 11)

- [ ] **Step 1: Modify `main.go`**

Add `"github.com/alex-sviridov/miniprotector/common/mtls"` to the import block. After the existing `policyConn` setup and before `srv := newServer(...)`:

```go
	policyConn, err := connection.Connect(conf.PolicyServerHost, conf.PolicyServerPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to policy-server failed", "error", err)
		os.Exit(1)
	}
	defer policyConn.Close()

	lokiTLSConfig, err := mtls.ClientTLSConfig(certsDir, conf.LogGatewayHost)
	if err != nil {
		logger.Error("loki client tls config failed", "error", err)
		os.Exit(1)
	}
	lokiHTTPClient := &http.Client{Transport: &http.Transport{TLSClientConfig: lokiTLSConfig}}
	lokiBaseURL := fmt.Sprintf("https://%s:%d", conf.LogGatewayHost, conf.LogGatewayPort)

	srv := newServer(pb.NewClientManagerServiceClient(cmConn), pb.NewCatalogServiceClient(catalogConn), pb.NewPolicyServiceClient(policyConn), logger)
	srv.loki = newCachingLokiClient(newHTTPLokiClient(lokiBaseURL, lokiHTTPClient), 10*time.Second)
```

(`fmt`, `net/http`, and `time` are already imported in `main.go`.)

- [ ] **Step 2: Verify it builds**

Run: `cd src && go build ./cmd/api-server/...`
Expected: builds cleanly

- [ ] **Step 3: Run the full package test suite**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add src/cmd/api-server/main.go
git commit -m "feat(api-server): wire the Loki client for GET /api/v1/jobs*"
```

---

## Task 14: Documentation

**Files:**
- Modify: `docs/api/rest-v1.md`
- Modify: `docs/components/api-server.md`
- Modify: `docs/components/log-gateway.md`
- Modify: `docs/protocols/log-gateway.md`
- Modify: `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: `docs/api/rest-v1.md`** — add two new sections after `## DELETE /api/v1/policies/{id}` and before `## See Also`:

```markdown
## `GET /api/v1/jobs`

Query parameters (all optional):

| Param | Type | Description |
|-------|------|--------------|
| `kind` | string | `backup` \| `bootstrap-refresh` \| `operating-refresh` \| `policy-update` |
| `source_host` | string | Exact match |
| `state` | string | `in_progress` \| `success` \| `failure` |
| `since` | unix seconds | Start of query window. Default: 24h before `until` |
| `until` | unix seconds | End of query window. Default: now |
| `limit` | int, 1–500 | Cap on returned jobs, default 100 |

`until - since` is capped at 168h (7 days) — `400` if exceeded.

```json
{
  "data": [
    {
      "job_id": "backup:nightly-db-backup:...:1752400000",
      "kind": "backup",
      "source_host": "database",
      "store_host": "bwfs-east",
      "started_at": 1752400000,
      "finished_at": 1752400010,
      "state": "success"
    }
  ],
  "truncated": false
}
```

Not cursor-paginated (unlike `/catalog`) — this result set is recomputed per query, not a stable
sequence. `truncated: true` means the underlying Loki query hit its own line cap and some jobs in
the window may be missing; narrow `since`/`until` and retry. `started_at` is `null` if a job's start
line fell outside the queried window; `finished_at`/`state: "in_progress"` if it hasn't finished yet.

## `GET /api/v1/jobs/{job_id}/logs`

| Param | Type | Description |
|-------|------|--------------|
| `since` | unix seconds | Only lines after this timestamp. Default: 24h before now |
| `source_host` / `store_host` | string | Optional — narrows the query to the hosts involved, if already known from a prior `/jobs` response |

`job_id` must match `^[a-zA-Z0-9:_-]+$` — `400` otherwise.

```json
{
  "data": [
    {"timestamp": 1752400000123456789, "hostname": "database", "binary": "brfs", "line": "{...raw json log line...}"}
  ]
}
```

A client polling with an advancing `since` cursor gets a near-real-time tail.
```

- [ ] **Step 2: `docs/components/api-server.md`** — in the `## Endpoints` section, note the exception:

```markdown
## Endpoints

See [REST API v1](../api/rest-v1.md) for the full endpoint reference. Every endpoint maps to exactly
one backend gRPC call except `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs`, which query
Loki (through `log-gateway`'s read-proxy route) and aggregate the result — the one deliberate
exception to that rule, documented in
[Design: /jobs REST Endpoint](../superpowers/specs/2026-07-19-jobs-endpoint-design.md).
```

Also add `log_gateway_host`/`log_gateway_port` to the `## Configuration Keys` list (they already exist in `local.conf` but were previously unused by `api-server`):

```markdown
- `log_gateway_host` / `log_gateway_port` — where to dial `log-gateway`'s Loki query-proxy route for `GET /api/v1/jobs*` *(default port: 9400)*
```

- [ ] **Step 3: `docs/components/log-gateway.md`** — add to its endpoint description (find the line describing the push-only behavior and add alongside it):

```markdown
`log-gateway` also proxies Loki's read path: `GET /loki/api/v1/query_range`, gated by the same
operating-tier mTLS check, forwarding query parameters unmodified. See
[log-gateway Protocol](../protocols/log-gateway.md) and
[Design: /jobs REST Endpoint](../superpowers/specs/2026-07-19-jobs-endpoint-design.md).
```

- [ ] **Step 4: `docs/protocols/log-gateway.md`** — add a new section after the existing `## Response` section:

```markdown
## `GET /loki/api/v1/query_range`

Same mTLS operating-tier gate as the push path. Query parameters are forwarded to Loki's real
`query_range` endpoint unmodified; the response body is forwarded back unmodified, capped at 10MB
(`502 Bad Gateway` if exceeded or if Loki is unreachable). `401 Unauthorized` if no verified peer
certificate was presented. `405 Method Not Allowed` for anything other than `GET`. Added for
`api-server`'s `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` — see
[Design: /jobs REST Endpoint](../superpowers/specs/2026-07-19-jobs-endpoint-design.md).
```

- [ ] **Step 5: `docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md`** — add a note near the Vector config section (`### Vector, bundled and supervised directly by agent`) pointing forward:

```markdown
> **Follow-up (2026-07-19):** Vector's config additionally lifts `job_id`/`event`/`status` into Loki
> structured metadata (not labels — avoids the per-job stream-cardinality problem this document's
> own Data Flow section already flags for `job_id`). See
> [Design: /jobs REST Endpoint](2026-07-19-jobs-endpoint-design.md).
```

- [ ] **Step 6: Commit**

```bash
git add docs/api/rest-v1.md docs/components/api-server.md docs/components/log-gateway.md docs/protocols/log-gateway.md docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md
git commit -m "docs: document GET /api/v1/jobs, /jobs/{job_id}/logs, and the log-gateway read proxy"
```

---

## Task 15: Changelog

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add a dated entry at the top**

Prepend to `CHANGELOG.md` (check the file's existing heading format first and match it exactly):

```markdown
## 2026-07-19

Added `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` to `api-server`, giving a fleet-wide
view of every job kind (backups, cert-refresh, policy-fetch) with start/end/source/state, plus
near-real-time per-job log tailing. Both are backed by Loki rather than a new database: `bwfs`,
`brfs`, and `agent` now tag each job's lifecycle boundary lines with `event`/`status`, and `agent`'s
bundled Vector lifts `job_id`/`event`/`status` into Loki structured metadata rather than plain
labels, avoiding the per-job stream-cardinality problem a naive `job_id` label would cause.
`log-gateway` gained a matching read-only proxy route onto Loki's query API alongside its existing
push proxy.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog entry for the jobs endpoint"
```

---

## Self-Review Notes

- **Spec coverage:** every spec section has a task — structured-metadata fields (Tasks 2–5), `log-gateway` read proxy (Task 6), Loki config bounds (Task 8), `GET /api/v1/jobs` (Task 11), `GET /api/v1/jobs/{job_id}/logs` (Task 12), performance (TTL cache in Task 10, label narrowing baked into Tasks 11/12's query construction), security (`job_id` charset validation in Task 12), testing (integration/e2e in Tasks 2 and 7), documentation (Task 14), changelog (Task 15).
- **Placeholder scan:** Task 11's first draft of `jobs_test.go` included a bogus `itoa` helper as a deliberately-flagged placeholder — replaced inline with the real one-line `strconv.FormatInt` implementation in that same step, not left dangling.
- **Type consistency:** `lokiQuerier`/`lokiStream`/`lokiValue` (Task 9) are used identically in Task 10 (`cachingLokiClient`), Task 11 (`queryEvent`), and Task 12 (`handleGetJobLogs`) — same field names (`Stream`, `Values`, `Timestamp`, `Line`, `Metadata`) throughout. `server.loki` (Task 11) is the same field Task 13 assigns in `main.go`. `jobDTO`'s JSON field names match `docs/api/rest-v1.md` exactly (Task 14).
