import { execSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect } from '@playwright/test'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
export const COMPOSE_FILE = path.resolve(__dirname, '../../../demo/docker-compose.yml')

const SOURCE_HOST = 'database'
const DIR_PATH = '/var/lib/dbdata'
const FILES = ['dump.sql', 'schema.sql']
const STORAGE_OPTION_LABEL = 'store (store:8080)'

// seedRestoreCartCatalogData creates and runs a fast ad-hoc backup policy
// through the real /policies UI (BackupPolicyFormModal's "Run now"), then
// forces the target node to pick it up immediately -- the ad-hoc policy is,
// server-side, an ordinary pull-model policy under the hood (see
// docs/superpowers/specs/2026-08-02-policy-disabled-at-design.md), so
// without this it wouldn't be discovered for up to policyclient's default
// 900s fetch interval. This is the one step with no UI/API surface, exactly
// like src/e2e/lifecycle_test.go's own two CLI-only steps.
export async function seedRestoreCartCatalogData(page) {
  const policyName = `e2e-restore-cart-${Date.now()}`

  await page.goto('/policies')
  await page.getByTestId('policy-new').click()

  await page.locator('input[name="name"]').fill(policyName)

  await page.getByTestId('hostname-add').click()
  await page.getByTestId('hostname-input').fill(SOURCE_HOST)

  await page.getByTestId('filter-add').click()
  await page.getByTestId('filter-path-input').fill(DIR_PATH)

  const storageSelect = page.getByTestId('backup-policy-storage-select')
  // storagePolicies.fetchAll() runs on the modal's onMounted -- wait for
  // the real option to exist before selecting it, rather than racing it.
  await expect(storageSelect.locator('option', { hasText: STORAGE_OPTION_LABEL })).toHaveCount(1)
  await storageSelect.selectOption({ label: STORAGE_OPTION_LABEL })

  await page.getByTestId('backup-policy-run-now').click()
  await page.waitForURL('**/jobs')

  // policyclient isn't on $PATH inside the container (only /app/policyclient
  // exists); docker compose exec's default cwd is the image's WORKDIR (/app),
  // so `./policyclient` resolves it without needing an absolute path.
  execSync(`docker compose -f ${COMPOSE_FILE} exec -T database ./policyclient fetch`, { stdio: 'inherit' })

  await waitForJobSuccess(page, policyName)
  await waitForCatalogFiles(page, DIR_PATH, FILES)

  return { sourceHost: SOURCE_HOST, dirPath: DIR_PATH, files: FILES }
}

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

async function waitForCatalogFiles(page, dirPath, files, timeoutMs = 60_000) {
  // The catalog's directory-children endpoint synthesizes a top-level "/"
  // folder row (rendered as "//" since the row template appends "/" to
  // row.name) that sits between Home and the path's real first segment --
  // confirmed against the live /catalog/directories/children API, which
  // returns a single {name: "/"} entry at parent_path="". It must be
  // clicked through like any other folder row before "var/" etc. appear.
  const segments = ['/', ...dirPath.split('/').filter(Boolean)]
  const deadline = Date.now() + timeoutMs
  for (;;) {
    await page.goto('/catalog')
    let reachedTarget = true
    for (const segment of segments) {
      try {
        await page.getByText(`${segment}/`, { exact: true }).click({ timeout: 5000 })
      } catch {
        reachedTarget = false
        break
      }
    }
    if (reachedTarget) {
      const counts = await Promise.all(files.map((f) => page.getByText(f, { exact: true }).count()))
      if (counts.every((c) => c > 0)) return
    }
    if (Date.now() > deadline) throw new Error(`Timed out waiting for catalog files under ${dirPath}`)
    await page.waitForTimeout(3000)
  }
}
