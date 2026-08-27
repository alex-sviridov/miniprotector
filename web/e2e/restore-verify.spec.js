import { execSync } from 'node:child_process'
import { test, expect, AUTH_HEADERS } from './helpers/test.js'
import { seedRestoreCartCatalogData, waitForJobSuccess, waitForJobState, COMPOSE_FILE } from './helpers/policySeeding.js'

test.describe.configure({ mode: 'serial' })

test('restore verification', async ({ page, context, trackPolicy }) => {
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

  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page, trackPolicy)
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

    // Verify submits directly (no confirmation modal -- only Restore has
    // one, Task 6) via RestoreView's per-row status column, not the old
    // flat submission-results list Task 7 removed.
    const entryKey = `${sourceHost}:${filePath}`
    await page.getByTestId('verify-button').click()

    const statusLink = page.getByTestId(`status-${entryKey}`).getByRole('link')
    await expect(statusLink).toBeVisible({ timeout: 30_000 })
    const href = await statusLink.getAttribute('href') // "/jobs/<job_id>", from RestoreView's router-link
    const jobId = href.replace(/^\/jobs\//, '')
    // policy-server's restorePolicyJobID (write.go) prefixes every
    // verify-mode job id "verify:" -- confirms the status link really
    // points at a verify (not restore) job before it's used below.
    expect(jobId).toMatch(/^verify:/)

    // policyDTO (cmd/api-server/policies.go) serializes job_id -- it's been
    // on the underlying pb.Policy message all along (field 22), policy-server
    // sets it synchronously at creation time, the REST layer just used to
    // never expose it -- so the policy this submission created can be found
    // directly by the job_id its own status link already named.
    const verifyPoliciesResp = await page.request.get('/api/v1/policies?type=restore', { headers: AUTH_HEADERS })
    const { data: verifyPolicies } = await verifyPoliciesResp.json()
    const verifyPolicy = verifyPolicies.find((p) => p.job_id === jobId)
    expect(verifyPolicy).toBeTruthy()
    trackPolicy(verifyPolicy.id)

    // No UI/API surface to force policyclient's pickup faster than its
    // default 900s fetch interval -- same non-UI escape hatch
    // seedRestoreCartCatalogData already uses for its own backup policy.
    execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${sourceHost} ./policyclient fetch`, { stdio: 'inherit' })
    await waitForJobSuccess(page, verifyPolicy.name)

    await page.locator('tbody tr', { hasText: verifyPolicy.name }).locator('a').click()

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
    // it's still there.
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

    // This step re-selects the same file fresh (see the comment above), so
    // its own entry starts with no prior status -- unlike restore-content.spec.js,
    // there's no stale-status race to guard against here.
    const entryKey = `${sourceHost}:${filePath}`
    await page.getByTestId('restore-button').click()
    await page.getByTestId('confirm-restore').click() // Task 6's pre-restore confirmation modal

    const statusLink = page.getByTestId(`status-${entryKey}`).getByRole('link')
    await expect(statusLink).toBeVisible({ timeout: 30_000 })
    const href = await statusLink.getAttribute('href') // "/jobs/<job_id>", from RestoreView's router-link
    const jobId = href.replace(/^\/jobs\//, '')
    // policy-server's restorePolicyJobID (write.go) prefixes every
    // restore-mode job id "restore:" -- confirms the status link really
    // points at a restore (not verify) job before it's used below.
    expect(jobId).toMatch(/^restore:/)

    // RestoreView's status link confirms the /restore call returned 201 and
    // which job it started; policyDTO now serializes job_id too (see step
    // 1's comment above), so the policy itself -- needed only for
    // trackPolicy's cleanup -- can be found directly by that job_id.
    const restorePoliciesResp = await page.request.get('/api/v1/policies?type=restore', { headers: AUTH_HEADERS })
    const { data: restorePolicies } = await restorePoliciesResp.json()
    const policy = restorePolicies.find((p) => p.job_id === jobId)
    expect(policy).toBeTruthy()
    trackPolicy(policy.id)
  })

  await test.step('a rule naming a file that was never backed up fails, readable in its job log', async () => {
    // No UI affordance exists to select a file that was never backed up --
    // CatalogView.vue only ever renders checkboxes for real catalog rows.
    // This is the one non-UI step in this scenario; everything after it
    // (waiting, opening the job, reading the log) is the same mix of
    // forced-fetch-then-browser-driven flow the success scenario above uses.
    const storagePoliciesResp = await page.request.get('/api/v1/policies?type=storage', { headers: AUTH_HEADERS })
    const { data: storagePolicies } = await storagePoliciesResp.json()
    const storagePolicyId = storagePolicies.find((p) => p.name === 'store').id

    const missingPath = `${dirPath}/does-not-exist.sql`
    const failPolicyName = `e2e-restore-verify-fail-${Date.now()}`
    const createResp = await page.request.post('/api/v1/restore', {
      headers: AUTH_HEADERS,
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
    // One-shot-until-success: left alive, this policy retries with backoff
    // forever (it names a file that can never exist). trackPolicy's
    // teardown runs regardless of what happens below, so registering it
    // here -- before any of the waiting/asserting that could throw --
    // guarantees it's cleaned up either way.
    trackPolicy(failPolicyId)

    execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${sourceHost} ./policyclient fetch`, { stdio: 'inherit' })

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
  })
})
