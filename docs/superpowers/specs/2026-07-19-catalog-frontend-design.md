# Design: Catalog page grouping and `simple-datatables` for `web`

**Date:** 2026-07-19
**Status:** Approved for planning

## Problem

`CatalogView.vue` lists catalog entries one row per stored file *version* (see
[Catalog Sync Protocol](../../protocols/catalog-sync.md) / `GET /api/v1/catalog`), server-paginated
with a custom filter form and Prev/Next buttons. A file backed up repeatedly shows up as N separate,
visually identical rows differing only in size/mtime/job id, with no way to see them as versions of
one file. Separately, [Design: Jobs pages for `web`](2026-07-19-jobs-frontend-design.md) introduced
`simple-datatables` for the Jobs list but explicitly kept `CatalogView` as a plain table — this
design brings Catalog onto the same library for a consistent sortable/searchable table feel, and
adds file-version grouping on top.

## Scope

**In scope:**
- Client-side grouping of the currently loaded page of `/api/v1/catalog` entries by
  `(source_host, path)`, so the table shows one row per distinct file (within that page) instead of
  one row per version.
- Porting `CatalogView.vue`'s table onto `simple-datatables`, matching `JobsListView.vue`'s
  integration pattern.
- A version-detail modal, opened from a per-file "Versions" count, listing that file's other
  versions from the currently loaded page.
- Documentation updates: `docs/components/web.md` (Pages list entry, grouping/page-boundary caveat),
  `CHANGELOG.md`.

**Out of scope:**
- Any backend change (`catalog.proto`, `catalog` service, `api-server`'s `/catalog` endpoint) — no
  new query for distinct files or full version history. Grouping is exactly as complete as whatever
  page(s) of data the browser currently has loaded; versions split across a Prev/Next page boundary
  will not be grouped together (see Data Flow, below).
- Changing the existing server-side filter form (source host / store host / pattern) or the
  Prev/Next cursor pagination controls — both are kept as-is.
- Diffing file content or metadata between versions — the modal lists version metadata only.

## Architecture

No change to `web`'s overall architecture. This modifies one view (`CatalogView.vue`) and its
rendering of already-fetched store data; the `catalog.js` Pinia store, its `/api/v1/catalog` calls,
and cursor pagination logic are unchanged.

### Grouping

On each successful fetch (initial load, filter submit, Prev, Next), the entries in
`catalog.entries` are grouped client-side by `(source_host, path)`. Each group's representative row
uses the version with the highest `store_created_at` — bwfs's own recording time for when that
backup was captured, not the file's own `mod_time` (which reflects the source file's mtime, not
when it was backed up, and is less trustworthy as a "latest" signal). Groups of size 1 render with
no version affordance; groups of size 2+ show a clickable count.

Grouping is recomputed from scratch on every fetch — it is a pure function of the current
`catalog.entries` array, not persisted state.

### `simple-datatables` integration

Same pattern as `JobsListView.vue` ([Design: Jobs pages for `web`](2026-07-19-jobs-frontend-design.md#simple-datatables-integration)):
Vue renders the grouped rows into a plain `<table ref="tableRef">` via `v-for`, and
`simple-datatables` is layered on top.

The difference from Jobs: Catalog's underlying data changes on every filter submit / Prev / Next
(Jobs fetches once and never again), so the `DataTable` instance must be destroyed and recreated on
every successful fetch, not just created once on mount:

```js
async function loadAndRender() {
  await catalog.search(form) // or nextPage() / prevPage()
  if (dataTable) { dataTable.destroy(); dataTable = null }
  await nextTick()
  if (tableRef.value) dataTable = new DataTable(tableRef.value)
}
```

`onBeforeUnmount` still calls `dataTable.destroy()` for the SPA navigate-away case, matching Jobs.

### Version modal

Inline in `CatalogView.vue` — a `<div v-if="selectedGroup">` overlay, no separate component file.
The app has no existing modal component to share; introducing one now for a single call site would
be premature (revisit if a second modal need shows up elsewhere). Closes on an X button, backdrop
click, or Escape (`@keydown.esc` on a document listener registered while open).

## Components

**`CatalogView.vue`:**
- Filter form and Prev/Next controls: unchanged.
- Table columns: Path, Source Host, Store Host, Size, Mode, Modified (all from the representative
  version), Versions (count, blank for single-version groups, clickable for 2+).
- Version modal: title "Versions of `<path>` on `<source_host>`"; table columns Captured
  (`store_created_at`), Size, Mode, Modified (`mod_time`), Job ID, Store Host; sorted newest-first
  by `store_created_at`.

**No changes to routes, sidebar, or `catalog.js` store.**

## Data Flow

1. `CatalogView` mounts, calls `loadAndRender()` (filters empty) — fetches page 1 from
   `/api/v1/catalog`, groups the response by `(source_host, path)`, renders grouped rows, attaches
   `simple-datatables`.
2. Operator uses the search box / column sort to narrow or reorder the grouped rows client-side —
   no additional API calls, matching Jobs.
3. Operator edits the filter form and submits, or clicks Prev/Next — `loadAndRender()` re-runs:
   fetch, destroy old `DataTable`, regroup, re-render, create new `DataTable`.
4. Operator clicks a "Versions" count — `selectedGroup` is set to that group's version list (already
   in memory, no fetch), opening the modal.
5. If versions of one file are split across a page boundary (e.g., a Next click), the two pages
   group independently — the operator sees "1 version" (or fewer than the true total) on each page
   rather than a single combined count. This is a known, accepted limitation of the frontend-only,
   current-page-scoped grouping approach.
6. Navigating away from `/catalog` triggers `onBeforeUnmount`, destroying the `simple-datatables`
   instance.

## Error Handling

No change from today's `CatalogView.vue` — loading/error inline messages driven by
`catalog.loading`/`catalog.error`, unchanged from the existing store. Grouping and modal logic only
run over already-successfully-fetched data, so they introduce no new error states.

## Testing

- **`CatalogView.spec.js`:** extend the existing spec (mocking `simple-datatables` the same way
  `JobsListView.spec.js` does) to cover: grouping multiple entries with the same
  `(source_host, path)` into one row with the correct version count and representative version;
  single-version entries rendering without a version affordance; opening the modal renders the
  expected version rows sorted newest-first by `store_created_at`; closing via X / backdrop /
  Escape.
- **Manual/integration:** `make demo-up`, browse `/catalog`, back up the same file twice (two
  `brfs` runs against the same path) to produce multiple versions, confirm grouping, the version
  count, and the modal's contents; confirm search/sort still work over the grouped rows; confirm
  Prev/Next still fetch and regroup correctly.

## Documentation

- Update `docs/components/web.md` — note that `/catalog` groups entries by file within the current
  page (with the page-boundary caveat) and now uses `simple-datatables`, matching Jobs.
- `CHANGELOG.md` — one dated entry before merge.

## See Also

- [Design: Jobs pages for `web`](2026-07-19-jobs-frontend-design.md) — the `simple-datatables`
  integration pattern this design reuses
- [Design: web frontend](2026-07-18-web-frontend-design.md)
- [Catalog Sync Protocol](../../protocols/catalog-sync.md) — `ListEntries`, the RPC behind
  `GET /api/v1/catalog`
- [web component doc](../../components/web.md)
