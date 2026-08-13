# Restore Verification E2E Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix a small `api-server` validation gap (`GET /api/v1/jobs?kind=restore` currently 400s) and add a browser-driven Playwright suite that proves restore-policy verification actually works end to end — a real file verifies successfully, and a rule naming a file that was never backed up fails — reading both outcomes from the real, rendered job log.

**Architecture:** `src/cmd/api-server/jobs.go` gains `"restore"` in its `kind` allowlist and its Loki-binary-selector switch (restore tasks are logged by `agent` itself, same as `bootstrap-refresh`/`operating-refresh`/`policy-update`). `web/e2e/helpers/policySeeding.js`'s existing job-polling helper is generalized from a hardcoded `'success'` wait into a `waitForJobState(page, name, state)` any scenario can call. A new `web/e2e/restore-verify.spec.js`, structured as one `test()` with two `test.step()`s (mirroring `restore-cart.spec.js`'s existing pattern), drives the real UI for the success case and reads the resulting job's log through the real `LogLine.vue` rendering; the failure case creates its restore policy via a direct API call (Playwright's `page.request`) since there's no UI affordance to select a file that was never backed up, then reads its job log the same browser-driven way.

**Tech Stack:** Go (`api-server`, `testify`), Playwright (`@playwright/test`, already a `web` devDependency), run against the already-running `make demo-up` demo lab at `http://localhost:8091`.

## Global Constraints

- **Precondition for every live-verification step in this plan:** `make demo-up` must already be running. Nothing in this plan starts, stops, or manages the demo lab.
- Demo web UI base URL: `http://localhost:8091`. `api-server` itself listens on `:8090`, but is only reached through `web/nginx.conf`'s `/api/` → `http://api-server:8090/api/` proxy — Playwright's `page.request` calls hit the same `http://localhost:8091/api/v1/...` origin the SPA itself uses, no separate base URL needed.
- Auth: `localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')` via `context.addInitScript`, same as `restore-cart.spec.js` — no UI login flow. For `page.request` calls (which don't read `localStorage`), send `Authorization: Bearer dev-placeholder-token-change-me` explicitly.
- `data-test="..."` is this repo's test-hook convention (`playwright.config.js` already sets `testIdAttribute: 'data-test'`).
- Seed fixture (from `seedRestoreCartCatalogData`, unchanged): host `database`, path `/var/lib/dbdata`, files `dump.sql`/`schema.sql`, backed up to the demo's `store` storage policy.
- **Client-side navigation only, until after the restore policy is submitted.** `restoreCart`'s selection state is in-memory only; `page.goto()`/`page.reload()` tears down the whole JS context and silently wipes it — see `restore-cart.spec.js`'s own comment on this. Every navigation in the success scenario, from checking the file's checkbox through reading `submission-results`, must be a real link/row click. Once the policy is created, cart state no longer matters and `page.goto()`/reload-polling (as `waitForJobState` already does) is fine.
- The failure scenario's restore policy is one-shot-until-success (`docs/components/agent.md`, "Policy-driven restore verification") and can never succeed — it names a file that doesn't exist. It **must** be deleted (`DELETE /api/v1/policies/{id}`) at the end of its own scenario, or it retries with backoff forever after the test run ends.
- No `mode`/intent field on the restore policy schema, no actual-restore execution, no precise backoff-timing assertion — all explicitly out of scope (see the design's Non-Goals).
- No `Makefile`/`package.json` wiring needed: `web/playwright.config.js`'s `testDir: './e2e'` already globs every `*.spec.js` file, so `npx playwright test` (and `make test-e2e`, which already runs it) picks up the new spec automatically.

---

## Task 1: `api-server` — fix the `kind=restore` validation gap

**Files:**
- Modify: `src/cmd/api-server/jobs.go:23-28` (`validJobKinds`), `:46-55` (`binariesForKind`), `:214` (error message)
- Test: `src/cmd/api-server/jobs_test.go`

**Interfaces:**
- Produces: `validJobKinds["restore"] == true`; `binariesForKind("restore") == "agent"`. Not consumed by any later task in this plan directly (the Playwright suite locates jobs by policy-name text, not by querying `?kind=restore`) — this stands alone as the correctness fix the design calls for, verified by its own Go tests.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/jobs_test.go`, after `TestKindFromJobID`:

```go
func TestBinariesForKind(t *testing.T) {
	assert.Equal(t, "brfs|bwfs", binariesForKind("backup"))
	assert.Equal(t, "agent", binariesForKind("bootstrap-refresh"))
	assert.Equal(t, "agent", binariesForKind("operating-refresh"))
	assert.Equal(t, "agent", binariesForKind("policy-update"))
	assert.Equal(t, "agent", binariesForKind("restore"))
	assert.Equal(t, "agent|brfs|bwfs", binariesForKind(""))
}
```

Add to `src/cmd/api-server/jobs_test.go`, after `TestHandleListJobs_InvalidKindReturns400`:

```go
func TestHandleListJobs_KindRestoreIsAccepted(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=restore", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleListJobs_RestoreKindUsesAgentBinaryLabel(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent"} | event="start"`: {
			{Stream: map[string]string{"hostname": "database"}, Values: []lokiValue{
				{Timestamp: 1752400500000000000, Metadata: map[string]string{"job_id": "restore:e2e-restore-verify:1752400500"}},
			}},
		},
		`{binary=~"agent"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "database"}, Values: []lokiValue{
				{Timestamp: 1752400501000000000, Metadata: map[string]string{"job_id": "restore:e2e-restore-verify:1752400500", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=restore", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	job := data[0].(map[string]any)
	assert.Equal(t, "restore", job["kind"])
	assert.Equal(t, "success", job["state"])
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestBinariesForKind|TestHandleListJobs_KindRestoreIsAccepted|TestHandleListJobs_RestoreKindUsesAgentBinaryLabel' -v`

Expected: `TestBinariesForKind` FAILs on the `"restore"` case (`binariesForKind("restore")` currently returns the default `"agent|brfs|bwfs"`, not `"agent"`); `TestHandleListJobs_KindRestoreIsAccepted` FAILs (`400`, not `200` — `validJobKinds` lacks `"restore"`); `TestHandleListJobs_RestoreKindUsesAgentBinaryLabel` FAILs the same way.

- [ ] **Step 3: Fix `validJobKinds` and `binariesForKind`**

In `src/cmd/api-server/jobs.go`, change:

```go
var validJobKinds = map[string]bool{
	"backup":            true,
	"bootstrap-refresh": true,
	"operating-refresh": true,
	"policy-update":     true,
}
```

to:

```go
var validJobKinds = map[string]bool{
	"backup":            true,
	"bootstrap-refresh": true,
	"operating-refresh": true,
	"policy-update":     true,
	"restore":           true,
}
```

Change:

```go
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
```

to:

```go
func binariesForKind(kind string) string {
	switch kind {
	case "backup":
		return "brfs|bwfs"
	case "bootstrap-refresh", "operating-refresh", "policy-update", "restore":
		return "agent"
	default:
		return "agent|brfs|bwfs"
	}
}
```

Change the 400 error message:

```go
		writeJSONError(w, http.StatusBadRequest, "kind must be one of backup, bootstrap-refresh, operating-refresh, policy-update")
```

to:

```go
		writeJSONError(w, http.StatusBadRequest, "kind must be one of backup, bootstrap-refresh, operating-refresh, policy-update, restore")
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run 'TestBinariesForKind|TestHandleListJobs_KindRestoreIsAccepted|TestHandleListJobs_RestoreKindUsesAgentBinaryLabel|TestHandleListJobs_InvalidKindReturns400' -v`

Expected: PASS for all four, including the pre-existing `TestHandleListJobs_InvalidKindReturns400` (an actually-invalid kind like `not-a-real-kind` still 400s).

- [ ] **Step 5: Run the full `api-server` package tests**

Run: `cd src && go test ./cmd/api-server/... -v`

Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go
git commit -m "fix(api-server): accept kind=restore in GET /jobs"
```

---

## Task 2: Generalize the job-wait helper in `policySeeding.js`

**Files:**
- Modify: `web/e2e/helpers/policySeeding.js`

**Interfaces:**
- Consumes: nothing new.
- Produces: `export async function waitForJobState(page, policyName, state, timeoutMs = 100_000)`; `export async function waitForJobSuccess(page, policyName, timeoutMs = 100_000)` (now a one-line wrapper: `waitForJobState(page, policyName, 'success', timeoutMs)`, same signature as today — internal callers, including `seedRestoreCartCatalogData` itself, are unaffected); `export const COMPOSE_FILE` (was a private `const`). Used by Task 3 and Task 4.

This is a small, mechanical refactor with no fast red/green unit-test cycle available (there's no unit-test harness for `web/e2e/helpers/*` — these are only ever exercised live). Its own correctness is established by the live run at the end of Task 3 (which calls the new `waitForJobSuccess` directly) and the plan's Final Check (which re-runs the complete suite, including `restore-cart.spec.js`, whose `seedRestoreCartCatalogData` still depends on this helper internally).

- [ ] **Step 1: Generalize `waitForJobSuccess` and export `COMPOSE_FILE`**

In `web/e2e/helpers/policySeeding.js`, change:

```js
const __dirname = path.dirname(fileURLToPath(import.meta.url))
const COMPOSE_FILE = path.resolve(__dirname, '../../../demo/docker-compose.yml')
```

to:

```js
const __dirname = path.dirname(fileURLToPath(import.meta.url))
export const COMPOSE_FILE = path.resolve(__dirname, '../../../demo/docker-compose.yml')
```

Change:

```js
async function waitForJobSuccess(page, policyName, timeoutMs = 100_000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    await page.goto('/jobs')
    const row = page.locator('tbody tr', { hasText: policyName })
    if ((await row.count()) > 0 && (await row.locator('text=success').count()) > 0) return
    if (Date.now() > deadline) throw new Error(`Timed out waiting for job "${policyName}" to reach success`)
    await page.waitForTimeout(3000)
  }
}
```

to:

```js
// waitForJobState polls /jobs (page-reload loop, since job state changes
// server-side, not via any client-side event this page could listen for)
// until policyName's row shows state in its State column, or throws after
// timeoutMs.
export async function waitForJobState(page, policyName, state, timeoutMs = 100_000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    await page.goto('/jobs')
    const row = page.locator('tbody tr', { hasText: policyName })
    if ((await row.count()) > 0 && (await row.locator(`text=${state}`).count()) > 0) return
    if (Date.now() > deadline) throw new Error(`Timed out waiting for job "${policyName}" to reach ${state}`)
    await page.waitForTimeout(3000)
  }
}

export async function waitForJobSuccess(page, policyName, timeoutMs = 100_000) {
  return waitForJobState(page, policyName, 'success', timeoutMs)
}
```

- [ ] **Step 2: Confirm nothing else in the file changes behavior**

`seedRestoreCartCatalogData`'s existing call, `await waitForJobSuccess(page, policyName)`, is untouched — same name, same signature, same default timeout. Read the full file once to confirm no other reference to the old (now-removed) private `waitForJobSuccess` declaration remains:

Run: `grep -n "waitForJobSuccess\|COMPOSE_FILE" web/e2e/helpers/policySeeding.js`

Expected: exactly the new `export async function waitForJobState`/`export async function waitForJobSuccess` declarations, the one call site inside `seedRestoreCartCatalogData`, and the one `export const COMPOSE_FILE` declaration plus its one use in the `docker compose -f ${COMPOSE_FILE} ...` line — no leftover duplicate or unexported declaration.

- [ ] **Step 3: Commit**

```bash
git add web/e2e/helpers/policySeeding.js
git commit -m "refactor(web-e2e): generalize waitForJobSuccess into waitForJobState"
```

---

## Task 6: Fix `job_id` validation to allow restore policy names

> Numbered 6 (out of document order, ahead of Task 3) so `task-brief PLAN 6` never collides with
> `task-brief PLAN 3`'s `Task 3` heading match. Discovered live, mid-execution, by Task 3's first
> genuine attempt to view a restore job's log through the real UI — see the plan's execution
> ledger. This blocks Task 3 and Task 4 outright: neither scenario can complete its "read the job
> log" step until this is fixed, because **no restore job's log can currently be viewed at all**.

**Files:**
- Modify: `src/cmd/api-server/jobs.go:287`
- Modify: `src/cmd/api-server/jobs_test.go`
- Modify: `docs/api/rest-v1.md:460`

**Interfaces:**
- Produces: `jobIDPattern` accepts `.` in a `job_id`. Consumed by: Task 3's and Task 4's
  live-verification steps (both open a restore job's `/jobs/:job_id` page, which calls
  `GET /api/v1/jobs/{job_id}/logs`).

**Root cause:** `web/src/stores/restoreSubmission.js:150` names every restore policy
`` `restore-${new Date().toISOString()}-${storeHost}` `` — `Date.prototype.toISOString()` always
includes a `.` before its millisecond digits (e.g. `2026-08-13T14:30:00.123Z`), so every restore
job's ID (`restore:<policy-name>:<timestamp>`) always contains a `.`. But
`src/cmd/api-server/jobs.go:287`'s `jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9:_-]+$`)`
(used by `handleGetJobLogs` to validate the `{job_id}` path parameter) does not allow `.` — so
`GET /api/v1/jobs/{job_id}/logs` 400s ("job_id contains invalid characters") for every restore
job, unconditionally. This is not restore-specific in the regex itself — it's that restore is the
only job kind whose name-generation scheme happens to always produce a `.`. The fix is the
allowlist, not the frontend's naming (changing `toISOString()`'s output shape would be a much
larger, riskier change for no added safety — the character just needs to be allowed, the same way
`jobHostnamePattern` (`jobs.go:289`) already allows `.` for hostnames).

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/api-server/jobs_test.go`, after `TestHandleGetJobLogs_InvalidJobIDCharacterReturns400`:

```go
func TestHandleGetJobLogs_JobIDWithDotIsAccepted(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | job_id="restore:restore-2026-08-13T14:30:00.123Z-store-a:1755094200"`: {
			{Stream: map[string]string{"hostname": "database", "binary": "agent"}, Values: []lokiValue{
				{Timestamp: 1755094200000000000, Line: "policy execution started"},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/restore:restore-2026-08-13T14:30:00.123Z-store-a:1755094200/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}
```

This job ID is exactly the shape `restoreSubmission.js` actually produces (`restore:` prefix +
`restore-<ISO-timestamp-with-millis>-<storeHost>` policy name + `:<unix-ts>` task suffix) — not a
simplified stand-in.

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleGetJobLogs_JobIDWithDotIsAccepted -v`

Expected: FAIL — `400`, not `200` (the `.` in the job ID is rejected by the current `jobIDPattern`).

- [ ] **Step 3: Widen `jobIDPattern`**

In `src/cmd/api-server/jobs.go`, change:

```go
var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9:_-]+$`)
```

to:

```go
var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9:._-]+$`)
```

- [ ] **Step 4: Run the test to verify it passes, plus the existing invalid-character test**

Run: `cd src && go test ./cmd/api-server/... -run 'TestHandleGetJobLogs_JobIDWithDotIsAccepted|TestHandleGetJobLogs_InvalidJobIDCharacterReturns400' -v`

Expected: PASS for both — the new dot-containing job ID is accepted, and the existing test's
actually-invalid job ID (`not%20valid;job` — a space and a semicolon, still outside the widened
allowlist) still 400s.

- [ ] **Step 5: Run the full `api-server` package tests**

Run: `cd src && go test ./cmd/api-server/... -v`

Expected: PASS, no regressions.

- [ ] **Step 6: Update `docs/api/rest-v1.md`**

Change:

```
`job_id` must match `^[a-zA-Z0-9:_-]+$` — `400` otherwise.
```

to:

```
`job_id` must match `^[a-zA-Z0-9:._-]+$` — `400` otherwise.
```

- [ ] **Step 7: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go docs/api/rest-v1.md
git commit -m "fix(api-server): allow dots in job_id, unblocking restore job log lookups"
```

---

## Task 7: Include `rwfs` in `handleGetJobLogs`'s Loki binary filter

> Numbered 7 for the same reason Task 6 is numbered 6 — avoids colliding with `task-brief PLAN 3`'s
> heading match. Discovered live, mid-execution, by Task 3's second genuine attempt to view a
> restore job's log — after Task 6 fixed the `job_id` 400, the job-detail page loaded but rendered
> none of the per-file `verified`/`summary` log lines a restore-verify job actually needs to prove
> anything. See the plan's execution ledger for the full discovery trail.

**Files:**
- Modify: `src/cmd/api-server/jobs.go:327,330,332,334`
- Modify: `src/cmd/api-server/jobs_test.go`

**Interfaces:**
- Produces: `handleGetJobLogs`'s Loki label selector includes `rwfs`. Consumed by: Task 3's and
  Task 4's live-verification steps (both assert on `verified`/`summary`/`verification failed`
  log-line content, which only `rwfs` itself emits — see `src/cmd/rwfs/verify.go`).

**Root cause:** `agent` logs a start/finish line for a restore task itself (`binary="agent"` — see
Task 1), but the actual `rwfs verify` child process it execs writes its **own** structured log
(`<log_dir>/rwfs.log`, per `docs/components/agent.md`'s "Logging and correlation" section — "every
binary `agent` execs writes structured JSON logs... one stable, rotated file per binary"), shipped
to Loki with `binary="rwfs"` by the same Vector pipeline every other binary uses (`agent/vector.go`'s
`add_binary_label` transform derives the label from the log filename). `rwfs verify`'s own
per-file `"verified"`/`"summary"`/`"verification failed"` lines (`src/cmd/rwfs/verify.go`) carry
the same `job_id` (threaded via `--job-id`, per `docs/protocols/restore.md`'s "CLI → RPC Mapping"
section, which already documents this correlation as intentional). But
`handleGetJobLogs` (`src/cmd/api-server/jobs.go`) hardcodes its Loki label selector to
`{binary=~"agent|brfs|bwfs"}` in all four of its branches — `rwfs` was never added when
restore-verification shipped, so every one of its own log lines is filtered out before reaching
the browser, even though they exist in Loki, correctly tagged with the right `job_id`. This isn't
a timing issue (waiting longer doesn't help) — the label filter itself excludes them
unconditionally.

Unlike Task 6, this selector is **not** kind-scoped — `handleGetJobLogs` takes no `kind` parameter,
it only takes `job_id` (plus optional `source_host`/`store_host` narrowing) — so the fix widens the
one shared selector every job-log lookup uses, the same way `brfs`/`bwfs` already sit there
unconditionally for every kind, not just backup.

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/api-server/jobs_test.go`, after `TestHandleGetJobLogs_SourceAndStoreHostNarrowLabelSelector`:

```go
func TestHandleGetJobLogs_IncludesRwfsBinaryLines(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="restore:e2e-restore-verify:1755094200"`: {
			{Stream: map[string]string{"hostname": "database", "binary": "rwfs"}, Values: []lokiValue{
				{Timestamp: 1755094201000000000, Line: `{"msg":"verified","path":"/var/lib/dbdata/dump.sql"}`},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/restore:e2e-restore-verify:1755094200/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1, "rwfs-emitted log lines must be included, not filtered out")
	assert.Equal(t, "rwfs", data[0].(map[string]any)["binary"])
}
```

Also update the two existing tests whose fake Loki query keys hardcode the old selector, since
after Step 3 the real code will construct queries against the widened selector and an un-updated
fake-map key would simply never match (silently returning zero rows, not a compile or an obvious
failure) —

In `TestHandleGetJobLogs_ReturnsLinesSortedByTimestamp`, change the fake's query key:

```go
		`{binary=~"agent|brfs|bwfs"} | job_id="operating-refresh:1752400500"`: {
```

to:

```go
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="operating-refresh:1752400500"`: {
```

In `TestHandleGetJobLogs_SourceAndStoreHostNarrowLabelSelector`, change the fake's query key:

```go
		`{binary=~"agent|brfs|bwfs", hostname=~"database|bwfs-east"} | job_id="backup:nightly:var-www:abcd1234:1752400000"`: {
```

to:

```go
		`{binary=~"agent|brfs|bwfs|rwfs", hostname=~"database|bwfs-east"} | job_id="backup:nightly:var-www:abcd1234:1752400000"`: {
```

- [ ] **Step 2: Run the tests to verify the new one fails and the two updated ones fail on the old code**

Run: `cd src && go test ./cmd/api-server/... -run 'TestHandleGetJobLogs' -v`

Expected: `TestHandleGetJobLogs_IncludesRwfsBinaryLines` FAILs (`require.Len` — 0 rows returned, the
fake's query key doesn't match what the current code constructs). The two updated existing tests
also FAIL now, for the same reason in reverse: their fake map keys were just changed to the
*post-fix* selector string, which the *pre-fix* code doesn't yet construct.

- [ ] **Step 3: Widen the four hardcoded selectors**

In `src/cmd/api-server/jobs.go`, change:

```go
	labelSelector := `{binary=~"agent|brfs|bwfs"}`
	switch {
	case sourceHost != "" && storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs", hostname=~"%s|%s"}`, sourceHost, storeHost)
	case sourceHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs", hostname="%s"}`, sourceHost)
	case storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs", hostname="%s"}`, storeHost)
	}
```

to:

```go
	labelSelector := `{binary=~"agent|brfs|bwfs|rwfs"}`
	switch {
	case sourceHost != "" && storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname=~"%s|%s"}`, sourceHost, storeHost)
	case sourceHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname="%s"}`, sourceHost)
	case storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname="%s"}`, storeHost)
	}
```

- [ ] **Step 4: Run the tests to verify they all pass**

Run: `cd src && go test ./cmd/api-server/... -run 'TestHandleGetJobLogs' -v`

Expected: PASS for all `TestHandleGetJobLogs_*` tests, including the new one and the two updated
ones.

- [ ] **Step 5: Run the full `api-server` package tests**

Run: `cd src && go test ./cmd/api-server/... -v`

Expected: PASS, no regressions.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/jobs.go src/cmd/api-server/jobs_test.go
git commit -m "fix(api-server): include rwfs in job-log queries, surfacing restore-verify's own log lines"
```

---

## Task 3: Success-scenario spec — `web/e2e/restore-verify.spec.js`

**Files:**
- Create: `web/e2e/restore-verify.spec.js`

**Interfaces:**
- Consumes: `seedRestoreCartCatalogData(page)` (unchanged, returns `{ sourceHost: 'database', dirPath: '/var/lib/dbdata', files: ['dump.sql', 'schema.sql'] }`), `waitForJobSuccess(page, policyName)`, `COMPOSE_FILE` (Task 2, `./helpers/policySeeding.js`). `data-test="file-checkbox-<host>:<path>"` (`CatalogView.vue`, pre-existing), `data-test="restore-row-<host>:<path>"` / `"destination-select"` / `"submit-restore"` / `"submission-results"` (`RestoreView.vue`, pre-existing), `data-test="log-line"` / `"log-line-summary"` / `"log-line-fields"` (`LogLine.vue`, pre-existing).
- Produces: `web/e2e/restore-verify.spec.js`'s file-level structure (`test.describe.configure({ mode: 'serial' })`, one `test('restore verification', ...)`), extended by Task 4 with a second `test.step`.

- [ ] **Step 1: Write the spec's success scenario**

Create `web/e2e/restore-verify.spec.js`:

```js
import { execSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { seedRestoreCartCatalogData, waitForJobSuccess, COMPOSE_FILE } from './helpers/policySeeding.js'

test.describe.configure({ mode: 'serial' })

test('restore verification', async ({ page, context }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page)
  const filePath = `${dirPath}/${files[0]}`
  const segments = dirPath.split('/').filter(Boolean)

  // Same drill-down sequence restore-cart.spec.js already uses: sidebar
  // link -> breadcrumb home -> the synthetic "/" root row -> each real path
  // segment. All real <router-link>/row clicks, never page.goto(), so
  // restoreCart's in-memory selection state survives (see Global
  // Constraints).
  async function goToCatalogHome() {
    await page.getByRole('link', { name: 'Catalog' }).click()
    await page.getByTestId('crumb-home').click()
    await page.getByText('//', { exact: true }).click()
  }

  await test.step('a real backed-up file verifies successfully, readable in its job log', async () => {
    await goToCatalogHome()
    for (const segment of segments) {
      await page.getByText(`${segment}/`, { exact: true }).click()
    }
    await page.getByTestId(`file-checkbox-${sourceHost}:${filePath}`).click()

    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByTestId(`restore-row-${sourceHost}:${filePath}`)).toBeVisible()

    const destinationSelect = page.getByTestId('destination-select')
    // clients.fetchAll() runs on RestoreView's onMounted -- wait for the
    // real option before selecting it, rather than racing it (same
    // reasoning as policySeeding.js's own storageSelect wait).
    await expect(destinationSelect.locator('option', { hasText: sourceHost })).toHaveCount(1)
    await destinationSelect.selectOption(sourceHost)
    await page.getByTestId('submit-restore').click()

    const resultText = await page.getByTestId('submission-results').innerText()
    const policyName = /Created (\S+) from/.exec(resultText)[1]

    // No UI/API surface to force policyclient's pickup faster than its
    // default 900s fetch interval -- same non-UI escape hatch
    // seedRestoreCartCatalogData already uses for its own backup policy.
    execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${sourceHost} ./policyclient fetch`, { stdio: 'inherit' })
    await waitForJobSuccess(page, policyName)

    await page.locator('tbody tr', { hasText: policyName }).locator('a').click()

    const verifiedLine = page.getByTestId('log-line').filter({ hasText: 'verified' }).first()
    await expect(verifiedLine).toBeVisible()
    await verifiedLine.getByTestId('log-line-summary').click()
    await expect(verifiedLine.getByTestId('log-line-fields')).toContainText(filePath)

    const summaryLine = page.getByTestId('log-line').filter({ hasText: 'summary' }).first()
    await summaryLine.getByTestId('log-line-summary').click()
    await expect(summaryLine.getByTestId('log-line-fields')).toContainText('warnings')
    await expect(summaryLine.getByTestId('log-line-fields')).toContainText('0')
  })
})
```

- [ ] **Step 2: Run it against the live demo lab**

Precondition: `make demo-up` is already running.

Run: `cd web && npx playwright test restore-verify.spec.js`

Expected: `PASS` — 1 test passed, 1 step shown in the list reporter. This run takes up to ~3 minutes (seeding dominates, matching `restore-cart.spec.js`'s own budget, plus one `ReconcileIntervalSec`-scale wait for the restore-verify job itself). If it fails, the Playwright trace (`test-results/`) pinpoints exactly which locator/assertion didn't resolve — check it against the real rendered markup (`RestoreView.vue`, `LogLine.vue`) before assuming the application itself is broken.

- [ ] **Step 3: Commit**

```bash
git add web/e2e/restore-verify.spec.js
git commit -m "feat(web-e2e): add restore verification success scenario"
```

---

## Task 4: Failure-scenario step — appended to `restore-verify.spec.js`

**Files:**
- Modify: `web/e2e/restore-verify.spec.js`

**Interfaces:**
- Consumes: `waitForJobState(page, policyName, state)` (Task 2). `sourceHost`, `dirPath` from Task 3's shared `test()` scope (same `test('restore verification', ...)` body — this step runs second, after the success step, sharing the same `page`).
- Produces: no new exports — this is the plan's last code change to this file.

- [ ] **Step 1: Add the failure scenario's `test.step`**

In `web/e2e/restore-verify.spec.js`, change the import line:

```js
import { seedRestoreCartCatalogData, waitForJobSuccess, COMPOSE_FILE } from './helpers/policySeeding.js'
```

to:

```js
import { seedRestoreCartCatalogData, waitForJobSuccess, waitForJobState, COMPOSE_FILE } from './helpers/policySeeding.js'
```

Add a second `test.step`, immediately after the closing `})` of the `'a real backed-up file verifies successfully...'` step (still inside the outer `test('restore verification', async ({ page, context }) => { ... })` body, so it shares `page`, `sourceHost`, and `dirPath`):

```js
  await test.step('a rule naming a file that was never backed up fails, readable in its job log', async () => {
    const authHeaders = { Authorization: 'Bearer dev-placeholder-token-change-me' }

    // No UI affordance exists to select a file that was never backed up --
    // CatalogView.vue only ever renders checkboxes for real catalog rows.
    // This is the one non-UI step in this scenario; everything after it
    // (waiting, opening the job, reading the log, cleanup) is the same
    // mix of forced-fetch-then-browser-driven flow the success scenario
    // above uses.
    const storagePoliciesResp = await page.request.get('/api/v1/policies?type=storage', { headers: authHeaders })
    const { data: storagePolicies } = await storagePoliciesResp.json()
    const storagePolicyId = storagePolicies.find((p) => p.name === 'store').id

    const missingPath = `${dirPath}/does-not-exist.sql`
    const failPolicyName = `e2e-restore-verify-fail-${Date.now()}`
    const createResp = await page.request.post('/api/v1/restore', {
      headers: authHeaders,
      data: {
        name: failPolicyName,
        client_filters: { hostnames: [sourceHost] },
        storage_policy_id: storagePolicyId,
        rules: [{ host: sourceHost, path: missingPath, include: true }],
      },
    })
    expect(createResp.status()).toBe(201)
    const { id: failPolicyId } = await createResp.json()

    execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${sourceHost} ./policyclient fetch`, { stdio: 'inherit' })
    await waitForJobState(page, failPolicyName, 'failure')

    await page.locator('tbody tr', { hasText: failPolicyName }).locator('a').click()

    const notFoundLine = page.getByTestId('log-line').filter({ hasText: 'verification failed' }).first()
    await expect(notFoundLine).toBeVisible()
    await notFoundLine.getByTestId('log-line-summary').click()
    await expect(notFoundLine.getByTestId('log-line-fields')).toContainText('not found on this store')
    await expect(notFoundLine.getByTestId('log-line-fields')).toContainText(missingPath)

    // One-shot-until-success: left alive, this policy retries with backoff
    // forever (it names a file that can never exist). Delete it the same
    // way it was created.
    await page.request.delete(`/api/v1/policies/${failPolicyId}`, { headers: authHeaders })
  })
```

- [ ] **Step 2: Run the full spec against the live demo lab**

Precondition: `make demo-up` is already running.

Run: `cd web && npx playwright test restore-verify.spec.js`

Expected: `PASS` — 1 test passed, both steps shown in the list reporter. Total run time up to ~4 minutes (one seed, one successful verify wait, one failed verify wait). If the failure step's `waitForJobState(..., 'failure')` times out, check `docker compose -f demo/docker-compose.yml logs agent` isn't itself failing to dispatch (e.g. a stale `policies-cache.json`) before assuming the assertion is wrong — this is the same class of flake `lifecycle_test.go`'s own polling helpers are already tuned against.

- [ ] **Step 3: Run the complete e2e suite together**

Run: `cd web && npx playwright test`

Expected: `PASS` — `smoke.spec.js`, `restore-cart.spec.js`, and `restore-verify.spec.js` all pass (3 tests total). This is the regression check for Task 2's helper generalization: `restore-cart.spec.js`'s `seedRestoreCartCatalogData` call still depends on `waitForJobSuccess` internally and must be unaffected.

- [ ] **Step 4: Commit**

```bash
git add web/e2e/restore-verify.spec.js
git commit -m "feat(web-e2e): add restore verification failure scenario"
```

---

## Task 5: Documentation and changelog

**Files:**
- Modify: `docs/components/agent.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `CHANGELOG.md`

**Interfaces:** None (docs only).

- [ ] **Step 0: Fix the stale `kind` enum in `docs/api/rest-v1.md`** (found by Task 1's review —
  Task 1's fix added a fifth valid `kind` value, but this file still documents only four)

In `docs/api/rest-v1.md`, in the `GET /api/v1/jobs` query-parameters table, change:

```
| `kind` | string | One of `backup`, `bootstrap-refresh`, `operating-refresh`, `policy-update` |
```

to:

```
| `kind` | string | One of `backup`, `bootstrap-refresh`, `operating-refresh`, `policy-update`, `restore` |
```

A few lines below, change:

```
`400` if `kind` isn't one of the four valid values, `since`/`until` aren't unix-second integers,
```

to:

```
`400` if `kind` isn't one of the five valid values, `since`/`until` aren't unix-second integers,
```

- [ ] **Step 1: Note the new coverage in `docs/components/agent.md`**

In the "Policy-driven restore verification" section, find the sentence ending (around the paragraph that cites the 2026-08-10 design):

```
...(see [Design: Restore Policy Verification Execution](../superpowers/specs/2026-08-10-restore-policy-verification-design.md)), not a functional bug.
```

Add a new sentence immediately after it, in the same paragraph:

```
This path has browser-driven integration coverage in `web/e2e/restore-verify.spec.js`
(`docs/superpowers/specs/2026-08-13-restore-verification-e2e-design.md`) — a real backed-up file
verifying successfully, and a rule naming a file that was never backed up failing — both read from
the real, rendered `/jobs/:job_id` log.
```

- [ ] **Step 2: Add the `CHANGELOG.md` entry**

Add at the top of `CHANGELOG.md`, above the current top entry:

```markdown
## 2026-08-13 — restore verification gains e2e coverage

`GET /api/v1/jobs?kind=restore` no longer 400s — `validJobKinds` and `binariesForKind` were never
updated when restore-policy verification shipped, even though `agent` already logged correct
`event=start`/`event=finish` lines for it. Separately, `GET /api/v1/jobs/{job_id}/logs` no longer
400s for a restore job specifically — every restore policy's generated name embeds a millisecond
ISO timestamp (which always contains a `.`), but the endpoint's `job_id` validation regex
disallowed `.`, so no restore job's log could ever be viewed via the API or the web UI. A third,
related gap: `GET /api/v1/jobs/{job_id}/logs`'s Loki query only ever selected `agent`/`brfs`/`bwfs`
log lines, so even once a restore job's log page loaded, the actual per-file `verified`/`summary`
lines `rwfs verify` itself emits (the only lines that say anything about what was actually checked)
were silently filtered out — `rwfs` is now included. All three were caught by writing this
release's own e2e coverage — restore-policy verification (submit a restore policy,
`agent` runs `rwfs verify`, the outcome appears as a job) now has browser-driven Playwright coverage
(`web/e2e/restore-verify.spec.js`): a real backed-up file verifies successfully, and a rule naming a
file that was never backed up fails — both scenarios read their outcome from the real, rendered job
log, catching real-DOM issues a mocked-store component test can't. This was a real gap: neither this
path nor the job list's handling of `kind=restore` had ever been exercised end to end before.
```

- [ ] **Step 3: Commit**

```bash
git add docs/components/agent.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "docs: document restore verification e2e coverage"
```

---

## Final Check

- [ ] Run the full Go test suite: `cd src && go test ./...` — expect PASS.
- [ ] Run `go vet`: `cd src && go vet ./...` — expect no findings.
- [ ] Run the full Vitest suite: `cd web && npx vitest run` — expect PASS, all files (this plan touches no Vue component or store, so no regression is expected, but this confirms it).
- [ ] With `make demo-up` running, run the entire e2e suite once more: `cd web && npx playwright test` — expect PASS, 3 tests (`smoke`, `restore-cart` selection, `restore-verify`).
