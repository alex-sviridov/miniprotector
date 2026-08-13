# Restore Verification E2E Tests — Design

## Problem

Restore-policy verification (`docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md`,
and the `dest_path` work on this branch,
`docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md`) has **zero automated
coverage** of the real pipeline: a restore policy created → picked up by `agent` → executed as
`rwfs verify` → appearing as a job → readable in its log. The 2026-08-10 design's own Testing
section called for this ("Integration (extends the existing e2e harness)...") but it was never
built — neither the Go integration suite (`src/e2e`) nor the browser suite (`web/e2e`) has a single
restore-related test beyond `restore-cart.spec.js`, which stops at cart *selection* and never
submits a restore policy at all.

While tracing how a restore-verify job would actually surface in `GET /api/v1/jobs`, a real, small
bug turned up: **`GET /api/v1/jobs?kind=restore` currently returns 400 Bad Request.**
`kindFromJobID` (`src/cmd/api-server/jobs.go`) already derives `"restore"` correctly from a job's
ID (`restore:<policy-name>:<timestamp>`), and `agent`'s `reconcile.go` already logs correct
`event=start`/`event=finish` lines for restore tasks — confirmed: `isBackupPolicy` only
special-cases IDs prefixed `backup:`, so a `restore:`-prefixed task ID falls through to the generic
`event`/`status`-tagged logging path, the same one `bootstrap-refresh`/`operating-refresh`/
`policy-update` already use. But `validJobKinds` (the query-param allowlist) was never updated to
include `"restore"`, so the request is rejected before it ever reaches the otherwise-correct
`binariesForKind` default case. This has been an invisible gap because nothing has ever queried
`kind=restore`.

## Scope

1. Fix the `kind=restore` gap in `api-server`.
2. A single Playwright browser suite (`web/e2e/restore-verify.spec.js`) covering **both** the
   success path and the failure path, driven through the real UI wherever a UI surface exists for
   it — including, for both scenarios, opening the resulting job in the browser and reading its
   rendered log. No separate Go integration test: everything that can run through the browser does,
   and the one step that structurally can't (constructing a rule naming a file that was never
   backed up — see Architecture, below) is a single API call from inside the same Playwright spec,
   not a second, separate suite.

## Non-Goals

- **No two-button UI, no actual restore execution.** Both remain a separate, larger design
  (bwfs/rwfs write path, agent task derivation, a way to express "verify" vs "restore" intent on
  the policy — none of which exist today; `"restore"`-typed policies only ever mean "verify,"
  since `agent`'s `restoreTasks()` is the only consumer and `rwfs verify` is the only thing it
  dispatches).
- **No precise backoff-timing assertion.** The failure scenario confirms a failed job appears with
  the right log content; it does not wait out a second retry attempt to verify backoff math —
  that's already unit-tested in `src/cmd/agent/reconcile_test.go`.
- **No `mode`/intent field added to the restore policy schema.** Confirmed out of scope: today
  `"restore"` implicitly means "verify" because nothing else exists yet; that ambiguity only needs
  resolving once an actual-restore executor exists.
- **No change to `rwfs verify`'s own behavior**, and no change to `web/e2e/restore-cart.spec.js`.
  `policySeeding.js` gets only the small, backward-compatible addition described in Architecture
  below (generalizing `waitForJobSuccess` to `waitForJobState`, exporting `COMPOSE_FILE`) — nothing
  `restore-cart.spec.js` or `seedRestoreCartCatalogData` itself does changes behavior.

## Architecture

### A. `api-server`: `kind=restore` fix (precedes the tests)

`src/cmd/api-server/jobs.go`:

- `validJobKinds` gains `"restore": true`, alongside the existing `backup`/`bootstrap-refresh`/
  `operating-refresh`/`policy-update`.
- `binariesForKind` gains an explicit `case "restore":` returning `"agent"`, grouped with the
  existing `bootstrap-refresh`/`operating-refresh`/`policy-update` case — restore tasks are logged
  by `agent` itself wrapping the dispatch (`reconcile.go`'s `logExecStart`/`logExecCompletion`), not
  by the child `rwfs` binary, exactly like those three kinds and unlike `backup` (whose start/finish
  lines come from `brfs`/`bwfs` themselves, which is why `backup` is handled separately and
  `binariesForKind`'s default case exists at all).
- The 400 error body's kind-list (`"kind must be one of backup, bootstrap-refresh,
  operating-refresh, policy-update"`) gains `, restore`.

`src/cmd/api-server/jobs_test.go`: extend the existing kind-validation table test with a
`kind=restore` case asserting it's accepted (no longer 400), and extend `binariesForKind`'s test
table with `"restore"` → `"agent"`.

### B. Playwright suite — `web/e2e/restore-verify.spec.js` (new)

One sequential spec, two `test()`s sharing a single seed (re-seeding per scenario would mean a
second real backup job to wait on — same reasoning `restore-cart.spec.js` already uses for its own
sequential-scenarios structure). Reuses `seedRestoreCartCatalogData` from the existing
`web/e2e/helpers/policySeeding.js`, unmodified — it already creates a real backup of `database`'s
`/var/lib/dbdata` (`dump.sql`, `schema.sql`) to `store` and confirms catalog visibility through the
real UI, exactly the fixture both scenarios need.

Both scenarios end the same way — navigate to the job in the browser and read its rendered log —
and both share one unavoidable non-UI step, forcing `policyclient fetch` via `docker compose exec`,
for the same reason `seedRestoreCartCatalogData` already needs it: the default 900s fetch interval
would otherwise make the test slow and nondeterministic. The failure scenario needs one additional
non-UI step: creating the restore policy itself via a direct API call
(`page.request.post('/api/v1/restore', ...)`), because there is no UI affordance to select a file
that was never backed up — `CatalogView.vue`'s checkboxes only ever render real catalog rows.
Everything else — submitting, waiting, opening the job, reading the log — goes through the real
page for both scenarios.

```js
import { execSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { seedRestoreCartCatalogData, waitForJobSuccess, waitForJobState, COMPOSE_FILE } from './helpers/policySeeding'

const API_TOKEN = 'dev-placeholder-token-change-me' // see policySeeding.js's auth note

test.describe.serial('restore verification', () => {
  let sourceHost, dirPath

  test.beforeAll(async ({ browser }) => {
    const page = await browser.newPage()
    ;({ sourceHost, dirPath } = await seedRestoreCartCatalogData(page))
    await page.close()
  })

  test('a real backed-up file verifies successfully, readable in its job log', async ({ page }) => {
    await page.goto('/catalog')
    // ...drill into dirPath (same "/" then path-segment click sequence as waitForCatalogFiles)...
    await page.locator('[data-test="file-checkbox-database:/var/lib/dbdata/dump.sql"]').click()

    await page.goto('/restore')
    await expect(page.locator('[data-test="restore-row-database:/var/lib/dbdata/dump.sql"]')).toBeVisible()
    await page.locator('[data-test="destination-select"]').selectOption('database')
    await page.locator('[data-test="submit-restore"]').click()

    const resultText = await page.locator('[data-test="submission-results"]').innerText()
    const policyName = /Created (\S+) from/.exec(resultText)[1]

    execSync(`docker compose -f ${COMPOSE_FILE} exec -T database ./policyclient fetch`, { stdio: 'inherit' })
    await waitForJobSuccess(page, policyName)

    await page.locator('tbody tr', { hasText: policyName }).locator('a').click() // -> /jobs/:job_id
    const verifiedLine = page.locator('[data-test="log-line"]', { hasText: 'verified' }).first()
    await expect(verifiedLine).toBeVisible()
    await verifiedLine.locator('[data-test="log-line-summary"]').click()
    await expect(verifiedLine.locator('[data-test="log-line-fields"]')).toContainText('/var/lib/dbdata/dump.sql')

    const summaryLine = page.locator('[data-test="log-line"]', { hasText: 'summary' }).first()
    await summaryLine.locator('[data-test="log-line-summary"]').click()
    await expect(summaryLine.locator('[data-test="log-line-fields"]')).toContainText('warnings')
    await expect(summaryLine.locator('[data-test="log-line-fields"]')).toContainText('0')
  })

  test('a rule naming a file that was never backed up fails, readable in its job log', async ({ page, request }) => {
    // No UI surface exists to select a nonexistent file (CatalogView only renders real rows) --
    // this one step is a direct API call, same escape-hatch precedent as
    // seedRestoreCartCatalogData's own docker-exec step. Everything after this is browser-driven.
    const storagePolicies = await request.get('/api/v1/policies?type=storage', {
      headers: { Authorization: `Bearer ${API_TOKEN}` },
    })
    const { data } = await storagePolicies.json()
    const storagePolicyId = data.find((p) => p.name === 'store').id

    const policyName = `e2e-restore-verify-fail-${Date.now()}`
    const created = await request.post('/api/v1/restore', {
      headers: { Authorization: `Bearer ${API_TOKEN}` },
      data: {
        name: policyName,
        client_filters: { hostnames: ['database'] },
        storage_policy_id: storagePolicyId,
        rules: [{ host: 'database', path: '/var/lib/dbdata/does-not-exist.sql', include: true }],
      },
    })
    const { id: policyId } = await created.json()

    execSync(`docker compose -f ${COMPOSE_FILE} exec -T database ./policyclient fetch`, { stdio: 'inherit' })
    await waitForJobState(page, policyName, 'failure') // same reload-poll pattern, terminal state parameterized

    await page.locator('tbody tr', { hasText: policyName }).locator('a').click() // -> /jobs/:job_id
    const notFoundLine = page.locator('[data-test="log-line"]', { hasText: 'verification failed' }).first()
    await expect(notFoundLine).toBeVisible()
    await notFoundLine.locator('[data-test="log-line-summary"]').click()
    await expect(notFoundLine.locator('[data-test="log-line-fields"]')).toContainText('not found on this store')
    await expect(notFoundLine.locator('[data-test="log-line-fields"]')).toContainText('/var/lib/dbdata/does-not-exist.sql')

    // One-shot-until-success: left alive, this policy retries with backoff forever. Delete it the
    // same way it was created.
    await request.delete(`/api/v1/policies/${policyId}`, { headers: { Authorization: `Bearer ${API_TOKEN}` } })
  })
})
```

(Exact selectors/assertions above are illustrative of intent; the implementation plan pins them
down precisely against the real rendered markup, same as every other design in this codebase.)

`web/e2e/helpers/policySeeding.js` gains one small addition: `waitForJobSuccess`'s existing
poll-by-reload loop (currently hardcoded to look for `'success'` in the row's text) is generalized
to `waitForJobState(page, policyName, state, timeoutMs)`, with `waitForJobSuccess` becoming a
one-line wrapper (`waitForJobState(page, policyName, 'success', timeoutMs)`) so both this spec and
the existing `restore-cart.spec.js`/its own callers keep working unchanged. `COMPOSE_FILE` is
exported alongside, since both scenarios above need it directly (today it's a private `const` in
`policySeeding.js`).

This also gives `LogLine.vue` its first real, non-mocked-API coverage for *both* a success and a
failure line — the same rationale `restore-cart-e2e`'s own design used to justify a browser suite
over mocking: catching real-DOM/real-data issues (e.g., a field-expand click not working against
real JSON structure) that component tests mocking the store one layer up can't.

## Data Flow

```
web/e2e/restore-verify.spec.js -- success scenario:
  UI: check dump.sql -> /restore table -> submit
    -> POST /api/v1/restore {rules: [real, backed-up path]}
    -> docker compose exec database policyclient fetch (forced pickup, non-UI)
    -> agent (on database) reconcile tick -> rwfs verify --rules-stdin
    -> bwfs ListFiles + RestoreFile (store) -> chunk hashes verified
    -> rwfs exits 0 -> agent logs event=finish status=success
    -> UI: /jobs shows success -> /jobs/:job_id renders "verified" + "summary" log lines

web/e2e/restore-verify.spec.js -- failure scenario:
  page.request.post /api/v1/restore {rules: [nonexistent path]} (non-UI, no selectable row exists)
    -> policy-server (restore policy, client_filters targets "database")
    -> docker compose exec database policyclient fetch (forced pickup, non-UI)
    -> agent (on database) reconcile tick -> rwfs verify --rules-stdin
    -> bwfs ListFiles (store) -> rule matches nothing -> notFound
    -> rwfs exits non-zero -> agent logs event=finish status=failure
    -> UI: /jobs shows failure -> /jobs/:job_id renders "verification failed" / "not found on
       this store" log line
    -> page.request.delete the policy (non-UI; stops the one-shot task's infinite retry)
```

## Error Handling

No new error-handling code paths beyond fix A itself (an out-of-range `kind` still 400s with an
accurate message). Both scenarios use the same bounded-timeout poll-by-reload pattern
`restore-cart.spec.js`'s helpers already use for eventual-consistency waits — no new pattern
introduced. The failure scenario's policy-deletion cleanup runs at the end of its own `test()` body
(not a separate `afterAll`/`afterEach`), since it's only ever needed by that one scenario and
keeping it inline keeps the two scenarios independently readable.

## Testing

This design's deliverable *is* tests, so "Testing" here means: how the new suite's own correctness
is established, and why putting both scenarios in the browser (rather than splitting failure off
into a Go integration test) is the right call.

- **Why both scenarios are browser-driven:** the whole point of e2e coverage here is exercising
  real, non-mocked behavior — real UI rendering, real job polling, real log rendering via
  `LogLine.vue`. Splitting the failure scenario into a separate Go-only suite (as originally
  considered) would mean that path's log-reading assertion never touches `LogLine.vue` at all,
  duplicating only the REST-layer behavior the success scenario's own submission step already
  exercises. Keeping both in one Playwright spec means every assertion about "the job log shows the
  right thing" is checked the same way an operator would actually see it: rendered, in the browser.
- **The two unavoidable non-UI steps are precedented, not new pattern.** `docker compose exec
  database policyclient fetch` already exists in `seedRestoreCartCatalogData` for the identical
  reason (no reachable UI/API surface to force a fetch). `page.request.post`/`.delete` for the
  failure scenario's policy is the same kind of exception — Playwright's own `request` fixture,
  not a shell-out — needed only because `CatalogView.vue` structurally cannot render a checkbox for
  a file that isn't in the catalog.
- **`jobs_test.go`** (unit, not e2e): `kind=restore` accepted; `binariesForKind("restore") ==
  "agent"`.
- The new suite runs only under its existing gate (`npx playwright test` / the `test:e2e` script) —
  it does not run in the default fast unit test loop, matching every other e2e test in this
  codebase, including the two other tests it lives alongside.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule:

- **`CHANGELOG.md`** — entry before merge: the `kind=restore` fix, and the new
  restore-verification e2e coverage (both success and failure, browser-driven).
- **`docs/components/agent.md`**'s "Policy-driven restore verification" section — one sentence
  noting this path now has browser-driven integration coverage
  (`web/e2e/restore-verify.spec.js`, both a success and a failure scenario), so a future reader
  doesn't have to rediscover that it was previously untested.
- No `docs/protocols/` change (no wire-protocol change) and no `docs/ARCHITECTURE.md` change (no
  topology/data-flow change) — this is a validation-list fix plus new tests, nothing new is added to
  what the system does.

## Relationship to Prior Work

Directly extends `docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md` (fills in
its own deferred Testing item) and reuses `docs/superpowers/specs/2026-08-09-restore-cart-e2e-design.md`'s
Playwright infrastructure and seeding helper as-is. Deliberately does not touch or extend
`docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md`'s scope (an actual-restore
executor and its UI) — that remains future work, discussed but explicitly deferred during this
design's brainstorm.
