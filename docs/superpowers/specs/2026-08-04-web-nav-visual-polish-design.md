# Design: `web` navigation shell & visual consistency polish

**Date:** 2026-08-04
**Status:** Approved for planning

## Problem

`web` went through a consistency/best-practices refresh in July (see See Also) that unified
loading/error/table/button patterns behind a shared `components/ui/` layer. Functionally solid, but
visually the app is still deliberately primitive:

- `Sidebar.vue` is plain text links on a light gray background — no branding, no icons, and the
  active route is distinguished only by a faint background tint.
- There is no breadcrumb or other orientation cue on any page; a user landing directly on a detail
  route (e.g. `/policies/:id`) has only the page title to go on.
- One button still drifts from the shared `BaseButton` component: `ClientsListView`'s "New Client"
  link hardcodes `bg-blue-600 text-white rounded px-3 py-1` inline instead of using it, because
  `BaseButton` only renders a `<button>` and this action needs to be a link.
- `DataTable.vue` wraps `vue-good-table-next` and imports its default stylesheet as-is — the
  library's own header/row/pagination/search styling doesn't match the rest of the app.
- Boolean/state columns (Revoked, job state) render as plain text ("Yes"/"No") with no visual
  weight.
- The browser tab shows a generic default icon and the plain title "Miniprotector".

This is a visual and navigational polish pass, not a redesign: same information architecture, same
routes, same data. The existing `blue-600` accent and neutral Tailwind palette stay — the goal is to
apply them more deliberately and close the one styling drift, not introduce a new color scheme.

## Scope

**In scope:**
- `Sidebar.vue`: dark slate background, brand header (logo mark + wordmark), one icon per nav item,
  left-accent-border active state.
- A small local icon set (`components/icons/`) — five hand-authored inline-SVG components, no new
  npm dependency.
- Breadcrumbs: `PageHeader.vue` gains an optional `crumbs` prop, rendered above the title on every
  view.
- `BaseButton.vue` gains an optional `to` prop to render as a `router-link` sharing the same variant
  classes, closing the `ClientsListView` styling drift.
- A `Badge.vue` component for boolean/state table columns (Revoked, job state), wired in via
  `DataTable`'s existing per-column slot mechanism.
- `DataTable.vue`: scoped style overrides of `vue-good-table-next`'s default theme (header, rows,
  hover, pagination, search box) to match the app's existing borders/colors.
- A favicon (small SVG "M" mark matching the sidebar logo) in `web/index.html`.
- Documentation: `docs/components/web.md`, `CHANGELOG.md`, per this repo's CLAUDE.md rules.

**Out of scope:**
- Any backend/API change — same REST endpoints, same request/response shapes.
- A `HomeView` dashboard, dark mode, or mobile/responsive sidebar collapse — separate concerns, not
  part of this pass.
- Toasts/success notifications or loading skeletons — `StatusMessage`'s plain-text states are
  unchanged.
- A new color palette — `blue-600` stays the sole accent; only neutrals used are existing Tailwind
  slate/gray tokens.
- Any icon package dependency (e.g. Heroicons) — icons are hand-authored inline SVG to keep the
  bundle and dependency list unchanged.

## Architecture

No change to the app's overall shape: SPA served by nginx, `/api/*` reverse-proxied to
`api-server`, bearer token in `localStorage`, Pinia + vue-router. This pass adds one new directory
and one new component, and touches `Sidebar`, `PageHeader`, `BaseButton`, `DataTable`, and every
view that renders a boolean/state table column.

```
web/
  index.html              (favicon)
  src/
    components/
      icons/
        IconClients.vue
        IconCatalog.vue
        IconPolicies.vue
        IconStorage.vue
        IconJobs.vue
      Sidebar.vue           (restyled)
      ui/
        PageHeader.vue      (+ crumbs prop)
        BaseButton.vue      (+ to prop)
        Badge.vue           (new)
        DataTable.vue       (restyled overrides)
    views/*.vue             (pass crumbs to PageHeader; use Badge in relevant columns)
```

## Components

**`Sidebar.vue`** — `bg-slate-900 text-slate-300`. A brand block at the top (`bg-blue-600` "M" mark
+ "Miniprotector" wordmark, `text-slate-50`), separated from the nav list by a `border-slate-800`
divider. Each `router-link` gets its matching icon from `components/icons/` before the label.
Active state (`router-link`'s `active-class`) becomes `bg-slate-800 text-white border-l-4
border-blue-500` (replacing the current `bg-gray-200 font-semibold`).

**`components/icons/`** — five components, each a single `<svg>` with `class="w-4 h-4"` via a
pass-through `class` attribute (Vue's automatic fallthrough), stroke-based line icons at 1.5px
weight to match a typical Tailwind-project icon style. No `viewBox` inconsistency, no external
asset — inline paths only, sized small enough (~10-15 lines each) that hand-authoring is simpler
than adding a dependency for five icons.

**`PageHeader.vue`** — new optional prop `crumbs: Array<{ label: String, to?: RouteLocation }>`.
When present, renders a `text-xs text-gray-400` line above the `<h1>`, joining segments with `/`;
segments with `to` render as `router-link`, the last (current page) renders as plain text. Every
view is updated to pass `crumbs`, e.g.:
- List views: `[{ label: 'Clients' }]` (single segment — mirrors the existing page title, so this
  is mostly about establishing the pattern for detail pages).
- Detail views: `[{ label: 'Clients', to: { name: 'clients' } }, { label: hostname }]`.
- `/policies/:id` and `/storage/:id` (tabbed detail pages): crumb is `[{ label: 'Policies', to: {
  name: 'policies' } }, { label: policy.name }]` — the tab itself isn't part of the breadcrumb,
  since `Tabs.vue` already shows which tab is active.

**`BaseButton.vue`** — new optional prop `to: [String, Object]`. When set, the component's root
element switches from `<button>` to `<router-link :to="to">`, keeping the same `VARIANT_CLASSES`
and `class` bindings; `type`/`disabled` props are simply not passed through in that branch (a link
has neither). `ClientsListView`'s "New Client" becomes `<BaseButton to="{ name: 'client-new' }"
variant="primary">New Client</BaseButton>`.

**`Badge.vue`** — props `variant: 'ok' | 'bad' | 'neutral'` (default `neutral`), default slot for
the label. Renders a small rounded-pill span (`rounded-full px-2 py-0.5 text-xs font-semibold`)
with `ok` → `bg-emerald-50 text-emerald-600`, `bad` → `bg-red-50 text-red-600`, `neutral` →
`bg-gray-100 text-gray-600`. Used via `DataTable`'s existing `#table-row` slot:
- `ClientsListView`'s Revoked column: `<Badge :variant="row.revoked ? 'bad' : 'ok'">{{ row.revoked
  ? 'Yes' : 'No' }}</Badge>`.
- `JobsListView`'s state column: `state === 'success'` → `ok`, `state === 'failure'` → `bad`,
  `state === 'in_progress'` → `neutral` (the three values `rest-v1.md` documents for a job's
  `state` field — no new state values are introduced).

**`DataTable.vue`** — adds a `<style scoped>` block using `:deep()` selectors against
`vue-good-table-next`'s fixed class names (`.vgt-table thead th`, `.vgt-table tbody tr:hover`,
`.vgt-wrap__footer`, `.vgt-input`, etc.) to align header background/casing, row borders/hover, and
pagination/search control styling with the rest of the app's Tailwind tokens (slate borders, blue
focus rings). Purely visual — no prop or event changes, so every existing usage
(`ClientsListView`, `CatalogView`, `JobsListView`, `PoliciesListView`, `StorageView`) picks it up
automatically.

**`index.html`** — adds `<link rel="icon" type="image/svg+xml" href="...">` pointing at a small
inline-able SVG "M" mark (same shape as the sidebar brand mark), replacing the browser's default
favicon.

## Data Flow

No change. All of the above is presentational — no new store state, no new API calls, no change to
existing request/response handling. `Badge`'s variant is derived client-side from data the stores
already expose (`client.revoked`, `job.state`).

## Error Handling

No change — `StatusMessage` and the underlying store `loading`/`error` flow are untouched.

## Testing

- **`Sidebar.spec.js`** — asserts the brand block renders, each nav link renders its icon, and the
  active-route link carries the new active classes.
- **`PageHeader.spec.js`** — asserts breadcrumb segments render in order, non-last segments with `to`
  render as `router-link`, and the component renders unchanged (no breadcrumb line) when `crumbs`
  is omitted.
- **`BaseButton.spec.js`** — new case asserting that passing `to` renders a `router-link` with the
  variant's classes instead of a `<button>`.
- **`Badge.spec.js`** (new) — asserts each variant applies its expected classes and renders the
  slot content.
- **`DataTable.spec.js`** — unchanged behavioral assertions (rows/columns/search/pagination/row-click
  still work); no new assertions needed since the override is styling-only.
- View specs (`ClientsListView`, `JobsListView`, etc.) updated only where they now assert `Badge`
  usage in place of plain text, or `crumbs` being passed to `PageHeader`.
- **Manual/integration:** `make demo-up`, click through every page — confirm breadcrumbs are
  correct and navigable on nested routes (client/policy/storage/job detail), sidebar icons and
  active-state render correctly, "New Client" now visually matches other primary buttons, table
  styling is consistent across all five list views, and the favicon shows in the browser tab.

## Documentation

- Update `docs/components/web.md`: mention the sidebar branding/icons, breadcrumbs on detail pages,
  and the `Badge` component where it describes the relevant pages/columns.
- `CHANGELOG.md` — one dated entry before merge, summarizing the navigation/visual-consistency pass.
- No `docs/protocols/` or `docs/ARCHITECTURE.md` changes — no protocol or topology change.

## See Also

- [Design: web frontend](2026-07-18-web-frontend-design.md) — original app structure
- [Design: web frontend consistency & best-practices refresh](2026-07-20-web-frontend-refresh-design.md) — the shared `components/ui/` layer this design builds on
- [Design: Storage policy web UI](2026-07-28-storage-policy-web-ui-design.md)
- [web component doc](../../components/web.md)
