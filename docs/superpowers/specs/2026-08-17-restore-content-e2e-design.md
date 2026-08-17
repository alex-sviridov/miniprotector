# E2E: Restore File Content, Verified by Checksum — Design

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

- Drive the backup-policy creation, the catalog folder selection, the destination-path rename, the
  restore submission, and both jobs' completion through the real browser UI wherever a UI affordance
  exists for it — matching this suite's existing convention (`policySeeding.js`, `restore-cart.spec.js`,
  `restore-verify.spec.js`) of using out-of-band `execSync`/API calls only for the couple of steps
  that have no UI/API surface at all.
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
- Also assert, from the restore job's own log detail page (real UI, `waitForLogLine`-style,
  mirroring `restore-verify.spec.js`), that its `restore complete` line reports `files_written=100` —
  a UI-visible confirmation of the same fact the checksum comparison proves independently.
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
- **No cross-host restore.** Source and restore destination are the same container (`webserver`),
  matching what was actually asked for ("restore with folder rename"), not a two-host scenario. A
  cross-host variant is a plausible future extension, not built here.
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
  spec instead — the established local pattern in this suite (`restore-verify.spec.js` inlines its
  own scenario-specific steps too, only pulling genuinely shared, generic polling logic out into
  `policySeeding.js`).

## Architecture

### 1. Dataset generation (`execSync`, no UI)

One `docker compose exec -T webserver bash -c '<script>'` call, generating a self-contained shell
script (heredoc'd into the JS string) that:

1. Checks free space on `/data` (`df --output=avail -B1 /data`) against a 2GiB floor — exits
   non-zero with a clear stderr message if it's short, which `execSync` surfaces as a thrown JS
   error with the script's output attached (no special handling needed on the JS side beyond letting
   it propagate — a failed precondition should fail the test loudly, not be swallowed).
2. Creates a fixed tree under a fresh `/data/e2e-restore-content-src-<runId>/`: 5 top-level
   directories (`d0`..`d4`), each with 4 subdirectories (`s0`..`s3`) — 20 leaf directories total.
   `runId` is `Date.now()`, matching this suite's existing uniqueness convention
   (`e2e-restore-cart-${Date.now()}`, etc.) — guarantees a fresh path every run, no leftover-collision
   handling needed.
3. Writes exactly 100 files (`file_000.bin`..`file_099.bin`), 5 per leaf directory (round-robin),
   each sized randomly between 100KB and 2MB via `shuf -i 102400-2097152 -n 1`, content from
   `/dev/urandom` (`head -c "$size" /dev/urandom > "$path"`).
4. Prints a sorted `sha256sum` manifest of the whole tree (`find ... -type f -exec sha256sum {} \;
   | sort`) to stdout as its last act — captured directly from `execSync`'s return value, no second
   round-trip needed to fetch it.

### 2. Backup, through the real UI

Inlined in the spec (see Non-Goals for why this isn't factored into `policySeeding.js`):
`/policies` → "New Policy" → fill name (`e2e-restore-content-<runId>`) → add hostname `webserver` →
add object filter path (the generated source directory) → select the `store` storage policy → click
"Run now" (`backup-policy-run-now`) — the exact sequence `seedRestoreCartCatalogData` already
demonstrates, parameterized to this test's own path instead of the fixed `/var/lib/dbdata`.

Then: force `./policyclient fetch` on `webserver` (`execSync`, no UI affordance to trigger this
faster than the default 900s interval — same justification every existing spec in this suite already
documents for this exact call), and wait for the backup job via the existing, unmodified
`waitForJobSuccess(page, policyName)`.

### 3. Catalog selection and destination rename, through the real UI

New export in `policySeeding.js`: `waitForCatalogFolderRow(page, parentSegments, folderName,
timeoutMs)` — generalizes `waitForCatalogFiles`'s existing drill-and-poll-with-reload shape (real
gap: that helper checks for exact leaf *filenames*; this one needs to confirm a *folder row* is
visible after drilling to its parent, without entering it) into its own small, single-purpose
function. This is the one genuinely reusable piece extracted from this scenario — the drill/reload
polling logic already exists twice in this codebase in slightly different forms
(`waitForCatalogFiles`, and `restore-cart.spec.js`'s own inline `drillInto`); a third near-copy for
"wait for a folder row" belongs in the shared helpers file, not inlined a third time.

Then, in the spec itself: drill to the source directory's parent, click its folder checkbox
(`folder-checkbox-<srcDir>`) — the same host-agnostic whole-folder selection mechanism
`restore-cart.spec.js` already exercises — navigate to `/restore`, confirm the
`restore-row-:<srcDir>` wildcard row appears.

Rename the destination using the real UI: click `dest-path-text-:<srcDir>` to enter edit mode, fill
`dest-path-input-:<srcDir>` with the destination directory
(`/data/e2e-restore-content-dest-<runId>`), commit via Enter (the same interaction
`RestoreView.vue`'s `commitEdit` expects). Select `webserver` in `destination-select`. Leave
`overwrite-checkbox` unchecked (default `false`) — the destination is guaranteed fresh, so there's
nothing to overwrite. Click `restore-button`.

Read the created restore policy's name from `submission-results` (regex `Started restore policy
(\S+) from`, the exact pattern `restore-verify.spec.js` step 2 already uses), and look up its `id`
via `GET /api/v1/policies?type=restore` (no UI affordance to read the id back — same gap
`restore-verify.spec.js` already documents and works around identically). From this point on, the
rest of the scenario runs inside a `try`/`finally` so the restore policy is deleted (`DELETE
/api/v1/policies/{id}`) even if a later assertion throws.

### 4. Restore completion, through the real UI

Force `./policyclient fetch` on `webserver` again (`execSync` — the restore policy didn't exist
during step 2's fetch). Wait for the restore job via `waitForJobSuccess(page, restorePolicyName)` —
confirmed real and meaningful now: `GET /api/v1/jobs` supports `kind=restore` end to end
(`src/cmd/api-server/jobs.go`'s `validJobKinds`), backed by real `event=start`/`event=finish` log
lines `agent` emits around its `rwfs restore` exec, the same mechanism `kind=verify` already proved
out working in `restore-verify.spec.js` step 1.

Open the job's detail page (`page.locator('tbody tr', {hasText: restorePolicyName}).locator('a').click()`),
wait for a `restore complete` log line (local `waitForLogLine` helper, copied from
`restore-verify.spec.js`'s own inline version — same reload-retry shape, same Loki-ingestion-race
justification documented there), expand it, and assert its `files_written` field is `100`.

### 5. Checksum verification (`execSync`, no UI)

Same manifest-generation script shape as step 1, run against the destination directory, producing a
second sorted `sha256sum` manifest.

Both manifests are `<hash>  <absolute path>` lines. Strip each side's own known directory prefix
(source prefix from the source manifest, destination prefix from the destination manifest) down to a
bare relative path, producing `<hash>  <relative path>` for both. Assert:
- Both normalized, sorted manifests are array-equal (proves content hash, file count, and directory
  structure/rename all in one comparison — a length or path mismatch fails the same assertion a
  content mismatch would).
- Explicitly assert length `=== 100` too, as a belt-and-braces guard against a vacuously-equal
  empty-vs-empty comparison masking a real failure upstream.

### 6. Cleanup

In the `finally` block (already covering the restore-policy delete): `rm -rf` both directories via
one more `execSync` docker-exec call. Best-effort — a failed cleanup is logged (`console.warn`,
matching `restore-verify.spec.js`'s existing cleanup-failure handling) rather than thrown, so it
never masks whatever error the `try` block raised.

## Error Handling

- Insufficient free space: the generation script itself fails fast and loud (see Architecture §1) —
  no separate space-check step, no silent skip.
- Backup or restore job failure: `waitForJobSuccess` already throws with a clear timeout message on
  either; no new handling needed.
- Checksum mismatch: a plain `expect(...).toEqual(...)` failure — Playwright's diff output on an
  array mismatch is already informative (shows which relative paths/hashes differ).
- Any assertion failure after policy creation: the `finally` block still deletes the restore policy
  and removes both directories, so a failing run doesn't leak state into the next one the way a
  bare `throw` partway through would.

## Testing

This spec *is* a test — "testing the test" isn't in scope beyond running it against a real
`make demo-up` stack and confirming it passes on a clean run, then confirming it correctly fails (and
still cleans up) when a deliberately-introduced defect is present — e.g. temporarily pointing the
destination-path rename at the wrong directory, confirming the checksum comparison catches it, then
reverting.

## Documentation Impact

None. This adds test coverage for already-shipped, already-documented behavior — no protocol,
component, or architecture change.
