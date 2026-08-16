import { execSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { seedRestoreCartCatalogData, waitForJobSuccess, waitForJobState, COMPOSE_FILE } from './helpers/policySeeding.js'

test.describe.configure({ mode: 'serial' })

test('restore verification', async ({ page, context }) => {
  // Seeding (its own real backup job) + this scenario's own restore job +
  // the log-line wait each poll a real backend interval in sequence -- the
  // task brief documents the full run as taking "up to ~3 minutes," which
  // exceeds playwright.config.js's project-wide 120s default (sized for
  // restore-cart.spec.js's shorter, single-job-wait scenario). Scoped to
  // just this test rather than raising the shared default. The
  // click-Restore step added after verification is UI-only and cheap, but
  // the budget still needs headroom beyond the original ~3 minutes since
  // step 1 alone runs close to it in practice.
  test.setTimeout(300_000)

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

  // JobDetailView only fetches logs once, on mount (no client-side polling) -- and rwfs's
  // own "verified"/"summary" lines are a separate Loki ingestion stream from the "agent"
  // binary's start/finish events waitForJobSuccess (or waitForJobState) just observed. Both
  // land at essentially the same real-world instant, but Loki's ingestion of the two streams
  // can still land a beat apart, so there's a short race window right after job success/failure
  // where this page's one-shot fetch can beat rwfs's lines into Loki. Retry by reloading (which
  // re-runs fetchLogs via onMounted) rather than waiting on the already-rendered, stale DOM --
  // same reasoning/shape as policySeeding.js's waitForJobState reload loop. Shared by both
  // steps below.
  async function waitForLogLine(filterText, timeoutMs = 30_000) {
    const deadline = Date.now() + timeoutMs
    for (;;) {
      const line = page.getByTestId('log-line').filter({ hasText: filterText }).first()
      if ((await line.count()) > 0) return line
      if (Date.now() > deadline) throw new Error(`Timed out waiting for a "${filterText}" log line`)
      await page.waitForTimeout(2000)
      await page.reload()
    }
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
    await page.getByTestId('verify-button').click()

    const resultText = await page.getByTestId('submission-results').innerText()
    const policyName = /Started verification policy (\S+) from/.exec(resultText)[1]

    // No UI/API surface to force policyclient's pickup faster than its
    // default 900s fetch interval -- same non-UI escape hatch
    // seedRestoreCartCatalogData already uses for its own backup policy.
    execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${sourceHost} ./policyclient fetch`, { stdio: 'inherit' })
    await waitForJobSuccess(page, policyName)

    await page.locator('tbody tr', { hasText: policyName }).locator('a').click()

    const verifiedLine = await waitForLogLine('verified')
    await expect(verifiedLine).toBeVisible()
    await verifiedLine.getByTestId('log-line-summary').click()
    await expect(verifiedLine.getByTestId('log-line-fields')).toContainText(filePath)

    // rwfs logs "summary" after each per-file "verified" line (see src/cmd/rwfs/verify.go),
    // but Loki delivery order across a stream isn't guaranteed by the time the "verified"
    // line above landed -- same reload-retry treatment.
    const summaryLine = await waitForLogLine('summary')
    await summaryLine.getByTestId('log-line-summary').click()
    await expect(summaryLine.getByTestId('log-line-fields')).toContainText('warnings')
    await expect(
      summaryLine
        .getByTestId('log-line-fields')
        .locator('dt', { hasText: 'warnings' })
        .locator('xpath=following-sibling::dd[1]')
    ).toHaveText('0')
  })

  await test.step('clicking Restore creates a real restore-execution policy, which agent picks up', async () => {
    // Step 1's waitForLogLine retries via page.reload() -- a real browser
    // reload, same as page.goto() -- so restoreCart's in-memory selection
    // doesn't survive step 1; re-select the same file rather than assuming
    // it's still there. mode: "restore" now succeeds end to end (agent runs
    // the new log-only `rwfs restore`), so unlike the old 501-rejection
    // scenario this creates a real policy that must be cleaned up -- same
    // try/finally-wrapped delete pattern as the sibling step below, minus
    // that step's infinite-retry risk: `rwfs restore` is one-shot and only
    // ever logs, so a leaked policy here can't starve later runs' dispatch
    // queue the way a permanently-failing verify policy would.
    const authHeaders = { Authorization: 'Bearer dev-placeholder-token-change-me' }

    await goToCatalogHome()
    for (const segment of segments) {
      await page.getByText(`${segment}/`, { exact: true }).click()
    }
    await page.getByTestId(`file-checkbox-${sourceHost}:${filePath}`).click()

    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByTestId(`restore-row-${sourceHost}:${filePath}`)).toBeVisible()

    const destinationSelect = page.getByTestId('destination-select')
    await expect(destinationSelect.locator('option', { hasText: sourceHost })).toHaveCount(1)
    await destinationSelect.selectOption(sourceHost)

    await page.getByTestId('overwrite-checkbox').check()
    await page.getByTestId('restore-button').click()

    // Unlike step 1's first-ever submission (where submission-results starts
    // absent and Playwright's own actionability wait for the element to
    // attach happens to synchronize with the async submit call), this step
    // starts with a stale submission-results <ul> already on the page --
    // step 1's own verify result, carried over in the (page-navigation-
    // persisted) restoreSubmission Pinia store. A plain .innerText() read
    // right after the click can win the race against submit()'s own reset
    // (results = [] synchronously, then repopulated once the POST
    // resolves) and return step 1's stale text. expect(...).toContainText
    // is a web-first assertion that polls until the DOM actually reflects
    // this step's own submission, so it can't observe that transient state.
    const resultsLocator = page.getByTestId('submission-results')
    await expect(resultsLocator).toContainText('Started restore policy')
    const resultText = await resultsLocator.innerText()
    const policyName = /Started restore policy (\S+) from/.exec(resultText)[1]

    // RestoreView's success copy confirms the /restore call returned 201,
    // but not the policy's id -- look it up by name via the REST API (the
    // UI has no affordance to read it back) so it can be deleted afterward.
    // Wrapped in try/finally, same shape as the sibling step below: if the
    // lookup/assertion throws, cleanup should still run rather than leaving
    // the policy behind.
    let policy
    try {
      const restorePoliciesResp = await page.request.get('/api/v1/policies?type=restore', { headers: authHeaders })
      const { data: restorePolicies } = await restorePoliciesResp.json()
      policy = restorePolicies.find((p) => p.name === policyName)
      expect(policy).toBeTruthy()
    } finally {
      // Don't throw on a failed delete -- that would mask whatever error
      // the try block raised -- but do warn, consistent with the sibling
      // step's cleanup below. `rwfs restore` is one-shot and log-only, so
      // unlike the sibling step's intentionally-failing policy, a leaked
      // policy here can't retry forever and starve later runs' dispatch
      // queue -- but it still shouldn't be left lying around.
      if (policy) {
        const deleteResp = await page.request.delete(`/api/v1/policies/${policy.id}`, { headers: authHeaders })
        if (!deleteResp.ok()) {
          console.warn(`cleanup: failed to delete policy ${policy.id}, status ${deleteResp.status()}`)
        }
      }
    }
  })

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
        mode: 'verify',
      },
    })
    expect(createResp.status()).toBe(201)
    const { id: failPolicyId } = await createResp.json()

    execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${sourceHost} ./policyclient fetch`, { stdio: 'inherit' })

    // One-shot-until-success: left alive, this policy retries with backoff
    // forever (it names a file that can never exist). The wait/assert block
    // below can throw (timeout or failed assertion) before ever reaching a
    // cleanup call at the bottom -- wrap it so the delete always runs,
    // otherwise a flaky run leaks a policy that never stops retrying and
    // silently degrades every later run's dispatch queue (exactly the
    // dispatch-starvation failure mode diagnosed earlier in this task).
    try {
      await waitForJobState(page, failPolicyName, 'failure')

      await page.locator('tbody tr', { hasText: failPolicyName }).locator('a').click()

      const notFoundLine = await waitForLogLine('verification failed')
      await expect(notFoundLine).toBeVisible()
      await notFoundLine.getByTestId('log-line-summary').click()
      // This rule sets no not_before/not_after, so the window covered all
      // of history and zero rows means the file is genuinely absent --
      // restoreResolver.NotFound reports the generic reason here, and
      // reserves "no version in timeframe" for a rule that actually asked
      // for a window. See cmd/rwfs/resolve.go's NotFound.
      await expect(notFoundLine.getByTestId('log-line-fields')).toContainText('not found on this store')
      await expect(notFoundLine.getByTestId('log-line-fields')).toContainText(missingPath)
    } finally {
      // Delete it the same way it was created. Don't throw on a failed
      // delete -- that would mask whatever error the try block raised --
      // but do warn, since a silently failed delete leaks a policy that
      // retries forever (see the comment above).
      const deleteResp = await page.request.delete(`/api/v1/policies/${failPolicyId}`, { headers: authHeaders })
      if (!deleteResp.ok()) {
        console.warn(`cleanup: failed to delete policy ${failPolicyId}, status ${deleteResp.status()}`)
      }
    }
  })
})
