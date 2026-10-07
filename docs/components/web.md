# web

A small browser UI over [api-server](./api-server.md)'s REST API — lists enrolled clients,
browses catalog entries, manages backup policies and storage policies (list/create/edit/delete for
each, in separate sections), and browses fleet-wide jobs and their logs. **Not a mesh member:**
unlike every other control-plane component, `web` has no mTLS identity of its own; it's a static Vue
single-page app served by nginx, which reverse-proxies `/api/*` to `api-server` so the browser's
calls stay same-origin (no CORS changes were needed on `api-server`).

## Usage

On first load, the app prompts for `api-server`'s bearer token and stores it in the browser's
`localStorage`; every request thereafter carries `Authorization: Bearer <token>`. No token means
no data — there's no read-only "guest" mode.

## Pages

- `/` — placeholder landing page
- `/clients` — every enrolled client (hostname, revoked, last seen), with client-side search/sort
  via `vue-good-table-next`, linking to:
- `/clients/new` — enroll a new client (hostname + optional SANs); shows the resulting one-time
  enrollment token on the new client's detail page after redirecting
- `/clients/:hostname` — one client's full record (SANs, attributes, descriptions), with actions to
  revoke/unrevoke, re-enroll (shows a fresh one-time token), and inline add/remove editing of
  description, attributes, and SANs, each gated by its own "Update" button that enables only once
  that section has a pending change
- `/catalog` — the catalog browsed like a file manager by default: a root listing (or, for
  Windows-sourced entries, a list of drives) that a user drills into by clicking a folder row, with a
  `DirectoryPathBar` breadcrumb ("Home / ... / current folder", each ancestor but the last clickable)
  above the table tracking the current location. A folder can hold both subfolders and files at once,
  so each level's table shows folder rows (name, direct file count, last-seen) above that folder's own
  file rows (existing columns from `groupEntriesByFile`, but "Path" renders just `short_filename` — the
  path bar already gives location, so the full path would be redundant). Per-column sorting is disabled
  while browsing so folder rows stay pinned above file rows (folders alphabetical by name, files newest
  first); it re-enables in pattern-search mode below, since that view is flat.

  Filters sit above the table as a three-row bar: a date-range row (received time, default last 7
  days, backed by `@vuepic/vue-datepicker`, a new dependency), a clients/job-policy row (each opening a
  searchable, checkbox-selectable list scoped to the other active filters — picking a client narrows
  the policy list and vice versa), and a "Path contains…" free-text row. Date/client/job filters apply
  in both modes and refetch automatically as they change (debounced where relevant) — there's no Search
  button or gating requirement. Typing in "Path contains…" exits directory browsing into a separate
  flat, ungrouped, cross-directory search mode — the path bar and folder rows disappear, and every
  matching page is fetched (the catalog API is cursor-paginated) before entries are grouped into one
  row per distinct file (source host + path) and handed to a client-side sortable/paginated table
  (`vue-good-table-next`) — grouping over the complete result set means a file's versions are never
  split across a page boundary. Clearing the pattern restores whichever folder was last being browsed
  (or root, if none). Sizes render human-readable (KB/MB/...); the "Captured" column on every row
  (folder or file) is a button that opens `VersionsModal` — a real version picker, not just a
  read-only list: it fetches every version of that exact path (newest first, ignoring the active
  date filter so the user can reach further back than what's currently browsed), and "Restore this
  version" on any row pins the restore cart's selection to that version's exact timestamp. The
  button's own label reflects an already-pinned version for that row's exact `(source_host, path)`
  when the restore cart has one, falling back to the representative/last-seen default otherwise, the
  same per-row cart lookup the selection checkbox already does.

  **Damaged badges.** Catalog entries carry a boolean `damaged` (replicated from the store as a
  snapshot, so it can lag by about a minute; see
  `docs/superpowers/specs/2026-10-08-catalog-damage-replication-design.md`). A red `Damaged` badge
  (tooltip: "Backup data for this version is damaged; restore may fail.") appears on a catalog file
  row when its latest version is damaged (`groupEntriesByFile` exposes this as `group.damaged`), on
  each damaged row of the versions modal, and on file rows of the restore cart (`restoreCart` keeps a
  display-only `damaged` flag on file rules, refreshed when a version is pinned or reset: "Use latest" takes
  the damaged state of the versions modal's newest version, falling back to not damaged when the
  modal has no versions; a missing flag counts as not damaged). The confirm modal adds an amber line with the number of damaged
  selected files. These are warnings only: "Restore this version" and Restore stay enabled, and folder
  rules never show the badge or count because `rwfs` resolves them at restore time. There is no
  Playwright test: the demo lab cannot create real damage. A folder's
  versions can span multiple source hosts (each host's own capture of that path is a separate row);
  the modal calls this out with a note when it detects more than one, since picking a version in that
  case scopes the selection down to just that host's capture. Each row (folder or file) also carries
  a checkbox for staging it into the restore cart (`stores/restoreCart.js`): checking a file adds it
  by `(source_host, path)`; checking a folder adds one host-agnostic wildcard rule covering
  everything under it, rather than one entry per file, so a large folder selection stays a single
  rule. Selection state is *resolved* from this small rule list on demand (longest-matching-path
  wins, like `.gitignore`), which is also what lets a user drill into an already-selected folder and
  see its contents pre-checked, then uncheck individual items to carve out exceptions — unchecking
  shows as a partial/indeterminate checkbox on any ancestor folder row. Checking a box (or confirming
  a version from the modal) defaults the selection's version window to the catalog's *currently
  active date filter* (previously an unbounded true-latest) — "latest" therefore means latest within
  whatever range is currently browsed, not latest ever. The cart is in-memory only (no persistence
  yet); submission itself happens from `/restore` below.
- `/restore` — a flat table, one row per cart selection, listing source host, source path (folder
  selections shown as `path/*`), the version captured (see below), a destination path, size (file
  rows only), and a live status column. Deliberately no storage-host grouping or column: which
  physical store a file happens to live on is an implementation detail the cart never surfaces, here
  or anywhere else in this view. The "Captured" column shows a formatted timestamp for a selection
  pinned to a specific version; for an unpinned one it shows the bare word "Latest" only when the
  window is genuinely unbounded (an explicit "Use latest" reset), and otherwise the captured filter
  window's upper-bound date, since an unpinned default is still a real `[notBefore, notAfter]` range
  rather than an unqualified "latest ever" (via the catalog's or this page's own version picker,
  `VersionsModal` — clicking the cell reopens it, and "Use latest" resets the pin back to the
  catalog's active date-filter window at pick time). The destination path defaults to the
  source path; clicking it swaps in a text input (`restoreCart.setDestPath`) to rename that
  selection's restore target, whether a file or a folder -- purely client-side data at this point,
  sent as `dest_path` on the submitted rule only when it differs from the source path (see
  [Design: Restore Destination Rename](../superpowers/specs/2026-08-13-restore-destination-rename-design.md));
  `rwfs restore` now reads it back out and logs it as each resolved file's renamed destination
  path, but nothing writes it to disk yet (see `docs/components/rwfs.md`'s `## restore` section).
  Each row also has a Remove button that unstages it (toggles the same rule back off, via
  `restoreCart.removeEntry`).

  Picking a destination host (from the enrolled-client list, `useClientsStore`) and clicking
  **Verify** submits immediately, but **Restore** now opens `RestoreConfirmModal` first — a summary
  ("You're about to restore N items (size) to `<host>`", plus callouts when overwrite is on or any
  items are pinned to an older version, or an amber line when some selected files are known
  damaged) that must be confirmed (or cancelled) before anything is
  submitted, so a destructive restore is never one accidental click away. Confirming (or clicking
  Verify directly) resolves the cart's rules into concrete catalog entries (`GET /catalog`),
  collapses those to one entry per distinct file (the catalog returns one row per *version*, so a
  nightly-backed-up file is many rows — only its latest version's row within the pinned/filtered
  window is kept), groups them by the physical `store_host` each file is actually stored on, resolves
  each group's dial address from a matching `"storage"` policy's checked-in hostname + port, and
  creates one `"restore"` policy per group (`POST /restore`) — so a selection spanning files backed
  up to more than one storage destination becomes multiple policies, each scoped to just the files
  that live there. Rather than a flat results list, each cart row now tracks its own submission
  status (`stores/restoreSubmission.js`'s `entryStatus`, keyed per entry — an array, since one folder
  entry can fan out to more than one store's policy) and renders it as a badge in the row's Status
  column: "submitting…" while in flight, then a link to the resulting job (`/jobs/:job_id`,
  live-updating via the jobs store) labeled with that job's current state once a policy is created,
  or an inline error message for a group that failed (e.g. no storage policy found for its store) —
  one group failing doesn't block the others, and a row's status persists even after the cart is
  emptied. A failure of the whole submission (the catalog fetch or the `"storage"` policy lookup
  itself) is still reported as a single submission-level error. Verify and Restore succeed
  identically at the submission layer: either `mode` creates a real `"restore"`-typed policy and
  `api-server` returns `201` (see
  `docs/superpowers/specs/2026-08-09-restore-policy-type-design.md`) — `api-server` no longer
  rejects `mode: "restore"`. A separate "Overwrite existing files" checkbox (unchecked by default)
  is sent as `overwrite` on every submission, alongside `mode` (`verify` for the Verify button,
  `restore` for the Restore button, both threaded through the confirmation summary above). The two
  modes diverge once `agent` picks up the resulting policy: a `verify` policy runs `rwfs verify` as
  before, while a `restore` policy runs the new `rwfs restore` subcommand (task/job-ID prefix
  `restore:<policy-name>`, with `--overwrite` appended when the policy's `overwrite` field is true)
  — this round, `rwfs restore` only resolves the policy's rules against the live store and logs each
  file's source path and its `dest_path`-renamed destination path, writing nothing to disk and
  calling no restore-execution RPC (see `docs/components/rwfs.md`'s `## restore` section and
  [Design: Restore Execute, Log-Only](../superpowers/specs/2026-08-16-restore-execute-log-only-design.md),
  which supersedes the 501-rejection split originally described in
  [Design: Restore Verify/Execute Split](../superpowers/specs/2026-08-14-restore-verify-execute-split-design.md)).
  The sidebar's Restore link still highlights whenever the cart is non-empty.
- `/policies` — every policy (name, RPO, destination), with a "New backup" action opening a form modal for creating new policies (fields: name, RPO, backup window, client filters, object filters (each filter's include/exclude glob patterns entered as individual chips via a reusable `TagInput` component (`components/ui/TagInput.vue`) — each pattern is validated client-side for glob syntax and checked against the rest of its own list for parent/child path overlap, e.g. `/var/log` and `/var/log/app` in the same list, before Save is allowed), destination (a required select over `/storage`'s storage policies, replacing free-text host:port entry)) and clickable policy names navigating to each policy's detail view. The modal (`BackupPolicyFormModal` in `components/backup_policies/`) offers two primary actions: "Save" to persist a new or edited policy, or "Run now" to execute the policy's filters immediately as a one-time ad-hoc backup job (the ad-hoc policy auto-sets its `disabled_at` to expire after its configured timeout, 1h by default) and redirects to `/jobs`, where the resulting job(s) can be found and opened for their log lines — same modal-plus-detail-page pattern as `/storage` below. Linking to:
- `/policies/:id` — one policy's full record, in two tabs built on a reusable `Tabs` component
  (`components/ui/Tabs.vue`, active tab synced to `?tab=details`/`?tab=checkins` so either can be
  linked directly): `Details` (the default — client filters, object filters, backup window) and
  `Status` (`components/storage/StorageStatus.vue` — one card per reporting storage node: online/stale/offline badge, disk usage bar amber above 85% and red above 95%, active connections, in-progress jobs, uptime, last report; polled every 30s) and `Check-ins` (`components/policies/PolicyCheckins.vue` — every host that has received this policy
  from `policy-server`, each with its most recent check-in time, and a manual Refresh button that
  re-fetches the policy). Edit and Delete buttons sit at the page level, outside the tabs; Edit opens
  `BackupPolicyFormModal` pre-filled with the policy's current values (both "Save" and "Run now" are
  available here). No separate `/policies/new` or `/policies/:id/edit` routes.
- `/retention` — retention rules in evaluation order: position, name, which clients it applies to,
  backup type, path (plus any file-name patterns), and how long to keep (`N days` / `Forever`), with
  a fixed last row explaining the built-in default (7 days unless a node's `RetentionDefaultDays`
  says otherwise). Rules are checked top to bottom and the first match decides retention; reorder by
  dragging a row or with its up/down arrows, which sends the complete ordered id list to
  `POST /retention-policies/reorder` — the list updates immediately and reverts (refetching, since the
  rejection usually means another operator changed the set) with the error shown if the server
  refuses. "New Retention Rule" and each row's Edit open `RetentionFormModal` (name, hostnames,
  labels, backup type, path, optional file-name globs, keep in days or "Keep forever"; validation
  mirrors `policy-server`'s — absolute path, no `..`, no `/` in a glob, whole days >= 1) and Delete
  asks for confirmation. Its store is `stores/retentionPolicies.js`; form/display helpers live in
  `utils/retentionRule.js`. See [Design: Retention Policies](../superpowers/specs/2026-10-05-retention-policies-design.md).
- `/storage` — every storage policy (name, target hostname, port, storage type), with a "New Storage
  Policy" action opening `StorageEditModal` (fields: name, target hostname, port, storage type —
  `filesystem` only today — and, when `filesystem` is selected, a filesystem path) and clickable
  policy names navigating to each storage policy's detail view. "Target hostname" submits as
  `client_filters.hostnames` — the same targeting mechanism `/policies` uses, not a separate field.
  Its store (`stores/storagePolicies.js`) is no longer read exclusively by `/storage`: `/policies`'
  form modal also reads it to populate its destination select (see the `/policies` bullet above).
  `/policies` itself still requests only `type=backup` policies, so a storage policy never appears
  in its list. Linking to:
- `/storage/:id` — one storage policy's full record (target hostname, port, storage type, path), in
  the same `Details`/`Check-ins` tab layout as `/policies/:id` above, with Edit (opens
  `StorageEditModal`) and Delete buttons. Editing has moved here from the list — `/storage`'s name
  column now navigates instead of opening the modal directly. Both policy detail pages share their
  component folder for this (`components/policies/`); `/storage` otherwise keeps its own
  (`components/storage/`).
- `/jobs` — every job across the fleet from the last 24h (job ID, kind, source host, store host,
  started/finished time, state), with client-side search, sort, and pagination via
  `vue-good-table-next` (also used on `/catalog`, `/clients`, and `/policies`), linking to:
- `/jobs/:job_id` — one job's log lines from the last 24h (for a `cleanup:` / `vacuum:` job — `bwfs`'s
  scheduled store maintenance — a banner above them summarizes the run from its finish line: versions
  deleted and deletion-log entries pruned, or chunks/file data removed, segments removed/compacted and bytes reclaimed, plus the
  duration, "Dry run" for a cleanup that deleted nothing, or the error of a failed run;
  restore/verify jobs have their own banner); each line is parsed from its underlying
  JSON via `LogLine.vue` into a level-colored `[LEVEL] time binary@hostname: message` summary, with
  the remaining fields (`job_id`, `event`, `status`, etc.) collapsed behind a click — a line that
  isn't valid JSON falls back to plain text

  Both `/jobs` and `/jobs/:job_id` now update live: each connects over WebSocket (via a ticketed
  `wsClient.createLiveStream`, `web/src/utils/wsClient.js`) to `api-server`'s `GET
  /api/v1/jobs/stream` / `GET /api/v1/jobs/{job_id}/logs/stream`, laid on top of the same REST fetch
  as before rather than replacing it — the initial page load still comes from `GET /api/v1/jobs` /
  `GET /api/v1/jobs/{job_id}/logs`, and a 60s periodic re-fetch of the same REST endpoint keeps
  running underneath the WS stream regardless of its health, as a correctness backstop. A
  `ConnectionStatus` indicator in the page header shows the stream's state — `live`,
  `reconnecting`, `polling`, or (on `/jobs/:job_id`, once a job's finish line has been seen)
  `finished`. After 5 failed reconnect attempts (jittered exponential backoff, capped at 8s) a page
  gives up on the socket and falls back to plain 10s REST polling instead — a stalled page is never
  left looking up to date. See
  [Design: Live Job & Log Updates](../superpowers/specs/2026-08-17-live-job-updates-design.md).

  `/jobs/:job_id`'s log view now caps itself at 2000 resident lines while the user is following the
  live tail (auto-scrolled to the bottom); scrolling away from the bottom pauses that cap so
  history being read isn't evicted out from under the reader, and returning to the bottom resumes
  it. A "Load older lines" button (visible whenever the backend reports more history exists) pages
  further history in via `GET /jobs/{job_id}/logs`'s new `ending_before` cursor — it disables itself
  while a page is in flight (so a double-click can't race two prepends) and shows any failure as an
  inline notice beside the button, leaving the already-visible lines on screen. A "N new lines —
  jump to latest" button appears instead of auto-scrolling once the user has scrolled away from the
  bottom; its count tracks live tail lines only, so paging older history in never registers as new
  activity. See
  [Design: Job Log Pagination & Bounded Retention](../superpowers/specs/2026-08-22-job-log-pagination-design.md).

  For a restore or verify job specifically (`job_id` prefixed `restore:`/`verify:`), the page now
  shows a human-readable outcome banner above the log itself, instead of leaving the raw log tail as
  the only way to tell what happened: "in progress" until `agent`'s own `event=finish` line (the
  authoritative terminal status) appears, then either a failure notice or a success line built from
  `rwfs`'s own summary log line — file/byte counts written and skipped for a restore, or
  verified/warning counts for a verify — falling back to a bare "complete" if that summary line
  hasn't landed yet (e.g. a job that predates this feature).

Every list and detail page's header now shows a breadcrumb trail (e.g. "Policies / nightly-db-backup") above the
title via `PageHeader`'s `crumbs` prop, and the sidebar (`Sidebar.vue`) carries a small brand mark
plus one icon per section (`components/icons/`, hand-authored inline SVG — no icon package
dependency). Boolean/state table columns (a client's Revoked column, a job's State column) render
as a colored pill via the new `Badge` component (`components/ui/Badge.vue`) instead of plain text.

## Local development

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run dev
```

The dev server proxies `/api` to `http://localhost:8090` — run `api-server` locally (or via
`make control-plane-up`) alongside it.

## End-to-end tests

`web/e2e/` holds a Playwright suite covering the restore cart's selection scenarios (file select,
folder-wildcard select with drill-down pre-checking, a nested exception, full deselection), run
against the real, already-running demo lab rather than mocked data — see
[Design: restore cart e2e tests](../superpowers/specs/2026-08-09-restore-cart-e2e-design.md) for why.
Seeding is itself UI-driven: the suite creates and runs a fast ad-hoc backup policy through the real
`/policies` form before asserting against the resulting catalog data.

One-time setup (host Node, not the Docker-based flow used for `dev`/`build` above — Playwright
needs a real browser binary, which isn't practical inside the `node:20-alpine` image used
elsewhere in this doc):

```bash
cd web && npm install
npx playwright install --with-deps chromium
```

```bash
make demo-up          # precondition, not managed by the suite itself
cd web && npx playwright test
```

## Deployment

Ships as the `web` service in `demo/docker-compose.yml`, published at `http://localhost:8091`. Not
yet wired into `deploy/control-plane/`.

## Building

```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run build
```

## See Also

- [api-server](./api-server.md) — the backend this UI is a client of
- [REST API v1](../api/rest-v1.md)
- [Design: web frontend](../superpowers/specs/2026-07-18-web-frontend-design.md)
- [Design: web frontend consistency & best-practices refresh](../superpowers/specs/2026-07-20-web-frontend-refresh-design.md)
- [Design: catalog directory-browsing UI](../superpowers/specs/2026-08-08-catalog-directory-browsing-design.md)
- [Design: restore cart](../superpowers/specs/2026-08-09-restore-cart-design.md)
- [Design: restore cart submission](../superpowers/specs/2026-08-10-restore-cart-submission-design.md)
- [Design: Restore Verify/Execute Split](../superpowers/specs/2026-08-14-restore-verify-execute-split-design.md)
- [Design: Live Job & Log Updates](../superpowers/specs/2026-08-17-live-job-updates-design.md)
- [Design: Job Log Pagination & Bounded Retention](../superpowers/specs/2026-08-22-job-log-pagination-design.md)
- [Design: restore workflow UI clarity](../superpowers/specs/2026-08-27-restore-ui-clarity-design.md)
- [Architecture](../ARCHITECTURE.md)
