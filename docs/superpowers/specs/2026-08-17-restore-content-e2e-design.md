# E2E: Restore File Content, Verified by Checksum — Design

> **Revision note (2nd revision):** This design went through two rounds of live validation against a
> real `make demo-up` stack, including a final whole-branch code review that found real issues in the
> first implementation. All three are folded in here. Two things changed materially from the first
> revision: (1) the source dataset is now a **fixed fixture, seeded once by `demo/up.sh` and backed up
> once by a seeded policy** — not generated and backed up fresh by every test run — because the
> original per-run approach permanently added ~120MB to the shared demo stack's storage with no
> reclamation path anywhere in the codebase; (2) restore completion is detected by **polling the
> checksum comparison itself** (`expect.poll`), not a bare file-count check, because `rwfs restore`
> creates each destination file at its final size before streaming content into it, so a bare count
> can observe a file that looks "done" but isn't yet.

## Problem

`rwfs restore` now writes real file content to disk (see
`docs/superpowers/specs/2026-08-17-restore-file-content-design.md`), but nothing in this repo's
test suite exercises that fact end-to-end against the real demo stack. The closest existing
coverage, `web/e2e/restore-verify.spec.js`, predates real file-content restore: its "clicking
Restore creates a real restore-execution policy" step only asserts the policy was *created* (HTTP
`201`) — written back when `mode: "restore"` was log-only, so there was nothing further to wait for
or check. It never waits for the restore task to finish, and never looks at what landed on disk.
This design adds that missing coverage: back up a nontrivial, randomly-sized dataset, restore it
through the real web UI with a folder rename, and prove — by checksum, not just by a green job —
that every byte came back correctly at the renamed location.

## Goals

- Drive the catalog folder selection, the destination-path rename, and the restore submission
  through the real browser UI — matching this suite's existing convention (`policySeeding.js`,
  `restore-cart.spec.js`, `restore-verify.spec.js`) of using out-of-band `execFileSync`/API calls
  only for steps that have no UI/API surface at all.
- Restore a fixed, 100-file (100KB–2MB each, random per file) nested directory tree into a
  **different, renamed destination path** on the **same host**, through the real `RestoreView.vue`
  destination-path-rename UI (`dest-path-text-*` → `dest-path-input-*`) — this is the UI's actual
  folder-rename affordance, not a synthetic one built for this test.
- Prove restored content is byte-identical to the source: a `sha256sum` manifest of the (permanent,
  already-backed-up) source tree compared against a `sha256sum` manifest of the destination tree
  after restore, with each side's known directory prefix stripped so only relative path + hash are
  compared.
- **The dataset is a fixed, permanent fixture, not generated per test run.** Seeded once by
  `demo/up.sh` (idempotent — skipped if already present) and backed up once by a seeded
  `demo/policy-server/policies/backup/e2e-fixture.json` policy, the same way `database-backup.json`/
  `webserver-backup.json`/`audit-logs.json` already seed the demo's other example data. This is the
  single most important structural decision in this design — see "Discovered: dataset accumulation
  on the shared store" below for why.
- Leave no residue on a passing run **beyond the permanent fixture and its one-time backup**: the
  per-run destination directory and the restore policy are both deleted.

## Non-Goals

- **Not a Go `e2e` package test.** This lives entirely in `web/e2e/` as a new Playwright spec
  (`restore-content.spec.js`), run by `test-e2e`'s existing `cd web && npx playwright test` step —
  no `Makefile` change, no competition with the Go `e2e` package's `-timeout=240s` budget.
- **No cross-host restore.** Source and restore destination are the same container (`database`),
  matching what was actually asked for ("restore with folder rename").
- **No per-run dataset generation or per-run backup.** See Goals — this was the first
  implementation's design, replaced after a final review found it accumulates unreclaimed storage on
  the shared demo stack indefinitely.
- **No UI-visible confirmation of `files_written` via the job's log page.** Considered in an earlier
  revision; cut after live validation found `GET /api/v1/jobs?kind=restore`'s visibility lag (see
  below) made it a source of severe, unrelated flakiness. The checksum comparison already proves
  completeness (file count) and correctness (content) together.
- **No investigation or fix of the `kind=restore` job-visibility lag itself.** Real, reproducible,
  and independently confirmed (by a later code review) to not be caused by anything in `rwfs
  restore`, `agent`, or `api-server`'s kind-mapping logic — most likely Vector batching or a Loki
  ingestion/query-window effect for low-volume `agent` log streams. Worth its own investigation;
  out of scope here.
- **No fix for `demo/policy-server/policies/restore/` being un-gitignored and root-owned.**
  Pre-existing (caused by `restore-verify.spec.js`, which already writes there), not introduced or
  worsened by this design in any new way. Flagged, not fixed, here.

## Discovered: `kind=restore` job-visibility lag

Live validation tried `waitForJobSuccess` (the helper `restore-verify.spec.js` already uses
successfully for `kind=verify`) for the restore step too, and it failed its 100s timeout twice in a
row. Investigation (reading `bwfs`/`store`/`agent` container logs directly, and both the aggregate
`GET /api/v1/jobs` endpoint and the per-job `GET /api/v1/jobs/{id}/logs` endpoint) established:

- **`rwfs restore` itself worked perfectly both times.** The per-job log endpoint showed the
  complete, correct sequence every time, with no delay: `restore starting` → 100× `resolved` →
  `summary resolved=100 warnings=0` → `creating restored directory structure` → `restored directory
  structure created` → `restoring file content` → `restore complete files_written=100
  bytes_written=... skipped=0`, start to finish in under a second.
- **The aggregate `GET /api/v1/jobs?kind=restore` listing did not show that job at all** for several
  minutes (once ~5, once ~9, in two separate live runs) — wildly inconsistent with `kind=backup`,
  `kind=verify`, and `kind=policy-update` jobs, confirmed to appear within seconds in every run,
  including runs immediately before and after the slow restore jobs.
- A later, independent code review confirmed statically that this isn't a defect in this branch's
  code: `agent` emits byte-identical `event=start`/`event=finish` line shapes for `restore:` and
  `verify:` job IDs (no special-casing anywhere), and `api-server`'s `kindFromJobID`/`validJobKinds`/
  `binariesForKind` all handle `"restore"` correctly. The review's working theory is Vector batching
  or a Loki ingestion/query-window effect specific to low-volume `agent`-binary log streams — a
  useful starting point for whoever eventually investigates this, but not something this design
  fixes.

The design responds to this with a completion-detection mechanism that doesn't depend on it at all
(polling the destination directly — see Architecture) rather than accepting multi-minute flakiness
or masking it with an unreasonably long timeout.

## Discovered: dataset accumulation on the shared store

The first implementation of this design generated a fresh 100-file dataset and created+ran a fresh
ad-hoc backup policy on every single test run. A final code review measured the actual effect after
several live validation runs: `store:/data` had grown to 796MB (from a baseline of a few MB), with
one set of backed-up chunks and 100 catalog rows per run, and confirmed — by reading `src/cmd/**` and
the storage/catalog component docs — that **no retention, pruning, or GC mechanism exists anywhere in
this codebase.** At the chosen dataset size, this is roughly 100 more test runs before the demo
stack's 2GiB free-space floor starts failing the test outright, an eventual, manual-`make demo-down
-v`-only wall on infrastructure shared by every other e2e test and every developer using the demo lab.

The fix is structural, not a smaller dataset: **generate and back up the dataset once, not once per
run.** `demo/up.sh` seeds a fixed 100-file tree on `database` (idempotent — skipped if a full 100-file
fixture is already present), and a new seeded backup policy
(`demo/policy-server/policies/backup/e2e-fixture.json`, in the same style as the existing seeded
policies) backs it up automatically once policy-server picks it up. `restore-content.spec.js` no
longer generates anything or creates a backup policy at all — it only restores the fixture (with a
per-run destination directory, still cleaned up every run) and computes a fresh source manifest by
reading the still-present fixture files directly. This keeps the original 100KB–2MB file sizes (a
deliberate choice, confirmed still worth keeping once the per-run cost is eliminated) while making
the storage cost of this test suite a one-time ~100-150MB per `demo-up`, not per run.

One consequence: policy-server only hot-reloads its policy cache on a write to `policies/.changed`
(`src/cmd/policy-server/watch.go`) — a fresh container's own startup `ReadDir` already picks up the
new seeded JSON file, but `demo/up.sh` also touches this sentinel explicitly after seeding, so
re-running `make demo-up` against an already-running `policy-server` (e.g. after a `git pull` that
only added policy files, with no code change to trigger a container recreate) still picks up the new
policy. Confirmed live: `policy-server` logged `"policies reloaded"` immediately after the touch.

## Architecture

### 1. Fixture seeding (`demo/up.sh`, once per `demo-up`, not per test run)

Inserted right after `enroll database`:

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

Validated live, twice: once against an already-running stack (manual generation + manual sentinel
touch, confirmed `"policies reloaded"` in `policy-server`'s log), and once end-to-end via a genuine
`make demo-down -v && make demo-up` — the fixture was generated automatically with exactly 100 files,
and the seeded policy appeared via the API with no manual intervention.

### 2. Seeded backup policy: `demo/policy-server/policies/backup/e2e-fixture.json`

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

Same shape and `storage_policy_id` as the existing seeded policies (`database-backup.json`, etc.). A
5-minute cadence (rather than the existing seeded policies' hourly one) so the very first run against
a freshly-up'd stack doesn't have to wait up to an hour for the first backup — confirmed live: the
fixture backup fired and succeeded within its first window. Unlike a restore policy, a backup policy
has no one-shot mode, so it keeps re-backing-up the same (unchanged) files every 5 minutes forever —
harmless for storage (content-addressed chunk dedup means an unchanged file's re-backup adds no new
chunks; confirmed live, `store:/data` did not grow between backup cycles), but it does mean the
catalog accumulates more than one version's worth of rows for the same files over a long-lived demo
session. `rwfs restore` always resolves the latest version regardless, and the test's own catalog
wait (below) accounts for this explicitly.

### 3. Catalog wait + selection + destination rename, through the real UI

```js
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
```

Live-validated pitfall: an earlier version of this check used `=== count`, which failed on the second
live run once the fixture's recurring backup had fired twice (126 entries found, not 100) — `>=` is
correct, `===` is not, for a fixture backed up by a recurring policy.

Then, in the spec itself: click the folder checkbox (`folder-checkbox-<FIXTURE_SRC_DIR>`, confirmed
live against `CatalogView.vue:235`'s actual test-id), navigate to `/restore`, confirm the
`restore-row-:<FIXTURE_SRC_DIR>` wildcard row appears (the `:` prefix confirmed live to match
`RestoreView.vue`'s `entryKey`, `${entry.host ?? ''}:${entry.path}`, for a host-agnostic folder
selection).

`waitForCatalogFolderRow` — the one small helper added to `policySeeding.js` — generalizes
`waitForCatalogFiles`'s existing drill-and-poll-with-reload shape into a "wait for a folder row"
version, leaving the page already drilled down so the caller can act on the row immediately.

Rename the destination: click `dest-path-text-:<FIXTURE_SRC_DIR>` to enter edit mode, fill
`dest-path-input-:<FIXTURE_SRC_DIR>` with the per-run destination directory, commit via `Enter`
(matching `RestoreView.vue`'s `commitEdit`). Select `database` in `destination-select`. Leave
`overwrite-checkbox` unchecked. Click `restore-button`.

Read the created restore policy's name from `submission-results` (regex `Started restore policy
(\S+) from` — the server auto-generates this name, e.g. `restore-2026-08-17T16:36:37.615Z-store`),
look up its `id` via `GET /api/v1/policies?type=restore`. From this point on, the rest of the
scenario runs inside a `try`/`finally` so the restore policy is deleted even if a later assertion
throws.

### 4. Restore completion — polls the checksum comparison itself, not a file count

Force `./policyclient fetch` (the restore policy didn't exist during the catalog wait above). Then:

```js
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
```

This is deliberately not `waitForJobSuccess` (see "Discovered: `kind=restore` job-visibility lag"
above) and deliberately not a bare file-count check either. A live-validated real bug in the first
implementation used a plain `find -type f | wc -l` poll: `writeRestoreFile`
(`src/cmd/rwfs/restorefile.go`) creates each destination file at its **final size** via
`os.OpenFile(O_CREATE|O_TRUNC)` then `Truncate(meta.Size)` **before streaming its content**, so a
file can be counted as "present" while it's still mid-write — a race a final code review identified
statically and that a count-based check cannot distinguish from genuine completion. Polling the
*checksum comparison itself* instead means an in-progress restore just fails one poll iteration
(wrong hash, since the file isn't fully written yet) and retries on the next interval — it can never
produce a false completion signal, and it collapses "wait for completion" and "verify correctness"
into one assertion instead of two.

### 5. Cleanup

In the `finally` block: delete the restore policy via the API, then `rm -rf` the destination
directory only — the fixture source directory and its backup are permanent, seeded once, never
deleted by this test.

## Error Handling

- Fixture not yet backed up (cold demo stack): `waitForCatalogEntryCount` throws with a clear
  found-vs-expected message after 6 minutes.
- Restore not completing correctly within budget: `expect.poll`'s own timeout/mismatch error is
  diagnostic (shows exactly which relative paths/hashes still differ).
- Any assertion failure after policy creation: the `finally` block still deletes the restore policy
  and removes the destination directory.

## Testing

This spec *is* a test. Validated live, repeatedly, against both an already-running stack and a
genuinely fresh `make demo-down -v && make demo-up` rebuild — including confirming the full 4-spec
suite (`smoke`, `restore-cart`, `restore-verify`, `restore-content`) passes cleanly together on a
settled fresh stack (one incidental finding: the very first test run immediately after `make demo-up`
completes can be flaky for *any* of these specs, including ones this design never touched, while
`agent`'s first reconcile/bootstrap cycles are still settling — not something this design needs to
fix, and not something it introduces).

## Documentation Impact

None beyond this spec and its implementation plan. This adds test coverage and demo-seed fixture data
for already-shipped, already-documented `rwfs restore` behavior — no protocol, component, or
architecture change.
