# Design: restore cart (catalog selection UI)

**Date:** 2026-08-09
**Status:** Approved for planning

## Problem

The catalog view (`CatalogView.vue`, see `2026-08-08-catalog-directory-browsing-design.md`) lets a
user browse and search backed-up files but has no way to mark files/folders for restore. This is
the first pass toward a restore workflow: UI-only, no restore execution. It adds a way to select
files and folders from the catalog into a "restore cart," see that selection reflected while
browsing, and see it surfaced in the sidebar and a placeholder restore view.

Scope is explicitly UI-only. Submitting a restore job, talking to `rwfs`/`bwfs`, and any backend
change are all out of scope — this only prepares the frontend selection model.

## Data model: prefix-matching rule engine

A naive "store every selected file id" model breaks down the moment a user selects an entire
folder: a folder can contain an arbitrary number of files, and the UI has no reason to ever fetch
that full file list just to build a selection set. Instead, the cart stores a small list of
**rules**, and a file or folder's checked state is *resolved* from those rules on demand.

```js
{ path: '/var/log', host: null, include: true }               // folder wildcard — any host
{ path: '/var/log/access.log', host: 'web01', include: false } // file exception — host-specific
```

- **Folder rules** always have `host: null` and apply across every source host. This matches how
  folder rows are already computed in `CatalogView.vue`/`ListDirectoryChildren` — folder existence
  is host-agnostic; only file rows are host-specific (grouped by `source_host + path`, per
  `groupEntriesByFile`).
- **File rules** are always `(host, path)` — matches how a file row's identity already works
  client-side. There is no stable cross-version "file id" today (each version is a distinct
  `EntryRecord.ID`), so a cart file entry means "restore whatever the latest version of this file
  is," not a pinned version. Version-level selection is out of scope for this pass.
- **Resolving a specific file's state**: look for an exact `(host, path)` rule first (most
  specific); otherwise walk `path`'s ancestor directories and take the longest matching
  host-agnostic folder rule. No match = unchecked.
- **Resolving a folder row's displayed state** (for its checkbox):
  - `checked` — a rule covers this path (self or an ancestor) **and** no other rule exists at or
    under this path.
  - `unchecked` — nothing covers this path and nothing exists under it.
  - `indeterminate` — anything else (mixed).
- **Invariant, maintained on every mutation**: a rule is only stored if it changes the resolved
  state at its exact path relative to the closest ancestor rule. A rule that would be a no-op
  (matches what's already resolved there) is never added, and existing rules are pruned when they
  become redundant. This keeps the rule list small and makes "is there anything under this path"
  a simple existence scan rather than a resolved-value comparison.

### Toggle algorithm

**Folder checkbox**, clicked:
- unchecked or indeterminate → checked: remove every existing rule at-or-under this path, then add
  one `{ path, host: null, include: true }` rule.
- checked → unchecked: if a rule exists at this exact path, remove it (this path was the origin of
  its own inclusion). Otherwise the checked state was inherited from an ancestor wildcard — add
  `{ path, host: null, include: false }` to carve out an exception.

**File checkbox**, clicked: same two branches, scoped to `(host, path)` — no descendant rules to
prune since a file has no children. `include: true`/`false` rules use `host: <sourceHost>`.

## Store: `web/src/stores/restoreCart.js`

```js
state: { rules: [] }  // [{ path, host: string|null, include: boolean }]

getters:
  hasSelections   // rules.length > 0 -- drives the sidebar highlight
  entries         // rules.filter(r => r.include) -- feeds the placeholder restore view

actions:
  toggleFile(sourceHost, path)
  toggleFolder(path)
  fileState(sourceHost, path) -> boolean
  folderState(path) -> 'checked' | 'unchecked' | 'indeterminate'
```

No `clear`/`remove` actions in this pass — the restore view has no controls yet (see below), so
nothing calls them.

State is in-memory only (plain Pinia store, no persistence plugin). The cart resets on page
reload; real persistence is deferred alongside actual restore-job submission.

## Catalog UI integration (`CatalogView.vue`)

- A new leading checkbox column is prepended to `baseColumns` (`{ label: '', field: 'select',
  sortable: false }`), rendered via the existing `table-row` slot.
- Folder rows: checkbox bound to `restoreCart.folderState(row.path)`. Since HTML checkboxes don't
  expose `indeterminate` as a static attribute, it's set imperatively (a small local directive or a
  `ref` + `watchEffect` per row) from the three-state getter. Click calls
  `restoreCart.toggleFolder(row.path)`.
- File rows: checkbox bound to `restoreCart.fileState(row.sourceHost, row.path)`. Click calls
  `restoreCart.toggleFile(row.sourceHost, row.path)`.
- Checkbox clicks use `@click.stop` so they don't also trigger the existing `onRowClick` (folder
  navigation / versions modal).
- Works unchanged in pattern-search (flat) mode: only file rows render there, same
  `toggleFile`/`fileState` calls apply.

## Sidebar (`Sidebar.vue`)

- New nav entry: `{ name: 'restore', label: 'Restore', icon: IconRestore }`, added to `NAV_ITEMS`.
- New route: `/restore` → `RestoreView.vue` (registered in `router.js`).
- The link's class binding adds a highlighted style (reuses the existing active-route color
  treatment) whenever `useRestoreCartStore().hasSelections` is true — independent of whether
  `/restore` is the current route. No count badge.

## Restore view placeholder (`web/src/views/RestoreView.vue`)

Bare list, no controls: iterate `restoreCart.entries`, rendering a folder rule as `path/*` and a
file rule as `path (host)`. Empty state: "No files selected for restore yet." No remove/clear
buttons, no grouping, no restore-execution UI — explicitly a placeholder for later work.

## Performance

The rule-list model is chosen specifically to keep this cheap regardless of catalog size:

- **The rule list is bounded by user clicks, not by data volume.** Selecting an entire folder is
  one rule no matter how many files it contains — the alternative (one entry per file id) would
  make a single large-folder selection scale with folder size instead of with how many times the
  user clicked something.
- **Resolution only runs for currently-rendered rows.** `fileState`/`folderState` are evaluated
  once per visible (paginated, 25/page) row, each a linear scan over the rules array plus a walk
  up the path's ancestor directories (a handful of levels). At realistic scale (tens to low
  hundreds of rules) this is a few thousand cheap comparisons per re-render — not perceptible.
- **Explicitly not doing yet**: any indexing structure (e.g. a trie keyed by path segment) for
  faster resolution. That only pays off if the rule list grows into the thousands, which the
  folder-wildcard model is specifically designed to avoid. Revisit only if it's actually observed
  to be a problem.

## Testing plan

- `web/src/stores/restoreCart.spec.js`: the rule engine is the highest-risk part of this feature —
  resolve for file rules (exact match, inherited from ancestor folder rule, no match), resolve for
  folder rules (checked/unchecked/indeterminate, including a nested host-specific file exception
  making an otherwise-fully-covered folder indeterminate), and all four toggle transitions (folder
  unchecked→checked prunes descendants, folder checked→unchecked removes-or-excepts, same two for
  files). Also cover the redundant-rule-pruning invariant directly (toggling back and forth leaves
  no stale rules).
- `web/src/views/CatalogView.spec.js`: checkbox column renders for both folder and file rows;
  clicking a checkbox doesn't also trigger row navigation/versions modal; checkbox reflects
  `restoreCart` state including indeterminate.
- `web/src/components/Sidebar.spec.js`: Restore nav item present; highlighted class present iff
  `hasSelections` is true.
- `web/src/views/RestoreView.spec.js` (new): renders `restoreCart.entries` as a path list with the
  right `path/*` vs `path (host)` formatting; empty state text when the cart is empty.

## Out of scope

- Restore job execution/submission — no `rwfs`/`bwfs` calls, no backend changes at all.
- Version-specific restore selection (cart always implies "latest at restore time").
- Cart persistence across page reload/browser restart.
- Remove/clear controls in the restore view, and any grouping/summary beyond a flat list.
- Bulk "select all visible rows" control.
- Making folder-rule resolution filter-aware (date range/host/job filters already don't affect
  folder *existence* per the directory-browsing design; the same non-goal applies here).

## Documentation

- `docs/components/web.md` — add the restore cart / selection UI to the catalog capability
  description.
- `README.md` — no change expected (no new component, no quick-start impact).
- `docs/ARCHITECTURE.md` — no change (frontend-only, no topology/data-flow change).
- `CHANGELOG.md` — one dated entry: the catalog UI gains file/folder selection for restore (a
  restore cart with folder-wildcard + exception selection), UI-only groundwork with no restore
  execution yet.
