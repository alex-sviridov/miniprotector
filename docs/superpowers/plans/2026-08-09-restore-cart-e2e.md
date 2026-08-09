# Restore Cart E2E Tests Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Playwright browser suite that drives the real demo lab through the restore cart's four core selection scenarios, seeded via the actual policy-creation UI.

**Architecture:** A UI-driven seeding helper (`web/e2e/helpers/policySeeding.js`) creates and runs a fast ad-hoc backup policy through the real `/policies` form, forces immediate pickup via one CLI shell-out (the only non-UI step, since the pull-based policy model has no other way to skip its 900s default fetch interval), then polls the real `/jobs` and `/catalog` pages until the seeded data is visible. A sequential spec (`web/e2e/restore-cart.spec.js`) then exercises file selection, folder-wildcard selection with drill-down pre-checking, a nested exception, and full deselection — asserting against the real browser (catching CSS/DOM-cascade bugs jsdom-based component tests structurally cannot).

**Tech Stack:** Playwright (`@playwright/test`, new devDependency), run against the already-running `make demo-up` stack at `http://localhost:8091` — no mocking, no additional test infrastructure.

## Global Constraints

- **Precondition for every live-verification step in this plan:** `make demo-up` must already be running. This plan's tests do not start, stop, or manage the demo lab, exactly like the existing `src/e2e` Go suite (`docs/superpowers/specs/2026-08-05-e2e-lifecycle-test-design.md`).
- Demo web UI base URL: `http://localhost:8091`.
- Auth: inject `localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')` before navigating — no UI login flow. (`dev-placeholder-token-change-me` is the demo lab's documented placeholder token, `demo/local.conf`.)
- This repo's test-hook convention is `data-test="..."`, not the Playwright default `data-testid` — `playwright.config.js` must set `use: { testIdAttribute: 'data-test' }` so `getByTestId()` matches it.
- Seed target: host `database`, path `/var/lib/dbdata`, files `dump.sql` and `schema.sql` (real mounted sample data, no changes needed).
- Destination storage policy for the seeded backup policy: the demo's existing `store` policy, rendered in the destination `<select>` as `store (store:8080)` (`BackupPolicyFormModal.vue`'s `storageOptionLabel`).
- No cleanup of the seeded policy after a run — it self-expires via the ad-hoc mechanism's `disabled_at` (1h timeout).
- No CI wiring — local, `make demo-up`-dependent, matching the Go e2e suite's current scope.
- No restore-execution coverage — restore execution doesn't exist anywhere in the product yet.

---

## Task 1: Playwright scaffolding

**Files:**
- Modify: `web/package.json`
- Create: `web/playwright.config.js`
- Create: `web/e2e/smoke.spec.js`

**Interfaces:**
- Produces: a working `npx playwright test` command from `web/`, and the `testIdAttribute: 'data-test'` config every later task's `getByTestId()` call relies on.

- [ ] **Step 1: Install Playwright and its browser**

Run from `web/`:
```bash
npm install --save-dev @playwright/test
npx playwright install --with-deps chromium
```

This adds `@playwright/test` to `package.json`'s `devDependencies` (npm resolves and writes the
version itself — do not hand-edit a version string in) and downloads a Chromium build with its
system dependencies.

- [ ] **Step 2: Add the `test:e2e` script**

In `web/package.json`, add to the existing `"scripts"` block (alongside `"dev"`, `"build"`, `"test"`):

```json
    "test:e2e": "playwright test"
```

- [ ] **Step 3: Create the Playwright config**

Create `web/playwright.config.js`:

```js
import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './e2e',
  timeout: 120_000,
  // The restore-cart suite seeds real backend state through a shared UI
  // flow and scenarios build on that shared fixture -- parallel workers
  // would race against the same backend data and each other's navigation.
  fullyParallel: false,
  workers: 1,
  reporter: 'list',
  use: {
    baseURL: 'http://localhost:8091',
    // This repo's test-hook convention is data-test="...", not
    // Playwright's default data-testid -- every getByTestId() call in
    // this suite depends on this.
    testIdAttribute: 'data-test',
    trace: 'retain-on-failure',
  },
})
```

- [ ] **Step 4: Create a minimal smoke test**

Create `web/e2e/smoke.spec.js`:

```js
import { test, expect } from '@playwright/test'

test('the demo web UI loads and accepts the placeholder token', async ({ page, context }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })
  await page.goto('/')
  // Authenticated shell renders the sidebar nav; the token-gate form
  // (rendered when unauthenticated) does not.
  await expect(page.locator('nav')).toBeVisible()
  await expect(page.locator('form')).toHaveCount(0)
})
```

This mirrors `web/src/App.spec.js`'s existing "shows the sidebar and content once authenticated"
assertion (`nav` visible, no `form`), but against the real running app instead of a mounted
component.

- [ ] **Step 5: Run it against the live demo lab**

Precondition: `make demo-up` is already running (see Global Constraints).

Run: `cd web && npx playwright test smoke.spec.js`
Expected: `PASS` — 1 test passed. If it fails with a connection error, the demo lab isn't up or
isn't reachable at `http://localhost:8091`; if it fails on the assertions, something about the
auth/token-gate flow doesn't match this task's assumptions — stop and report rather than guessing a
fix, since every later task depends on this working.

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/package-lock.json web/playwright.config.js web/e2e/smoke.spec.js
git commit -m "feat: add Playwright e2e scaffolding for the web app"
```

---

## Task 2: `data-test` hooks on the catalog checkbox column

**Files:**
- Modify: `web/src/views/CatalogView.vue:233-235`
- Modify: `web/src/views/CatalogView.spec.js`

**Interfaces:**
- Produces: `[data-test="file-checkbox-<sourceHost>:<path>"]` on every file row's checkbox,
  `[data-test="folder-checkbox-<path>"]` on every folder row's checkbox. Task 4's scenarios select
  the seed data's known files/folders through these exact selectors:
  `file-checkbox-database:/var/lib/dbdata/dump.sql`, `file-checkbox-database:/var/lib/dbdata/schema.sql`,
  `folder-checkbox-/var/lib/dbdata`.

This closes a gap the restore-cart branch's own final review flagged (no test hooks on the checkbox
column) and gives Playwright a stable way to target a specific row's checkbox instead of relying on
DOM position.

- [ ] **Step 1: Write the failing test**

In `web/src/views/CatalogView.spec.js`, add this test at the end of the `describe('CatalogView', ...)`
block, before the closing `})` (the file already has a `mountView` helper accepting an optional
`restoreCartState` second argument, and an `entry()` fixture helper — both from the restore-cart
work already merged):

```js
  it('sets a data-test attribute identifying each row\'s checkbox', () => {
    const { wrapper } = mountView({
      currentPath: '/var',
      directoryChildren: [{ path: '/var/lib', name: 'lib', file_count: 0, last_seen: 0, has_children: true }],
    })
    expect(wrapper.find('[data-test="folder-checkbox-/var/lib"]').exists()).toBe(true)
  })

  it('sets a data-test attribute identifying a file row\'s checkbox', () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/dump.sql' })],
    })
    expect(wrapper.find('[data-test="file-checkbox-database:/var/lib/dbdata/dump.sql"]').exists()).toBe(true)
  })
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: FAIL — no element matches either `[data-test="..."]` selector.

- [ ] **Step 3: Add the attribute**

In `web/src/views/CatalogView.vue`, change:

```vue
          <span v-if="column.field === 'select'">
            <TriStateCheckbox v-bind="checkboxProps(row)" @toggle="toggleSelection(row)" />
          </span>
```

to:

```vue
          <span v-if="column.field === 'select'">
            <TriStateCheckbox
              :data-test="row.isFolder ? `folder-checkbox-${row.path}` : `file-checkbox-${row.sourceHost}:${row.path}`"
              v-bind="checkboxProps(row)"
              @toggle="toggleSelection(row)"
            />
          </span>
```

`TriStateCheckbox.vue` needs no change — it has a single root `<input>` element, so Vue's automatic
attribute inheritance forwards `data-test` onto it already.

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: PASS, including both new tests and every pre-existing test in the file (no regressions).

- [ ] **Step 5: Run the full suite**

Run: `cd web && npx vitest run`
Expected: PASS, all files.

- [ ] **Step 6: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js
git commit -m "feat: add data-test hooks to catalog row checkboxes"
```

---

## Task 3: UI-driven policy seeding helper

**Files:**
- Create: `web/e2e/helpers/policySeeding.js`

**Interfaces:**
- Consumes: `data-test="policy-new"` (`BackupPoliciesView.vue`), `input[name="name"]`,
  `data-test="hostname-add"`/`"hostname-input"`, `data-test="filter-add"`/`"filter-path-input"`,
  `data-test="backup-policy-storage-select"`, `data-test="backup-policy-run-now"`
  (`BackupPolicyFormModal.vue`) — all pre-existing, unchanged by this plan.
- Produces (used by Task 4): `async function seedRestoreCartCatalogData(page) -> Promise<{ sourceHost: string, dirPath: string, files: string[] }>`, resolving to
  `{ sourceHost: 'database', dirPath: '/var/lib/dbdata', files: ['dump.sql', 'schema.sql'] }` once
  the seeded backup has actually landed in the catalog and is visible in the UI.

This task cannot be driven by a fast red/green unit-test cycle — it only means anything against the
real demo lab. Verification is: run it standalone (Step 3 below) and confirm it completes without
throwing, then Task 4 depends on it succeeding as a `test.beforeAll`.

- [ ] **Step 1: Write the helper**

Create `web/e2e/helpers/policySeeding.js`:

```js
import { execSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect } from '@playwright/test'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const COMPOSE_FILE = path.resolve(__dirname, '../../../demo/docker-compose.yml')

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

  execSync(`docker compose -f ${COMPOSE_FILE} exec -T database policyclient fetch`, { stdio: 'inherit' })

  await waitForJobSuccess(page, policyName)
  await waitForCatalogFiles(page, DIR_PATH, FILES)

  return { sourceHost: SOURCE_HOST, dirPath: DIR_PATH, files: FILES }
}

async function waitForJobSuccess(page, policyName, timeoutMs = 100_000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    await page.goto('/jobs')
    const row = page.locator('tbody tr', { hasText: policyName })
    if ((await row.count()) > 0 && (await row.locator('text=success').count()) > 0) return
    if (Date.now() > deadline) throw new Error(`Timed out waiting for job "${policyName}" to reach success`)
    await page.waitForTimeout(3000)
  }
}

async function waitForCatalogFiles(page, dirPath, files, timeoutMs = 60_000) {
  const segments = dirPath.split('/').filter(Boolean)
  const deadline = Date.now() + timeoutMs
  for (;;) {
    await page.goto('/catalog')
    for (const segment of segments) {
      await page.getByText(`${segment}/`, { exact: true }).click()
    }
    const counts = await Promise.all(files.map((f) => page.getByText(f, { exact: true }).count()))
    if (counts.every((c) => c > 0)) return
    if (Date.now() > deadline) throw new Error(`Timed out waiting for catalog files under ${dirPath}`)
    await page.waitForTimeout(3000)
  }
}
```

- [ ] **Step 2: Add a temporary standalone-run spec to verify it in isolation**

Create `web/e2e/_seeding-check.spec.js` (temporary — deleted in Step 5 below, after it's confirmed
working; it exists only so this task has its own verifiable checkpoint before Task 4 builds on it):

```js
import { test, expect } from '@playwright/test'
import { seedRestoreCartCatalogData } from './helpers/policySeeding.js'

test('seeds real catalog data via the policy UI', async ({ page, context }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })
  const fixture = await seedRestoreCartCatalogData(page)
  expect(fixture).toEqual({
    sourceHost: 'database',
    dirPath: '/var/lib/dbdata',
    files: ['dump.sql', 'schema.sql'],
  })
})
```

- [ ] **Step 3: Run it against the live demo lab**

Precondition: `make demo-up` is already running.

Run: `cd web && npx playwright test _seeding-check.spec.js`
Expected: `PASS` — 1 test passed. This run takes up to ~2 minutes in the worst case (policy
creation, forced fetch, job completion, catalog sync — matching the timeouts in Step 1's helper).
If it fails, read the Playwright trace (`test-results/`) for exactly which step didn't find its
expected element or timed out, and fix the helper before proceeding — do not move to Task 4 with an
unverified seeding helper.

- [ ] **Step 4: Commit the helper**

```bash
git add web/e2e/helpers/policySeeding.js
git commit -m "feat: add UI-driven policy seeding helper for restore-cart e2e"
```

- [ ] **Step 5: Remove the temporary verification spec**

```bash
rm web/e2e/_seeding-check.spec.js
```

(Nothing to commit yet — this file was never committed; it only existed on disk to give this task
a standalone checkpoint. Task 4 is what actually consumes and commits real coverage of this
helper.)

---

## Task 4: Restore-cart selection scenarios

**Files:**
- Create: `web/e2e/restore-cart.spec.js`

**Interfaces:**
- Consumes: `seedRestoreCartCatalogData(page)` from Task 3 (`../helpers/policySeeding.js`), returning
  `{ sourceHost: 'database', dirPath: '/var/lib/dbdata', files: ['dump.sql', 'schema.sql'] }`.
  `data-test="file-checkbox-<sourceHost>:<path>"` / `data-test="folder-checkbox-<path>"` from Task 2.
  Sidebar highlight class `text-blue-400` and `RestoreView.vue`'s empty-state text `"No files
  selected for restore yet."` and entry formats `path/*` (folder) / `path (host)` (file) — all
  pre-existing, unchanged by this plan.

One sequential test using `test.step()` for each scenario (re-seeding per scenario would mean
re-running Task 3's ~2-minute flow four times) — later steps depend on earlier ones' selection
state, mirroring `src/e2e/lifecycle_test.go`'s own ordered-subtests-sharing-state pattern.

- [ ] **Step 1: Write the spec**

Create `web/e2e/restore-cart.spec.js`:

```js
import { test, expect } from '@playwright/test'
import { seedRestoreCartCatalogData } from './helpers/policySeeding.js'

test.describe.configure({ mode: 'serial' })

test('restore cart selection', async ({ page, context }) => {
  await context.addInitScript(() => {
    localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me')
  })

  const { sourceHost, dirPath, files } = await seedRestoreCartCatalogData(page)
  const [firstFile, secondFile] = files
  const segments = dirPath.split('/').filter(Boolean) // ['var', 'lib', 'dbdata']

  // IMPORTANT: every navigation from here on must be a client-side SPA
  // navigation (clicking an in-app link/row), never page.goto()/page.reload().
  // restoreCart's selection state is deliberately in-memory only (no
  // persistence, by design -- see docs/superpowers/specs/2026-08-09-restore-
  // cart-design.md), which lives in the page's live JS context. page.goto()
  // is a real browser navigation: it reloads the document and tears down
  // that JS context (all Pinia stores, not just restoreCart), silently
  // wiping every selection made in an earlier step. Clicking a <router-link>
  // (the sidebar, breadcrumbs, table rows) never reloads the document, so
  // selection state survives across scenarios the way it would for a real
  // user browsing the app. (Task 3's seeding helper uses page.goto() freely
  // for its own polling loops -- that's fine there, because it all happens
  // before any restore-cart selection exists to lose.)

  async function goToCatalogHome(page) {
    await page.getByRole('link', { name: 'Catalog' }).click()
    await page.getByTestId('crumb-home').click()
  }

  async function drillInto(page, pathSegments) {
    await goToCatalogHome(page)
    for (const segment of pathSegments) {
      await page.getByText(`${segment}/`, { exact: true }).click()
    }
  }

  await test.step('selecting a file checks it, highlights the sidebar, and lists it on /restore', async () => {
    await drillInto(page, segments)
    const fileCheckbox = page.getByTestId(`file-checkbox-${sourceHost}:${dirPath}/${firstFile}`)
    await fileCheckbox.click()
    await expect(fileCheckbox).toBeChecked()

    const restoreLink = page.getByRole('link', { name: 'Restore' })
    await expect(restoreLink).toHaveClass(/text-blue-400/)

    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByText(`${dirPath}/${firstFile} (${sourceHost})`)).toBeVisible()
  })

  await test.step('selecting the parent folder pre-checks its children on drill-down', async () => {
    await drillInto(page, segments.slice(0, -1)) // up to dirPath's parent
    const folderCheckbox = page.getByTestId(`folder-checkbox-${dirPath}`)
    await folderCheckbox.click()
    await expect(folderCheckbox).toBeChecked()

    await page.getByText(`${segments[segments.length - 1]}/`, { exact: true }).click() // drill into dirPath
    for (const file of files) {
      await expect(page.getByTestId(`file-checkbox-${sourceHost}:${dirPath}/${file}`)).toBeChecked()
    }

    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByText(`${dirPath}/*`)).toBeVisible()
  })

  await test.step('unchecking a nested file creates an exception, shown as indeterminate on the parent', async () => {
    await drillInto(page, segments) // back into dirPath
    const secondFileCheckbox = page.getByTestId(`file-checkbox-${sourceHost}:${dirPath}/${secondFile}`)
    await secondFileCheckbox.click()
    await expect(secondFileCheckbox).not.toBeChecked()

    await drillInto(page, segments.slice(0, -1)) // up to dirPath's parent
    const folderCheckbox = page.getByTestId(`folder-checkbox-${dirPath}`)
    await expect(folderCheckbox).toHaveJSProperty('indeterminate', true)

    // the exception itself is never shown -- only the wildcard entry
    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByText(`${dirPath}/*`)).toBeVisible()
    await expect(page.getByText(`${dirPath}/${secondFile}`)).toHaveCount(0)
  })

  await test.step('unchecking the folder clears the sidebar highlight and empties /restore', async () => {
    await drillInto(page, segments.slice(0, -1))
    const folderCheckbox = page.getByTestId(`folder-checkbox-${dirPath}`)
    await folderCheckbox.click()
    await expect(folderCheckbox).not.toBeChecked()

    const restoreLink = page.getByRole('link', { name: 'Restore' })
    await expect(restoreLink).not.toHaveClass(/text-blue-400/)

    await page.getByRole('link', { name: 'Restore' }).click()
    await expect(page.getByText('No files selected for restore yet.')).toBeVisible()
  })
})
```

Every in-scenario navigation goes through `getByRole('link', ...).click()` (sidebar) or
`getByText('<segment>/', ...).click()` (table rows) or `getByTestId('crumb-home').click()`
(breadcrumb) — real `<router-link>`/row clicks, never `page.goto()` — specifically so
`restoreCart`'s in-memory selection state survives from one `test.step()` to the next.

- [ ] **Step 2: Run it against the live demo lab**

Precondition: `make demo-up` is already running.

Run: `cd web && npx playwright test restore-cart.spec.js`
Expected: `PASS` — 1 test passed, all 4 steps shown in the list reporter. This run takes up to ~2-3
minutes (seeding dominates; the four selection steps themselves are fast). If a step fails, the
trace (`test-results/`) pinpoints exactly which assertion or locator didn't resolve — check it
against the exact `data-test` values Task 2 introduced and the fixture shape Task 3 returns before
assuming the application itself is broken.

- [ ] **Step 3: Run the full e2e suite together**

Run: `cd web && npx playwright test`
Expected: `PASS` — both `smoke.spec.js` and `restore-cart.spec.js` (2 tests total).

- [ ] **Step 4: Commit**

```bash
git add web/e2e/restore-cart.spec.js
git commit -m "feat: add restore-cart selection e2e scenarios"
```

---

## Task 5: Wiring and documentation

**Files:**
- Modify: `Makefile`
- Modify: `README.md`
- Modify: `docs/components/web.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Extend the `test-e2e` Makefile target**

In `Makefile`, find the `test-e2e` target:

```makefile
test-e2e: ## Run e2e tests against the running demo lab (run `make demo-up` first)
	cd src && go test -tags=e2e -count=1 -timeout=240s ./e2e/...
```

Change it to also run the web suite afterward:

```makefile
test-e2e: ## Run e2e tests against the running demo lab (run `make demo-up` first)
	cd src && go test -tags=e2e -count=1 -timeout=240s ./e2e/...
	cd web && npx playwright test
```

- [ ] **Step 2: Update the README's `test-e2e` disclosure**

In `README.md`, find:

```
# Run e2e tests against the running demo lab (run `make demo-up` first) -- takes ~1-2 minutes and
# mutates the demo lab (revokes/reissues a client cert, creates and deletes a policy)
make test-e2e
```

Change the comment to:

```
# Run e2e tests against the running demo lab (run `make demo-up` first) -- takes ~3-5 minutes and
# mutates the demo lab (revokes/reissues a client cert, creates and deletes a policy, creates an
# ad-hoc backup policy and runs it, and drives the web UI through the resulting catalog data)
make test-e2e
```

- [ ] **Step 3: Add a testing note to `docs/components/web.md`**

Add a new subsection after the existing `## Local development` section (before `## Deployment`):

```markdown
## End-to-end tests

`web/e2e/` holds a Playwright suite covering the restore cart's selection scenarios (file select,
folder-wildcard select with drill-down pre-checking, a nested exception, full deselection), run
against the real, already-running demo lab rather than mocked data — see
[Design: restore cart e2e tests](../superpowers/specs/2026-08-09-restore-cart-e2e-design.md) for why.
Seeding is itself UI-driven: the suite creates and runs a fast ad-hoc backup policy through the real
`/policies` form before asserting against the resulting catalog data.

```bash
make demo-up          # precondition, not managed by the suite itself
cd web && npx playwright test
```
```

- [ ] **Step 4: Add the CHANGELOG entry**

Insert a new dated section at the top of `CHANGELOG.md`, immediately after the `# Changelog` header
and its intro line, above the current top entry:

```markdown
## 2026-08-09 — restore cart gains a real-browser e2e suite

The catalog's restore-cart selection (file/folder checkboxes, sidebar highlight, `/restore` list)
now has Playwright coverage running against the real demo lab instead of mocked data — the kind of
suite that would have caught the restore-cart branch's own CSS-cascade bug, which every jsdom-based
component test missed. Seeding is UI-driven: the suite creates and runs a fast ad-hoc backup policy
through the actual `/policies` form (forcing one CLI-only pickup step the pull-based policy model
has no other way to skip), then drives the browser through file selection, folder-wildcard selection
with drill-down pre-checking, a nested exception, and full deselection. `make test-e2e` now runs
this alongside the existing Go e2e suite.
```

- [ ] **Step 5: Commit**

```bash
git add Makefile README.md docs/components/web.md CHANGELOG.md
git commit -m "docs: wire up and document the restore-cart e2e suite"
```

---

## Final check

- [ ] Run the entire Vitest suite once more: `cd web && npx vitest run`. Expected: PASS, all files.
- [ ] With `make demo-up` running, run the entire e2e suite once more: `cd web && npx playwright test`. Expected: PASS, 2 tests (smoke + restore-cart selection).
