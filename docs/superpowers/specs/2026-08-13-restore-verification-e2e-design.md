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
2. A Go integration test (`src/e2e`) covering the **failure** path — a file-level rule naming a
   file that was never backed up.
3. A Playwright browser test (`web/e2e`) covering the **success** path, UI-driven end to end,
   including opening the resulting job and checking its rendered log.

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
- **No change to `rwfs verify`'s own behavior**, and no change to `web/e2e/restore-cart.spec.js` or
  its `policySeeding.js` helper beyond reusing it as-is.

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

### B. Go integration test — `src/e2e/restore_verify_test.go` (new, `//go:build e2e`)

Same package as `lifecycle_test.go`; reuses its unexported helpers (`apiRequest`, `requireStatus`,
`decodeJSON`, `dockerComposeExec`, `fetchStoragePolicyID`, `composeFile`) rather than duplicating
them. Targets the same `database`/`store` fixtures the existing Go and Playwright suites already
use.

`TestE2E_RestoreVerification`:

1. **Seed a real backed-up file.** Create a fast one-off backup policy for `database`'s
   `/var/lib/dbdata` (mirrors `lifecycle_test.go`'s `create_minute_policy_triggers_backup_job`
   subtest: `rpo: "1m"`, `backup_window: ["* * * * *"]`, a short `disabled_at`), force
   `./policyclient fetch` on `database`, wait for the backup job to reach `success`. This guarantees
   `/var/lib/dbdata/dump.sql` genuinely exists on `store` before the restore-verify subtest runs.
   `t.Cleanup` deletes this seed policy.

2. **Subtest `verify_fails_for_never_backed_up_file`:**
   - `storagePolicyID := fetchStoragePolicyID(t, "store")`.
   - `POST /api/v1/restore`:
     ```json
     {
       "name": "e2e-restore-verify-<unix-ts>",
       "client_filters": {"hostnames": ["database"]},
       "storage_policy_id": "<storagePolicyID>",
       "rules": [{"host": "database", "path": "/var/lib/dbdata/does-not-exist.sql", "include": true}]
     }
     ```
     Expect `201 Created`; capture the created policy's `id` for cleanup and its `name` for the
     job-id prefix.
   - `dockerComposeExec(t, "database", "./policyclient", "fetch")` to force immediate pickup
     (default fetch interval is otherwise 900s, same reasoning as every other seeding step in this
     codebase's e2e suites).
   - Poll `GET /api/v1/jobs?kind=restore&source_host=database&since=<unix-ts>` (now valid, per fix
     A) every 5s, up to a 90s deadline (matches `ReconcileIntervalSec=30` + `JobTimeoutSec=30` +
     headroom, same budget `waitForBackupJob` uses), until a job whose `job_id` has prefix
     `restore:<policy-name>:` reaches `"state": "failure"`.
   - `GET /api/v1/jobs/{job_id}/logs`; assert some line's raw JSON contains
     `"reason":"not found on this store"` and `"path":"/var/lib/dbdata/does-not-exist.sql"` (string
     `Contains` assertions on the raw log line, same pattern `bwfs/integration_test.go` and
     `agent/reconcile_test.go` already use — no need to fully decode each line).
   - `t.Cleanup` deletes this restore policy. This is load-bearing, not just tidiness: a restore
     task is one-shot-until-success (`docs/components/agent.md`'s "Policy-driven restore
     verification"), and this one can never succeed (it names a file that doesn't exist) — left
     alive, it retries with backoff forever, generating jobs and log volume indefinitely after the
     test run ends.

The success path is deliberately *not* duplicated here in Go — see Testing, below, for why it lives
in the Playwright suite instead.

### C. Playwright test — `web/e2e/restore-verify.spec.js` (new)

Reuses `seedRestoreCartCatalogData` from the existing `web/e2e/helpers/policySeeding.js`, unmodified
— it already creates a real backup of `database`'s `/var/lib/dbdata` (`dump.sql`, `schema.sql`) to
`store` and confirms catalog visibility through the real UI, exactly the fixture this test needs.

```js
test('restore verification runs and its job log is readable', async ({ page }) => {
  const { sourceHost, dirPath } = await seedRestoreCartCatalogData(page)

  await page.goto('/catalog')
  // ...drill into dirPath via the existing breadcrumb-click pattern (see policySeeding.js's
  // waitForCatalogFiles for the "/" then path-segment click sequence)...
  await page.locator('[data-test="file-checkbox-database:/var/lib/dbdata/dump.sql"]').click()

  await page.goto('/restore')
  await expect(page.locator('[data-test="restore-row-database:/var/lib/dbdata/dump.sql"]')).toBeVisible()
  await page.locator('[data-test="destination-select"]').selectOption('database')
  await page.locator('[data-test="submit-restore"]').click()

  const resultText = await page.locator('[data-test="submission-results"]').innerText()
  const policyName = /Created (\S+) from/.exec(resultText)[1]

  execSync(`docker compose -f ${COMPOSE_FILE} exec -T database ./policyclient fetch`, { stdio: 'inherit' })

  await waitForJobSuccess(page, policyName) // reuse the existing helper's poll-by-reload pattern

  await page.locator('tbody tr', { hasText: policyName }).locator('a').click() // -> /jobs/:job_id
  await expect(page.locator('[data-test="log-line"]').first()).toBeVisible()
  await expect(page.locator('[data-test="log-line-message"]', { hasText: 'verified' }).first()).toBeVisible()

  const verifiedLine = page.locator('[data-test="log-line"]', { hasText: 'verified' }).first()
  await verifiedLine.locator('[data-test="log-line-summary"]').click()
  await expect(verifiedLine.locator('[data-test="log-line-fields"]')).toContainText('/var/lib/dbdata/dump.sql')

  const summaryLine = page.locator('[data-test="log-line"]', { hasText: 'summary' }).first()
  await summaryLine.locator('[data-test="log-line-summary"]').click()
  await expect(summaryLine.locator('[data-test="log-line-fields"]')).toContainText('warnings')
  await expect(summaryLine.locator('[data-test="log-line-fields"]')).toContainText('0')
})
```

(Exact selectors/assertions above are illustrative of intent; the implementation plan pins them
down precisely against the real rendered markup, same as every other design in this codebase.)

Steps, in prose:

1. Seed via the helper (real backup, real catalog data).
2. `/catalog` → drill into `/var/lib/dbdata` → check `dump.sql`'s checkbox.
3. `/restore` → assert the row appears (source and destination path both `/var/lib/dbdata/dump.sql`,
   since no rename was made), pick `database` as the destination host — restoring a reviewed copy
   back to its own host, the exact "stage it somewhere inspectable" motivation `dest_path` was built
   for — click submit.
4. Read the created policy's name off `[data-test="submission-results"]`'s rendered text
   (`Created {name} from {storeHost}`, per `RestoreView.vue`'s existing template).
5. `docker compose exec database ./policyclient fetch` — same non-UI forced-pickup step
   `seedRestoreCartCatalogData` already uses for its own backup policy.
6. Poll `/jobs` (page-reload loop, same pattern as the existing `waitForJobSuccess` helper) for a
   row containing that policy name with `success` in its State column.
7. Click that row's Job ID link → `/jobs/:job_id` → assert log lines rendered
   (`[data-test="log-line"]`), a `"verified"` message line whose expanded fields
   (`[data-test="log-line-fields"]`, reached by clicking `[data-test="log-line-summary"]`) show
   `path: /var/lib/dbdata/dump.sql`, and a `"summary"` message line whose fields show
   `warnings: 0`.

This also gives `LogLine.vue` its first real, non-mocked-API coverage — the same rationale
`restore-cart-e2e`'s own design used to justify a browser suite over mocking: catching real-DOM/
real-data issues (e.g., a field-expand click not working against real JSON structure) that
component tests mocking the store one layer up can't.

## Data Flow

```
Go test (src/e2e/restore_verify_test.go):
  POST /api/v1/restore {rules: [nonexistent path]}
    -> policy-server (restore policy, client_filters targets "database")
    -> docker compose exec database policyclient fetch (forced pickup)
    -> agent (on database) reconcile tick -> rwfs verify --rules-stdin
    -> bwfs ListFiles (store) -> rule matches nothing -> notFound
    -> rwfs exits non-zero -> agent logs event=finish status=failure
    -> GET /api/v1/jobs?kind=restore&source_host=database -> job state=failure
    -> GET /api/v1/jobs/{id}/logs -> "not found on this store" line

Playwright test (web/e2e/restore-verify.spec.js):
  UI: check dump.sql -> /restore table -> submit
    -> POST /api/v1/restore {rules: [real, backed-up path]}
    -> docker compose exec database policyclient fetch (forced pickup)
    -> agent (on database) reconcile tick -> rwfs verify --rules-stdin
    -> bwfs ListFiles + RestoreFile (store) -> chunk hashes verified
    -> rwfs exits 0 -> agent logs event=finish status=success
    -> UI: /jobs shows success -> /jobs/:job_id renders "verified" + "summary" log lines
```

## Error Handling

No new error-handling code paths beyond fix A itself (an out-of-range `kind` still 400s with an
accurate message). Both tests use the same bounded-timeout-then-`t.Fatalf`/Playwright-assertion-
timeout pattern every existing suite in this codebase already uses for eventual-consistency polling
— no new pattern introduced.

## Testing

This design's deliverable *is* tests, so "Testing" here means: how the two new suites' own
correctness is established, and why the success/failure split between Go and Playwright is the
right one (not an arbitrary one).

- **Why the failure path is Go-only:** Selecting a file for restore is driven entirely by
  checkbox-clicking real catalog rows (`CatalogView.vue`) — there is no UI affordance to name an
  arbitrary, non-existent path. A file-level rule for a file that was never backed up can only be
  constructed by calling `POST /api/v1/restore` directly, which has no client-side-only behavior to
  exercise; a Go integration test (REST calls only, no browser) gives full coverage of the
  server/agent-side "not found" behavior and its log output without paying for a browser.
- **Why the success path is Playwright-only:** It's the one avenue that provides real UI coverage
  end-to-end (selection → cart → submit → job list → job detail log rendering) — duplicating it in
  Go would just re-verify the REST layer a second time with no new signal, the same reasoning
  `restore-cart-e2e`'s design already used to justify Playwright over a second Go test.
- **`jobs_test.go`** (unit, not e2e): `kind=restore` accepted; `binariesForKind("restore") ==
  "agent"`.
- Both new e2e suites run only under their existing gates (`make test-e2e` for Go's `//go:build
  e2e`; `npx playwright test` / `test:e2e` script for `web`) — neither runs in the default fast unit
  test loop, matching every other e2e test in this codebase.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule:

- **`CHANGELOG.md`** — entry before merge: the `kind=restore` fix, and the new Go + Playwright
  restore-verification e2e coverage.
- **`docs/components/agent.md`**'s "Policy-driven restore verification" section — one sentence
  noting this path now has integration coverage (Go `TestE2E_RestoreVerification` for the failure
  case, `web/e2e/restore-verify.spec.js` for the success case), so a future reader doesn't have to
  rediscover that it was previously untested.
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
