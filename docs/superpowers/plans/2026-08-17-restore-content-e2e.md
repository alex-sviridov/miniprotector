# E2E: Restore File Content, Verified by Checksum Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Playwright e2e test that generates 100 random files in a nested directory tree, backs them up, restores them through the real web UI with a folder rename, and proves byte-for-byte correctness via checksum comparison.

**Architecture:** One new Playwright spec (`web/e2e/restore-content.spec.js`) plus one small additive helper export in `web/e2e/helpers/policySeeding.js`. Backup creation, catalog browsing, folder selection, destination-path rename, and restore submission all go through the real browser UI; dataset generation and checksumming are the only steps with no UI/API surface, so they use `execFileSync` docker-exec calls, matching this suite's existing convention.

**Tech Stack:** Playwright (`@playwright/test`, already a devDependency), Node's `node:child_process` (`execFileSync`), the existing demo Docker Compose stack (`demo/docker-compose.yml`), `bash`/`shuf`/`sha256sum`/`find`/`head`/`df` inside the `database` container (all confirmed present, standard `debian:bookworm-slim` coreutils).

## Global Constraints

- This spec **has already been built and validated twice, live, against a real, freshly-rebuilt `make demo-up` stack** during the design phase — both runs passed cleanly (~38s and ~51s), with confirmed clean cleanup (no leftover directories, no leftover policies) afterward. The code in this plan is that exact validated code, not a first draft. Treat deviations from it as requiring a specific reason, not stylistic preference.
- Design spec: `docs/superpowers/specs/2026-08-17-restore-content-e2e-design.md` — read it for the full "why," including a real, reproducible finding (`kind=restore` job-visibility lag via `GET /api/v1/jobs`, up to ~9 minutes in two observed runs) that is the reason this plan's restore-completion detection polls the destination filesystem instead of the Jobs page. Do not "fix" this back to `waitForJobSuccess` without re-reading that section.
- No `Makefile` change. This lives in the Playwright suite (`cd web && npx playwright test`, already run by `make test-e2e`), not the Go `e2e` package — no shared timeout budget.
- Host: `database` (not `webserver`, and not both) — see the design doc's "Host choice" section for why.
- The demo stack must be running **and built from the current checkout** (i.e., including all `restore-file-content` work already on this branch) before this test can pass — `rwfs restore` must actually write file content for this test to have anything to verify. If `make demo-up` hasn't been (re-)run since the last code change on this branch, run it first; it rebuilds every service's image before starting anything, and is safe to re-run against already-enrolled nodes.
- `web/node_modules` must be installed (`npm install` in `web/`) and Playwright's browsers available (`npx playwright install` if `npx playwright test` reports a missing browser).

---

## File Map

| Path | Status | Responsibility |
|------|--------|----------------|
| `web/e2e/helpers/policySeeding.js` | Modify | Adds `waitForCatalogFolderRow`, a new export generalizing the existing `waitForCatalogFiles` drill-and-poll shape to wait for a folder row instead of specific leaf filenames |
| `web/e2e/restore-content.spec.js` | Create | The new e2e test: generate → back up → restore with rename (real UI) → verify by checksum |

---

## Task 1: `waitForCatalogFolderRow` helper + `restore-content.spec.js`

**Files:**
- Modify: `web/e2e/helpers/policySeeding.js`
- Create: `web/e2e/restore-content.spec.js`

**Interfaces:**
- Consumes: `COMPOSE_FILE`, `waitForJobSuccess` (both already exported from `policySeeding.js`, unchanged).
- Produces: `export async function waitForCatalogFolderRow(page, parentSegments, dirPath, timeoutMs = 60_000)` — no other file consumes it yet; this task's own spec is the only caller.

- [ ] **Step 1: Add `waitForCatalogFolderRow` to `policySeeding.js`**

Append to the end of `web/e2e/helpers/policySeeding.js` (after the existing `waitForCatalogFiles` function's closing `}`):

```js

// waitForCatalogFolderRow polls (reload loop, same shape as
// waitForCatalogFiles -- catalog replication is server-side, no
// client-side event to await) until dirPath's own folder row is visible
// under its parent, then leaves the page drilled down to that parent so a
// caller can act on the row immediately (e.g. click its checkbox) without
// navigating again. parentSegments is dirPath's parent's real path
// segments (not including the synthetic "/" root, which this function
// prepends itself). Unlike waitForCatalogFiles, this checks for the
// folder row itself rather than specific leaf filenames -- a freshly
// generated tree's filenames aren't known ahead of time the way
// waitForCatalogFiles' fixed sample-data filenames are.
export async function waitForCatalogFolderRow(page, parentSegments, dirPath, timeoutMs = 60_000) {
  const segments = ['/', ...parentSegments]
  const deadline = Date.now() + timeoutMs
  for (;;) {
    await page.goto('/catalog')
    let reachedParent = true
    for (const segment of segments) {
      try {
        await page.getByText(`${segment}/`, { exact: true }).click({ timeout: 5000 })
      } catch {
        reachedParent = false
        break
      }
    }
    if (reachedParent && (await page.getByTestId(`folder-checkbox-${dirPath}`).count()) > 0) return
    if (Date.now() > deadline) throw new Error(`Timed out waiting for catalog folder ${dirPath} to appear`)
    await page.waitForTimeout(3000)
  }
}
```

`waitForCatalogFiles` immediately above it in the file is completely unchanged — this is a pure
addition, no existing export's signature or behavior changes.

- [ ] **Step 2: Create `web/e2e/restore-content.spec.js`**

```js
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
```

- [ ] **Step 3: Confirm the demo stack is up to date**

Run: `docker compose -f demo/docker-compose.yml ps`
Expected: all services `Up`. Check the `database` image's build time against the current git HEAD's
commit time:

```bash
docker inspect $(docker compose -f demo/docker-compose.yml ps -q database) --format '{{.Created}}'
git log -1 --format=%cI
```

If the image predates the latest commit on this branch (in particular, anything from the
`restore-file-content` work), rebuild: `make demo-up` (idempotent, rebuilds every service's image
from the current checkout before restarting; safe to re-run against already-enrolled nodes — see
`demo/up.sh`). This takes a few minutes.

- [ ] **Step 4: Install dependencies if needed**

```bash
cd web && npm install
```

Expected: completes without error (some `npm audit` warnings are pre-existing and unrelated).

- [ ] **Step 5: Run the new test**

```bash
cd web && npx playwright test restore-content.spec.js
```

Expected: `1 passed`, roughly 30-60s. If Playwright reports a missing browser, run
`npx playwright install` once and retry.

- [ ] **Step 6: Run it a second time to confirm reliability**

```bash
cd web && npx playwright test restore-content.spec.js
```

Expected: `1 passed` again. (This step exists because the whole reason this plan's restore-completion
mechanism looks the way it does is a reliability finding from exactly this kind of repeat run during
design — confirm it still holds.)

- [ ] **Step 7: Confirm clean cleanup**

```bash
docker compose -f demo/docker-compose.yml exec -T database bash -c 'ls /data | grep e2e-restore-content || echo "none left"'
curl -s -H "Authorization: Bearer dev-placeholder-token-change-me" "http://localhost:8090/api/v1/policies?type=restore" | python3 -m json.tool
```

Expected: `none left` for the directory check; the restore-policies list contains no
`e2e-restore-content-*`-named entries (only whatever pre-existing demo-seeded restore policies were
already there, if any, are acceptable).

- [ ] **Step 8: Run the existing e2e suite once to confirm no regression**

```bash
cd web && npx playwright test
```

Expected: all specs pass (`smoke.spec.js`, `restore-cart.spec.js`, `restore-verify.spec.js`,
`restore-content.spec.js`). This suite runs fully sequentially (`workers: 1` in
`playwright.config.js`), so this also re-confirms there's no cross-spec interference from adding a
fourth spec to the same `database` host the two `restore-*` specs already share.

- [ ] **Step 9: Commit**

```bash
git add web/e2e/helpers/policySeeding.js web/e2e/restore-content.spec.js
git commit -m "test(e2e): verify rwfs restore's file content by checksum, via the real UI

Generates 100 random files (100KB-2MB) in a nested tree, backs them
up and restores them with a folder rename entirely through the real
web UI (catalog folder selection, the destination-path-rename UI,
restore submission), then proves byte-for-byte correctness via a
sha256sum manifest comparison -- the two steps with no UI/API
affordance stay exec-driven, matching this suite's existing
convention.

Restore completion is detected by polling the destination
filesystem rather than waiting on the Jobs page: kind=restore job
visibility via GET /api/v1/jobs was found, during design validation,
to lag by minutes while rwfs restore itself (confirmed via its own
per-job logs) completed correctly in under a second every time -- a
separate, out-of-scope observability gap, not a defect in restore
itself. See docs/superpowers/specs/2026-08-17-restore-content-e2e-design.md."
```

---

## Self-Review

**Spec coverage:** every Goal in the design doc maps to a concrete piece of this task — dataset
generation (Step 2's `generateAndManifest`), real-UI backup/catalog/rename/restore (Step 2's main
body), checksum proof (Step 2's final `expect(destManifest).toEqual(srcManifest)`), free-space guard
(Step 2's `generateAndManifest`'s `df` check), cleanup (Step 2's `finally` block). The one Non-Goal
that changed from the original ask (dropping the job-log `files_written` UI assertion) is explained
inline in both the design doc and this plan's Global Constraints, not silently omitted.

**Placeholder scan:** none — every step has complete, real code, already run twice successfully.

**Type/interface consistency:** `waitForCatalogFolderRow`'s signature
(`page, parentSegments, dirPath, timeoutMs`) matches its one call site in Step 2 exactly
(`waitForCatalogFolderRow(page, parentSegments, srcDir)`, using the default `timeoutMs`). `COMPOSE_FILE`
and `waitForJobSuccess` are consumed with their existing, unmodified signatures.
