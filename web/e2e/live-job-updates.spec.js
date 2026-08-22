import { execSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { COMPOSE_FILE } from './helpers/policySeeding.js'

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
async function runAdhocBackupPolicy(page, policyName) {
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

  // Same non-UI escape hatch policySeeding.js uses -- policyclient isn't on
  // $PATH inside the container (only /app/policyclient exists); docker
  // compose exec's default cwd is the image's WORKDIR (/app), so
  // `./policyclient` resolves it without needing an absolute path.
  execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${SOURCE_HOST} ./policyclient fetch`, { stdio: 'inherit' })
}

test('job detail page flips to Finished live, with no manual reload', async ({ page, context }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  const policyName = `e2e-live-detail-${Date.now()}`
  await runAdhocBackupPolicy(page, policyName)

  // /jobs is already open with its own live jobs-list WS connection (Task
  // 10) -- the new job's row appears here purely from that stream's
  // "upsert" message, no reload.
  const row = page.locator('tbody tr', { hasText: policyName })
  await expect(row).toBeVisible({ timeout: JOB_DISPATCH_TIMEOUT_MS })

  // Click through to the job's detail page while the job may still be
  // in_progress -- this is the scenario the whole feature exists for.
  await row.locator('a').click()
  await page.waitForURL('**/jobs/**')

  await expect(page.getByTestId('connection-status')).toHaveText(/Live|Connecting/)

  // Wait for the connection-status badge to flip to Finished purely from
  // the WS push -- Playwright's built-in auto-retrying expect() polls the
  // DOM without any reload or manual polling from this test, which is
  // exactly what a real user would see.
  await expect(page.getByTestId('connection-status')).toHaveText('Finished', { timeout: 30000 })

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

test('jobs list page shows a new job appear and transition to success live', async ({ page, context }) => {
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
  await runAdhocBackupPolicy(policyPage, policyName)
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
