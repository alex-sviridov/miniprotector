# E2E: Automatic Policy Cleanup for the Web Playwright Suite — Design

## Problem

`demo/policy-server/policies/restore/` has been accumulating untracked, root-owned JSON files —
one per `web/e2e` run that exercises the restore-verify flow — because
`restore-verify.spec.js`'s first `test.step` ("a real backed-up file verifies successfully")
creates a real verify-mode restore policy and never deletes it. Its sibling steps 2 and 3 both
clean up their own policies via a manual `try { ... } finally { delete }` block; step 1 never got
that treatment.

The same root cause — cleanup left to a manually-written `try/finally` at each call site — has a
second, currently invisible instance: `seedRestoreCartCatalogData` (`policySeeding.js`) and
`runAdhocBackupPolicy` (`live-job-updates.spec.js`) both create real ad-hoc backup policies through
the app's "Run now" UI flow, and neither is ever deleted. This doesn't show up as `git status`
noise only because `.gitignore` already has a `demo/policy-server/policies/backup/adhoc-*.json`
pattern hiding it — the policies still pile up, undeleted, in the running demo stack's policy
store.

This design replaces the manual, easy-to-miss `try/finally` convention with a Playwright fixture
that makes policy cleanup automatic, and closes both leaks it currently misses.

## Goals

- Every policy any `web/e2e` spec creates — backup, restore, or verify — is deleted by the end of
  the test that created it, regardless of whether that test passes or fails.
- Replace the scattered, manually-written `try/finally`-delete blocks with a single shared
  mechanism, so a future spec can't reproduce this bug by simply forgetting one.
- Fix the two concrete leaks identified above: `restore-verify.spec.js` step 1's verify policy, and
  the ad-hoc backup policies created by `seedRestoreCartCatalogData` / `runAdhocBackupPolicy`.
- Add a defensive `.gitignore` pattern for the auto-generated restore-policy filenames, mirroring
  the existing `backup/adhoc-*.json` entry, as a backstop for a run that's killed before its
  fixture teardown gets to run.

## Non-Goals

- **No change to the Go `e2e` package.** `src/e2e/lifecycle_test.go` already cleans up correctly via
  `t.Cleanup` — this design only touches `web/e2e`.
- **No retroactive cleanup of already-existing leftover files.** The root-owned files currently
  sitting in `demo/policy-server/policies/restore/` are a one-time byproduct of the bug this design
  fixes; removing them (if desired) is a manual, out-of-band step, not part of this design.
- **No change to `smoke.spec.js`.** It creates no policies and has nothing to track.
- **No handling for a policy whose id was never learned** (e.g., the creation flow throws before an
  id lookup can run). This is a pre-existing limitation of every cleanup path in this suite today
  and isn't solved here — a fixture can only clean up what it was told about.
- **No global end-of-suite sweep.** Considered and rejected in favor of per-test cleanup (see
  "Discovered: rejected approaches" below).

## Discovered: rejected approaches

Two alternatives were considered before settling on a shared fixture:

- **Patch each gap in place** (add the missing `try/finally` to step 1, add similar inline cleanup
  at the two ad-hoc-backup call sites): smallest diff, but it's the same manual pattern that already
  let one call site slip through, and it does nothing to prevent the next one from slipping through
  too.
- **A `globalTeardown` that sweeps by name pattern** after the whole suite finishes: fewer call
  sites to touch, but it has to carefully exclude the permanent `e2e-fixture` seed policy (whose
  name also starts with `e2e-`) by pattern, and it only runs once the entire `npx playwright test`
  invocation completes — it doesn't help someone iterating on a single spec file, and an
  interrupted run skips it exactly as easily as a `finally` block would.

The fixture approach (this design) avoids both problems: it requires touching every current call
site once (same one-time cost as patching in place), but makes the *next* call site correct by
construction, and it runs after every individual test rather than only at the end of a full suite
run.

## Architecture

A new `web/e2e/helpers/test.js` extends Playwright's base `test` with one fixture:

```js
import { test as base, expect } from '@playwright/test'

export const AUTH_HEADERS = { Authorization: 'Bearer dev-placeholder-token-change-me' }

export const test = base.extend({
  trackPolicy: async ({ request }, use) => {
    const ids = []
    await use((id) => ids.push(id))
    for (const id of ids) {
      const resp = await request.delete(`/api/v1/policies/${id}`, { headers: AUTH_HEADERS }).catch((err) => {
        console.warn(`cleanup: failed to delete policy ${id}: ${err.message}`)
        return null
      })
      if (resp && !resp.ok()) {
        console.warn(`cleanup: failed to delete policy ${id}, status ${resp.status()}`)
      }
    }
  },
})

export { expect }
```

`trackPolicy` is injected into a test like any other Playwright fixture (`async ({ page,
trackPolicy }) => { ... }`). A test (or a helper it calls) invokes `trackPolicy(id)` as soon as it
knows a policy's id; the fixture's teardown then deletes every tracked id, in registration order,
after the test body finishes — Playwright runs fixture teardown regardless of whether the test
passed, failed, or threw, so no `try/finally` is needed around the code that uses it.

This uses Playwright's own `request` fixture (an isolated `APIRequestContext` bound to the
project's configured `baseURL`) rather than `page.request`, since cleanup has no need for the
page's browser context or cookies — only the bearer token, same as every other direct API call this
suite already makes.

### Call-site changes

- **`restore-verify.spec.js`**: each of the three `test.step`s calls `trackPolicy(id)` immediately
  after its policy is created (step 1's verify policy — newly tracked; step 2's restore policy; step
  3's failing verify policy). Steps 2 and 3 drop their now-redundant manual
  `try { ... } finally { delete }` wrapper — the lookup/assert code that used to sit inside `try`
  stays exactly as it is, just without the surrounding `try/finally`.
- **`restore-content.spec.js`**: `trackPolicy(restorePolicyId)` replaces the manual
  finally-delete block for the restore policy. The destination-directory removal
  (`rm -rf destDir`) isn't a policy and keeps its own `finally`, unchanged.
- **`policySeeding.js`**: `seedRestoreCartCatalogData(page, trackPolicy)` gains a `trackPolicy`
  parameter. After the ad-hoc backup policy's "Run now" submission succeeds, it looks the policy up
  by name (`GET /api/v1/policies?type=backup`, matching `policyName`) and calls
  `trackPolicy(id)` — the same lookup-by-name-after-creation pattern the restore specs already use,
  since the UI gives no other way to learn a created policy's id.
- **`live-job-updates.spec.js`**: `runAdhocBackupPolicy(page, policyName, trackPolicy)` gains the
  same parameter and does the same lookup-and-track, for both of its call sites
  (`e2e-live-detail-*`, `e2e-live-list-*`).
- **Import changes**: `restore-cart.spec.js`, `restore-verify.spec.js`, `restore-content.spec.js`,
  and `live-job-updates.spec.js` switch `import { test, expect } from '@playwright/test'` to
  `import { test, expect } from './helpers/test.js'`. `restore-content.spec.js`'s own local
  `AUTH_HEADERS` constant and `restore-verify.spec.js`'s two local `authHeaders` constants are
  removed in favor of the one exported from `helpers/test.js`. `smoke.spec.js` is untouched.

### `.gitignore`

Extend the existing e2e-artifacts block:

```
# e2e test artifacts (root-owned, written by docker compose exec)
demo/policy-server/policies/backup/adhoc-*.json
demo/policy-server/policies/restore/restore-*.json
demo/policy-server/policies/.changed
```

`restore-*.json` matches only the auto-generated names `restoreSubmission.js` produces
(`restore-<ISO timestamp>-<storeHost>.json` on disk, slugified from
`restore-${new Date().toISOString()}-${storeHost}`); it does not match the two manually-named
fixture-style files already in that directory (`demo-restore-preview.json`,
`demo-restore-nested-complex.json`), which aren't e2e-test artifacts.

## Error Handling

- A failed delete (non-2xx response, or a thrown network error) is logged via `console.warn` and
  never thrown from the fixture — consistent with every existing cleanup block's best-effort
  behavior today. One tracked id's failed cleanup doesn't stop the rest from being attempted.
- A test that throws before it ever calls `trackPolicy(id)` for a given policy still leaks that
  policy — same limitation the manual `try/finally` blocks had (a `finally` can only delete what it
  already knows the id of). Not solved by this design; see Non-Goals.
- If the id lookup-by-name itself fails (e.g., a race where the policy hasn't appeared yet in a
  `GET` response), the caller's own existing assertion around that lookup fails the test as it does
  today — `trackPolicy` is simply never called in that case, so nothing new is left behind versus
  the status quo for that specific race.

## Testing

This is test infrastructure, so validation is: run `make demo-up`, then `cd web && npx playwright
test`, and confirm (a) all specs (`smoke`, `restore-cart`, `restore-verify`, `restore-content`,
`live-job-updates`) still pass, and (b) `git status` and `ls demo/policy-server/policies/{restore,backup}/`
show no new untracked files once the run completes — i.e., every policy the run created is gone by
the time it finishes.

## Documentation Impact

None. This changes test infrastructure only (`web/e2e/`, plus a `.gitignore` entry) — no protocol,
component, CLI, or architecture change.
