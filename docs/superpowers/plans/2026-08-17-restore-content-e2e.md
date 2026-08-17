# E2E: Restore File Content, Verified by Checksum Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Playwright e2e test that restores a fixed, 100-file nested directory tree through the real web UI with a folder rename, and proves byte-for-byte correctness via checksum comparison — against a fixture seeded once by the demo stack itself, not generated fresh by every test run.

**Architecture:** A one-time fixture-seeding step in `demo/up.sh` plus a new seeded backup policy (`demo/policy-server/policies/backup/e2e-fixture.json`, same shape as the demo's existing example policies) generate and back up a fixed 100-file dataset exactly once per `demo-up`. A new Playwright spec (`web/e2e/restore-content.spec.js`) restores that fixture through the real UI (catalog folder selection, the destination-path-rename UI, restore submission) with a per-run destination directory, then proves correctness via a `sha256sum` manifest comparison. One small additive helper, `waitForCatalogFolderRow`, is added to `web/e2e/helpers/policySeeding.js`.

**Tech Stack:** Playwright (`@playwright/test`, already a devDependency), Node's `node:child_process` (`execFileSync`), the existing demo Docker Compose stack (`demo/docker-compose.yml`), `bash`/`shuf`/`sha256sum`/`find`/`head`/`df` inside the `database` container (all confirmed present, standard `debian:bookworm-slim` coreutils).

## Global Constraints

- **This plan has already been built and validated live, twice, against a real demo stack** — once against an already-running stack (manual fixture generation + manual `policies/.changed` touch, confirmed `policy-server` hot-reload) and once end-to-end via a genuine `make demo-down -v && make demo-up` rebuild, followed by a full 4-spec suite run on the settled, fresh stack (all passing). The code in this plan is that exact validated code. Treat deviations from it as requiring a specific reason, not stylistic preference.
- Design spec: `docs/superpowers/specs/2026-08-17-restore-content-e2e-design.md` — read it for the full "why," including two real, reproducible findings from live validation:
  1. `kind=restore` job-visibility lag via `GET /api/v1/jobs` (up to ~9 minutes observed) — why restore completion is detected by polling, not `waitForJobSuccess`.
  2. Per-run dataset generation permanently added ~120MB to the shared `store` with no reclamation path anywhere in the codebase (confirmed: `store:/data` measured at 796MB after a handful of validation runs) — why the dataset is now a fixed fixture seeded once by `demo/up.sh`, not generated per test run.

  Do not "fix" either of these back to the simpler-looking original approach without re-reading those sections.
- No `Makefile` change. This lives in the Playwright suite (`cd web && npx playwright test`, already run by `make test-e2e`), not the Go `e2e` package — no shared timeout budget.
- Host: `database` (not `webserver`) — matches `restore-verify.spec.js`'s existing host; see the design doc's "Host choice" reasoning (from the first revision, still valid).
- The demo stack must be rebuilt (`make demo-up`, idempotent, safe to re-run) after this plan's `demo/up.sh` and `demo/policy-server/policies/backup/e2e-fixture.json` changes land, before the test can pass — the fixture must actually exist and be backed up.
- `web/node_modules` must be installed (`npm install` in `web/`) and Playwright's browsers available (`npx playwright install` if `npx playwright test` reports a missing browser).
- **Restore completion is detected by polling the checksum comparison itself (`expect.poll`), not a file-count check.** `writeRestoreFile` (`src/cmd/rwfs/restorefile.go`) creates each destination file at its final size before streaming content into it, so a bare `find | wc -l` count can observe a file that looks complete but isn't yet — a real race a final code review found in the first implementation. Do not "simplify" this back to a count-based check.

---

## File Map

| Path | Status | Responsibility |
|------|--------|----------------|
| `demo/up.sh` | Modify | Adds a one-time fixture-generation step (idempotent) right after `enroll database`, plus a `policies/.changed` touch so `policy-server` picks up the new seeded policy file even against an already-running container |
| `demo/policy-server/policies/backup/e2e-fixture.json` | Create | Seeded backup policy for the fixture directory, same shape as `database-backup.json` — backs it up automatically, once, on a 5-minute cadence |
| `.gitignore` | Modify | Adds `demo/policy-server/policies/.changed` (a runtime touch-file, not source content) |
| `web/e2e/helpers/policySeeding.js` | Modify | Adds `waitForCatalogFolderRow`, a new export generalizing the existing `waitForCatalogFiles` drill-and-poll shape to wait for a folder row instead of specific leaf filenames |
| `web/e2e/restore-content.spec.js` | Create | The new e2e test: wait for the fixture to be cataloged → restore with rename (real UI) → verify by checksum (poll-based, race-safe) |

---

## Task 1: Fixture seeding + `waitForCatalogFolderRow` helper + `restore-content.spec.js`

**Files:**
- Modify: `demo/up.sh`
- Create: `demo/policy-server/policies/backup/e2e-fixture.json`
- Modify: `.gitignore`
- Modify: `web/e2e/helpers/policySeeding.js`
- Create: `web/e2e/restore-content.spec.js`

**Interfaces:**
- Consumes: `COMPOSE_FILE` (already exported from `policySeeding.js`, unchanged). The fixture directory path `/data/e2e-restore-content-fixture` is a contract shared between `demo/up.sh` (creates it), `demo/policy-server/policies/backup/e2e-fixture.json` (`object_filters[0].path`, must match exactly), and `restore-content.spec.js`'s `FIXTURE_SRC_DIR` constant (must match exactly).
- Produces: `export async function waitForCatalogFolderRow(page, parentSegments, dirPath, timeoutMs = 60_000)` — this task's own spec is the only caller.

- [ ] **Step 1: Add the fixture-seeding step to `demo/up.sh`**

Insert immediately after the existing `enroll store` line (before the `echo "Starting web..."` line):

```sh

# Seeds a fixed, 100-file/~100-150MB dataset on database, backed up once by
# the seeded demo/policy-server/policies/backup/e2e-fixture.json policy --
# web/e2e/restore-content.spec.js restores it (with a folder rename) on
# every run rather than generating and backing up a fresh dataset per run,
# so repeated test runs don't each add ~120MB to the shared store with no
# way to reclaim it. Idempotent: skips regeneration if the fixture already
# has exactly 100 files (e.g. a re-run of this script against an
# already-seeded volume).
echo "Seeding e2e restore-content fixture data on database..."
docker compose exec -T database bash -c '
fixture=/data/e2e-restore-content-fixture
if [ "$(find "$fixture" -type f 2>/dev/null | wc -l)" = "100" ]; then
  echo "fixture already present, skipping"
  exit 0
fi
set -eu
rm -rf "$fixture"
for d in 0 1 2 3 4; do
  for s in 0 1 2 3; do
    mkdir -p "$fixture/d$d/s$s"
  done
done
i=0
while [ "$i" -lt 100 ]; do
  d=$(( i / 20 ))
  s=$(( (i / 5) % 4 ))
  size=$(shuf -i 102400-2097152 -n 1)
  path=$(printf "$fixture/d%d/s%d/file_%03d.bin" "$d" "$s" "$i")
  head -c "$size" /dev/urandom > "$path"
  i=$((i + 1))
done
'

# policy-server hot-reloads only on a write to policies/.changed
# (src/cmd/policy-server/watch.go) -- its initial ReadDir at container
# startup already picks up e2e-fixture.json on a genuinely fresh stack, but
# touching the sentinel here also covers re-running this script against an
# already-running policy-server (e.g. after a `git pull` that only added
# new policy files, with no code change to trigger a container recreate).
touch policy-server/policies/.changed
```

The existing `echo "Starting web..."` / `docker compose up -d --no-deps web` lines that follow are
unchanged.

- [ ] **Step 2: Create the seeded backup policy**

Create `demo/policy-server/policies/backup/e2e-fixture.json`:

```json
{
  "metadata": {
    "name": "e2e-restore-content-fixture",
    "created_at": "2026-08-17T00:00:00Z",
    "updated_at": "2026-08-17T00:00:00Z"
  },
  "client_filters": {
    "hostnames": ["database"]
  },
  "object_filters": [
    {"path": "/data/e2e-restore-content-fixture"}
  ],
  "rpo": "5m",
  "backup_window": ["*/5 * * * *"],
  "storage_policy_id": "93dd1442-461e-571f-95f6-21a5022c7af5"
}
```

`storage_policy_id` matches the `store` storage policy's real id, the same value
`database-backup.json`/`audit-logs.json`/`webserver-backup.json` already use — do not regenerate or
guess this value; copy it from one of those existing files (or confirm live via
`GET /api/v1/policies?type=storage`) if this exact literal ever needs re-deriving.

- [ ] **Step 3: Add the `.gitignore` entry**

In `.gitignore`, in the existing "e2e test artifacts" section, add one line:

```
demo/policy-server/policies/.changed
```

so it reads:

```
# e2e test artifacts (root-owned, written by docker compose exec)
demo/policy-server/policies/backup/adhoc-*.json
demo/policy-server/policies/.changed
web/test-results/
```

- [ ] **Step 4: Add `waitForCatalogFolderRow` to `policySeeding.js`**

Append to the end of `web/e2e/helpers/policySeeding.js` (after the existing `waitForCatalogFiles`
function's closing `}`):

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

- [ ] **Step 5: Create `web/e2e/restore-content.spec.js`**

```js
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
```

- [ ] **Step 6: Rebuild the demo stack**

```bash
make demo-down
make demo-up
```

Expected: completes without error. Confirm the fixture and policy seeded correctly:

```bash
docker compose -f demo/docker-compose.yml exec -T database bash -c 'find /data/e2e-restore-content-fixture -type f | wc -l'
curl -s -H "Authorization: Bearer dev-placeholder-token-change-me" "http://localhost:8090/api/v1/policies?type=backup" | python3 -m json.tool
```

Expected: `100` for the file count; `e2e-restore-content-fixture` present in the backup-policies list
alongside `audit-logs`, `database-backup`, `webserver-backup` (exactly 4 total on a genuinely fresh
stack).

- [ ] **Step 7: Install dependencies if needed**

```bash
cd web && npm install
```

- [ ] **Step 8: Run the new test**

```bash
cd web && npx playwright test restore-content.spec.js
```

Expected: `1 passed`. On the very first run against a freshly-rebuilt stack, this may take a few
minutes (waiting for the fixture's first backup cycle); on a settled stack, well under a minute.

- [ ] **Step 9: Run it a second time to confirm reliability**

```bash
cd web && npx playwright test restore-content.spec.js
```

Expected: `1 passed` again, this time fast (the fixture is already cataloged).

- [ ] **Step 10: Confirm clean cleanup**

```bash
docker compose -f demo/docker-compose.yml exec -T database bash -c 'ls /data | grep e2e-restore-content-dest || echo "none left"'
```

Expected: `none left` (per-run destination directories are cleaned up; the fixture source directory
`e2e-restore-content-fixture` itself, with no `-dest-` in its name, is expected to persist and will
not match this grep).

- [ ] **Step 11: Run the existing e2e suite once to confirm no regression**

```bash
cd web && npx playwright test
```

Expected: all 4 specs pass (`smoke.spec.js`, `restore-cart.spec.js`, `restore-verify.spec.js`,
`restore-content.spec.js`). If this is run immediately after Step 6's rebuild (within the first
minute or two), a transient failure in an *unrelated* spec (anything other than
`restore-content.spec.js`) while the stack's agents are still finishing their first
bootstrap/reconcile cycle is a known, pre-existing characteristic of a cold demo stack, not a
regression -- re-run once more after giving the stack a few more minutes to settle before treating it
as a real failure.

- [ ] **Step 12: Commit**

```bash
git add demo/up.sh demo/policy-server/policies/backup/e2e-fixture.json .gitignore web/e2e/helpers/policySeeding.js web/e2e/restore-content.spec.js
git commit -m "test(e2e): verify rwfs restore's file content by checksum, via the real UI

Restores a fixed 100-file (100KB-2MB) nested directory tree with a
folder rename, entirely through the real web UI (catalog folder
selection, the destination-path-rename UI, restore submission), then
proves byte-for-byte correctness via a sha256sum manifest comparison
polled against the checksum result itself -- rwfs restore creates
each destination file at its final size before streaming content
into it, so a bare file-count check can't distinguish a fully-
restored file from one still mid-write.

The dataset is seeded once by demo/up.sh and backed up once by a new
seeded policy (demo/policy-server/policies/backup/e2e-fixture.json,
same shape as the demo's existing example policies), not generated
fresh per test run -- an earlier per-run approach was found, via live
validation, to permanently add ~120MB to the shared store on every
run with no reclamation path anywhere in the codebase.

Restore completion is detected by polling the destination filesystem
rather than waiting on the Jobs page: kind=restore job visibility via
GET /api/v1/jobs was found, during design validation, to lag by
minutes while rwfs restore itself (confirmed via its own per-job
logs) completed correctly in under a second every time -- a separate,
out-of-scope observability gap, not a defect in restore itself.

See docs/superpowers/specs/2026-08-17-restore-content-e2e-design.md."
```

---

## Self-Review

**Spec coverage:** every Goal in the design doc maps to a concrete piece of this task — fixed-fixture
seeding (Steps 1-2), no-Makefile-change/host choice (unchanged from the design), real-UI selection/
rename/restore (Step 5's main body), race-safe checksum proof (Step 5's `expect.poll`), cleanup (Step
5's `finally` block, scoped to only the per-run destination and policy). The two structural changes
from the very first draft (fixed fixture instead of per-run generation; poll-the-checksum instead of
poll-the-count) are both explained inline, in the design doc, and in this plan's Global Constraints —
not silently different from what a reader might expect.

**Placeholder scan:** none — every step has complete, real code, already run successfully multiple
times against a live stack, including one full `demo-down`/`demo-up` cycle from scratch.

**Type/interface consistency:** `waitForCatalogFolderRow`'s signature
(`page, parentSegments, dirPath, timeoutMs`) matches its one call site in Step 5 exactly. The fixture
path string `/data/e2e-restore-content-fixture` is identical, character-for-character, across Step 1
(`demo/up.sh`), Step 2 (`e2e-fixture.json`'s `object_filters[0].path`), and Step 5
(`FIXTURE_SRC_DIR`) -- verified by direct comparison, since these three independent literals are the
one place a typo would silently break the whole scenario (the backup policy would simply never find
anything to back up, and the test would time out at the catalog-wait step with no more specific
signal than "found 0").
