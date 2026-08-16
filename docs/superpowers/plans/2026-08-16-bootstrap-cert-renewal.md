# Bootstrap Certificate Renewal Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix bootstrap certificates silently getting step-ca's 24h default instead of the configured
~90-day `BootstrapCertTTLSec`, and make a node's `bootstrap-refresh` failures visible centrally before
the node goes fully dark.

**Architecture:** `certclient bootstrap()` starts requesting an explicit certificate duration on its
`Sign` call (step-ca's `/renew` then carries that duration forward on every future renewal
automatically — no other client-side change needed). Production's CA provisioner gains the ceiling to
actually grant it. Separately, `policyclient fetch` does a best-effort local read of `agent-state.json`
and reports any current `bootstrap-refresh` failure on its already-happening, already-authenticated
`GetPolicies` call — a channel that stays valid for up to `OperatingCertTTLSec` after
`bootstrap-refresh` starts failing. `policy-server` records it (once per call, unconditionally — not
inside the per-matched-policy checkin loop, which would silently skip hosts with no policies) and
serves it back through one new small RPC, proxied by one new `api-server` route.

**Tech Stack:** Go, gRPC/protobuf, GORM + SQLite (`storage/policyserver`), `testify` for Go tests.

## Global Constraints

- No backward compatibility required for any proto/schema/signature change in this plan.
- Every proto change ships with its `docs/protocols/` update in the same commit, per
  `.claude/CLAUDE.md`.
- Every feature change updates the relevant `docs/components/<component>.md` in the same commit.
- `CHANGELOG.md` gets one entry before this branch merges (last task).
- This plan does **not** touch `certclient renew()` — confirmed unnecessary: step-ca's `/renew` request
  body is `http.NoBody` (no parameters to send), and `authority/tls.go`'s `renewContext` always copies
  the *original* certificate's own duration forward. Fixing `bootstrap()`'s initial `Sign` request is
  sufficient for the whole lineage.
- This plan does **not** touch the web UI. The status becomes queryable via `api-server`; rendering it
  is an explicit follow-on.

---

## File Structure

| File | Responsibility |
|---|---|
| `src/api/policyserver.proto` | `GetPoliciesRequest` gains 2 fields; new `GetNodeCertStatus` RPC + `NodeCertStatus`/`GetNodeCertStatusRequest` messages |
| `src/cmd/certclient/bootstrap.go` | `bootstrap()` requests an explicit certificate duration |
| `src/cmd/certclient/main.go` | Passes `conf.BootstrapCertTTLSec` into `bootstrap()` |
| `deploy/control-plane/ca/entrypoint.sh` | Provisioner gains `--x509-max-dur` so the requested duration is actually granted |
| `src/cmd/policyclient/fetch.go` | Best-effort read of `agent-state.json`'s `bootstrap-refresh` entry, reported on the existing `GetPolicies` call |
| `src/storage/policyserver/models.go` | `NodeCertStatus` GORM model |
| `src/storage/policyserver/store.go` | `RecordCertStatus`/`CertStatusForHost` methods |
| `src/cmd/policy-server/server.go` | `GetPolicies` records status unconditionally; new `GetNodeCertStatus` handler |
| `src/cmd/api-server/server.go`, `policies.go` | New `GET /api/v1/clients/{hostname}/cert-status` route |

---

### Task 1: Proto — `GetPoliciesRequest` fields + `GetNodeCertStatus` RPC

**Files:**
- Modify: `src/api/policyserver.proto`
- Modify (generated via `make proto`, do not hand-edit): `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go`
- Modify: `docs/protocols/policy-server.md`

**Interfaces:**
- Produces: `pb.GetPoliciesRequest.BootstrapRefreshLastError`/`.BootstrapRefreshLastAttemptAt` (getters
  `GetBootstrapRefreshLastError()`/`GetBootstrapRefreshLastAttemptAt()`); `pb.NodeCertStatus{Hostname,
  LastError, LastAttemptAt}`; `pb.GetNodeCertStatusRequest{Hostname}`;
  `pb.PolicyServiceClient.GetNodeCertStatus(ctx, *GetNodeCertStatusRequest, ...) (*NodeCertStatus,
  error)` and the matching server-side `PolicyServiceServer.GetNodeCertStatus` method.

- [ ] **Step 1: Add the two fields to `GetPoliciesRequest`**

Edit `src/api/policyserver.proto`, replacing:

```proto
message GetPoliciesRequest {}
```

with:

```proto
// Set only when this node's bootstrap-refresh task is currently failing
// (agent-state.json's "bootstrap-refresh" entry has a non-empty
// last_error) -- see docs/superpowers/specs/
// 2026-08-16-bootstrap-cert-renewal-design.md. Empty means either
// healthy or nothing to report; policy-server records whatever is sent,
// healthy or not, so a recovery is visible too.
message GetPoliciesRequest {
  string bootstrap_refresh_last_error      = 1;
  int64  bootstrap_refresh_last_attempt_at = 2; // unix seconds; 0 = not reported
}
```

- [ ] **Step 2: Add `GetNodeCertStatus` and its messages**

In the same file, add the RPC to the existing `service PolicyService { ... }` block (alongside
`GetPolicies`/`ListPolicies`/etc.):

```proto
  rpc GetNodeCertStatus(GetNodeCertStatusRequest) returns (NodeCertStatus);
```

And these new messages (place near `PolicyCheckin`, which they're conceptually adjacent to):

```proto
message GetNodeCertStatusRequest {
  string hostname = 1; // required
}

// NodeCertStatus is a node-wide property, unlike PolicyCheckin (scoped to
// (policy_id, hostname) pairs) -- bootstrap-refresh is agent's own
// built-in task, never a policy fetched from policy-server, so there is
// no policy_id to key this on. hostname with no reported status ever
// returns a NodeCertStatus with empty LastError and a zero LastAttemptAt
// -- not an error.
message NodeCertStatus {
  string hostname = 1;
  string last_error = 2; // "" = healthy or never reported
  google.protobuf.Timestamp last_attempt_at = 3;
}
```

- [ ] **Step 3: Regenerate and verify**

```bash
make proto
cd src && go build ./... && cd ..
```

Expected: no errors. `pb.PolicyServiceServer` now requires a `GetNodeCertStatus` method — this will
break the build until Task 6 implements it on `policyServerServer`; that's expected and resolved by
Task 6, not this one (confirm the *proto/generated code* itself compiles standalone by checking
`go build ./api/...` succeeds even if `./cmd/policy-server/...` doesn't yet).

- [ ] **Step 4: Update `docs/protocols/policy-server.md`**

Find `GetPoliciesRequest`'s existing proto block and add the two new fields with the same one-line
comment style already used there. Add a new section for `GetNodeCertStatus`: request/response shape,
"hostname with no reported status returns an empty-but-present result, not an error," and a
cross-reference to `docs/superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md`.

- [ ] **Step 5: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go docs/protocols/policy-server.md
git commit -m "feat(api): add GetNodeCertStatus RPC and GetPoliciesRequest status fields"
```

---

### Task 2: `certclient` — request the correct bootstrap TTL

**Files:**
- Modify: `src/cmd/certclient/bootstrap.go`
- Modify: `src/cmd/certclient/main.go`
- Modify: `src/cmd/certclient/bootstrap_test.go`
- Modify: `docs/components/certclient.md`

**Interfaces:**
- Consumes: none from other tasks (independent of Task 1's proto — this touches `api.SignRequest`, a
  third-party type, not this repo's proto).
- Produces: `bootstrap(token string, client signer, certsDir string, ttlSec int) error` — signature
  change, every call site must pass a TTL.

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/certclient/bootstrap_test.go`:

```go
func TestBootstrap_SetsRequestedNotAfter(t *testing.T) {
	root := loadFixtureCert(t, "ca.crt")
	leaf := loadFixtureCert(t, "client.crt")

	tok := makeTestToken(t, "test-host", []string{"test-host"}, root)
	signer := &fakeSigner{resp: fakeSignResponse(root, leaf, leaf)}
	certsDir := t.TempDir()

	before := time.Now()
	err := bootstrap(tok, signer, certsDir, 3600) // 1 hour
	after := time.Now()
	require.NoError(t, err)

	require.NotNil(t, signer.gotReq)
	gotNotAfter := signer.gotReq.NotAfter.Time()
	assert.True(t, !gotNotAfter.Before(before.Add(3600*time.Second)), "NotAfter must be at least ttlSec out from before start")
	assert.True(t, !gotNotAfter.After(after.Add(3600*time.Second)), "NotAfter must be at most ttlSec out from after start")
}
```

- [ ] **Step 2: Update the four existing test call sites to compile**

In the same file, `TestBootstrap_WritesIdentityFiles`, `TestBootstrap_SignErrorPropagates`,
`TestBootstrap_InvalidTokenErrors`, and `TestBootstrap_SetsBootstrapTierTemplateData` each call
`bootstrap(tok, signer, certsDir)` (or `bootstrap("not-a-real-token", &fakeSigner{}, certsDir)`). Add a
fourth argument to each — `7776000` (90 days, matching `BootstrapCertTTLSec`'s default; the exact value
doesn't matter for these tests, they aren't testing TTL behavior) — so the file compiles.

- [ ] **Step 3: Run the tests to verify the new one fails, the others fail to compile**

```bash
cd src && go test ./cmd/certclient/... -run TestBootstrap -v && cd ..
```

Expected: build failure (`bootstrap` signature mismatch) — confirms Step 2's edits are necessary and
Step 4 hasn't happened yet.

- [ ] **Step 4: Implement**

Edit `src/cmd/certclient/bootstrap.go`:

```go
func bootstrap(token string, client signer, certsDir string, ttlSec int) error {
	req, pk, err := ca.CreateSignRequest(token)
	if err != nil {
		return fmt.Errorf("create sign request: %w", err)
	}
	req.NotAfter = api.NewTimeDuration(time.Now().Add(time.Duration(ttlSec) * time.Second))

	templateData, err := json.Marshal(struct {
		Tier string `json:"tier"`
	}{Tier: "bootstrap"})
	if err != nil {
		return fmt.Errorf("marshal template data: %w", err)
	}
	req.TemplateData = templateData

	sign, err := client.Sign(req)
	if err != nil {
		return fmt.Errorf("sign request: %w", err)
	}

	return writeIdentity(certsDir, sign, pk)
}
```

Add `"time"` to the file's imports.

Edit `src/cmd/certclient/main.go`'s `"bootstrap"` case, changing:

```go
if err := bootstrap(tok, client, certsDir); err != nil {
```

to:

```go
if err := bootstrap(tok, client, certsDir, conf.BootstrapCertTTLSec); err != nil {
```

(`conf` is already in scope at this call site — no new config loading needed.)

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd src && go test ./cmd/certclient/... -run TestBootstrap -v && cd ..
```

Expected: PASS, all 5 tests.

- [ ] **Step 6: Run the full `certclient` package test suite**

```bash
cd src && go test ./cmd/certclient/... && cd ..
```

Expected: PASS.

- [ ] **Step 7: Update `docs/components/certclient.md`**

Find the `bootstrap` subcommand's description (near the existing `renew`/`operating-refresh`
descriptions) and add a sentence: the `Sign` request now includes an explicit `NotAfter` derived from
`BootstrapCertTTLSec`, so the issued certificate actually gets the configured lifetime instead of
step-ca's own 24-hour default — cross-reference
`docs/superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md`.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/certclient/bootstrap.go src/cmd/certclient/main.go src/cmd/certclient/bootstrap_test.go docs/components/certclient.md
git commit -m "fix(certclient): request BootstrapCertTTLSec instead of taking step-ca's 24h default"
```

---

### Task 3: production CA provisioner gains a real max duration

**Files:**
- Modify: `deploy/control-plane/ca/entrypoint.sh`

**Interfaces:**
- Consumes: none.
- Produces: no code interface — an operational config change. Task 2's fixed client requests
  `BootstrapCertTTLSec` (2160h/90 days by default); without this, step-ca's provisioner ceiling stays
  at its own unconfigured default (`MaxTLSDur = 24h`, from the pinned `smallstep/certificates` library),
  and every bootstrap enrollment fails outright in production.

  > **Corrected after final review.** This originally said step-ca would "silently clamp the request
  > straight back down to 24h." It does not clamp — `authority/provisioner/sign_options.go`'s
  > `validityValidator.Valid` in the pinned `smallstep/certificates@v0.30.2` returns a Forbidden
  > error when the requested duration exceeds the ceiling, failing the whole `Sign`/`Renew` call.
  > The task's actual change (adding `--x509-max-dur=2200h`) is unaffected and still correct; only
  > this rationale's description of the failure mode was wrong. See the design doc's Error Handling
  > section and `CHANGELOG.md` for the rollout-ordering consequence.

- [ ] **Step 1: Confirm the current entrypoint has no duration override**

```bash
grep -n "provisioner update" deploy/control-plane/ca/entrypoint.sh
```

Expected: one line, `step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl` — no `--x509-max-dur`. (`demo/ca/entrypoint.sh` already has `--x509-max-dur=2200h` and needs no change — confirm this too: `grep -n "x509-max-dur" demo/ca/entrypoint.sh` should show it already present.)

- [ ] **Step 2: Add the flag**

Edit `deploy/control-plane/ca/entrypoint.sh`, changing:

```sh
step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl
```

to:

```sh
step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl --x509-max-dur=2200h
```

(2200h matches the demo's existing value — comfortably above the 2160h/90-day default request, without
being so wide it hides a misconfigured `BootstrapCertTTLSec` from ever being noticed.)

- [ ] **Step 3: Commit**

```bash
git add deploy/control-plane/ca/entrypoint.sh
git commit -m "fix(deploy): raise production CA provisioner max cert duration to match bootstrap TTL"
```

---

### Task 4: `policyclient` — report `bootstrap-refresh` status on fetch

**Files:**
- Modify: `src/cmd/policyclient/fetch.go`
- Modify: `src/cmd/policyclient/fetch_test.go`
- Modify: `docs/components/policyclient.md`

**Interfaces:**
- Consumes: `agent-state.json`'s on-disk shape — `map[string]PolicyState` where `PolicyState` has
  `last_attempt_at` (`*time.Time`, may be null) and `last_error` (`string`, `omitempty`) — this is
  `cmd/agent/cache.go`'s `Cache`/`PolicyState` types' JSON shape, read here as a plain untyped-ish
  struct since `cmd/policyclient` cannot import `cmd/agent` (different `main` packages).
- Produces: `bootstrapRefreshFailure(varDir string) (lastError string, lastAttemptAt int64)`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policyclient/fetch_test.go` (check the file's existing imports first — it likely already
has `os`, `path/filepath`, `testing`, `require`/`assert` from other tests in the package; add only what's
missing):

```go
func TestBootstrapRefreshFailure_ReadsFailingEntry(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `{"bootstrap-refresh":{"last_attempt_at":"2026-08-16T12:00:00Z","last_error":"renew request: connection refused"}}`)

	lastErr, lastAt := bootstrapRefreshFailure(dir)
	assert.Equal(t, "renew request: connection refused", lastErr)
	assert.Equal(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC).Unix(), lastAt)
}

func TestBootstrapRefreshFailure_HealthyEntryReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `{"bootstrap-refresh":{"last_attempt_at":"2026-08-16T12:00:00Z","last_error":""},"operating-refresh":{"last_error":"unrelated failure"}}`)

	lastErr, lastAt := bootstrapRefreshFailure(dir)
	assert.Equal(t, "", lastErr, "a healthy bootstrap-refresh entry must report nothing, even if another task is failing")
	assert.Equal(t, int64(0), lastAt)
}

func TestBootstrapRefreshFailure_MissingKeyReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `{"operating-refresh":{"last_error":"unrelated"}}`)

	lastErr, lastAt := bootstrapRefreshFailure(dir)
	assert.Equal(t, "", lastErr)
	assert.Equal(t, int64(0), lastAt)
}

func TestBootstrapRefreshFailure_MissingFileReturnsEmpty(t *testing.T) {
	lastErr, lastAt := bootstrapRefreshFailure(t.TempDir()) // no agent-state.json written
	assert.Equal(t, "", lastErr)
	assert.Equal(t, int64(0), lastAt)
}

func TestBootstrapRefreshFailure_MalformedFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `not valid json`)

	lastErr, lastAt := bootstrapRefreshFailure(dir)
	assert.Equal(t, "", lastErr)
	assert.Equal(t, int64(0), lastAt)
}

func writeAgentState(t *testing.T, dir, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "agent-state.json"), []byte(content), 0o644))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./cmd/policyclient/... -run TestBootstrapRefreshFailure -v && cd ..
```

Expected: FAIL — `bootstrapRefreshFailure` doesn't exist yet.

- [ ] **Step 3: Implement `bootstrapRefreshFailure`**

Add to `src/cmd/policyclient/fetch.go`:

```go
// bootstrapRefreshFailure does a best-effort read of agent-state.json's
// "bootstrap-refresh" entry -- the one piece of agent's local state this
// binary has any reason to look at, so agent-state.json's other entries
// (backup/storage/restore task history) are decoded into but otherwise
// ignored. A missing file, unparseable JSON, or an absent/healthy
// "bootstrap-refresh" key are all "nothing to report", the same fail-safe
// direction agent's own readCache already takes for this identical file
// (cmd/agent/cache.go). This must never block the GetPolicies call that
// follows it -- every error path here returns cleanly, never panics or
// propagates.
func bootstrapRefreshFailure(varDir string) (lastError string, lastAttemptAt int64) {
	data, err := os.ReadFile(filepath.Join(varDir, "agent-state.json"))
	if err != nil {
		return "", 0
	}
	var cache map[string]struct {
		LastAttemptAt *time.Time `json:"last_attempt_at"`
		LastError     string     `json:"last_error,omitempty"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return "", 0
	}
	entry, ok := cache["bootstrap-refresh"]
	if !ok || entry.LastError == "" {
		return "", 0
	}
	if entry.LastAttemptAt != nil {
		lastAttemptAt = entry.LastAttemptAt.Unix()
	}
	return entry.LastError, lastAttemptAt
}
```

Add `"os"` and `"path/filepath"` to the file's imports.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd src && go test ./cmd/policyclient/... -run TestBootstrapRefreshFailure -v && cd ..
```

Expected: PASS, all 5 tests.

- [ ] **Step 5: Wire it into `runFetch`**

Edit `src/cmd/policyclient/fetch.go`'s `runFetch`, changing:

```go
func runFetch(ctx context.Context, client policyServiceClient, cachePath string, logger *slog.Logger) error {
	logger.Debug("fetching policies")
	resp, err := client.GetPolicies(ctx, &pb.GetPoliciesRequest{})
```

to:

```go
func runFetch(ctx context.Context, client policyServiceClient, cachePath string, logger *slog.Logger) error {
	logger.Debug("fetching policies")
	lastErr, lastAt := bootstrapRefreshFailure(filepath.Dir(cachePath))
	resp, err := client.GetPolicies(ctx, &pb.GetPoliciesRequest{
		BootstrapRefreshLastError:     lastErr,
		BootstrapRefreshLastAttemptAt: lastAt,
	})
```

`filepath.Dir(cachePath)` recovers the same `varDir` `main.go` already computed to build `cachePath`
(`filepath.Join(varDir, "policies-cache.json")`) — no new parameter needed on `runFetch` or its caller
`fetchAndCache`, and this is exactly the directory `agent-state.json` lives in too (`cmd/agent/main.go`
builds both paths from the same `varDir`, confirmed during design).

- [ ] **Step 6: Write a test proving the wiring**

Add to `fetch_test.go` (check the file's existing `fakePolicyServiceClient`-equivalent test double for
`runFetch` first — mirror its exact construction pattern rather than inventing a new one):

```go
func TestRunFetch_IncludesBootstrapRefreshStatusFromAgentState(t *testing.T) {
	dir := t.TempDir()
	writeAgentState(t, dir, `{"bootstrap-refresh":{"last_attempt_at":"2026-08-16T12:00:00Z","last_error":"renew request: connection refused"}}`)
	cachePath := filepath.Join(dir, "policies-cache.json")

	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{}} // adapt to this file's real fake type/field names
	require.NoError(t, runFetch(t.Context(), fake, cachePath, testLogger()))

	require.NotNil(t, fake.gotReq) // adapt to this file's real captured-request field name
	assert.Equal(t, "renew request: connection refused", fake.gotReq.GetBootstrapRefreshLastError())
	assert.Equal(t, time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC).Unix(), fake.gotReq.GetBootstrapRefreshLastAttemptAt())
}
```

The exact fake type name/fields and `testLogger()`-equivalent helper aren't known precisely without
reading the rest of `fetch_test.go` — read it first and adapt this test to whatever pattern
`runFetch`'s existing tests already use, rather than inventing a parallel one.

- [ ] **Step 7: Run the full `policyclient` package test suite**

```bash
cd src && go test ./cmd/policyclient/... && cd ..
```

Expected: PASS.

- [ ] **Step 8: Update `docs/components/policyclient.md`**

Note that `fetch` now also does a best-effort local read of `agent-state.json`'s `bootstrap-refresh`
entry and includes it on the `GetPolicies` call — cross-reference
`docs/superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md`.

- [ ] **Step 9: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go docs/components/policyclient.md
git commit -m "feat(policyclient): report bootstrap-refresh failures on the existing GetPolicies call"
```

---

### Task 5: `storage/policyserver` — `NodeCertStatus` table

**Files:**
- Modify: `src/storage/policyserver/models.go`
- Modify: `src/storage/policyserver/store.go`
- Modify: `src/storage/policyserver/store_test.go`

**Interfaces:**
- Consumes: none from other tasks.
- Produces: `NodeCertStatus{Hostname, LastError, LastAttemptAt}`;
  `(*Store).RecordCertStatus(ctx, hostname, lastError string, lastAttemptAt time.Time) error`;
  `(*Store).CertStatusForHost(ctx, hostname string) (NodeCertStatus, bool, error)` — `bool` is whether
  a row exists (false = never reported, distinct from an empty-but-recorded healthy status).

- [ ] **Step 1: Write the failing tests**

Add to `src/storage/policyserver/store_test.go`, mirroring the existing `CheckinRecord` tests' exact
style (`newTestStore(t)`, `t.Context()`, `assert`/`require`):

```go
func TestRecordCertStatus_ThenCertStatusForHost_RoundTrips(t *testing.T) {
	store := newTestStore(t)
	at := time.Now().Truncate(time.Second)

	require.NoError(t, store.RecordCertStatus(t.Context(), "host-a", "renew failed: timeout", at))

	got, found, err := store.CertStatusForHost(t.Context(), "host-a")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "host-a", got.Hostname)
	assert.Equal(t, "renew failed: timeout", got.LastError)
	assert.True(t, at.Equal(got.LastAttemptAt))
}

func TestRecordCertStatus_UpsertOverwritesRatherThanDuplicating(t *testing.T) {
	store := newTestStore(t)
	first := time.Now().Add(-time.Hour).Truncate(time.Second)
	second := time.Now().Truncate(time.Second)

	require.NoError(t, store.RecordCertStatus(t.Context(), "host-a", "first error", first))
	require.NoError(t, store.RecordCertStatus(t.Context(), "host-a", "second error", second))

	got, found, err := store.CertStatusForHost(t.Context(), "host-a")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "second error", got.LastError)
	assert.True(t, second.Equal(got.LastAttemptAt))
}

func TestRecordCertStatus_EmptyErrorOverwritesPriorFailure(t *testing.T) {
	store := newTestStore(t)
	failedAt := time.Now().Add(-time.Hour).Truncate(time.Second)
	recoveredAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.RecordCertStatus(t.Context(), "host-a", "renew failed", failedAt))
	require.NoError(t, store.RecordCertStatus(t.Context(), "host-a", "", recoveredAt))

	got, found, err := store.CertStatusForHost(t.Context(), "host-a")
	require.NoError(t, err)
	require.True(t, found, "a healthy report must still be recorded, not treated as nothing to store")
	assert.Equal(t, "", got.LastError, "recovery must actually clear the stale failure")
	assert.True(t, recoveredAt.Equal(got.LastAttemptAt))
}

func TestCertStatusForHost_UnknownHostReturnsNotFound(t *testing.T) {
	store := newTestStore(t)
	_, found, err := store.CertStatusForHost(t.Context(), "ghost")
	require.NoError(t, err)
	assert.False(t, found)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./storage/policyserver/... -run 'TestRecordCertStatus|TestCertStatusForHost' -v && cd ..
```

Expected: FAIL — `NodeCertStatus`/`RecordCertStatus`/`CertStatusForHost` don't exist yet.

- [ ] **Step 3: Add the model**

Edit `src/storage/policyserver/models.go`, adding:

```go
// NodeCertStatus is the most recently reported bootstrap-refresh status
// for hostname -- separate from CheckinRecord (scoped to (PolicyID,
// Hostname) pairs, tracking which policies a node is actively polling)
// because this is a node-wide property with no policy_id to key on:
// bootstrap-refresh is agent's own built-in task, never a policy fetched
// from policy-server. Absence of a row (see Store.CertStatusForHost's
// bool return) means "never reported", distinct from a present row with
// an empty LastError, which means "reported healthy as of LastAttemptAt".
type NodeCertStatus struct {
	Hostname      string `gorm:"primaryKey"`
	LastError     string
	LastAttemptAt time.Time
}
```

- [ ] **Step 4: Add the store methods and register the model**

Edit `src/storage/policyserver/store.go`:

In `New`, change `Models: []any{&CheckinRecord{}}` to `Models: []any{&CheckinRecord{}, &NodeCertStatus{}}`.

Add:

```go
// RecordCertStatus upserts hostname's current bootstrap-refresh status --
// called on every GetPolicies request, healthy or not, so a recovery
// (an empty-error report overwriting a stale failure) is captured, not
// left stuck. See docs/superpowers/specs/
// 2026-08-16-bootstrap-cert-renewal-design.md.
func (s *Store) RecordCertStatus(ctx context.Context, hostname, lastError string, lastAttemptAt time.Time) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_error", "last_attempt_at"}),
	}).Create(&NodeCertStatus{Hostname: hostname, LastError: lastError, LastAttemptAt: lastAttemptAt}).Error
}

// CertStatusForHost returns hostname's most recently recorded status.
// found is false when hostname has never called GetPolicies with this
// feature active -- distinct from a present row with an empty LastError
// (reported healthy).
func (s *Store) CertStatusForHost(ctx context.Context, hostname string) (NodeCertStatus, bool, error) {
	var out NodeCertStatus
	err := s.db.WithContext(ctx).Where("hostname = ?", hostname).First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NodeCertStatus{}, false, nil
	}
	if err != nil {
		return NodeCertStatus{}, false, err
	}
	return out, true, nil
}
```

Add `"errors"` and `"gorm.io/gorm"` to the file's imports if not already present (check first — `store.go`
already imports `"gorm.io/gorm"` for `*gorm.DB`, so likely only `"errors"` is new).

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd src && go test ./storage/policyserver/... -run 'TestRecordCertStatus|TestCertStatusForHost' -v && cd ..
```

Expected: PASS, all 4 tests.

- [ ] **Step 6: Run the full `storage/policyserver` package test suite**

```bash
cd src && go test ./storage/policyserver/... && cd ..
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/storage/policyserver/models.go src/storage/policyserver/store.go src/storage/policyserver/store_test.go
git commit -m "feat(storage): add NodeCertStatus table for bootstrap-refresh status"
```

---

### Task 6: `policy-server` — record status unconditionally + `GetNodeCertStatus`

**Files:**
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/server_test.go`
- Modify: `docs/components/policy-server.md`

**Interfaces:**
- Consumes: `Store.RecordCertStatus`/`.CertStatusForHost` (Task 5); `pb.GetPoliciesRequest`'s new
  fields, `pb.NodeCertStatus`/`pb.GetNodeCertStatusRequest` (Task 1).
- Produces: `GetPolicies` records cert status on every call; new
  `(*policyServerServer).GetNodeCertStatus(ctx, *pb.GetNodeCertStatusRequest) (*pb.NodeCertStatus, error)`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/server_test.go`, mirroring `TestGetPolicies_RecordsCheckinForEachMatchedPolicy`'s
existing setup pattern (`newTestServerWithPolicies`, `fakeAuthContext`) — read that test first to match
its exact shape:

```go
func TestGetPolicies_RecordsCertStatusOnEveryCall(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithPolicies(t, dir)

	req := &pb.GetPoliciesRequest{BootstrapRefreshLastError: "renew failed: timeout", BootstrapRefreshLastAttemptAt: time.Now().Unix()}
	_, err := srv.GetPolicies(fakeAuthContext(t, "host-a", nil), req)
	require.NoError(t, err)

	status, err := srv.GetNodeCertStatus(fakeAuthContext(t, "host-a", nil), &pb.GetNodeCertStatusRequest{Hostname: "host-a"})
	require.NoError(t, err)
	assert.Equal(t, "renew failed: timeout", status.GetLastError())
}

func TestGetPolicies_RecordsCertStatusEvenWithNoMatchingPolicies(t *testing.T) {
	// dir with zero policies matching "host-a" (an empty policies dir, or
	// one whose only policy's client_filters exclude host-a -- match
	// whatever pattern TestGetPolicies_EmptyFiltersMatchEveryone's sibling
	// "no match" test in this file already uses, if one exists; otherwise
	// an empty temp dir with no policy files is simplest).
	dir := t.TempDir()
	srv := newTestServerWithPolicies(t, dir)

	req := &pb.GetPoliciesRequest{BootstrapRefreshLastError: "renew failed: timeout", BootstrapRefreshLastAttemptAt: time.Now().Unix()}
	resp, err := srv.GetPolicies(fakeAuthContext(t, "host-a", nil), req)
	require.NoError(t, err)
	require.Empty(t, resp.GetPolicies(), "this test's premise is that nothing matches host-a")

	status, err := srv.GetNodeCertStatus(fakeAuthContext(t, "host-a", nil), &pb.GetNodeCertStatusRequest{Hostname: "host-a"})
	require.NoError(t, err)
	assert.Equal(t, "renew failed: timeout", status.GetLastError(), "status must be recorded even when GetPolicies matches nothing -- this is exactly the gap that ruled out piggybacking on RecordCheckin")
}

func TestGetPolicies_HealthyReportOverwritesPriorFailure(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithPolicies(t, dir)

	_, err := srv.GetPolicies(fakeAuthContext(t, "host-a", nil), &pb.GetPoliciesRequest{BootstrapRefreshLastError: "renew failed"})
	require.NoError(t, err)
	_, err = srv.GetPolicies(fakeAuthContext(t, "host-a", nil), &pb.GetPoliciesRequest{}) // healthy: no error fields set
	require.NoError(t, err)

	status, err := srv.GetNodeCertStatus(fakeAuthContext(t, "host-a", nil), &pb.GetNodeCertStatusRequest{Hostname: "host-a"})
	require.NoError(t, err)
	assert.Equal(t, "", status.GetLastError(), "a subsequent healthy GetPolicies call must clear the prior failure")
}

func TestGetNodeCertStatus_UnknownHostReturnsEmptyNotError(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServerWithPolicies(t, dir)

	status, err := srv.GetNodeCertStatus(fakeAuthContext(t, "whoever", nil), &pb.GetNodeCertStatusRequest{Hostname: "never-reported-host"})
	require.NoError(t, err)
	assert.Equal(t, "", status.GetLastError())
}
```

`TestGetPolicies_CertStatusStoreFailureDoesNotFailTheRPC` — read `TestGetPolicies_CheckinStoreFailureFailsTheRPC`
(the existing sibling test for the checkin path) first: that path is fatal (checkin failure fails the
whole RPC). This plan's cert-status recording is explicitly **non-fatal** — a different behavior. Write
this test in whatever way that existing test's failure-injection mechanism allows (likely a fake/failing
store double), asserting `GetPolicies` still returns `nil` error and the correct `Policies` even when
cert-status recording fails.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./cmd/policy-server/... -run 'TestGetPolicies_RecordsCertStatus|TestGetPolicies_HealthyReport|TestGetNodeCertStatus' -v && cd ..
```

Expected: FAIL — `GetNodeCertStatus` doesn't exist on `policyServerServer` yet, and `GetPolicies` doesn't
record cert status yet.

- [ ] **Step 3: Wire cert-status recording into `GetPolicies`, non-fatally**

Edit `src/cmd/policy-server/server.go`'s `GetPolicies`, adding a call after `hostname`/`jobID` are
resolved (outside the per-policy loop, so it runs exactly once regardless of how many policies match):

```go
	labels, err := mtls.PeerAttributes(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not read peer attributes", "hostname", hostname, "job_id", jobID, "error", err)
		return nil, err
	}

	if err := s.checkins.RecordCertStatus(ctx, hostname, req.GetBootstrapRefreshLastError(),
		time.Unix(req.GetBootstrapRefreshLastAttemptAt(), 0)); err != nil {
		s.logger.Error("GetPolicies: failed to record cert status", "hostname", hostname, "job_id", jobID, "error", err)
		// non-fatal: unlike RecordCheckin below, a cert-status recording
		// failure must not prevent this node from getting its policies.
	}

	now := time.Now()
```

The handler's parameter changes from `_ *pb.GetPoliciesRequest` to `req *pb.GetPoliciesRequest` (it's
now read).

- [ ] **Step 4: Implement `GetNodeCertStatus`**

Add to `src/cmd/policy-server/server.go`:

```go
func (s *policyServerServer) GetNodeCertStatus(ctx context.Context, req *pb.GetNodeCertStatusRequest) (*pb.NodeCertStatus, error) {
	certStatus, _, err := s.checkins.CertStatusForHost(ctx, req.GetHostname())
	if err != nil {
		s.logger.Error("GetNodeCertStatus: store read failed", "hostname", req.GetHostname(), "error", err)
		return nil, status.Error(codes.Internal, "failed to read cert status")
	}
	return &pb.NodeCertStatus{
		Hostname:      req.GetHostname(),
		LastError:     certStatus.LastError,
		LastAttemptAt: timestamppb.New(certStatus.LastAttemptAt),
	}, nil
}
```

The local variable is named `certStatus`, not `status`, to avoid shadowing the file's existing
`"google.golang.org/grpc/status"` import (already used for `status.Error(...)` elsewhere in this file,
e.g. the checkin-failure path).

`CertStatusForHost`'s `bool` (found/not-found) is intentionally unused here — an unknown host and a
known-healthy host both correctly produce an empty-`LastError` `NodeCertStatus`, per the message's own
documented "not an error" contract from Task 1.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd src && go test ./cmd/policy-server/... -run 'TestGetPolicies_RecordsCertStatus|TestGetPolicies_HealthyReport|TestGetNodeCertStatus' -v && cd ..
```

Expected: PASS, all 5 tests.

- [ ] **Step 6: Run the full `policy-server` package test suite**

```bash
cd src && go test ./cmd/policy-server/... && cd ..
```

Expected: PASS — including the pre-existing `TestGetPolicies_*` tests, unaffected by this change.

- [ ] **Step 7: Update `docs/components/policy-server.md`**

Note that `GetPolicies` now also records the caller's bootstrap-refresh status (unconditionally, once
per call — not tied to matched policies) and that a new `GetNodeCertStatus` RPC serves it back.
Cross-reference `docs/superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md`.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go docs/components/policy-server.md
git commit -m "feat(policy-server): record bootstrap-refresh status unconditionally, serve via GetNodeCertStatus"
```

---

### Task 7: `api-server` — new cert-status route

**Files:**
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/policies_test.go`
- Modify: `docs/components/api-server.md`
- Modify: `docs/api/rest-v1.md` (if this file documents the REST surface — check first: `grep -l
  "GET /api/v1/clients" docs/api/rest-v1.md`)

**Interfaces:**
- Consumes: `pb.PolicyServiceClient.GetNodeCertStatus` (Task 1, once Task 6 implements the server side —
  this task only needs the *client*-side generated method, already available after Task 1's `make
  proto`).
- Produces: `GET /api/v1/clients/{hostname}/cert-status`.

This handler is grouped with `policies.go` (handlers backed by `s.policy`), not `clients.go` (backed by
`s.clientManager`) — file organization in this codebase follows which backend a handler calls, not the
URL namespace, and this one calls `policy-server`.

- [ ] **Step 1: Write the failing test**

Read `src/cmd/api-server/policies_test.go`'s existing fake-policy-client pattern first (the same
`fakePolicyServiceClient` used by earlier restore-related tests this session) and add to it:

```go
func TestHandleGetClientCertStatus_ReturnsStatus(t *testing.T) {
	fake := &fakePolicyServiceClient{ /* however this file's fake is constructed */ }
	fake.certStatusResp = &pb.NodeCertStatus{Hostname: "host-a", LastError: "renew failed", LastAttemptAt: timestamppb.New(time.Unix(1723800000, 0))}

	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/clients/host-a/cert-status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var got certStatusDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "host-a", got.Hostname)
	assert.Equal(t, "renew failed", got.LastError)
	assert.Equal(t, int64(1723800000), got.LastAttemptAt)
}
```

The exact fake type's field for stubbing `GetNodeCertStatus`'s response (`certStatusResp` above is a
guess) needs to match whatever `fakePolicyServiceClient` in this file actually looks like — read it
first and adapt. If the fake needs a new method/field added to satisfy the now-larger
`policyServiceClient` interface (Step 3 below adds `GetNodeCertStatus` to it), add that too, following
the fake's existing per-RPC field-and-method pattern.

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd src && go test ./cmd/api-server/... -run TestHandleGetClientCertStatus -v && cd ..
```

Expected: FAIL — route doesn't exist, handler doesn't exist, `policyServiceClient` interface doesn't
have `GetNodeCertStatus` yet (build failure on the fake).

- [ ] **Step 3: Extend the `policyServiceClient` interface**

Edit `src/cmd/api-server/server.go`, adding to the existing interface:

```go
type policyServiceClient interface {
	ListPolicies(ctx context.Context, in *pb.ListPoliciesRequest, opts ...grpc.CallOption) (*pb.ListPoliciesResponse, error)
	CreatePolicy(ctx context.Context, in *pb.CreatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error)
	UpdatePolicy(ctx context.Context, in *pb.UpdatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error)
	DeletePolicy(ctx context.Context, in *pb.DeletePolicyRequest, opts ...grpc.CallOption) (*pb.DeletePolicyResponse, error)
	GetNodeCertStatus(ctx context.Context, in *pb.GetNodeCertStatusRequest, opts ...grpc.CallOption) (*pb.NodeCertStatus, error)
}
```

And register the route in `registerRoutes`:

```go
	mux.HandleFunc("GET /api/v1/clients/{hostname}/cert-status", s.handleGetClientCertStatus)
```

(placed alongside the other `/clients/{hostname}/...` routes, for readability — even though this
handler is implemented in `policies.go`, not `clients.go`).

- [ ] **Step 4: Implement the handler and DTO**

Add to `src/cmd/api-server/policies.go`:

```go
type certStatusDTO struct {
	Hostname      string `json:"hostname"`
	LastError     string `json:"last_error,omitempty"`
	LastAttemptAt int64  `json:"last_attempt_at,omitempty"`
}

func (s *server) handleGetClientCertStatus(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	resp, err := s.policy.GetNodeCertStatus(r.Context(), &pb.GetNodeCertStatusRequest{Hostname: hostname})
	if err != nil {
		s.logger.Error("handleGetClientCertStatus: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, certStatusDTO{
		Hostname:      resp.GetHostname(),
		LastError:     resp.GetLastError(),
		LastAttemptAt: resp.GetLastAttemptAt().AsTime().Unix(),
	})
}
```

A hostname that never reported returns `200` with `last_error`/`last_attempt_at` omitted (proto3's zero
values — empty string, nil `Timestamp` whose `.AsTime().Unix()` is `0`) rather than a `404`, matching
`GetNodeCertStatus`'s own "not an error" contract from Task 1.

- [ ] **Step 5: Run the test to verify it passes**

```bash
cd src && go test ./cmd/api-server/... -run TestHandleGetClientCertStatus -v && cd ..
```

Expected: PASS.

- [ ] **Step 6: Run the full `api-server` package test suite**

```bash
cd src && go test ./cmd/api-server/... && cd ..
```

Expected: PASS.

- [ ] **Step 7: Update `docs/components/api-server.md`, and `docs/api/rest-v1.md` if it exists**

Document the new route: method, path, response shape, "absent fields mean never reported, not an
error." Cross-reference `docs/superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md`.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go docs/components/api-server.md
git commit -m "feat(api-server): add GET /api/v1/clients/{hostname}/cert-status"
```

(Add `docs/api/rest-v1.md` to this commit too if Step 7 touched it.)

---

### Task 8: Documentation closeout + Changelog

**Files:**
- Modify: `docs/SECURITY.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update `docs/SECURITY.md`**

Find the credential table's `BootstrapCertTTLSec` cell (currently: "Long, governed entirely by the CA
provisioner's own claims today — `BootstrapCertTTLSec` (~90 days by default) is parsed and defaulted
but not yet consumed by any request path (tracked follow-up)"). Update it to reflect that it's now
consumed: `bootstrap`'s `Sign` request sets `NotAfter` from it, and step-ca's `/renew` carries that
duration forward for the credential's whole lineage.

Add a short paragraph near the existing revocation-latency/`issuer`-unreachable discussion, covering the
*other* failure mode this plan addresses: a bootstrap credential that's allowed to actually lapse
(missed renewals exceeding its lifetime) is unrecoverable via `/renew` — an expired client certificate
is rejected at the TLS handshake, before any application code runs — and requires a fresh `certclient
bootstrap` with a new enrollment token. Note the failure is now visible via `GetNodeCertStatus`
(`GET /api/v1/clients/{hostname}/cert-status`) for up to `OperatingCertTTLSec` after `bootstrap-refresh`
starts failing, since reporting rides the independently-scheduled operating credential.

Add a one-line **rollout note**: this fix is forward-only. A node's certificate lineage keeps whatever
duration its *original* `bootstrap` call was granted — existing enrolled nodes stay on their current
(pre-fix, ~24h) lineage until re-bootstrapped with a fresh enrollment token.

- [ ] **Step 2: Add the changelog entry**

Add to `CHANGELOG.md`, most recent first, matching the file's existing heading format:

```markdown
## 2026-08-16 — bootstrap certificates get their real TTL, renewal failures become visible

`certclient bootstrap` was never requesting a certificate duration, so every bootstrap credential
silently got step-ca's own 24-hour default instead of the configured ~90-day `BootstrapCertTTLSec` —
and because step-ca's renewal always carries a certificate's *original* duration forward, that 24-hour
window applied to every future renewal too, leaving essentially no safety margin between "renewed" and
"expired unrecoverably." Newly-bootstrapped nodes now get the intended lifetime; production's CA
provisioner gained the ceiling to actually grant it. Separately, when a node's daily renewal starts
failing, that's now visible centrally (`GET /api/v1/clients/{hostname}/cert-status`) via the one
channel still guaranteed to work in that window, instead of only in logs the node may soon be unable to
ship at all. Existing enrolled nodes keep their current certificate lineage until re-bootstrapped with a
fresh enrollment token — this fix does not apply retroactively.
```

- [ ] **Step 3: Commit**

```bash
git add docs/SECURITY.md CHANGELOG.md
git commit -m "docs: close out bootstrap cert renewal follow-up, changelog entry"
```

---

## Self-Review Notes

- **Spec coverage:** Design's Part A (TTL fix) → Tasks 2, 3. Part B (failure visibility) → Tasks 1, 4,
  5, 6, 7. Documentation Impact section → folded into each task's own doc-update step, plus Task 8 for
  `docs/SECURITY.md`/`CHANGELOG.md` specifically. The design's corrected "one new RPC + one new route"
  (from the mid-brainstorm gap found while grounding the plan) is fully reflected in Tasks 1, 6, 7 —
  nothing reverted to the earlier, unworkable "extend an existing DTO" idea.
- **Non-goals respected:** no task touches `certclient renew()`; no task touches web/; no task builds a
  generic multi-task status-reporting mechanism (scoped to `bootstrap-refresh` only, per the design's
  explicit narrowing).
- **Placeholder scan:** every step has real code. Two spots explicitly instruct reading existing test
  file conventions before writing new code that must match them exactly (Task 4 Step 6's fake-client
  field names, Task 7 Step 1's fake-client field names) — flagged honestly, per the same pattern used in
  the prior plan this session, rather than guessing field names that weren't read during planning.
  Task 6 Step 4 flags a real naming collision (`status` package vs. local variable) explicitly rather
  than silently producing code that wouldn't compile.
- **Type consistency:** `bootstrapRefreshFailure`'s `(string, int64)` return (Task 4) matches exactly
  how it's consumed in `runFetch`'s `GetPoliciesRequest` construction, immediately below it in the same
  task. `RecordCertStatus`/`CertStatusForHost`'s signatures (Task 5) match exactly how Task 6's
  `GetPolicies`/`GetNodeCertStatus` call them. `certStatusDTO`'s fields (Task 7) match exactly what
  `GetNodeCertStatus`'s response (Task 1/6) provides.
