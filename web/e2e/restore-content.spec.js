import { execFileSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { COMPOSE_FILE, waitForCatalogFolderRow } from './helpers/policySeeding.js'

const HOST = 'database'
const FILE_COUNT = 100
// The fixture is seeded once by demo/up.sh and backed up once by the
// seeded demo/policy-server/policies/backup/e2e-fixture.json policy --
// this test never generates data or triggers a backup itself, so repeated
// runs don't each add a fresh ~100-150MB to the shared store.
const FIXTURE_SRC_DIR = '/data/e2e-restore-content-fixture'
const AUTH_HEADERS = { Authorization: 'Bearer dev-placeholder-token-change-me' }

test.describe.configure({ mode: 'serial' })

test('restore writes real file content, verified by checksum, with a folder rename', async ({ page, context }) => {
  test.setTimeout(600_000)

  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  const runId = Date.now()
  const destDir = `/data/e2e-restore-content-dest-${runId}`

  // dockerExec runs script inside HOST's container via `bash -c`, passed
  // as a single execFileSync argv element -- no shell re-interpretation of
  // script's own contents by the outer Node process, so multi-line
  // scripts with their own quoting need no double-escaping.
  function dockerExec(script) {
    return execFileSync('docker', ['compose', '-f', COMPOSE_FILE, 'exec', '-T', HOST, 'bash', '-c', script], {
      encoding: 'utf8',
    })
  }

  // manifestOf prints a sorted "<hash>  <absolute path>" manifest of dir's
  // current contents. There is no UI/API affordance for hashing a file, so
  // this stays exec-driven, same justification this suite's other
  // CLI-only steps already document. find's own stderr is discarded (not
  // just the thrown error caught) so the expect.poll below's expected
  // early "destDir doesn't exist yet" attempts don't spam test output --
  // the poll's own final timeout/mismatch error is diagnostic enough.
  function manifestOf(dir) {
    return dockerExec(`find "${dir}" -type f -exec sha256sum {} \\; 2>/dev/null`)
  }

  // normalizeManifest strips dir's own absolute prefix from each
  // "<hash>  <path>" line, leaving "<hash>  <relative path>" -- the two
  // sides are different absolute directories (the restore's dest_path
  // rename), so only the relative shape is comparable. Sorted here (not
  // by the shell) since this is the single place both sides' ordering
  // actually needs to agree.
  function normalizeManifest(raw, dir) {
    return raw
      .split('\n')
      .filter((line) => line.trim() !== '')
      .map((line) => {
        const idx = line.indexOf('  ')
        const hash = line.slice(0, idx)
        const filePath = line.slice(idx + 2)
        return `${hash}  ${filePath.slice(dir.length + 1)}`
      })
      .sort()
  }

  // waitForCatalogEntryCount polls the catalog API (not the rendered
  // table -- this only needs a count, and may need to poll for minutes on
  // a cold demo stack whose seeded fixture policy hasn't backed up yet)
  // until source_host/pattern together match at least minCount entries.
  // Deliberately >=, not ===: the fixture's seeded backup policy is
  // recurring (every 5 minutes, same as every other demo-seeded backup
  // policy), so a long-lived demo stack accumulates more than one
  // version's worth of catalog rows for the same unchanged files over
  // time -- restore always resolves the latest version regardless, so
  // extra historical rows are expected, not a problem to wait out.
  async function waitForCatalogEntryCount(sourceHost, pattern, minCount, timeoutMs) {
    const deadline = Date.now() + timeoutMs
    for (;;) {
      const resp = await page.request.get(
        `/api/v1/catalog?source_host=${encodeURIComponent(sourceHost)}&pattern=${encodeURIComponent(pattern)}&limit=500`,
        { headers: AUTH_HEADERS }
      )
      const { data } = await resp.json()
      if (data.length >= minCount) return
      if (Date.now() > deadline) {
        throw new Error(`Timed out waiting for >=${minCount} catalog entries matching ${pattern} (found ${data.length})`)
      }
      await new Promise((resolve) => setTimeout(resolve, 5000))
    }
  }

  const srcManifest = normalizeManifest(manifestOf(FIXTURE_SRC_DIR), FIXTURE_SRC_DIR)
  expect(srcManifest.length).toBe(FILE_COUNT)

  // The fixture's seeded backup policy runs on its own 5-minute schedule
  // (demo/policy-server/policies/backup/e2e-fixture.json) -- on a demo
  // stack that just came up, this is the step that can genuinely take a
  // few minutes on the very first run. Once backed up, later runs (against
  // the same demo-up session) find it immediately.
  await waitForCatalogEntryCount(HOST, FIXTURE_SRC_DIR, FILE_COUNT, 360_000)

  let restorePolicyId = null
  try {
    // --- Catalog selection + destination rename, through the real UI ---
    const parentSegments = FIXTURE_SRC_DIR.split('/').filter(Boolean).slice(0, -1) // ['data']
    await waitForCatalogFolderRow(page, parentSegments, FIXTURE_SRC_DIR)

    const folderCheckbox = page.getByTestId(`folder-checkbox-${FIXTURE_SRC_DIR}`)
    await folderCheckbox.click()
    await expect(folderCheckbox).toBeChecked()

    await page.getByRole('link', { name: 'Restore' }).click()
    const entryKey = `:${FIXTURE_SRC_DIR}`
    await expect(page.getByTestId(`restore-row-${entryKey}`)).toBeVisible()

    await page.getByTestId(`dest-path-text-${entryKey}`).click()
    await page.getByTestId(`dest-path-input-${entryKey}`).fill(destDir)
    await page.getByTestId(`dest-path-input-${entryKey}`).press('Enter')
    await expect(page.getByTestId(`dest-path-text-${entryKey}`)).toHaveText(destDir)

    const destinationSelect = page.getByTestId('destination-select')
    await expect(destinationSelect.locator('option', { hasText: HOST })).toHaveCount(1)
    await destinationSelect.selectOption(HOST)

    await page.getByTestId('restore-button').click()

    const resultsLocator = page.getByTestId('submission-results')
    await expect(resultsLocator).toContainText('Started restore policy')
    const resultText = await resultsLocator.innerText()
    const restorePolicyName = /Started restore policy (\S+) from/.exec(resultText)[1]

    const restorePoliciesResp = await page.request.get('/api/v1/policies?type=restore', { headers: AUTH_HEADERS })
    const { data: restorePolicies } = await restorePoliciesResp.json()
    const restorePolicy = restorePolicies.find((p) => p.name === restorePolicyName)
    expect(restorePolicy).toBeTruthy()
    restorePolicyId = restorePolicy.id

    dockerExec('./policyclient fetch')

    // --- Restore completion + checksum verification, combined ---
    //
    // Deliberately not waitForJobSuccess: GET /api/v1/jobs?kind=restore was
    // found, during design validation, to lag by minutes before showing a
    // completed restore job, even though rwfs restore itself (confirmed via
    // its own per-job logs) completes correctly in under a second every
    // time -- a separate, out-of-scope observability gap. See the design
    // doc's "Discovered: kind=restore job visibility lag" section.
    //
    // Also deliberately not a plain file-count poll: writeRestoreFile
    // creates each destination file at its final size (O_CREATE|O_TRUNC
    // then Truncate(meta.Size)) *before* streaming its content
    // (src/cmd/rwfs/restorefile.go), so a bare file-count check can
    // observe the right count while some files are still mid-write --
    // real content, not yet fully written. Polling the checksum comparison
    // itself instead means an in-progress restore just fails one iteration
    // (wrong hash) and retries, never a false completion signal.
    await expect
      .poll(
        () => {
          let raw
          try {
            raw = manifestOf(destDir)
          } catch {
            return [] // destDir doesn't exist yet
          }
          return normalizeManifest(raw, destDir)
        },
        { timeout: 90_000, intervals: [2000, 3000, 5000] }
      )
      .toEqual(srcManifest)
  } finally {
    // Best-effort: a failed cleanup is logged, never thrown, so it can't
    // mask whatever error the try block raised. The fixture source
    // directory and its backup are permanent (seeded once by demo/up.sh),
    // never deleted here -- only this run's own destination directory and
    // restore policy are.
    if (restorePolicyId) {
      const deleteResp = await page.request.delete(`/api/v1/policies/${restorePolicyId}`, { headers: AUTH_HEADERS })
      if (!deleteResp.ok()) {
        console.warn(`cleanup: failed to delete restore policy ${restorePolicyId}, status ${deleteResp.status()}`)
      }
    }
    try {
      dockerExec(`rm -rf "${destDir}"`)
    } catch (err) {
      console.warn(`cleanup: failed to remove destination directory: ${err.message}`)
    }
  }
})
