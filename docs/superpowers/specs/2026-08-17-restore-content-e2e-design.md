# E2E: Restore File Content, Verified by Checksum — Design

> **Revision note:** This design was validated by actually building and running it against a live
> `make demo-up` stack during the design phase (not just read against source). Two details from the
> first draft changed as a direct result and are called out below where they matter: the host
> (`webserver` → `database`) and the restore-completion detection mechanism (Jobs-page polling →
> destination-filesystem polling), the latter driven by a real, reproducible finding — see
> "Discovered: `kind=restore` job-visibility lag" below.

## Problem

`rwfs restore` now writes real file content to disk (see
`docs/superpowers/specs/2026-08-17-restore-file-content-design.md`), but nothing in this repo's
test suite exercises that fact end-to-end against the real demo stack. The closest existing
coverage, `web/e2e/restore-verify.spec.js`, predates real file-content restore: its "clicking
Restore creates a real restore-execution policy" step only asserts the policy was *created* (HTTP
`201`) — written back when `mode: "restore"` was log-only, so there was nothing further to wait for
or check. It never waits for the restore task to finish, and never looks at what landed on disk.
This design adds that missing coverage: generate a nontrivial, randomly-sized dataset, back it up,
restore it through the real web UI with a folder rename, and prove — by checksum, not just by a
green job — that every byte came back correctly at the renamed location.

## Goals

- Drive the backup-policy creation, the catalog folder selection, the destination-path rename, and
  the restore submission through the real browser UI — matching this suite's existing convention
  (`policySeeding.js`, `restore-cart.spec.js`, `restore-verify.spec.js`) of using out-of-band
  `execSync`/API calls only for the steps that have no UI/API surface at all.
- Generate 100 files (100KB–2MB each, random per file) across a fixed, nested directory tree,
  entirely on the destination container's filesystem (no host bind-mount, no `docker-compose.yml`
  change) — there is no UI affordance for creating source files, so this one step stays
  `execSync`-driven, same justification `policySeeding.js` already documents for its own one CLI-only
  step.
- Restore into a **different, renamed destination path** on the **same host**, through the real
  `RestoreView.vue` destination-path-rename UI (`dest-path-text-*` → `dest-path-input-*`) — this is
  the UI's actual folder-rename affordance, not a synthetic one built for this test.
- Prove restored content is byte-identical to the source: a `sha256sum` manifest of the source tree,
  captured immediately after generation, compared against a `sha256sum` manifest of the destination
  tree after restore, with each side's known directory prefix stripped so only relative path + hash
  are compared. This is the other step with no UI affordance (there is no browser-based way to hash
  a file), so it stays `execSync`-driven too.
- Fail fast with a clear message if the destination container doesn't have comfortable free disk
  space before generating anything, rather than risking a full disk.
- Leave no residue on a passing run: generated directories removed, the restore policy deleted (the
  backup policy is ad-hoc and self-expires — see Non-Goals).

## Non-Goals

- **Not a Go `e2e` package test.** This lives entirely in `web/e2e/` as a new Playwright spec
  (`restore-content.spec.js`), run by `test-e2e`'s existing `cd web && npx playwright test` step —
  no `Makefile` change, no competition with the Go `e2e` package's `-timeout=240s` budget. Playwright
  timeouts are set per-test (`test.setTimeout(...)`), the same mechanism `restore-verify.spec.js`
  already uses for its own longer-running scenario.
- **No cross-host restore.** Source and restore destination are the same container, matching what
  was actually asked for ("restore with folder rename"), not a two-host scenario. A cross-host
  variant is a plausible future extension, not built here.
- **No UI-visible confirmation of `files_written` via the job's log page.** The first draft of this
  design included one — reading the restore job's `restore complete` log line and asserting
  `files_written=100` in the browser. Live validation found this specific assertion to be a real
  source of severe, unpredictable flakiness unrelated to the feature under test (see "Discovered"
  below) and it was cut. The checksum comparison already proves completeness (file count) and
  correctness (content) together; this was always a "nice to have" second confirmation, not the
  test's core proof.
- **No cleanup of the ad-hoc backup policy.** Confirmed in `src/cmd/api-server/policies.go`:
  `POST /api/v1/policies/adhoc` (what `BackupPolicyFormModal`'s "Run now" button calls) sets
  `DisabledAt = now + AdhocPolicyTimeoutSec` (3600s default) server-side — it self-expires. This
  matches `policySeeding.js`'s existing `seedRestoreCartCatalogData`, which also never deletes its
  ad-hoc backup policy. The **restore** policy has no ad-hoc/self-expiring variant (confirmed: the
  REST docs list only `POST /api/v1/restore`), so it still needs the explicit try/finally delete
  `restore-verify.spec.js` already uses for its own restore policy.
- **No verification of directory-only or zero-byte-file edge cases.** This is a straightforward
  "100 real, non-empty files, one rename" scenario — it is not trying to be the exhaustive edge-case
  suite `restorefile_test.go`/`restore_test.go` already are at the unit/integration level.
- **No new shared "ad-hoc backup via UI" helper.** `seedRestoreCartCatalogData` in `policySeeding.js`
  already encapsulates the same UI steps (fill name, add hostname, add filter path, select storage,
  click "Run now"), but tightly coupled to its own hardcoded source directory and its own
  exact-filename catalog-wait. Generalizing it risks destabilizing the two existing specs that depend
  on it for one new caller's benefit. This design inlines the equivalent steps directly in the new
  spec instead.
- **No investigation or fix of the `kind=restore` job-visibility lag itself.** It's real and
  reproducible (see below), but it's a separate concern in `src/cmd/api-server/jobs.go` (or the
  Vector/Loki log-shipping pipeline), unrelated to `rwfs restore`'s own correctness, which this
  design independently confirmed is fine. Worth its own investigation later; out of scope here.

## Discovered: `kind=restore` job-visibility lag

While validating this design against a live stack, `waitForJobSuccess` (the same helper
`restore-verify.spec.js` already uses successfully for `kind=verify`) was tried for the restore step
too, and failed its 100s timeout twice in a row. Direct investigation (reading `bwfs`/`store`/`agent`
container logs, and both the aggregate `GET /api/v1/jobs` endpoint and the per-job
`GET /api/v1/jobs/{id}/logs` endpoint directly) established, conclusively:

- **`rwfs restore` itself worked perfectly both times.** The per-job log endpoint
  (`/api/v1/jobs/{job_id}/logs`, fetched by exact job ID, distinct from the aggregate listing) showed
  the complete, correct sequence every time, with no delay: `restore starting` → 100× `resolved` →
  `summary resolved=100 warnings=0` → `creating restored directory structure` →
  `restored directory structure created created=26 reused=0` → `restoring file content` →
  `restore complete files_written=100 bytes_written=123284512 skipped=0`, start to finish in under a
  second.
- **The aggregate `GET /api/v1/jobs?kind=restore` listing — the same one the Jobs page's table
  renders, and what `waitForJobSuccess` polls — did not show that job at all** for several minutes.
  In two live runs it eventually appeared as `success`, but after roughly 5 and 9 minutes
  respectively — far past any sane test timeout, and wildly inconsistent with `kind=backup`,
  `kind=verify`, and `kind=policy-update` jobs, which were confirmed to appear within seconds in every
  run, including runs immediately before and after the slow restore jobs.
- This is not a `webserver`-vs-`database` host effect (both hosts showed the same restore-specific
  lag; the backup-side flakiness observed once on `webserver` was a separate, transient,
  self-resolving issue, most likely related to a one-time `bwfs` "database is locked" restart
  immediately after that particular container rebuild — a red herring, ruled out by successfully
  reproducing the *restore*-specific lag again afterward on `database`, a host that never had that
  restart).
- A restore policy created directly via `POST /api/v1/restore` (bypassing the web form) happened to
  surface quickly in one trial — but this alone doesn't establish UI-vs-API as the actual cause, and
  wasn't pursued further; the practical takeaway is the same regardless of root cause: **this specific
  aggregate query is not reliable enough, for `kind=restore` specifically, to gate a test's pass/fail
  on within a normal timeout.**

The design responds to this with a completion-detection mechanism that doesn't depend on it at all
(see Architecture §4) rather than either accepting multi-minute flakiness or working around it with
an unreasonably long timeout. This finding is worth its own follow-up investigation someday (a real
gap in observability for one-shot restore jobs specifically), but fixing `jobs.go`/Vector/Loki is not
this task's job.

## Architecture

### 1. Dataset generation (`execSync`, no UI)

One `docker compose exec -T <host> bash -c '<script>'` call (via Node's `execFileSync`, passing the
script as a single argv element so its own internal quoting never needs escaping through an outer
shell — validated live; a naive `execSync` template-string approach would have needed fragile
double-escaping instead), generating a self-contained shell script that:

1. Checks free space on `/data` (`df --output=avail -B1 /data`) against a 2GiB floor — exits
   non-zero with a clear stderr message if it's short, which `execFileSync` surfaces as a thrown JS
   error with the script's output attached.
2. Creates a fixed tree under a fresh `/data/e2e-restore-content-src-<runId>/`: 5 top-level
   directories (`d0`..`d4`), each with 4 subdirectories (`s0`..`s3`) — 20 leaf directories total, 5
   files each. `runId` is `Date.now()`, matching this suite's existing uniqueness convention.
3. Writes exactly 100 files (`file_000.bin`..`file_099.bin`), each sized randomly between 100KB and
   2MB via `shuf -i 102400-2097152 -n 1`, content from `/dev/urandom`.
4. Prints a sorted `sha256sum` manifest of the whole tree to stdout as its last act — captured
   directly from the call's return value.

Validated live: 100 files generated in well under a second, ~110–125MB total per run, against
~14GB free — comfortably safe.

### 2. Backup, through the real UI

Inlined in the spec: `/policies` → "New Policy" → fill name (`e2e-restore-content-<runId>`) → add
hostname → add object filter path (the generated source directory) → select the `store` storage
policy (`store (store:8080)`) → click "Run now" (`backup-policy-run-now`) — the exact sequence
`seedRestoreCartCatalogData` already demonstrates, parameterized to this test's own path.

Then: force `./policyclient fetch` (`execFileSync`, no UI affordance to trigger this faster than the
default 900s interval), and wait for the backup job via the existing, unmodified
`waitForJobSuccess(page, policyName)` — **confirmed live, reliably fast** (backup-kind job visibility
was never the problem; see "Discovered" above).

### 3. Catalog selection and destination rename, through the real UI

New export in `policySeeding.js`: `waitForCatalogFolderRow(page, parentSegments, dirPath,
timeoutMs)` — generalizes `waitForCatalogFiles`'s existing drill-and-poll-with-reload shape into a
"wait for a folder row" version (that helper checks leaf *filenames*; this one confirms a *folder
row* is visible after drilling to its parent, and — matching `waitForCatalogFiles`'s own behavior —
leaves the page already drilled down so the caller can act on the row immediately, no redundant
re-navigation).

```js
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

Then, in the spec itself: click the folder checkbox (`folder-checkbox-<srcDir>`) — confirmed live to
match `CatalogView.vue:235`'s actual test-id (`row.isFolder ? \`folder-checkbox-${row.path}\` : ...`)
— the same host-agnostic whole-folder selection mechanism `restore-cart.spec.js` already exercises.
Navigate to `/restore`, confirm the `restore-row-:<srcDir>` wildcard row appears (the `:` prefix
confirmed live to match `RestoreView.vue`'s `entryKey`, `${entry.host ?? ''}:${entry.path}`, for a
host-agnostic folder selection).

Rename the destination using the real UI: click `dest-path-text-:<srcDir>` to enter edit mode, fill
`dest-path-input-:<srcDir>` with the destination directory, commit via `Enter` (matching
`RestoreView.vue`'s `commitEdit`, confirmed live: the span re-renders with the new text). Select the
host in `destination-select`. Leave `overwrite-checkbox` unchecked — the destination is guaranteed
fresh. Click `restore-button`.

Read the created restore policy's name from `submission-results` (regex `Started restore policy
(\S+) from` — confirmed live: the server auto-generates this name, e.g.
`restore-2026-08-17T16:36:37.615Z-store`; there is no name input field in `RestoreView.vue` at all),
and look up its `id` via `GET /api/v1/policies?type=restore`. From this point on, the rest of the
scenario runs inside a `try`/`finally` so the restore policy is deleted even if a later assertion
throws.

### 4. Restore completion — polls the filesystem, not the Jobs page

Force `./policyclient fetch` again (the restore policy didn't exist during step 2's fetch). Then,
**deliberately not `waitForJobSuccess`** — see "Discovered" above. Instead:

```js
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
    if (Date.now() > deadline) throw new Error(`Timed out waiting for ${count} files under ${dir} (found ${n})`)
    await new Promise((resolve) => setTimeout(resolve, 3000))
  }
}
```

This polls the actual, user-visible outcome of the restore directly, sidestepping the observability
pipeline's own (separate, out-of-scope) reliability entirely. The 90s ceiling comfortably covers a
real 30s agent reconcile tick plus the restore itself, which completed in about a second in every
observed run.

### 5. Checksum verification (`execSync`, no UI)

Same manifest-generation script shape as step 1 (without the generation part), run against the
destination directory. Both manifests are `<hash>  <absolute path>` lines; each side's own known
directory prefix is stripped down to a bare relative path, and both normalized, sorted manifests are
asserted array-equal — proving content hash, file count, and directory structure/rename all in one
comparison. Length is also asserted `=== 100` explicitly, as a guard against a vacuously-equal
empty-vs-empty comparison.

### 6. Cleanup

In the `finally` block: delete the restore policy via the API, then `rm -rf` both directories via one
more `execFileSync` docker-exec call. Best-effort — a failed cleanup is logged (`console.warn`) rather
than thrown, so it never masks whatever error the `try` block raised.

## Host choice: `database`, not `webserver`

The first draft of this design chose `webserver` specifically to avoid any coupling with
`src/e2e/lifecycle_test.go`'s use of `database`. That reasoning doesn't actually hold: `lifecycle_test.go`
is a completely separate Go test binary/process from this Playwright suite, so there was never any
real risk there. What live validation actually found was a one-time, self-resolving backup-job
visibility hiccup specific to `webserver` immediately after a from-scratch container rebuild
(unrelated to restore, and unrelated to the real `kind=restore` finding above) — switching to
`database` sidestepped it, and `database` is also the exact host `restore-verify.spec.js` already
uses successfully for the closest existing analog to this scenario. `restore-cart.spec.js` and
`restore-verify.spec.js` both already use `database` within the same Playwright suite (which runs
fully sequentially — `workers: 1` in `playwright.config.js` — so there's no parallel-execution
concern regardless of host choice).

## Error Handling

- Insufficient free space: the generation script itself fails fast and loud — no separate space-check
  step, no silent skip.
- Backup job failure: `waitForJobSuccess` already throws with a clear timeout message.
- Restore not completing: `waitForDestinationFileCount` throws with the expected vs. found count.
- Checksum mismatch: a plain `expect(...).toEqual(...)` failure — Playwright's diff output on an
  array mismatch is already informative.
- Any assertion failure after policy creation: the `finally` block still deletes the restore policy
  and removes both directories.

## Testing

This spec *is* a test. It was run twice, back to back, against a live, freshly-rebuilt `make demo-up`
stack during design validation, passing cleanly both times (~38s and ~51s) with clean cleanup
confirmed afterward (no leftover directories, no leftover policies).

## Documentation Impact

None. This adds test coverage for already-shipped, already-documented behavior — no protocol,
component, or architecture change.
