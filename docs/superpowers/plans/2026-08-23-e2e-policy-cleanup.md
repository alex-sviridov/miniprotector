# E2E Policy Cleanup Fixture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `web/e2e`'s scattered, manually-written `try/finally`-delete cleanup pattern with a shared Playwright fixture (`trackPolicy`) so every policy an e2e spec creates — backup, restore, or verify — is reliably deleted when the test finishes, fixing the leaked verify-mode restore policy from `restore-verify.spec.js` and the never-cleaned ad-hoc backup policies from the "Run now" flow.

**Architecture:** One new fixture module (`web/e2e/helpers/test.js`) extends Playwright's `test` with a `trackPolicy(id)` fixture whose teardown deletes every tracked id via `DELETE /api/v1/policies/{id}`, run regardless of test pass/fail. Every spec and helper that creates a policy is updated to call `trackPolicy(id)` right after learning that policy's id, and to import `test`/`expect` from the new module instead of `@playwright/test` directly.

**Tech Stack:** Playwright (`@playwright/test`), plain JS (ES modules), Node's `child_process` for `docker compose exec` calls — no new dependencies.

## Global Constraints

- Policy deletion always goes through `DELETE /api/v1/policies/{id}` with header `Authorization: Bearer dev-placeholder-token-change-me` — the same endpoint and token every existing spec already uses for direct API calls.
- Cleanup must never throw or fail the test: a failed delete is logged via `console.warn`, never thrown — matches every existing cleanup block's behavior today.
- `web/e2e/smoke.spec.js` is not modified — it creates no policies and has nothing to track.
- No `globalTeardown` and no suite-level sweep — cleanup is strictly per-test, via the `trackPolicy` fixture (see the spec's "Discovered: rejected approaches").
- Do not retroactively delete the pre-existing leftover files already sitting in `demo/policy-server/policies/restore/` — out of scope for this plan (they're root-owned artifacts of the bug being fixed here, not something this plan needs to clean up).
- Every verification step in this plan runs real Playwright specs against a live demo stack. Before starting Task 1, run `make demo-up` from the repo root and leave it running for the whole plan — these are integration tests against real backend state, not mocked unit tests.
- **This work happens in a git worktree.** The live demo stack's `policy-server` container has its `/data/policies` directory bind-mounted from `/home/alex/miniprotector/demo/policy-server/policies` (confirmed via `docker inspect demo-policy-server-1`) — the original checkout, not the worktree. Every verification `ls`/`diff` step in this plan already uses that absolute path for this reason; keep using it in any ad-hoc verification too. All *code* changes (the `.js`/`.gitignore` edits) still target the worktree's own relative paths, unaffected by this.
- Follow this repo's `.claude/CLAUDE.md` doc rule for feature changes... this plan is test-infrastructure-only (see the spec's "Documentation Impact": none), so no `docs/components/`, `README.md`, or `docs/ARCHITECTURE.md` updates are needed. Do still add a `CHANGELOG.md` entry before this branch merges to `main` (handled in Task 6).

---

### Task 1: Shared `trackPolicy` fixture + wire it into `seedRestoreCartCatalogData`

**Files:**
- Create: `web/e2e/helpers/test.js`
- Modify: `web/e2e/helpers/policySeeding.js`
- Modify: `web/e2e/restore-cart.spec.js`
- Modify: `web/e2e/restore-verify.spec.js` (import line, test signature, and the `seedRestoreCartCatalogData` call only — its three `test.step`s are fixed in Task 2)

**Interfaces:**
- Produces: `test` (Playwright's `test` extended with a `trackPolicy` fixture of type `(id: string) => void`) and `expect`, both exported from `web/e2e/helpers/test.js`. Also exports `AUTH_HEADERS = { Authorization: 'Bearer dev-placeholder-token-change-me' }`.
- Produces: `seedRestoreCartCatalogData(page, trackPolicy)` — same return shape as before (`{ sourceHost, dirPath, files }`), now also tracks the ad-hoc backup policy it creates.

- [ ] **Step 1: Create the fixture module**

Create `web/e2e/helpers/test.js`:

```js
import { test as base, expect } from '@playwright/test'

export const AUTH_HEADERS = { Authorization: 'Bearer dev-placeholder-token-change-me' }

// trackPolicy lets a test (or a helper it calls) register a policy id as
// soon as it's known. Teardown deletes every registered id after the test
// finishes, whether it passed, failed, or threw -- Playwright always runs
// fixture teardown, so this replaces the need for a manual try/finally at
// every call site.
export const test = base.extend({
  trackPolicy: async ({ request }, use) => {
    const ids = []
    await use((id) => ids.push(id))
    for (const id of ids) {
      let resp
      try {
        resp = await request.delete(`/api/v1/policies/${id}`, { headers: AUTH_HEADERS })
      } catch (err) {
        console.warn(`cleanup: failed to delete policy ${id}: ${err.message}`)
        continue
      }
      if (!resp.ok()) {
        console.warn(`cleanup: failed to delete policy ${id}, status ${resp.status()}`)
      }
    }
  },
})

export { expect }
```

- [ ] **Step 2: Wire `trackPolicy` into `seedRestoreCartCatalogData`**

In `web/e2e/helpers/policySeeding.js`, replace the `expect` import (currently
`import { expect } from '@playwright/test'`) with:

```js
import { expect, AUTH_HEADERS } from './test.js'
```

Change the function signature and body. Replace:

```js
export async function seedRestoreCartCatalogData(page) {
```

with:

```js
export async function seedRestoreCartCatalogData(page, trackPolicy) {
```

Then, immediately after the existing `await page.waitForURL('**/jobs')` line
(right before the existing `// policyclient isn't on $PATH...` comment and
`execSync(...)` call), insert:

```js
  // The UI gives no way to read back the ad-hoc backup policy's id, so look
  // it up by the name we just gave it and hand it to trackPolicy -- this is
  // the same lookup-by-name-after-creation pattern used everywhere else in
  // this suite that a submission's id isn't directly visible.
  const policiesResp = await page.request.get('/api/v1/policies?type=backup', { headers: AUTH_HEADERS })
  const { data: backupPolicies } = await policiesResp.json()
  const backupPolicy = backupPolicies.find((p) => p.name === policyName)
  if (backupPolicy) trackPolicy(backupPolicy.id)

```

The function's full body should now read:

```js
export async function seedRestoreCartCatalogData(page, trackPolicy) {
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

  // The UI gives no way to read back the ad-hoc backup policy's id, so look
  // it up by the name we just gave it and hand it to trackPolicy -- this is
  // the same lookup-by-name-after-creation pattern used everywhere else in
  // this suite that a submission's id isn't directly visible.
  const policiesResp = await page.request.get('/api/v1/policies?type=backup', { headers: AUTH_HEADERS })
  const { data: backupPolicies } = await policiesResp.json()
  const backupPolicy = backupPolicies.find((p) => p.name === policyName)
  if (backupPolicy) trackPolicy(backupPolicy.id)

  // policyclient isn't on $PATH inside the container (only /app/policyclient
  // exists); docker compose exec's default cwd is the image's WORKDIR (/app),
  // so `./policyclient` resolves it without needing an absolute path.
  execSync(`docker compose -f ${COMPOSE_FILE} exec -T database ./policyclient fetch`, { stdio: 'inherit' })

  await waitForJobSuccess(page, policyName)
  await waitForCatalogFiles(page, DIR_PATH, FILES)

  return { sourceHost: SOURCE_HOST, dirPath: DIR_PATH, files: FILES }
}
```

- [ ] **Step 3: Update `restore-cart.spec.js`'s caller**

In `web/e2e/restore-cart.spec.js`, replace:

```js
import { test, expect } from '@playwright/test'
import { seedRestoreCartCatalogData } from './helpers/policySeeding.js'
```

with:

```js
import { test, expect } from './helpers/test.js'
import { seedRestoreCartCatalogData } from './helpers/policySeeding.js'
```

Replace:

```js
test('restore cart selection', async ({ page, context }) => {
```

with:

```js
test('restore cart selection', async ({ page, context, trackPolicy }) => {
```

Replace:

```js
  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page)
```

with:

```js
  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page, trackPolicy)
```

- [ ] **Step 4: Update `restore-verify.spec.js`'s import, signature, and call (steps themselves come in Task 2)**

In `web/e2e/restore-verify.spec.js`, replace:

```js
import { test, expect } from '@playwright/test'
```

with:

```js
import { test, expect } from './helpers/test.js'
```

Replace:

```js
test('restore verification', async ({ page, context }) => {
```

with:

```js
test('restore verification', async ({ page, context, trackPolicy }) => {
```

Replace:

```js
  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page)
```

with:

```js
  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page, trackPolicy)
```

- [ ] **Step 5: Run `restore-cart.spec.js` and verify no leftover ad-hoc backup policy**

Run:

```bash
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | sort > /tmp/before-t1a.txt
cd web && npx playwright test restore-cart.spec.js && cd ..
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | sort > /tmp/after-t1a.txt
diff /tmp/before-t1a.txt /tmp/after-t1a.txt
```

Expected: the Playwright test passes, and `diff` produces no output — `restore-cart.spec.js` creates no restore/verify policy at all, and its ad-hoc backup policy is now tracked and deleted.

- [ ] **Step 6: Run `restore-verify.spec.js` and verify the ad-hoc backup leak is gone (the restore-policy leak from step 1 is still expected here — fixed in Task 2)**

Run:

```bash
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json 2>/dev/null | sort > /tmp/before-t1b.txt
ls /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | wc -l > /tmp/before-t1b-restore-count.txt
cd web && npx playwright test restore-verify.spec.js && cd ..
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json 2>/dev/null | sort > /tmp/after-t1b.txt
ls /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | wc -l > /tmp/after-t1b-restore-count.txt
diff /tmp/before-t1b.txt /tmp/after-t1b.txt
diff /tmp/before-t1b-restore-count.txt /tmp/after-t1b-restore-count.txt
```

Expected: the Playwright test passes. The `backup/` diff is empty (the ad-hoc backup policy this spec creates is now cleaned up). The `restore/` count diff shows exactly `+1` new file — this is `test.step` 1's still-unfixed verify-policy leak; Task 2 removes it. (Steps 2 and 3 of this spec still clean up their own restore policies via their existing, not-yet-touched `try/finally` blocks.)

- [ ] **Step 7: Commit**

```bash
git add web/e2e/helpers/test.js web/e2e/helpers/policySeeding.js web/e2e/restore-cart.spec.js web/e2e/restore-verify.spec.js
git commit -m "test(e2e): add trackPolicy fixture, wire into seedRestoreCartCatalogData"
```

---

### Task 2: Fix `restore-verify.spec.js`'s three steps

**Files:**
- Modify: `web/e2e/restore-verify.spec.js`

**Interfaces:**
- Consumes: `trackPolicy` (from Task 1's test signature change, already in scope), `AUTH_HEADERS` (from `web/e2e/helpers/test.js`, Task 1).

- [ ] **Step 1: Update the import to also pull in `AUTH_HEADERS`**

Replace:

```js
import { test, expect } from './helpers/test.js'
```

with:

```js
import { test, expect, AUTH_HEADERS } from './helpers/test.js'
```

- [ ] **Step 2: Track the verify-mode policy created in step 1 (the actual leak)**

In the first `test.step` (`'a real backed-up file verifies successfully, readable in its job log'`),
find:

```js
    const resultText = await page.getByTestId('submission-results').innerText()
    const policyName = /Started verification policy (\S+) from/.exec(resultText)[1]
```

and insert immediately after it:

```js

    const verifyPoliciesResp = await page.request.get('/api/v1/policies?type=restore', { headers: AUTH_HEADERS })
    const { data: verifyPolicies } = await verifyPoliciesResp.json()
    const verifyPolicy = verifyPolicies.find((p) => p.name === policyName)
    if (verifyPolicy) trackPolicy(verifyPolicy.id)
```

- [ ] **Step 3: Simplify step 2's cleanup to use `trackPolicy`**

Replace the entire second `test.step` (`'clicking Restore creates a real restore-execution policy, which agent picks up'`) body. It currently reads:

```js
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
```

Replace it with:

```js
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
    await page.getByTestId('restore-button').click()

    // Unlike step 1's first-ever submission (where submission-results starts
    // absent and Playwright's own actionability wait for the element to
    // attach happens to synchronize with the async submit call), this step
    // starts with a stale submission-results <ul> already on the page --
    // step 1's own verify result, carried over in the (page-navigation-
    // persisted) restoreSubmission Pinia store. expect(...).toContainText
    // is a web-first assertion that polls until the DOM actually reflects
    // this step's own submission, so it can't observe that transient state.
    const resultsLocator = page.getByTestId('submission-results')
    await expect(resultsLocator).toContainText('Started restore policy')
    const resultText = await resultsLocator.innerText()
    const policyName = /Started restore policy (\S+) from/.exec(resultText)[1]

    // RestoreView's success copy confirms the /restore call returned 201,
    // but not the policy's id -- look it up by name via the REST API (the
    // UI has no affordance to read it back) so trackPolicy can clean it up.
    const restorePoliciesResp = await page.request.get('/api/v1/policies?type=restore', { headers: AUTH_HEADERS })
    const { data: restorePolicies } = await restorePoliciesResp.json()
    const policy = restorePolicies.find((p) => p.name === policyName)
    expect(policy).toBeTruthy()
    trackPolicy(policy.id)
  })
```

- [ ] **Step 4: Simplify step 3's cleanup to use `trackPolicy`**

Replace the entire third `test.step` (`'a rule naming a file that was never backed up fails, readable in its job log'`). It currently reads:

```js
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
```

Replace it with:

```js
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
```

- [ ] **Step 5: Run `restore-verify.spec.js` and verify zero leftover policies**

Run:

```bash
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | sort > /tmp/before-t2.txt
cd web && npx playwright test restore-verify.spec.js && cd ..
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | sort > /tmp/after-t2.txt
diff /tmp/before-t2.txt /tmp/after-t2.txt
```

Expected: the Playwright test passes (all three steps), and `diff` produces no output — this is the fix for the original bug.

- [ ] **Step 6: Commit**

```bash
git add web/e2e/restore-verify.spec.js
git commit -m "test(e2e): fix leaked verify-policy in restore-verify.spec.js via trackPolicy"
```

---

### Task 3: Fix `restore-content.spec.js`

**Files:**
- Modify: `web/e2e/restore-content.spec.js`

**Interfaces:**
- Consumes: `test`, `expect`, `AUTH_HEADERS` from `web/e2e/helpers/test.js` (Task 1); `trackPolicy` fixture.

- [ ] **Step 1: Update the import and drop the now-redundant local `AUTH_HEADERS`**

Replace:

```js
import { execFileSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { COMPOSE_FILE, waitForCatalogFolderRow } from './helpers/policySeeding.js'
```

with:

```js
import { execFileSync } from 'node:child_process'
import { test, expect, AUTH_HEADERS } from './helpers/test.js'
import { COMPOSE_FILE, waitForCatalogFolderRow } from './helpers/policySeeding.js'
```

Remove the now-duplicate local constant:

```js
const AUTH_HEADERS = { Authorization: 'Bearer dev-placeholder-token-change-me' }
```

- [ ] **Step 2: Add `trackPolicy` to the test signature**

Replace:

```js
test('restore writes real file content, verified by checksum, with a folder rename', async ({ page, context }) => {
```

with:

```js
test('restore writes real file content, verified by checksum, with a folder rename', async ({ page, context, trackPolicy }) => {
```

- [ ] **Step 3: Replace the manual policy-delete with `trackPolicy`**

Find:

```js
  let restorePolicyId = null
  try {
```

and the matching:

```js
    restorePolicyId = restorePolicy.id
```

and the `finally` block's policy-delete portion:

```js
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
```

Replace the whole `try { ... } finally { ... }` block with:

```js
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
    trackPolicy(restorePolicy.id)

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
    // never deleted here -- only this run's own destination directory is
    // (the restore policy itself is now trackPolicy's job).
    try {
      dockerExec(`rm -rf "${destDir}"`)
    } catch (err) {
      console.warn(`cleanup: failed to remove destination directory: ${err.message}`)
    }
  }
```

- [ ] **Step 4: Run `restore-content.spec.js` and verify zero leftover policies**

Run:

```bash
ls /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | sort > /tmp/before-t3.txt
cd web && npx playwright test restore-content.spec.js && cd ..
ls /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | sort > /tmp/after-t3.txt
diff /tmp/before-t3.txt /tmp/after-t3.txt
```

Expected: the Playwright test passes (this one takes a few minutes — `test.setTimeout(600_000)`), and `diff` produces no output.

- [ ] **Step 5: Commit**

```bash
git add web/e2e/restore-content.spec.js
git commit -m "test(e2e): use trackPolicy fixture in restore-content.spec.js"
```

---

### Task 4: Fix `live-job-updates.spec.js`

**Files:**
- Modify: `web/e2e/live-job-updates.spec.js`

**Interfaces:**
- Consumes: `test`, `expect`, `AUTH_HEADERS` from `web/e2e/helpers/test.js` (Task 1); `trackPolicy` fixture.

- [ ] **Step 1: Update the import**

Replace:

```js
import { execSync } from 'node:child_process'
import { test, expect } from '@playwright/test'
import { COMPOSE_FILE } from './helpers/policySeeding.js'
```

with:

```js
import { execSync } from 'node:child_process'
import { test, expect, AUTH_HEADERS } from './helpers/test.js'
import { COMPOSE_FILE } from './helpers/policySeeding.js'
```

- [ ] **Step 2: Add `trackPolicy` to `runAdhocBackupPolicy` and track the policy it creates**

Replace:

```js
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
```

with:

```js
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
  const backupPolicy = backupPolicies.find((p) => p.name === policyName)
  if (backupPolicy) trackPolicy(backupPolicy.id)

  // Same non-UI escape hatch policySeeding.js uses -- policyclient isn't on
  // $PATH inside the container (only /app/policyclient exists); docker
  // compose exec's default cwd is the image's WORKDIR (/app), so
  // `./policyclient` resolves it without needing an absolute path.
  execSync(`docker compose -f ${COMPOSE_FILE} exec -T ${SOURCE_HOST} ./policyclient fetch`, { stdio: 'inherit' })
}
```

- [ ] **Step 3: Update both test callbacks and call sites**

Replace:

```js
test('job detail page flips to Finished live, with no manual reload', async ({ page, context }) => {
```

with:

```js
test('job detail page flips to Finished live, with no manual reload', async ({ page, context, trackPolicy }) => {
```

Replace:

```js
  const policyName = `e2e-live-detail-${Date.now()}`
  await runAdhocBackupPolicy(page, policyName)
```

with:

```js
  const policyName = `e2e-live-detail-${Date.now()}`
  await runAdhocBackupPolicy(page, policyName, trackPolicy)
```

Replace:

```js
test('jobs list page shows a new job appear and transition to success live', async ({ page, context }) => {
```

with:

```js
test('jobs list page shows a new job appear and transition to success live', async ({ page, context, trackPolicy }) => {
```

Replace:

```js
  const policyName = `e2e-live-list-${Date.now()}`
  const policyPage = await context.newPage()
  await runAdhocBackupPolicy(policyPage, policyName)
  await policyPage.close()
```

with:

```js
  const policyName = `e2e-live-list-${Date.now()}`
  const policyPage = await context.newPage()
  await runAdhocBackupPolicy(policyPage, policyName, trackPolicy)
  await policyPage.close()
```

- [ ] **Step 4: Run `live-job-updates.spec.js` and verify zero leftover policies**

Run:

```bash
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json 2>/dev/null | sort > /tmp/before-t4.txt
cd web && npx playwright test live-job-updates.spec.js && cd ..
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json 2>/dev/null | sort > /tmp/after-t4.txt
diff /tmp/before-t4.txt /tmp/after-t4.txt
```

Expected: both tests pass, and `diff` produces no output — both ad-hoc backup policies (`e2e-live-detail-*`, `e2e-live-list-*`) are now cleaned up.

- [ ] **Step 5: Commit**

```bash
git add web/e2e/live-job-updates.spec.js
git commit -m "test(e2e): use trackPolicy fixture in live-job-updates.spec.js"
```

---

### Task 5: Defensive `.gitignore` backstop

**Files:**
- Modify: `.gitignore`

- [ ] **Step 1: Add the pattern**

In `.gitignore`, find:

```
# e2e test artifacts (root-owned, written by docker compose exec)
demo/policy-server/policies/backup/adhoc-*.json
demo/policy-server/policies/.changed
```

Replace with:

```
# e2e test artifacts (root-owned, written by docker compose exec)
demo/policy-server/policies/backup/adhoc-*.json
demo/policy-server/policies/restore/restore-*.json
demo/policy-server/policies/.changed
```

- [ ] **Step 2: Verify the pattern matches only auto-generated names, not the two manual fixture files already in that directory**

Run:

```bash
git check-ignore -v demo/policy-server/policies/restore/restore-2026-08-22t13-04-17-157z-store.json
git check-ignore -v demo/policy-server/policies/restore/demo-restore-preview.json; echo "exit: $?"
git check-ignore -v demo/policy-server/policies/restore/demo-restore-nested-complex.json; echo "exit: $?"
```

Expected: the first command prints a match against the new `.gitignore` line (the auto-generated file is now ignored). The second and third print nothing and exit non-zero (`1`) — the two manually-named files are *not* matched, since they don't leave the repo in a worse spot than before (they'll still need cleaning up manually, per this plan's Global Constraints).

- [ ] **Step 3: Commit**

```bash
git add .gitignore
git commit -m "chore: gitignore auto-generated e2e restore-policy files"
```

---

### Task 6: Full-suite validation

**Files:** none (verification only)

- [ ] **Step 1: Confirm the demo stack is up**

Run:

```bash
docker compose -f demo/docker-compose.yml ps
```

Expected: all services show as running. If not, run `make demo-up` first and wait for it to settle.

- [ ] **Step 2: Snapshot policy-directory state before the full run**

Run:

```bash
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | sort > /tmp/before-t6.txt
git status --short
```

Note any pre-existing untracked files (e.g. the already-known leftover `restore-*.json` files from before this plan) — they are expected to still be present and are explicitly out of scope (Global Constraints). What matters is that no *new* ones appear after this run.

- [ ] **Step 3: Run the entire web e2e suite**

Run:

```bash
cd web && npx playwright test && cd ..
```

Expected: all 5 specs pass — `smoke`, `restore-cart`, `restore-verify`, `restore-content`, `live-job-updates`.

- [ ] **Step 4: Confirm no new leftover policy files**

Run:

```bash
ls /home/alex/miniprotector/demo/policy-server/policies/backup/*.json /home/alex/miniprotector/demo/policy-server/policies/restore/*.json 2>/dev/null | sort > /tmp/after-t6.txt
diff /tmp/before-t6.txt /tmp/after-t6.txt
git status --short
```

Expected: `diff` produces no output, and `git status --short` shows the same pre-existing untracked entries as Step 2 (if any) and nothing new.

- [ ] **Step 5: Add the CHANGELOG entry**

Per this repo's `.claude/CLAUDE.md`, add a dated entry (most recent first) to `CHANGELOG.md` before this branch merges to `main`. `CHANGELOG.md`'s existing entries use `## YYYY-MM-DD — Title` headings (see the `## 2026-08-22 — Role-based gRPC authorization` entry already at the top). Insert, right after the file's `All notable changes...` intro line and before the current top entry:

```markdown
## 2026-08-23 — E2E policy cleanup fixture

Fixed a leaked e2e-test policy: `restore-verify.spec.js` created a verify-mode restore policy in
its first `test.step` but never deleted it, unlike its sibling steps — the cause of the untracked
files accumulating in `demo/policy-server/policies/restore/`. Replaced every manually-written
`try/finally`-delete cleanup block across `web/e2e` with a shared `trackPolicy` Playwright fixture
(`web/e2e/helpers/test.js`) that deletes every policy a test creates once that test finishes, pass
or fail — the same guarantee Go's `t.Cleanup` already gave `src/e2e`. This also closes a second,
previously invisible leak: the ad-hoc backup policies created by the "Run now" UI flow
(`seedRestoreCartCatalogData`, `runAdhocBackupPolicy`) are now cleaned up too, not just hidden from
`git status` by a `.gitignore` pattern.
```

- [ ] **Step 6: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog entry for e2e policy cleanup fixture"
```

---

## Self-Review Notes

- **Spec coverage:** Architecture (fixture + all 5 call-site updates) — Tasks 1–4. `.gitignore` backstop — Task 5. Testing/validation — Task 6. Non-Goals (no `smoke.spec.js` change, no global teardown, no retroactive cleanup of pre-existing files) — respected throughout; called out explicitly in Global Constraints and Task 6 Step 2.
- **Type consistency:** `trackPolicy` is always `(id: string) => void` everywhere it's threaded through (`seedRestoreCartCatalogData`, `runAdhocBackupPolicy`, both `restore-verify.spec.js` steps, `restore-content.spec.js`). `AUTH_HEADERS` is the same shape (`{ Authorization: string }`) everywhere it replaces a local `authHeaders`/`AUTH_HEADERS` constant.
- **No placeholders:** every step shows the literal before/after code an implementer needs; no "add error handling" or "similar to Task N" shortcuts.
