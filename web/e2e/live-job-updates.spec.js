import { execSync } from 'node:child_process'
import { test, expect, AUTH_HEADERS } from './helpers/test.js'
import { COMPOSE_FILE, waitForJobState } from './helpers/policySeeding.js'

const SOURCE_HOST = 'database'
const DIR_PATH = '/var/lib/dbdata'
const STORAGE_OPTION_LABEL = 'store (store:8080)'

// An ad-hoc policy is only picked up on SOURCE_HOST's agent's own next
// reconcile tick (demo/local.conf: ReconcileIntervalSec=30) -- policyclient
// fetch refreshes the policy cache immediately, but dispatch itself can
// still be up to one full reconcile_interval away. Row-visibility waits
// below must comfortably clear that worst case plus live-push propagation,
// not just the (near-instant) job runtime itself.
const JOB_DISPATCH_TIMEOUT_MS = 45000

test.describe.configure({ mode: 'serial' })

// runAdhocBackupPolicy drives the same "New backup" -> fill form -> "Run now"
// flow as helpers/policySeeding.js's seedRestoreCartCatalogData (same
// data-test selectors, same order), then forces the target node to pick the
// resulting ad-hoc policy up immediately the same way that helper does --
// ad-hoc policies are, server-side, ordinary pull-model policies (see that
// helper's own comment), so without this they wouldn't be discovered for up
// to policyclient's default 900s fetch interval.
//
// Unlike seedRestoreCartCatalogData, this deliberately does NOT wait for the
// job to reach any particular state afterward: the whole point of this suite
// is to land on /jobs (or /jobs/:job_id) while the job may still be running
// and observe the WS-pushed live update do the rest -- waiting here would
// defeat that.
async function runAdhocBackupPolicy(page, policyName, trackPolicy) {
  await page.goto('/policies')
  await page.getByTestId('policy-new').click()

  await page.locator('input[name="name"]').fill(policyName)

  await page.getByTestId('hostname-add').click()
  await page.getByTestId('hostname-input').fill(SOURCE_HOST)

  await page.getByTestId('filter-add').click()
  await page.getByTestId('filter-path-input').fill(DIR_PATH)

  const storageSelect = page.getByTestId('backup-policy-storage-select')
  // storagePolicies.fetchAll() runs on the modal's onMounted -- wait for the
  // real option to exist before selecting it, rather than racing it (same
  // reasoning as policySeeding.js's own storageSelect wait).
  await expect(storageSelect.locator('option', { hasText: STORAGE_OPTION_LABEL })).toHaveCount(1)
  await storageSelect.selectOption({ label: STORAGE_OPTION_LABEL })

  await page.getByTestId('backup-policy-run-now').click()
  await page.waitForURL('**/jobs')

  // The UI gives no way to read back the ad-hoc backup policy's id, so look
  // it up by the name we just gave it and hand it to trackPolicy -- same
  // lookup-by-name-after-creation pattern policySeeding.js's
  // seedRestoreCartCatalogData uses for the same kind of policy.
  const policiesResp = await page.request.get('/api/v1/policies?type=backup', { headers: AUTH_HEADERS })
  const { data: backupPolicies } = await policiesResp.json()
  const backupPolicy = backupPolicies.find((p) => p.name === `adhoc_${policyName}`)
  if (backupPolicy) trackPolicy(backupPolicy.id)

  // Same non-UI escape hatch policySeeding.js uses -- policyclient isn't on
  // $PATH inside the container (only /app/policyclient exists); docker
  // compose exec's default cwd is the image's WORKDIR (/app), so
  // `./policyclient` resolves it without needing an absolute path.
  execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${SOURCE_HOST} ./policyclient fetch`, { stdio: 'inherit' })
}

test('job detail page flips to Finished live, with no manual reload', async ({ page, context, trackPolicy }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  const policyName = `e2e-live-detail-${Date.now()}`
  await runAdhocBackupPolicy(page, policyName, trackPolicy)

  // /jobs is already open with its own live jobs-list WS connection (Task
  // 10) -- the new job's row appears here purely from that stream's
  // "upsert" message, no reload.
  const row = page.locator('tbody tr', { hasText: policyName })
  await expect(row).toBeVisible({ timeout: JOB_DISPATCH_TIMEOUT_MS })

  // Click through to the job's detail page while the job may still be
  // in_progress -- this is the scenario the whole feature exists for. It's
  // optimistic, not guaranteed: this demo's backup jobs (a couple of tiny
  // fixture files) routinely finish in well under 100ms, so by the time the
  // row above became visible the backend may already be done, and the
  // detail page's live tail can receive a backlog that already includes the
  // finish line before this test ever gets to look -- landing straight on
  // "Finished" with no observable "Live"/"Connecting" step in between.
  // Accepting all three here (rather than asserting an in-progress state
  // that isn't actually promised) removes that race; the meaningful checks
  // are the ones below, that the page reaches "Finished" and shows the
  // finish log line without a manual reload, regardless of how much of that
  // happened before or after navigation.
  await row.locator('a').click()
  await page.waitForURL('**/jobs/**')

  await expect(page.getByTestId('connection-status')).toHaveText(/Live|Connecting|Finished/)

  // Wait for the connection-status badge to show Finished purely from the
  // WS push (or, per the above, it may already be there) -- Playwright's
  // built-in auto-retrying expect() polls the DOM without any reload or
  // manual polling from this test, which is exactly what a real user would
  // see. Timeout matches JOB_DISPATCH_TIMEOUT_MS's own margin for a loaded
  // host/CI runner, not just this page's own live-push latency.
  await expect(page.getByTestId('connection-status')).toHaveText('Finished', {
    timeout: JOB_DISPATCH_TIMEOUT_MS,
  })

  // The finish log line itself must be visible in the rendered list too,
  // not just the status badge. Not agent's own "policy execution completed"
  // wrapper line: _mergeLogLine disconnects the stream the instant it sees
  // a finish-marking line, and real timestamps show bwfs's own commit line
  // (event=finish) consistently lands a few ms *before* agent's trailing
  // wrapper line -- the stream closes right as bwfs's line arrives, before
  // agent's line would even be ingested, so it's structurally unreachable
  // here regardless of timeout. bwfs/brfs's own finish line is what
  // logsStatus actually keys off (isFinishLine), so it's what's guaranteed
  // to render.
  await expect(
    page.locator('[data-test="log-line-summary"]', { hasText: /finished|committed/i }).first()
  ).toBeVisible({
    timeout: 15000,
  })
})

test('jobs list page shows a new job appear and transition to success live', async ({ page, context, trackPolicy }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  await page.goto('/jobs')
  await expect(page.getByTestId('connection-status')).toHaveText(/Live|Connecting/)

  // Trigger the job from a second tab in the same context (so it shares the
  // localStorage auth token) while the first tab stays on /jobs -- the key
  // assertions below are about what happens on THAT already-open page,
  // purely from its own live stream, not about how the job gets triggered.
  const policyName = `e2e-live-list-${Date.now()}`
  const policyPage = await context.newPage()
  await runAdhocBackupPolicy(policyPage, policyName, trackPolicy)
  await policyPage.close()

  // Not a row-count delta: the table is paginated (DataTable's perPage), so
  // on a long-running fleet with a full page of history already showing,
  // the visible DOM row count stays flat regardless of how many jobs exist
  // -- the row this test actually cares about is the new one landing on
  // the visible (first) page at all, which DataTable's started_at-desc
  // default sort is what guarantees.
  const newRow = page.locator('tbody tr', { hasText: policyName })
  await expect(newRow).toBeVisible({ timeout: JOB_DISPATCH_TIMEOUT_MS })
  // Badge.vue's "ok" variant (used for state === 'success') renders
  // bg-emerald-50 -- no data-test on Badge itself, so match the same way
  // policySeeding.js's own waitForJobState does (state text within the row),
  // scoped to the emerald success styling to distinguish it from any other
  // cell that might incidentally contain the word "success".
  await expect(newRow.locator('.bg-emerald-50', { hasText: 'success' })).toBeVisible({ timeout: 30000 })
})

test('restore policy job shows in_progress from creation, then the policy is cleaned up once the job finishes and the grace period elapses', async ({
  page,
  context,
  trackPolicy,
}) => {
  // Covers the full restore-policy lifecycle this feature exists for
  // (docs/superpowers/specs/2026-08-23-restore-policy-lifecycle-design.md):
  // a job_id exists, and shows in_progress, from the instant the policy is
  // created -- not from whenever the target node happens to pick it up --
  // and the policy itself is deleted automatically once its job is done and
  // a grace period has passed. demo/local.conf shortens
  // RestoreCleanupIntervalSec/RestoreCleanupGracePeriodSec (production
  // defaults: 300s/900s) to 5s each for exactly this test, so the wait
  // below stays practical.
  test.setTimeout(180_000)

  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  // Same non-UI lookup restore-verify.spec.js's third scenario uses to get
  // a real storage_policy_id without going through the UI.
  const storagePoliciesResp = await page.request.get('/api/v1/policies?type=storage', { headers: AUTH_HEADERS })
  const { data: storagePolicies } = await storagePoliciesResp.json()
  const storagePolicyId = storagePolicies.find((p) => p.name === 'store').id

  // verify-mode is sufficient here -- no destination/catalog seeding
  // needed (brief). The rule's path deliberately doesn't need to exist:
  // whether the eventual verify job succeeds or fails is irrelevant below
  // (GetPolicyJobStatus's "finished" signal, and the cleanup sweep that
  // reads it, fire on any terminal state -- restore_cleanup.go), so a
  // nonexistent path (the same trick restore-verify.spec.js's third
  // scenario uses) keeps this test independent of any prior seeding.
  const policyName = `e2e-restore-lifecycle-${Date.now()}`
  const missingPath = `${DIR_PATH}/does-not-exist-${Date.now()}.sql`

  const createResp = await page.request.post('/api/v1/restore', {
    headers: AUTH_HEADERS,
    data: {
      name: policyName,
      client_filters: { hostnames: [SOURCE_HOST] },
      storage_policy_id: storagePolicyId,
      rules: [{ host: SOURCE_HOST, path: missingPath, include: true }],
      mode: 'verify',
    },
  })
  expect(createResp.status()).toBe(201)
  const { id: policyId } = await createResp.json()
  // Registered before any waiting/polling below that could throw --
  // trackPolicy's teardown runs regardless of outcome, so a slow or failed
  // run (e.g. the cleanup-sweep assertion below never converging) still
  // deletes this policy rather than leaking it past the test.
  trackPolicy(policyId)

  // policyDTO (api-server's REST layer) never exposes job_id (see
  // cmd/api-server/policies.go's toPolicyDTO) -- so, same as every other
  // scenario in this suite, the job is only observable through the Jobs
  // list, found by the policy name it embeds verbatim. policy-server's
  // restorePolicyJobID (write.go) builds it as "verify:<name>:<nanos>" for
  // mode=="verify", so the row-by-name lookup every other test here uses
  // applies unchanged.
  await page.goto('/jobs')
  const row = page.locator('tbody tr', { hasText: policyName })

  // The scenario's central assertion: this row must exist and already
  // show in_progress *before* SOURCE_HOST's agent has been forced to do
  // anything -- no "policyclient fetch" has been issued yet at this point.
  // policy-server's CreatePolicy logs event=created synchronously
  // (write.go), which api-server's job aggregator turns into an
  // in_progress row (jobs.go's jobEventAccumulator.get seeds
  // State: "in_progress" for any job it has no finish line for yet) --
  // independent of whether any agent has even received the policy.
  await expect(row).toBeVisible({ timeout: JOB_DISPATCH_TIMEOUT_MS })
  await expect(row).toContainText('in_progress')

  // Now let the real lifecycle proceed: force SOURCE_HOST's agent to pick
  // the policy up immediately (same non-UI escape hatch every other spec
  // in this suite/helpers file uses -- policyclient's own fetch interval
  // is far too slow for e2e), then wait for the job to reach a terminal
  // state. Failure is expected (missingPath was never backed up) and is
  // fine: GetPolicyJobStatus's "finished" signal fires on any terminal
  // state, not just success.
  execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${SOURCE_HOST} ./policyclient fetch`, { stdio: 'inherit' })
  await waitForJobState(page, policyName, 'failure')

  // Finally, the policy itself must disappear once policy-server's
  // cleanup sweep (restore_cleanup.go) observes the finished job and the
  // (shortened, see above) grace period elapses.
  await expect
    .poll(
      async () => {
        const resp = await page.request.get('/api/v1/policies?type=restore', { headers: AUTH_HEADERS })
        const { data } = await resp.json()
        return data.some((p) => p.id === policyId)
      },
      { timeout: 60_000, intervals: [3000] }
    )
    .toBe(false)
})
