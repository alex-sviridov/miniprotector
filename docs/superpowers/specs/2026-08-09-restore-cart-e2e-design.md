# Design: restore cart e2e tests

**Date:** 2026-08-09
**Status:** Approved for planning

## Problem

The restore cart (`docs/superpowers/specs/2026-08-09-restore-cart-design.md`) — checkbox selection
in the catalog UI, backed by a rule-engine store — is covered by thorough component/unit tests
(Vitest + `@vue/test-utils` + jsdom), but nothing exercises it in a real browser against real
backend data. That gap is not hypothetical: the restore-cart branch's own final review caught a bug
(the sidebar highlight was silently defeated by Tailwind's compiled-CSS class-emission order) that
every jsdom-based test missed, because jsdom doesn't compile or apply real CSS cascade rules. This
adds a browser-driven end-to-end suite for the restore cart's core selection scenarios.

This repo has no browser-automation framework today. The existing "e2e" suite (`src/e2e`, `//go:build
e2e`) is Go-only and currently a single HTTP smoke test plus one lifecycle test
(`docs/superpowers/specs/2026-08-05-e2e-lifecycle-test-design.md`) — neither drives a browser, since
neither needed to observe client-side-only behavior. The restore cart is the first feature in this
repo where a Go-level e2e test structurally cannot verify the behavior in question.

## Approach

Playwright, driving the actual browser against the real, already-running demo lab
(`make demo-up`, `http://localhost:8091`) — not a mocked API, not a standalone dev server. This
matches the existing Go e2e suite's philosophy exactly: the demo lab is a precondition the test does
not start, stop, or manage. `make test-e2e`'s existing doc comment already warns that it mutates the
demo lab (revokes/reissues a client cert, creates and deletes a policy); this suite adds to that
disclosure rather than introducing a new one.

### Why not mock the API

The restore cart is pure client-side state, so a mocked-API approach was considered and rejected:
it would only re-verify what the existing Vitest component tests (which mock stores directly, one
layer closer to the code) already cover. Running against the real stack is what lets this suite
catch real-browser-only defects (CSS cascade, real routing, real DOM timing) instead of duplicating
existing coverage with extra infrastructure.

### Why seed data via the UI, not raw API calls

The demo's three built-in backup policies run hourly (`backup_window: ["0 * * * *"]`), which is
non-deterministic for a test's timing. The existing Go lifecycle test solves this by creating its
own fast, ad-hoc policy — this suite reuses that same idea, but drives it through the actual
`/policies` UI (`BackupPolicyFormModal`'s "Run now" button) instead of a raw `POST /api/v1/policies`
call. This means the seeding step doubles as real coverage of the policy-creation-and-run-now UI
flow, rather than being pure test scaffolding.

One step still can't go through the UI: per `docs/superpowers/specs/2026-08-02-policy-disabled-at-
design.md`, an ad-hoc policy created via "Run now" (`POST /policies/adhoc`) is, server-side, an
ordinary backup policy under the hood — it is not pushed to the target node. The target's
`policyclient` still discovers it only on its own next fetch, which defaults to a 900-second
interval. Forcing that fetch immediately requires the same `docker compose exec database
policyclient fetch` shell-out the Go lifecycle test already uses for its own two CLI-only steps —
there is no host-reachable API or UI surface for it.

### Sample data is sufficient without changes

`database`'s mounted sample data at `/var/lib/dbdata` (`demo/sample-data/db/{dump.sql,schema.sql}`)
is a folder with two direct file children — one level of real nesting, which is enough to exercise
folder-wildcard selection, drill-down pre-checking, and a single nested exception (the three
behaviors that differentiate the restore cart's rule engine from a flat selection list). No changes
to `demo/sample-data` are needed.

## Design

### File layout

```
web/
  playwright.config.js          # baseURL: http://localhost:8091, testDir: ./e2e, no webServer block
  e2e/
    helpers/
      policySeeding.js          # Playwright-page-driven: create+run policy via UI, force fetch, poll via UI
    restore-cart.spec.js        # the restore-cart selection scenarios
  package.json                  # + devDependency @playwright/test, + script "test:e2e": "playwright test"
```

`policySeeding.js` takes a Playwright `page` and returns `{ sourceHost: 'database', dirPath:
'/var/lib/dbdata', files: ['dump.sql', 'schema.sql'] }` once seeding completes. Isolating it from
`restore-cart.spec.js` keeps the "get real data into the catalog" concern (UI-driven policy
creation, one CLI shell-out, UI-driven polling of `/jobs` and `/catalog`) separate from the "verify
restore-cart selection behavior" concern the spec file itself covers — matching this codebase's
existing pattern of keeping setup/fixture logic out of the assertions that consume it.

### Authentication

No UI login flow is driven per test run. `web`'s auth (`stores/auth.js`) is a single bearer token
read from `localStorage['mp_api_token']` on store init — Playwright's `context.addInitScript(() =>
localStorage.setItem('mp_api_token', 'dev-placeholder-token-change-me'))`, run before the first
navigation, satisfies it directly. (`dev-placeholder-token-change-me` is the demo lab's documented
placeholder token, per `demo/README.md` and `demo/local.conf`.)

### Seeding flow (`policySeeding.js`, called once per suite run)

All steps operate on the Playwright `page` except step 4, which has no UI/API surface:

1. Navigate to `/policies`, click **New backup** — opens `BackupPolicyFormModal`.
2. Fill the form: name `e2e-restore-cart-<timestamp>` (unique per run, avoiding collisions across
   repeated runs — same reasoning as the Go lifecycle test's own unique-suffix policy names), client
   filter hostname `database`, object filter path `/var/lib/dbdata`, destination = the demo's
   existing `store` storage policy (selected from the real, API-populated dropdown).
3. Click **Run now** — modal closes, app redirects to `/jobs` (a real navigation assertion, not
   assumed).
4. **Non-UI step:** `docker compose -f demo/docker-compose.yml exec -T database policyclient fetch`
   (via Node's `child_process`), forcing immediate pickup instead of waiting up to 900s.
5. On `/jobs`, poll via page reload until the new job's row shows `success` (bounded timeout,
   matching the Go lifecycle test's poll-with-timeout pattern) — exercises the real Jobs list
   rendering.
6. Navigate to `/catalog`, drill into `database` → `/var/lib/dbdata`, poll via page reload until
   `dump.sql` and `schema.sql` both appear — confirms catalog-sync visibility through the real UI
   before the restore-cart scenarios begin.

### `data-test` attributes (`web/src/views/CatalogView.vue`)

```vue
<TriStateCheckbox
  :data-test="row.isFolder ? `folder-checkbox-${row.path}` : `file-checkbox-${row.sourceHost}:${row.path}`"
  v-bind="checkboxProps(row)"
  @toggle="toggleSelection(row)"
/>
```

Matches this codebase's existing `data-test="..."` convention (`chip-date`, `nav-link`, `breadcrumb`,
`crumb`, etc.). `TriStateCheckbox.vue` needs no change — Vue's automatic attribute inheritance
forwards `data-test` onto its single root `<input>` element already. This also closes a gap the
restore-cart branch's final review flagged (no test hooks on the checkbox column).

### Restore-cart scenarios (`restore-cart.spec.js`)

One sequential spec (re-seeding per scenario would be slow — each seed involves polling a real
backup job to completion), walking through in order, using the `{ sourceHost, dirPath, files }`
fixture `policySeeding.js` returns:

1. Navigate to `/catalog`, drill to `database`'s `/var/lib/dbdata`. Click `dump.sql`'s file
   checkbox (`[data-test="file-checkbox-database:/var/lib/dbdata/dump.sql"]`) → assert it's checked,
   the sidebar Restore link carries the highlight class, and `/restore` lists
   `/var/lib/dbdata/dump.sql (database)`.
2. Back on `/catalog`, click the parent folder's checkbox (`[data-test="folder-checkbox-/var/lib/
   dbdata"]`) → assert it's checked; drill in → assert both `dump.sql` and `schema.sql` show
   pre-checked; `/restore` now lists `/var/lib/dbdata/*`.
3. While drilled in, uncheck `schema.sql` → assert it's unchecked; navigate back up → assert the
   folder checkbox is now indeterminate; `/restore` still lists only `/var/lib/dbdata/*` (exceptions
   are never shown, per the restore-cart design's placeholder-list rule).
4. Uncheck the folder → assert the sidebar highlight disappears and `/restore` shows its empty
   state.

### Wiring

- `web/package.json`: `+devDependency @playwright/test`, `+script "test:e2e": "playwright test"`.
- `web/playwright.config.js`: `baseURL: 'http://localhost:8091'`, `testDir: './e2e'`, no `webServer`
  block (the demo lab is a precondition, Playwright launches nothing).
- `Makefile`: extend the existing `test-e2e` target to run `cd web && npx playwright test` after the
  Go e2e run, under the same `make demo-up`-first precondition.
- `README.md`: update `make test-e2e`'s doc comment to note it now also drives the web UI and
  creates/runs a backup policy through it, in addition to the existing client cert revoke/reissue
  disclosure.
- `docs/components/web.md`: add a brief mention of `web/e2e/` alongside existing testing-relevant
  content, cross-linking this design doc.

## Testing plan

This entire spec *is* the testing plan — there is no separate "tests for the tests." Coverage is the
four restore-cart scenarios in the Design section above, seeded by the UI-driven policy flow.

## Out of scope

- No CI wiring — local, `make demo-up`-dependent, matching the Go e2e suite's current scope.
- No cleanup/deletion of the seeded policy after the run — it self-expires via the ad-hoc mechanism's
  `disabled_at` (1h timeout), so no teardown step is needed.
- No coverage of restore *execution* — that still doesn't exist anywhere in the product; scope stays
  exactly the four selection scenarios above.
- No new synthetic directory nesting added to `demo/sample-data` — the existing one level under
  `/var/lib/dbdata` is sufficient (see "Sample data is sufficient without changes" above).
- No mocked-API test mode — see "Why not mock the API" above.

## Documentation

- `README.md` — update the `make test-e2e` comment (see Wiring).
- `docs/components/web.md` — add the `web/e2e/` mention (see Wiring).
- `docs/ARCHITECTURE.md` — no change (test-only addition, no topology/data-flow change).
- `CHANGELOG.md` — one dated entry: the repo gains a Playwright-based browser e2e suite for the
  restore cart's selection scenarios, run against the real demo lab and seeded via the actual
  policy-creation UI.
