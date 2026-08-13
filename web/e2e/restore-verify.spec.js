import { execSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { seedRestoreCartCatalogData, waitForJobSuccess, COMPOSE_FILE } from './helpers/policySeeding.js'

test.describe.configure({ mode: 'serial' })

test('restore verification', async ({ page, context }) => {
  // Seeding (its own real backup job) + this scenario's own restore job +
  // the log-line wait each poll a real backend interval in sequence -- the
  // task brief documents the full run as taking "up to ~3 minutes," which
  // exceeds playwright.config.js's project-wide 120s default (sized for
  // restore-cart.spec.js's shorter, single-job-wait scenario). Scoped to
  // just this test rather than raising the shared default.
  test.setTimeout(240_000)

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

    // JobDetailView only fetches logs once, on mount (no client-side polling) -- and rwfs's
    // own "verified"/"summary" lines are a separate Loki ingestion stream from the "agent"
    // binary's start/finish events waitForJobSuccess just observed. Both land at essentially
    // the same real-world instant, but Loki's ingestion of the two streams can still land a
    // beat apart, so there's a short race window right after job success where this page's
    // one-shot fetch can beat rwfs's lines into Loki. Retry by reloading (which re-runs
    // fetchLogs via onMounted) rather than waiting on the already-rendered, stale DOM --
    // same reasoning/shape as policySeeding.js's waitForJobState reload loop.
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
    await expect(summaryLine.getByTestId('log-line-fields')).toContainText('0')
  })
})
