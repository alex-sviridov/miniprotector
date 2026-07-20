# Design: `web` frontend consistency & best-practices refresh

**Date:** 2026-07-20
**Status:** Approved for planning

## Problem

`web` (Vue 3.5 + Pinia + vue-router 4 + Tailwind 4, ~2000 lines across 10 views, 5 components, 5
stores) works, but grew one view/store at a time across several prior design passes (see See Also)
without a shared layer to keep them consistent. Concretely:

- Every list/detail view repeats the same loading/error markup, page-header layout, and detail
  key-value grid by hand, with small drifts between copies (e.g. `ClientsListView`'s table rows are
  missing the `hover:bg-gray-50` that `JobsListView`'s and `PoliciesListView`'s have).
- `PolicyFormView`, `ClientFormView`, `KeyValueEditor`, and `SanListEditor` each hand-roll the same
  add-row/remove-row list pattern (5 occurrences).
- Every Pinia store action repeats the same `loading = true; error = null; try {...} catch
  {...} finally {...}` block (9 times in `clients.js` alone).
- `simple-datatables` (a jQuery-era library that manipulates the DOM directly, outside Vue's
  reactivity) backs every list table. In `CatalogView`, this has caused a real bug: the row-click
  handler correlates the library's post-sort row index back into the original unsorted `groups`
  array, so sorting a column and then clicking a row can open the wrong file's version modal.
- `router.js` eagerly imports all 10 view components (no code-splitting) and every internal link is
  a hand-built path string (`` `/jobs/${job.job_id}` ``) rather than a named route, so a path change
  means hunting down every template that built it manually.
- Buttons have no visual distinction between routine actions (Add Hostname) and destructive ones
  (Revoke, Delete, Unrevoke) — both render as a plain bordered button.

This is a cross-cutting quality pass, not a new feature: no new pages, no new API calls, no new
user-facing capability. **No backward compatibility constraint** — internal structure, test files,
and the `simple-datatables` dependency are all free to change.

## Scope

**In scope:**
- A small shared UI layer (`components/ui/`) used by every view: `BaseButton`, `PageHeader`,
  `StatusMessage`, `DetailList`, `RepeatableFieldList`.
- Replacing `simple-datatables` with `vue-good-table-next` across `ClientsListView`,
  `PoliciesListView`, `JobsListView`, `CatalogView`, via one `DataTable.vue` wrapper — fixes the
  sort/click correlation bug structurally (no more index correlation at all).
- A shared `withRequest` store helper (`stores/helpers.js`), applied to all 5 stores.
- Lazy-loaded (code-split) + named routes in `router.js`; every internal `:to=` and `router.push`
  switches from string paths to `{ name, params }`.
- Minimal visual refresh: keep the existing neutral/blue Tailwind palette, add a danger (red)
  button variant for destructive actions, standardize spacing/hover states via the shared
  components above. No new color palette, no layout restructuring, no dark mode.
- Rewriting the existing `*.spec.js` files to match the new structure.
- Documentation: `docs/components/web.md`, `CHANGELOG.md`, per this repo's CLAUDE.md rules.

**Out of scope:**
- Any backend/API change — same REST endpoints, same request/response shapes.
- New pages, new data on existing pages (e.g. no `HomeView` dashboard), or any new user-facing
  capability.
- Switching Pinia stores from Options-style (`defineStore({ state, actions })`) to setup-style —
  Options-style is still idiomatic Pinia and switching wouldn't reduce the boilerplate this design
  removes.
- A generic `BaseInput` / form-control abstraction — labels and `<input>` stay plain Tailwind-styled
  native elements; the only extracted list-editing pattern is `RepeatableFieldList`.
- `vue-good-table-next`'s advanced features (server-side pagination, grouping, virtual scroll) —
  only client-side sort/search/pagination, matching what `simple-datatables` provided today.

## Architecture

No change to the app's overall shape: SPA served by nginx, `/api/*` reverse-proxied to
`api-server`, bearer token in `localStorage`, Pinia + vue-router. This pass adds one new directory
(`components/ui/`) and one new module (`stores/helpers.js`), and touches every existing view,
component, and store to consume them.

```
web/src/
  components/
    ui/
      BaseButton.vue
      PageHeader.vue
      StatusMessage.vue
      DetailList.vue
      RepeatableFieldList.vue
      DataTable.vue
    KeyValueEditor.vue    (refactored onto RepeatableFieldList)
    SanListEditor.vue     (refactored onto RepeatableFieldList)
    VersionsModal.vue     (restyled onto BaseButton)
    Sidebar.vue           (restyled)
    TokenGate.vue         (restyled onto BaseButton)
  stores/
    helpers.js            (new: withRequest)
    *.js                  (all 5, refactored onto withRequest)
  router.js                (lazy imports, named routes)
  views/*.vue               (all 10, refactored onto the ui/ layer)
```

## Components

**`BaseButton`** — props: `variant: 'primary' | 'secondary' | 'danger'` (default `secondary`),
plus standard `type`/`disabled` pass-through. Danger replaces the plain bordered button on
Revoke, Unrevoke, Delete (client/policy), and Remove-row actions.

**`PageHeader`** — props: `title`. Default slot for body content, named `actions` slot for the
top-right button/link (e.g. "New Client"). Replaces the `flex items-center justify-between`
header block repeated in every list/detail view.

**`StatusMessage`** — props: `loading`, `error`, `empty` (with an `emptyText` slot/prop). Renders
"Loading...", the error text in red, or the empty-state message; renders nothing (so the default
slot shows) when none apply. Replaces the `v-if="loading" / v-else-if="error" / v-else-if="..."`
chain repeated in all 9 data-driven views. `CatalogView` has one extra state beyond the other 8
views — "no filter entered yet" versus "searched, zero results" are distinct messages — so it keeps
its own leading `v-if="!hasSearched"` branch around `StatusMessage`, which still owns the
loading/error/empty-after-search leaves.

**`DetailList`** — takes an array of `{ label, value }` entries (or a default slot for rows needing
custom markup, e.g. `PolicyDetailView`'s object-filters list) and renders the `<dl
class="grid grid-cols-[auto_1fr] ...">` pattern from `ClientDetailView` and `PolicyDetailView`.

**`RepeatableFieldList`** — props: `modelValue: Array`, `addLabel`. Emits `update:modelValue` on
add/remove; the caller supplies row markup via a scoped default slot (`#default="{ row, index }"`).
Used directly by `PolicyFormView` (hostnames, labels, object filters, backup windows) and
`ClientFormView` (SANs); `KeyValueEditor` and `SanListEditor` build their existing
snapshot/draft/dirty-tracking logic on top of it instead of hand-rolling their own row lists.

**`DataTable`** — wraps `vue-good-table-next`. Props: `columns` (field/label/sortable), `rows`,
`rowKey`; emits `row-click` with the actual row object (not an index), which is how `CatalogView`
opens the version modal — eliminating the index-correlation bug by construction, since there's no
index to correlate. Search/sort/pagination are the library's built-in behavior; styling is passed
through Tailwind classes so it matches the rest of the app rather than the library's default theme.
Per-column cell content beyond plain text (the `ClientsListView`/`JobsListView`/`PoliciesListView`
hostname/job-id/policy-name router-links, and `PoliciesListView`'s per-row Delete `BaseButton`) uses
`vue-good-table-next`'s named cell slots, passed through `DataTable` by field name (e.g.
`#field-hostname`) rather than the wrapper trying to model links/buttons as data.

**No changes** to `VersionsModal`'s structure (only its buttons move to `BaseButton`), or to
`groupEntriesByFile` / `formatBytes` / `formatTimestamp` (pure data transforms, table-library
agnostic).

## Data Flow

**Stores** (`stores/helpers.js`):
```js
export async function withRequest(store, fn, { rethrow = true, loadingKey = 'loading', errorKey = 'error' } = {}) {
  store[loadingKey] = true
  store[errorKey] = null
  try {
    return await fn()
  } catch (err) {
    store[errorKey] = err.message
    if (rethrow) throw err
  } finally {
    store[loadingKey] = false
  }
}
```
Every action becomes its request plus state update, e.g. `clients.js`'s `revoke`:
```js
async revoke(hostname) {
  const client = await withRequest(this, () =>
    apiFetch(`/clients/${encodeURIComponent(hostname)}/revoke`, { method: 'POST' }))
  this.updateCache(client)
  return client
}
```
`rethrow: false` is used only where today's code already swallows the error because no caller
awaits it in a try/catch (`fetchAll` in `clients.js`/`jobs.js`/`policies.js`, `catalog.search`,
`jobs.fetchLogs` — all invoked from `onMounted` reading `store.error` reactively instead).
Everywhere else keeps the default rethrow, since `PolicyFormView.submit` /
`ClientFormView.submit` rely on the throw to skip `router.push` on failure. `jobs.js`'s two
independent loading flags (`loading`/`logsLoading`, `error`/`logsError`) use the `loadingKey`/
`errorKey` options.

**Routing:** `router.js` routes gain `name` (kebab-case: `client-detail`, `policy-edit`,
`job-detail`, etc.) and lazy `component: () => import('./views/X.vue')`. Every `:to=` binding and
`router.push` call across views switches from a template-literal path to `{ name, params }`, e.g.
`ClientsListView`'s row link becomes `:to="{ name: 'client-detail', params: { hostname:
client.hostname } }"`.

**Tables:** unchanged data flow from the existing stores (`clients.list`, `policies.list`,
`jobs.list`, grouped `catalog.entries`) — only the rendering layer between "array of rows" and "DOM
table" changes, from `simple-datatables`'s imperative `new DataTable(tableRef.value)` +
`nextTick`/`destroy` dance to `vue-good-table-next`'s declarative `<vue-good-table :rows :columns>`
driven directly by Vue's reactivity (no manual instance lifecycle to manage).

## Error Handling

No behavioral change to what errors are shown or when — `store.error` is still the source of truth,
still rendered inline (now via `StatusMessage` instead of hand-copied markup). The 401 →
`auth.clearToken()` flow in `api/client.js` is untouched.

## Testing

- Every existing `*.spec.js` is rewritten to match the new component structure. `data-test`
  attributes are preserved and threaded through `RepeatableFieldList` (via a `testPrefix` prop, same
  as today's `KeyValueEditor`/`SanListEditor`) so tests assert the same selectors where the
  underlying markup is unchanged.
- Table-driven specs (`ClientsListView`, `PoliciesListView`, `JobsListView`, `CatalogView`) drop
  their `simple-datatables` mocks and instead assert against `vue-good-table-next`'s rendered rows;
  `CatalogView.spec.js`'s row-click test asserts the modal opens for the *clicked row's own group*
  after a sort — the regression test for the bug this design fixes.
- New unit specs for `withRequest` (rethrow vs. swallow, loading/error lifecycle) and for
  `RepeatableFieldList` (add/remove/emit) as shared, reusable logic.
- **Manual/integration:** `make demo-up`, click through every page — Clients (list, new, detail,
  revoke/unrevoke, re-enroll, description/attributes/SANs editing), Catalog (search, sort a column,
  click a multi-version row to confirm the *correct* file's versions open), Policies (list, new,
  edit, delete), Jobs (list, detail/logs) — confirming no visual or behavioral regression.

## Documentation

- Update `docs/components/web.md`: replace `simple-datatables` mentions with
  `vue-good-table-next`, note the shared `components/ui/` layer.
- `CHANGELOG.md` — one dated entry before merge, summarizing the consistency/best-practices pass.
- No `docs/protocols/` or `docs/ARCHITECTURE.md` changes — no protocol or topology change.

## See Also

- [Design: web frontend](2026-07-18-web-frontend-design.md) — original app structure this refresh
  builds on
- [Design: Jobs pages for `web`](2026-07-19-jobs-frontend-design.md) — introduced
  `simple-datatables`, now superseded by this design
- [Design: Catalog page grouping and `simple-datatables`](2026-07-19-catalog-frontend-design.md) —
  the row-index-correlation bug this design fixes structurally
- [Design: ClientManager admin API frontend](2026-07-19-clientmanager-admin-api-frontend-design.md)
- [web component doc](../../components/web.md)
