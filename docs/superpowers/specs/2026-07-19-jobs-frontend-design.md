# Design: Jobs pages for `web`

**Date:** 2026-07-19
**Status:** Approved for planning

## Problem

`api-server` now exposes `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` (see
[Design: `/jobs` REST Endpoint](2026-07-19-jobs-endpoint-design.md)), but `web` (see
[Design: web frontend](2026-07-18-web-frontend-design.md)) has no page for them — that design
explicitly deferred jobs as future work, pending exactly this API. An operator today has no
browser-based way to see what jobs ran fleet-wide or drill into one job's logs; only `curl` plus a
bearer token.

## Scope

**In scope (v1):**
- `web/src/views/JobsListView.vue` — table of jobs from `GET /jobs` (server defaults: last 24h, up
  to 100, all kinds/hosts/states — no filter form), each row's Job ID linking to its detail page
- `web/src/views/JobDetailView.vue` — one job's log lines from `GET /jobs/{job_id}/logs` (server
  default: last 24h), fetched once on page load
- `web/src/stores/jobs.js` — Pinia store for both fetches, mirroring `clients.js`'s shape
- New sidebar link ("Jobs") and routes `/jobs`, `/jobs/:job_id`
- `simple-datatables` as a new dependency, used only on the Jobs list table, to provide client-side
  search/sort/pagination over the fetched batch in place of a custom filter form

**Out of scope (v1):**
- Any server-side filtering UI (`kind`/`source_host`/`state`/`since`/`until` query params) — the
  API supports them, but `simple-datatables`'s client-side search over the default 24h/100-row batch
  covers the same operator need without a custom form
- Live-tail / polling log view — `JobDetailView` fetches once; no advancing `since` cursor, no
  auto-refresh
- Color-coded state styling — `state` renders as plain text, matching every other table in the app
- Parsing/pretty-printing log line JSON — each line's raw `line` string is rendered as-is in a
  monospace block
- Retrofitting `ClientsListView`/`CatalogView` to use `simple-datatables` — they stay plain HTML
  tables; the new dependency is scoped to the Jobs page only

## Architecture

No change to the existing `web` architecture (static SPA, nginx same-origin proxy to `api-server`,
bearer token from the `auth` store) — this adds two views, one store, two routes, and one new
frontend-only npm dependency on top of the pattern [Design: web frontend](2026-07-18-web-frontend-design.md)
already established.

### `simple-datatables` integration

`simple-datatables` enhances an already-rendered `<table>` rather than owning data directly — Vue
keeps rendering rows the normal way (`v-for` over `jobs.list`, same as `ClientsListView`), and the
library is layered on top of the resulting DOM:

- `onMounted`: `await jobs.fetchAll()`, then `nextTick()` (so the `v-for` rows exist in the DOM),
  then `new DataTable(tableRef.value)` — default options, which ships search box, column sort, and
  pagination enabled.
- `onBeforeUnmount`: `dataTable.destroy()` — required because this is an SPA; without it, navigating
  away from `/jobs` would leak the instance's listeners/DOM.
- CSS: `import 'simple-datatables/dist/style.css'` inside `JobsListView.vue` only, not `main.js`.

Since `JobsListView` fetches once on mount and never mutates `jobs.list` afterward (no filter form,
no polling), there's no need for the library's own `update()`/`refresh()` data API — the table is
initialized exactly once per page visit.

## Components

**Routes (`web/src/router.js`):**
- `/jobs` — `JobsListView.vue`
- `/jobs/:job_id` — `JobDetailView.vue`

**Sidebar (`web/src/components/Sidebar.vue`):** new "Jobs" link, same style as
Clients/Catalog/Policies.

**Store (`web/src/stores/jobs.js`):**
- State: `list`, `loading`, `error` (for `/jobs`); `logs`, `logsLoading`, `logsError` (for
  `/jobs/{id}/logs`) — kept separate so navigating list → detail → list doesn't clobber list state.
- `fetchAll()` — `GET /jobs`, no query params.
- `fetchLogs(jobId)` — `GET /jobs/{job_id}/logs`, no query params.

**`JobsListView.vue`:** Plain `<table>` (same structure as `ClientsListView`), columns: Job ID
(linked to `/jobs/:job_id`), Kind, Source Host, Store Host, Started At, Finished At, State — all
plain text, `formatTimestamp` for the two time columns, `—` for null `store_host`/`finished_at`.
Rows arrive newest-first from the API. `simple-datatables` is attached after mount per Architecture,
above.

**`JobDetailView.vue`:** Reads `route.params.job_id`, calls `jobs.fetchLogs(jobId)` on mount.
Heading shows the job ID. Log lines render as a monospace list, one row per line: formatted
timestamp + hostname + binary prefix, then the raw `line` string, unparsed. Empty-state message
when no lines are returned. Loading/error handling matches `ClientDetailView`'s inline-message
pattern.

## Data Flow

1. Sidebar "Jobs" link navigates to `/jobs`; `JobsListView`'s `onMounted` calls
   `jobs.fetchAll()`, then wraps the rendered table with `simple-datatables` once rows exist.
2. Operator uses the library's built-in search box / column-header sort to narrow or reorder the
   fetched batch client-side — no additional API calls.
3. Clicking a Job ID navigates to `/jobs/:job_id`; `JobDetailView`'s `onMounted` calls
   `jobs.fetchLogs(job_id)` and renders the returned lines.
4. Navigating away from `/jobs` triggers `onBeforeUnmount`, destroying the `simple-datatables`
   instance.

## Error Handling

- **Loading `/jobs` or `/jobs/{id}/logs`:** inline "Loading..." message, matching every other view.
- **Network/4xx/5xx errors:** surfaced inline with the response body's `error` text when available,
  same as `ClientsListView`/`CatalogView` — no retry logic.
- **Unknown/expired `job_id`:** `GET /jobs/{job_id}/logs` returns `200` with an empty result rather
  than a 404 (per the REST API design, a job outside Loki's retention or query window is
  indistinguishable from one that never existed). Note `data` is `null`, not `[]`, when no lines
  match — `api-server`'s handler leaves the slice unallocated and Go's `encoding/json` marshals a
  nil slice as `null` — so `jobs.js` must treat `body.data ?? []` as the line list. `JobDetailView`
  then shows an empty-state message ("No log lines found for this job in the last 24h"), not an
  error.
- **401:** handled globally by the existing `api/client.js` interceptor (clears token, re-shows the
  token gate) — no page-specific handling needed.

## Testing

- **Store:** Vitest unit tests for `jobs.js` — `fetchAll()`/`fetchLogs()` success and error paths,
  against a mocked `api/client.js`, mirroring `clients.spec.js`.
- **`JobsListView`:** `simple-datatables` is mocked (`vi.mock('simple-datatables', ...)`) so the
  test verifies Vue-side wiring only — fetch-on-mount, row data rendering, Job ID link hrefs — not
  the vendor library's internals.
- **`JobDetailView`:** unit test covering loading/error/empty/populated states and correct
  rendering of timestamp/hostname/binary/line per row, mirroring `ClientDetailView.spec.js`.
- **Manual/integration:** `make demo-up`, browse `/jobs`, confirm search/sort work over a real
  fetched batch, click into a job, confirm its logs render.

## Documentation

- Update `docs/components/web.md` — add `/jobs` and `/jobs/:job_id` to the Pages list, note the new
  `simple-datatables` dependency and its scope (Jobs page only).
- Update `README.md` if the Jobs page affects the quick-start examples or documentation index.
- `CHANGELOG.md` — one dated entry before merge.

## See Also

- [Design: `/jobs` REST Endpoint](2026-07-19-jobs-endpoint-design.md)
- [Design: web frontend](2026-07-18-web-frontend-design.md)
- [web component doc](../../components/web.md)
