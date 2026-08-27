# Restore Workflow UI Clarity — Design

## Problem

The restore workflow (`web/src/views/CatalogView.vue` → `RestoreView.vue` → `restoreSubmission.js`
→ policy-server → agent/rwfs → Jobs UI) works end to end, but the UI around it is confusing at
every step:

- Nothing shown while browsing the catalog indicates what's already selected — a user has to
  navigate away to `/restore` to see their own cart.
- The cart table silently spans multiple storage hosts. Submitting fans out into one restore
  policy per storage host behind the scenes, with zero indication that's about to happen or that
  a per-store submission can fail independently of the others.
- `destPath` editing is "click the blue text" with no visible affordance. Verify/Restore and the
  Overwrite checkbox carry no explanation of what they actually do.
- There's no confirmation step before a destructive, overwrite-capable Restore fires.
- After submitting, results are a flat, unlinked text list with an opaque generated policy name
  (`restore-<ISO timestamp>-<storeHost>`) — even though the API response already carries a
  `job_id` (`RestorePolicy.JobID`, `src/cmd/policy-server/restore_policy.go:73`) that could link
  straight to `/jobs/:job_id`, nothing does.
- `JobDetailView.vue` is a raw structured-log tail. Fine for a developer, but there's no
  human-readable "12 of 15 files restored, 3 failed" summary for a restore/verify job.
- The protocol already supports selecting a specific historical version per rule
  (`RestoreRule.not_before`/`not_after`, `src/api/policyserver.proto:98-105`; resolution picks
  "the latest version whose backup date falls inside the window, never a fallback outside it"),
  but the web UI never sets these fields — every restore today implicitly means "the single
  latest version, unbounded history." `VersionsModal.vue` lists a file's version history but is
  read-only; clicking a row does nothing.

## Goals

- Make the cart page show, before submission, everything relevant to what's about to happen:
  what's selected, roughly how much, where it's going, and what Verify/Restore/Overwrite actually
  do — without exposing which storage host anything lives on (an implementation detail the user
  should never have to reason about).
- Add a confirmation step before a destructive Restore.
- Connect a submitted restore to its live job status directly, inline, per selected item — no
  separate unlinked results list.
- Give a restore/verify job a human-readable outcome summary, not just a raw log tail.
- Let a user pick a specific historical version of a file *or a folder* to restore, using one
  consistent interaction for both — and make the default selection "the latest version within
  whatever date range the catalog is currently filtered to," not an unbounded global latest.

## Non-Goals

- Anything on the reliability side: retry semantics, `destinations[0]`-only fallback, etc. (a
  separate, later design — this pass is UI-only).
- Any change to the wire protocol, `RestoreRule`/`RestoreFileFilter` messages, or `rwfs`/`bwfs`
  resolution logic. `not_before`/`not_after` already exist and already do exactly what's needed;
  this design only starts populating them from the UI.
- Any new backend REST endpoint. Folder version history turns out to be fetchable through the
  existing `/catalog` search surface (see "Folder version history" below) — if that assumption
  doesn't hold once implementation starts, that's a scope change to flag, not something to design
  around speculatively here.
- Restructuring `JobsListView`'s generic columns (e.g., the "Source Host" label meaning something
  different for a restore job than a backup job) — out of scope for this pass; only
  `JobDetailView`'s per-kind summary banner is in scope.

## Design

### 1. Cart page (`RestoreView.vue`)

No grouping by storage host — the table stays a single flat list, ordered however the cart already
orders it. Above the Verify/Restore buttons, a plain summary line: "12 files selected, 4.2 GB."
`destPath` editing gets a visible pencil-icon button in place of "click the blue text." Verify and
Restore each get a one-line explanation directly under their button ("Restore writes files to the
destination." / "Verify checks integrity only — writes nothing."), and the Overwrite checkbox gets
inline helper text: "Replaces files that already exist at the destination. Unchecked: existing
files are left alone and skipped" (matches `writeRestoreFile`'s actual behavior,
`src/cmd/rwfs/restorefile.go:61-78`: `os.Stat` hit + `!overwrite` → skip, not an error).

When a destination host is chosen, look up its `last_seen_at` (already returned by `GET /clients`,
`src/cmd/api-server/clients.go:13` — same field `ClientDetailView.vue` already displays) and show
an inline warning if it's stale: "This host hasn't checked in recently — the restore will wait
until it comes online." Threshold left for implementation (something on the order of the client's
own expected check-in interval).

### 2. Confirmation modal before Restore

Clicking Restore (not Verify — it's non-destructive) opens a modal, structurally like the existing
`VersionsModal.vue`: total file count, total size, destination host, and the Overwrite state called
out explicitly when on ("Existing files at the destination will be overwritten."). If any selected
item is pinned to a non-latest version (see §5), the modal calls that out too ("2 items pinned to
an older version"). Confirm submits exactly as today; Cancel closes with no request sent.

### 3. Per-row live submission status

`restoreSubmission.js` already computes, internally, which cart entries map to which per-store
policy request (`buildRulesByStore`) before discarding that mapping and reducing everything down to
a `results` list keyed by `storeHost`. Instead: keep the entry-to-request mapping, and once each
`restorePolicies.create()` call resolves, record its outcome (`job_id` on success, error message on
failure) against every cart entry that request covered.

`RestoreView.vue`'s existing table gains a Status column reflecting that per-entry state:

- Not yet submitted: blank.
- Submitting: a spinner.
- Submitted successfully: a live badge (reusing `JobsListView.vue`'s `stateVariant`/`Badge`
  pattern, sourced from the jobs store's existing stream) linking to `/jobs/:job_id`.
- That entry's underlying submission failed outright (e.g. "No storage policy found for X"): the
  error shown inline on the row.

No separate results list below the table. A row stays visible after submission regardless of
outcome — a success badge needs to stay on screen long enough to actually be seen and clicked, so
nothing auto-removes it. Instead, an entry that already carries a successful outcome is simply
excluded from being resubmitted on a later Verify/Restore click (a failed one remains fully
eligible for retry). Manually clearing a completed row out of the cart is what the existing Remove
button is for.

This still never surfaces which storage host anything went through — the per-row status derives
from whichever request ended up covering that entry, but the entry itself never displays a store
host.

### 4. Job detail summary banner (`JobDetailView.vue`)

`rwfs` already logs one final structured line per run: `"restore complete"` with
`files_written`/`bytes_written`/`skipped` (`src/cmd/rwfs/restore.go:253`), or `"summary"` with
`verified`/`warnings` (`src/cmd/rwfs/verify.go:249`) — both already reach the web UI through the
existing log stream. For a `job_id` starting with `restore:`/`verify:`, parse that line client-side
and render a small banner above the raw log:

- In progress (no finish line yet): "⏳ Restore in progress."
- Success: "✅ Restore complete — 12 files written (4.1 GB), 0 skipped." (or the verify equivalent)
- Failure: "❌ Restore failed — see log below."

The raw log view underneath is unchanged — this is a banner on top, not a replacement.

### 5. Unified version picking, defaulted to the current catalog filter window

**Default on select.** `catalog.filters.receivedAfter`/`receivedBefore` (the existing
`DateRangePanel` browsing filter) is already sent to `/catalog` search
(`catalog.js:buildQuery`), so `groupEntriesByFile`'s `representative` (`versions[0]` after sorting
desc by `store_created_at`) is already, today, "the latest version within the current filter
window" — the entries it's computed from are already scoped to that window server-side. Checking a
box captures that same window as the new rule's `notBefore`/`notAfter` at toggle time (the same
"capture at toggle time" pattern `toggleFile`/`toggleFolder` already use for `storeHost`/`size`,
`web/src/utils/restoreRules.js:97-102`), rather than leaving them unbounded. This is a real default
*behavior* change, not just cosmetic: today a checked box means "restore the single latest version
ever, ignoring the browsing filter entirely"; after this change it means "restore the latest
version within whatever window you were browsing when you checked it." See the note on
`DEFAULT_RANGE_SECONDS` under Open Questions.

Changing the catalog filter afterward does not retroactively move an already-made selection — the
window is captured once, the same way `destPath` is captured once and then only changes if
explicitly edited.

**Display/control merge into one element.** No separate "Versions" button or count. The catalog
table's "Versions" column becomes "Captured," showing the `store_created_at` of whichever version
is currently in play (the filter-default described above, or whatever was explicitly picked),
rendered as clickable text — same visual treatment as the cart's destination-path edit control —
that opens a version-picker modal (`VersionsModal.vue`, generalized to also carry a "Restore this
version" button per row and a "Use latest" reset, rather than being purely read-only). Applies to
file rows and folder rows identically. The cart table carries the same clickable-timestamp element
per selected row, so a pinned version can be seen and changed without leaving the cart.

Picking a specific version sets `notBefore = notAfter = <that version's store_created_at>` — a
single-instant window, which resolution already treats as "exactly that version" (nothing else can
be "the latest in \[t, t\]" except the version whose own timestamp is *t*). "Use latest" clears the
pin back to the filter-window default from above.

**Folder version history.** `FileVersionRecord` (`src/storage/filesystem/models.go:29-39`) already
stores directories as versioned rows (`Type: 'd'`, same `ObjectID`/`JobID`/`CreatedAt` shape as a
file), and nothing in `catalogsync` or api-server's `/catalog` search filters by type — so a
folder's own version history should be fetchable through the existing search endpoint, scoped to
its own exact path rather than its children. Today's browse-mode folder rows come from the
aggregated `directoryChildren` endpoint (`/catalog/directories/children`, child-directory summaries
only, no version list) — giving a folder row the same "Captured" treatment as a file row needs a
new frontend fetch (a `catalog.js` action querying `/catalog` for that exact path's own entries,
mirroring how `groupEntriesByFile` already groups a file's rows into a version list) rather than a
new backend endpoint. Flagged to confirm precisely at implementation time, not fully specified
here.

## Data Model Changes (frontend only)

- `web/src/utils/restoreRules.js`: a rule gains optional `notBefore`/`notAfter` (Unix seconds,
  matching the wire field names' units). `toggleFile`/`toggleFolder` accept them through the
  existing `extra` parameter, exactly like `storeHost`/`size`/`destPath` today.
- `web/src/stores/restoreCart.js`: gains a `setVersionWindow(entry, notBefore, notAfter)` action,
  mirroring the existing `setDestPath` — mutates the matching rule in place.
- `web/src/stores/restoreSubmission.js`: `toWireRule` includes `not_before`/`not_after` in the
  outgoing payload whenever set (they will be, by default, for every newly-created rule per §5);
  `submit()` retains the entry↔request mapping instead of discarding it, to drive §3's per-row
  status.
- `web/src/stores/catalog.js`: new action to fetch a single path's own version rows (folder
  support for §5).

## Error Handling

- A per-entry submission failure (§3) is scoped to that entry alone — it doesn't block or roll back
  any other entry's request, matching how `buildRulesByStore` already partitions independently per
  store today.
- A version pin (`notBefore`/`notAfter`) pointing at a version that no longer exists by the time the
  restore actually runs (e.g. the underlying chunk was cleaned up) surfaces exactly the way an
  unmatched rule already does today — `rwfs`'s existing "no version in timeframe" vs. "not found on
  this store" distinction (`src/cmd/rwfs/resolve.go:107-144`) is unchanged and already correct for
  this case.
- If the destination-host liveness check (§1) can't be determined (client fetch failed), show
  nothing rather than a false-negative warning — absence of data isn't evidence of staleness.

## Testing

- `web/src/utils/restoreRules.spec.js`: `toggleFile`/`toggleFolder` pass through
  `notBefore`/`notAfter` via `extra` unchanged from the `storeHost`/`size` precedent already
  covered.
- `web/src/stores/restoreCart.spec.js`: new `setVersionWindow` tests (sets on matching rule,
  no-op if the entry was removed — mirrors existing `setDestPath` coverage).
- `web/src/stores/restoreSubmission.spec.js`: `toWireRule` emits `not_before`/`not_after` when set;
  entry↔request mapping drives correct per-entry status on success/failure; only successful
  entries are cleared from the cart after submit.
- `web/src/views/RestoreView.spec.js`: summary line, pencil-edit control, Overwrite/Verify/Restore
  helper text, destination-liveness warning, per-row status column (submitting → success/failure →
  link), confirmation modal (opens on Restore only, blocks submission until confirmed, surfaces
  pinned-version callout).
- `web/src/views/JobDetailView.spec.js`: summary banner parses `"restore complete"`/`"summary"`
  lines into the three states (in-progress/success/failure); absent for non-restore/verify job
  kinds.
- `web/src/components/VersionsModal.spec.js`: "Restore this version"/"Use latest" actions; works
  for both a file group and a folder group.
- `web/e2e/restore-cart.spec.js` and `restore-content.spec.js`: extend to cover picking a specific
  version and confirming it's what actually gets restored (not just resolved-and-not-written, as
  today's coverage mostly checks); update any assertion that currently relies on the old "always
  restores the true latest version" default, since §5 changes that default.

## Documentation Impact

Per `.claude/CLAUDE.md`:

- `docs/components/web.md`: update the restore-cart description to describe the version-picking
  default and the per-row status flow.
- `CHANGELOG.md`: entry before merge, calling out the default-selection behavior change explicitly
  (§5) since it changes what gets restored by default, not only how it's presented.
- No `docs/protocols/*.md` changes — the wire protocol is unchanged.

## Open Questions / Follow-ups

- **`DEFAULT_RANGE_SECONDS` (7 days, `catalog.js:6`) now does double duty**: it's both the
  browsing window's default and, per §5, the default restore-selection cutoff. A user who widens
  the browsing range purely to *look* further back, then checks a box, now also changes what
  version that selection would restore. Worth revisiting once this ships and real usage shows
  whether that's actually confusing in practice — not resolved here.
- Exact staleness threshold for the destination-host liveness warning (§1) is left to
  implementation.
- The precise `/catalog` query shape for fetching a single folder's own version rows (§5) needs
  confirming against the actual api-server handler once implementation starts.
