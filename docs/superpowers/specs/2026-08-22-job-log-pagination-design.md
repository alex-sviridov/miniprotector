# Design: Job Log Pagination & Bounded Retention

> **Amends:** [Design: Live Job & Log Updates](2026-08-17-live-job-updates-design.md) (`GET
> /jobs/{job_id}/logs`'s REST shape, `jobs.js`'s merge/dedup logic, `JobDetailView.vue`'s rendering)
> and [REST API v1](../../api/rest-v1.md) (`GET /jobs/{job_id}/logs`'s response shape). No new
> component, no schema change to any other endpoint.

## Problem

`web/src/views/JobDetailView.vue` renders every line in `jobs.js`'s `logs` array with no cap, keyed
by array index. `jobs.js`'s `_mergeLogLine` — called once per incoming WebSocket line — does
`this.logs.push(line); this.logs.sort(...)`: a full re-sort of the entire array on every single
line. For a long-running or verbose job this is:

- **Unbounded memory**: `logs` and the dedup Set `_logsSeen` both grow for as long as the job runs
  and the page stays open.
- **Unbounded DOM**: one live `LogLine` component instance per line, forever.
- **Growing per-line cost**: the sort is `O(n log n)` against the *whole* array on every single
  incoming line, so total cost over a job's lifetime is worse than linear in line count.

Separately, `GET /api/v1/jobs/{job_id}/logs` has no `limit`/pagination of its own — it fetches up to
a fixed, undocumented `jobsQueryLineLimit` (5000) Loki lines and returns all of them, with no
`has_more` signal (unlike `GET /jobs`, which does report `truncated`). Because Loki's `query_range`
defaults to `direction=backward`, a job exceeding that cap today silently loses its *earliest* lines
with no indication — a pre-existing correctness gap this design also closes.

## Goals

- `JobDetailView.vue` never holds more than a bounded number of live-followed lines in memory or the
  DOM, regardless of how verbose or long-running a job is.
- A user can still see a job's full history on demand, paged in from the backend rather than
  fetched/held all at once.
- Per-line merge cost stays cheap (no full-array re-sort per incoming line).
- `GET /jobs/{job_id}/logs` gets an honest, documented pagination contract, closing the silent-
  truncation gap above.

## Non-Goals

- No change to `JobsListView.vue`, `GET /jobs`, or `GET /jobs/stream` — the fleet job list is already
  bounded (`limit`, max 500) and snapshot/upsert-based; it doesn't have this problem.
- No DOM virtualization library. The live-follow cap (below) already bounds rendered node count to a
  fixed ceiling; if that ceiling later proves too high for smooth scrolling, that's a follow-up, not
  part of this pass.
- No cap on how much history a user can manually page back through in one sitting via repeated "Load
  older" clicks. That growth is bounded by deliberate user action, not automatic — a materially
  different risk than the current unbounded background growth this design fixes. Worth revisiting
  only if it proves to be a real problem in practice.
- No change to `jobsQueryLineLimit`'s role as the hard per-query Loki safety cap, or to Loki
  retention.

## Architecture

### Backend: `GET /api/v1/jobs/{job_id}/logs`

New query parameters, following the bidirectional-cursor convention `rest-v1.md` already documents
(`starting_after`/`ending_before`, Stripe-style):

| Param | Type | Description |
|-------|------|--------------|
| `limit` | int | Page size. Default and max: 500 (matches `GET /jobs`'s existing max limit). `400` if outside `[1, 500]`. |
| `ending_before` | unix nanoseconds | Opaque cursor — the timestamp of the oldest line already loaded. Returns the `limit` lines immediately before it. Omit for the most recent page. |

Behavior change: **without `ending_before`, the endpoint now returns the most recent `limit` lines
in the `since`/`until` window**, not "everything since 24h ago." This falls out of Loki's own
`query_range` default (`direction=backward`) once the handler passes the request's `limit` through
instead of the fixed `jobsQueryLineLimit`; the handler still re-sorts the returned page ascending
before responding, same as today. With `ending_before` set, `end` is derived from the cursor
(implementation must exclude the cursor line itself — verify Loki's exact start/end inclusivity
during implementation and adjust by 1ns if needed) and `start` stays floored at `since`, so paging
backward can't run past the existing window.

Response gains `"has_more": bool` — true when the page returned exactly `limit` lines (same
heuristic `queryEvent`'s existing `truncated` flag already uses), alongside the existing `"data"`
array. `since`/`until` keep their current meaning (the outer window `ending_before` paging can't
cross); no change to `source_host`/`store_host` filtering.

`cachingLokiClient`'s cache key already includes `query|start|end|limit` — an `ending_before`-shifted
`end` or a different `limit` naturally produces a distinct cache entry, so no change needed there.

### Frontend store (`web/src/stores/jobs.js`)

- **Cheap merge, no full re-sort.** `_mergeLogLine` appends in the common case (new line's timestamp
  ≥ the current last line's) in O(1); an out-of-order line (rare — different-host clock skew) gets a
  binary-search insert instead of a full re-sort.
- **One FIFO cap, gated on follow state.** A live line appended while the view is "following" (see
  below) and the resident count exceeds `JOB_LOGS_LIVE_CAP` (2000) drops the oldest line. Appending
  while *not* following (user has scrolled up into history) never evicts — browsing loaded history is
  never yanked out from under the user. Once they return to following, normal live growth resumes
  trimming from the front, which by then is whatever's oldest (manually-paged-in history included) —
  self-correcting without a separate "protect paged-in history" special case.
- **`_logsSeen` evicted in lockstep with `logs`.** Every line dropped from the front of `logs` has its
  key removed from `_logsSeen` in the same step, so the dedup Set stays bounded together with the
  array instead of growing forever.
- **New `loadOlder(jobId)` action**: calls `GET /jobs/{id}/logs` with `ending_before` set to the
  current oldest resident line's timestamp and `limit=JOB_LOGS_PAGE_SIZE` (500), prepends the
  (deduped) result, and updates `hasOlderLogs` from the response's `has_more`.
- **Reconcile backstop simplifies.** `_reconcileLogs`'s 60s periodic refetch switches from re-fetching
  the whole `since` window to fetching just the latest page (`limit=JOB_LOGS_PAGE_SIZE`, no
  `ending_before`) and merging — cheaper per poll, and consistent with the endpoint's new default.
- **Initial fetch** (`fetchLogs`) also switches to fetching the latest page rather than the whole
  window — for a job that's been running a long time before the page opens, this shows its current
  tail immediately instead of its oldest lines.

### Frontend view (`JobDetailView.vue` / `LogLine.vue`)

- **Key by `logKey` (timestamp|hostname|binary), not array index.** Once the store mutates `logs` via
  shift/splice, index-keying would let Vue reattach a `LogLine`'s local `expanded` state to whatever
  line now occupies that index — a real correctness bug once eviction ships, not just a style nit.
- **"Load older" affordance** at the top of the log list, calling `loadOlder`, shown only while
  `hasOlderLogs` is true.
- **A small `useAutoFollow` composable** (new, `web/src/composables/`) owns "is the view pinned to the
  bottom" state: tracks scroll position against a bottom sentinel, exposes `isFollowing` and a
  `scrollToBottom()`. `JobDetailView.vue` uses `isFollowing` both to drive the store's eviction gate
  and to show a "N new lines — jump to latest" affordance instead of auto-scrolling out from under a
  user who's deliberately scrolled up. Single-responsibility, unit-testable without mounting the full
  view — matches the existing pattern of extracting shared behavior (`wsClient.js`,
  `stores/helpers.js`) into small, focused modules rather than growing view components.

## Error Handling

- `400` on `limit` outside `[1, 500]` — mirrors `GET /jobs`'s existing `limit` validation.
- `ending_before` that isn't a valid integer — `400`, same style as the existing `since`/`until`
  parsing.
- An `ending_before` older than the job's actual earliest line simply returns an empty page with
  `has_more: false` — not an error; the frontend hides "Load older" once that's seen.
- No change to the existing `502` (Loki query failure) behavior.

## Testing

- Unit, `api-server`: `limit`/`ending_before` validation; a page without `ending_before` returns the
  most recent `limit` lines (backward-direction default); a page with `ending_before` returns the
  `limit` lines immediately before it, excluding the cursor line itself; `has_more` true/false at the
  boundary; `since`/`until` still bound how far `ending_before` paging can go.
- Unit, frontend (`jobs.spec.js`): append-in-order stays O(1) (no full-array sort call observed);
  out-of-order insert lands in the right position; eviction only fires while following; `_logsSeen`
  shrinks in step with `logs`; `loadOlder` prepends and dedupes correctly against lines already
  resident.
- Unit, frontend (`useAutoFollow`): scroll-to-bottom detection, `isFollowing` transitions, "new
  lines" counting while not following.
- Component, `JobDetailView.spec.js`: keys by `logKey` not index (assert state doesn't migrate across
  an eviction); "Load older" hidden once `hasOlderLogs` is false.
- No new e2e case planned — the existing live-job-updates e2e coverage (`web/e2e/live-job-updates.spec.js`)
  continues to exercise the live-tail path end-to-end; pagination is adequately covered at the unit/
  component level given it's triggered by explicit user action, not the default path a demo job hits.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change rule (behavior change to an existing endpoint, no new
component):

- **`docs/api/rest-v1.md`** — `GET /jobs/{job_id}/logs` section: document `limit`, `ending_before`,
  `has_more`, and the changed no-cursor default (most recent page, not whole window).
- **`docs/components/api-server.md`** — note the pagination params if this endpoint is described
  there beyond the REST reference.
- **`docs/components/web.md`** — `/jobs/:job_id` bullet: describe the live-follow cap, "Load older,"
  and the jump-to-latest affordance, replacing the current "no cap" implication.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.
