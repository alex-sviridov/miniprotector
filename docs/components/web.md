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
  (or root, if none). Sizes render human-readable (KB/MB/...); a "Versions" count on multi-version
  files opens a modal (click anywhere on that row) listing that file's other versions. Each row (folder or file) now also carries a checkbox for staging it into the restore cart (`stores/restoreCart.js`): checking a file adds it by `(source_host, path)`; checking a folder adds one host-agnostic wildcard rule covering everything under it, rather than one entry per file, so a large folder selection stays a single rule. Selection state is *resolved* from this small rule list on demand (longest-matching-path wins, like `.gitignore`), which is also what lets a user drill into an already-selected folder and see its contents pre-checked, then uncheck individual items to carve out exceptions — unchecking shows as a partial/indeterminate checkbox on any ancestor folder row. The cart is in-memory only (no persistence yet) and UI-only: nothing is submitted for restore in this pass.
- `/restore` — placeholder list of everything currently staged in the restore cart (folder selections as `path/*`, file selections as `path (host)`); no actions yet, just a preview of what `/catalog`'s checkboxes have accumulated. The sidebar's Restore link highlights whenever the cart is non-empty.
- `/policies` — every policy (name, RPO, destination), with a "New backup" action opening a form modal for creating new policies (fields: name, RPO, backup window, client filters, object filters (each filter's include/exclude glob patterns entered as individual chips via a reusable `TagInput` component (`components/ui/TagInput.vue`) — each pattern is validated client-side for glob syntax and checked against the rest of its own list for parent/child path overlap, e.g. `/var/log` and `/var/log/app` in the same list, before Save is allowed), destination (a required select over `/storage`'s storage policies, replacing free-text host:port entry)) and clickable policy names navigating to each policy's detail view. The modal (`BackupPolicyFormModal` in `components/backup_policies/`) offers two primary actions: "Save" to persist a new or edited policy, or "Run now" to execute the policy's filters immediately as a one-time ad-hoc backup job (the ad-hoc policy auto-sets its `disabled_at` to expire after its configured timeout, 1h by default) and redirects to `/jobs`, where the resulting job(s) can be found and opened for their log lines — same modal-plus-detail-page pattern as `/storage` below. Linking to:
- `/policies/:id` — one policy's full record, in two tabs built on a reusable `Tabs` component
  (`components/ui/Tabs.vue`, active tab synced to `?tab=details`/`?tab=checkins` so either can be
  linked directly): `Details` (the default — client filters, object filters, backup window) and
  `Check-ins` (`components/policies/PolicyCheckins.vue` — every host that has received this policy
  from `policy-server`, each with its most recent check-in time, and a manual Refresh button that
  re-fetches the policy). Edit and Delete buttons sit at the page level, outside the tabs; Edit opens
  `BackupPolicyFormModal` pre-filled with the policy's current values (both "Save" and "Run now" are
  available here). No separate `/policies/new` or `/policies/:id/edit` routes.
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
- `/jobs/:job_id` — one job's log lines from the last 24h, fetched once on page load (no
  live-tail/polling); each line is parsed from its underlying JSON via `LogLine.vue` into a
  level-colored `[LEVEL] time binary@hostname: message` summary, with the remaining fields
  (`job_id`, `event`, `status`, etc.) collapsed behind a click — a line that isn't valid JSON
  falls back to plain text

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
- [Architecture](../ARCHITECTURE.md)
