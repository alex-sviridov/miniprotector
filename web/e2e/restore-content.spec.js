import { execFileSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { COMPOSE_FILE, waitForJobSuccess, waitForCatalogFolderRow } from './helpers/policySeeding.js'

const HOST = 'database'
const FILE_COUNT = 100
const MIN_SIZE = 102400 // 100KB
const MAX_SIZE = 2097152 // 2MB
const MIN_FREE_BYTES = 2 * 1024 * 1024 * 1024 // 2GiB

test.describe.configure({ mode: 'serial' })

test('restore writes real file content, verified by checksum, with a folder rename', async ({ page, context }) => {
  // Backup wait + restore wait each span a real 30s agent reconcile tick
  // plus real transfer time for ~100-150MB across 100 files, on top of the
  // UI navigation itself -- generously budgeted, same reasoning
  // restore-verify.spec.js's own 300s override documents.
  test.setTimeout(300_000)

  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  const runId = Date.now()
  const srcDir = `/data/e2e-restore-content-src-${runId}`
  const destDir = `/data/e2e-restore-content-dest-${runId}`
  const authHeaders = { Authorization: 'Bearer dev-placeholder-token-change-me' }

  // dockerExec runs script inside HOST's container via `bash -c`, passed
  // as a single execFileSync argv element -- no shell re-interpretation of
  // script's own contents by the outer Node process, so multi-line
  // scripts with their own quoting need no double-escaping.
  function dockerExec(script) {
    return execFileSync('docker', ['compose', '-f', COMPOSE_FILE, 'exec', '-T', HOST, 'bash', '-c', script], {
      encoding: 'utf8',
    })
  }

  // waitForDestinationFileCount polls (no UI/API affordance exists for
  // "has this restore finished" other than the Jobs page -- see the design
  // doc's discovered kind=restore visibility-lag note for why that's
  // deliberately not used here) until dir contains exactly count files, or
  // throws after timeoutMs. A real agent reconcile tick (up to 30s) must
  // elapse before the restore even starts, so the floor is well above
  // that; the restore itself completed in ~1s in every observed run.
  async function waitForDestinationFileCount(dir, count, timeoutMs = 90_000) {
    const deadline = Date.now() + timeoutMs
    for (;;) {
      let n = 0
      try {
        const out = dockerExec(`find "${dir}" -type f 2>/dev/null | wc -l`)
        n = parseInt(out.trim(), 10) || 0
      } catch {
        // dir doesn't exist yet -- treat as 0 and keep polling.
      }
      if (n === count) return
      if (Date.now() > deadline) {
        throw new Error(`Timed out waiting for ${count} files under ${dir} (found ${n})`)
      }
      await new Promise((resolve) => setTimeout(resolve, 3000))
    }
  }

  // generateAndManifest creates dir's tree (only when populate is true --
  // the destination side just needs a manifest of what restore already
  // wrote there) and always prints a sorted "<hash>  <absolute path>"
  // manifest as its last act, captured directly from dockerExec's return
  // value -- no second round-trip needed. There is no UI/API affordance
  // for creating files or hashing them, so this whole step stays
  // exec-driven, same justification this suite's other CLI-only steps
  // already document.
  function generateAndManifest(dir, populate) {
    const genScript = populate
      ? `
need_bytes=${MIN_FREE_BYTES}
avail=$(df --output=avail -B1 /data | tail -1 | tr -d ' ')
if [ "$avail" -lt "$need_bytes" ]; then
  echo "not enough free space on /data: need ${MIN_FREE_BYTES} bytes, have $avail" >&2
  exit 1
fi
rm -rf "${dir}"
for d in 0 1 2 3 4; do
  for s in 0 1 2 3; do
    mkdir -p "${dir}/d$d/s$s"
  done
done
i=0
while [ "$i" -lt ${FILE_COUNT} ]; do
  d=$(( i / 20 ))
  s=$(( (i / 5) % 4 ))
  size=$(shuf -i ${MIN_SIZE}-${MAX_SIZE} -n 1)
  path=$(printf "${dir}/d%d/s%d/file_%03d.bin" "$d" "$s" "$i")
  head -c "$size" /dev/urandom > "$path"
  i=$((i + 1))
done
`
      : ''
    return dockerExec(`set -eu
${genScript}
find "${dir}" -type f -exec sha256sum {} \\; | sort`)
  }

  // normalizeManifest strips dir's own absolute prefix from each
  // "<hash>  <path>" line, leaving "<hash>  <relative path>" -- the two
  // sides are different absolute directories (the restore's dest_path
  // rename), so only the relative shape is comparable.
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

  const srcManifest = normalizeManifest(generateAndManifest(srcDir, true), srcDir)
  expect(srcManifest.length).toBe(FILE_COUNT)

  let restorePolicyId = null
  try {
    // --- Backup, through the real UI (mirrors seedRestoreCartCatalogData's
    // exact steps, parameterized to this test's own path) ---
    const backupPolicyName = `e2e-restore-content-${runId}`
    await page.goto('/policies')
    await page.getByTestId('policy-new').click()
    await page.locator('input[name="name"]').fill(backupPolicyName)
    await page.getByTestId('hostname-add').click()
    await page.getByTestId('hostname-input').fill(HOST)
    await page.getByTestId('filter-add').click()
    await page.getByTestId('filter-path-input').fill(srcDir)

    const storageSelect = page.getByTestId('backup-policy-storage-select')
    await expect(storageSelect.locator('option', { hasText: 'store (store:8080)' })).toHaveCount(1)
    await storageSelect.selectOption({ label: 'store (store:8080)' })

    await page.getByTestId('backup-policy-run-now').click()
    await page.waitForURL('**/jobs')

    dockerExec('./policyclient fetch')
    await waitForJobSuccess(page, backupPolicyName)

    // --- Catalog selection + destination rename, through the real UI ---
    const parentSegments = srcDir.split('/').filter(Boolean).slice(0, -1) // ['data']
    await waitForCatalogFolderRow(page, parentSegments, srcDir)

    const folderCheckbox = page.getByTestId(`folder-checkbox-${srcDir}`)
    await folderCheckbox.click()
    await expect(folderCheckbox).toBeChecked()

    await page.getByRole('link', { name: 'Restore' }).click()
    const entryKey = `:${srcDir}`
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

    const restorePoliciesResp = await page.request.get('/api/v1/policies?type=restore', { headers: authHeaders })
    const { data: restorePolicies } = await restorePoliciesResp.json()
    const restorePolicy = restorePolicies.find((p) => p.name === restorePolicyName)
    expect(restorePolicy).toBeTruthy()
    restorePolicyId = restorePolicy.id

    // --- Restore completion ---
    dockerExec('./policyclient fetch')

    // NOT waitForJobSuccess here, deliberately -- see the design doc's
    // "Discovered: kind=restore job visibility lag" note. Empirically
    // (three live runs against a real demo stack), kind=backup/verify/
    // policy-update jobs become visible on the Jobs page within seconds,
    // but a real kind=restore job's own start/finish pair was observed
    // taking as long as ~9 minutes to appear via GET /api/v1/jobs -- while
    // the restore itself, and its own per-job log lines (fetched by job_id
    // directly, not through that aggregate listing), were already
    // available within about a second every single time. Waiting on the
    // Jobs list for this specific kind would make the test's runtime
    // hostage to an observability-pipeline latency issue that has nothing
    // to do with whether rwfs restore itself worked -- so completion is
    // instead detected the same way its correctness is: by looking at
    // what actually landed on disk.
    await waitForDestinationFileCount(destDir, FILE_COUNT)

    // --- Checksum verification, out of band (the other step with no
    // UI/API affordance -- there is no browser-based way to hash a file) ---
    const destManifest = normalizeManifest(generateAndManifest(destDir, false), destDir)
    expect(destManifest.length).toBe(FILE_COUNT)
    expect(destManifest).toEqual(srcManifest)
  } finally {
    // Best-effort: a failed cleanup is logged, never thrown, so it can't
    // mask whatever error the try block raised. The backup policy is
    // ad-hoc and self-expires (policies/adhoc sets DisabledAt server-side)
    // -- only the restore policy needs an explicit delete, same as
    // restore-verify.spec.js's own restore-policy cleanup.
    if (restorePolicyId) {
      const deleteResp = await page.request.delete(`/api/v1/policies/${restorePolicyId}`, { headers: authHeaders })
      if (!deleteResp.ok()) {
        console.warn(`cleanup: failed to delete restore policy ${restorePolicyId}, status ${deleteResp.status()}`)
      }
    }
    try {
      dockerExec(`rm -rf "${srcDir}" "${destDir}"`)
    } catch (err) {
      console.warn(`cleanup: failed to remove generated directories: ${err.message}`)
    }
  }
})
