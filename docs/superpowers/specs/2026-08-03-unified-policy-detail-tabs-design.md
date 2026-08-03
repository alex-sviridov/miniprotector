# Unified Policy Detail Tabs — Design

## Context

`policy-server`/`api-server` now track and surface check-in data (`checkins: [{hostname,
last_seen_at}]`) on every policy returned by `GET /api/v1/policies` and `GET
/api/v1/policies/{id}` (see [Design: Policy Check-in
Tracking](2026-08-03-policy-checkin-tracking-design.md)). There is currently no frontend surface
for it.

Separately, Backup Policies and Storage Policies have diverged: Backup Policies has a dedicated
details page (`/policies/:id` → `BackupPolicyView.vue`) with Edit opening
`BackupPolicyFormModal`; Storage Policies has no details page at all — its list
(`StorageView.vue`) opens `StorageEditModal` directly from a row click.

This change unifies both onto the same details-page-with-edit-modal shape, and adds a tabbed
layout to that shape — a default **Details** tab (today's read-only field list) and a new
**Check-ins** tab (hosts that have received the policy, with a manual refresh). The tab layout is
built as a reusable component so future detail pages can adopt it the same way.

## Scope

- Add a generic `Tabs.vue` UI component, URL-query-synced, for tabbed detail-page layouts.
- Add a shared `PolicyCheckins.vue` component (hostname/last-seen table + refresh) used by both
  policy types.
- Add `StoragePolicyView.vue` + `/storage/:id` route, giving Storage Policies a details page for
  the first time, matching `BackupPolicyView.vue`'s shape.
- Change `StorageView.vue`'s name column from opening `StorageEditModal` directly to a
  `router-link` to the new detail route (matching `BackupPoliciesView.vue`'s existing pattern).
- Wrap both `BackupPolicyView.vue` and `StoragePolicyView.vue`'s content in `Tabs`, with the
  existing read-only field list as the `Details` tab and `PolicyCheckins` as the `Checkins` tab.
- Add a `refresh(id)` action to both `stores/policies.js` and `stores/storagePolicies.js`.

Out of scope: any change to the check-in data itself or its retention (already shipped
server-side); a details page for any type other than policies; showing check-in data on the list
pages (`BackupPoliciesView`/`StorageView`).

## Component: `components/ui/Tabs.vue`

```vue
<Tabs :tabs="[{ key: 'details', label: 'Details' }, { key: 'checkins', label: 'Check-ins' }]">
  <template #details>...</template>
  <template #checkins>...</template>
</Tabs>
```

- Props: `tabs` (array of `{ key, label }`, first entry is the default).
- Renders a tab-strip of buttons (active tab visually distinguished — underline/highlight, plain
  Tailwind, no new dependency) followed by the active tab's slot content, keyed by `slot
  :name="tab.key"` — the same slot-per-key idiom `DetailList` already uses for its rows.
- Active tab is derived from `route.query.tab`, defaulting to `tabs[0].key` when the query param
  is absent or doesn't match any tab's key. Clicking a tab calls `router.replace({ query: { ...
  route.query, tab: key } })` — `replace`, not `push`, so switching tabs doesn't pollute browser
  history.
- This is the reusable pattern: any future detail page wanting tabs wraps its content in `<Tabs>`
  the same way; no page-specific tab logic to reimplement.

## Component: `components/policies/PolicyCheckins.vue`

New shared folder (`components/policies/`) for content shared across the `backup_policies`/
`storage` component split.

- Props: `checkins` (array of `{hostname, last_seen_at}`), `loading` (bool), `error` (string or
  null). Emits: `refresh`.
- Renders a plain HTML table (Hostname, Last Seen — via the existing `formatTimestamp` util),
  rows sorted by `last_seen_at` descending (most-recently-checked-in host first). Not `DataTable`/
  `vue-good-table`: check-in lists are bounded by matched hosts and don't need search/pagination
  chrome.
- Empty state: "No hosts have checked in yet."
- A `Refresh` button (top-right of the tab, `BaseButton` `variant="secondary"`), disabled while
  `loading`. `error`, when set, renders inline above the table without clearing existing rows — a
  failed refresh shouldn't blank out data already on screen.

## Views

**`BackupPolicyView.vue`** — unchanged content, wrapped in `Tabs`:
- `Details` tab: today's `DetailList` (unchanged rows/slot).
- `Checkins` tab: `<PolicyCheckins :checkins="policy.checkins" :loading="policies.checkinsLoading" :error="policies.checkinsError" @refresh="policies.refresh(id)" />`.

**`StoragePolicyView.vue`** (new, mirrors `BackupPolicyView.vue` 1:1):
- `PageHeader` with Edit (opens `StorageEditModal`) / Delete actions.
- `StatusMessage` wrapping `Tabs`.
- `Details` tab: `DetailList` with rows Name, Target Hostname, Port, Storage Type, Path, Created,
  Updated — same fields `StorageEditModal` already edits, read-only here.
- `Checkins` tab: same `PolicyCheckins` usage as above, wired to `storagePolicies.refresh(id)`.

**`StorageView.vue`**: name column becomes a `router-link` to `{ name: 'storage-detail', params:
{ id: row.id } }`, replacing the current `@click="openEdit(row)"`. `openEdit`/edit-from-list is
removed; create-from-list (`openCreate` / `StorageEditModal` in create mode) is unchanged.

## Store: `stores/policies.js` and `stores/storagePolicies.js`

Both stores already have `fetchOne(id)`, which short-circuits on a cache hit
(`if (this.byId[id]) return`) — unusable for an explicit refresh. Add a sibling action to each
store that always refetches:

```js
async refresh(id) {
  return withRequest(this, async () => {
    const policy = await apiFetch(`/policies/${encodeURIComponent(id)}`)
    this.byId[id] = policy
    const idx = this.list.findIndex((p) => p.id === id)
    if (idx !== -1) this.list[idx] = policy
    return policy
  }, { loadingKey: 'checkinsLoading', errorKey: 'checkinsError' })
}
```

`withRequest`'s existing `loadingKey`/`errorKey` override (already supported, currently unused)
keeps this state separate from the page-level `loading`/`error` `StatusMessage` reads — clicking
Refresh only affects the Check-ins tab, not the whole page. Add `checkinsLoading: false,
checkinsError: null` to both stores' `state()`.

## Router: `web/src/router.js`

```js
{ path: '/storage/:id', name: 'storage-detail', component: () => import('./views/StoragePolicyView.vue') },
```

Added alongside the existing `/storage` route; `/policies/:id` is unchanged.

## Data Flow: Check-ins Refresh

1. Detail page mounts, `fetchOne(id)` populates `policy.checkins` from the initial `GET
   /policies/{id}` response (already includes check-ins — no separate endpoint exists or is
   needed).
2. User switches to the `Checkins` tab (or it's the default via `?tab=checkins` deep link) and
   sees the current rows.
3. User clicks `Refresh` → `store.refresh(id)` re-issues `GET /policies/{id}`, overwrites
   `byId[id]` and the matching `list` entry.
4. Both views' `policy` computed already reads from `byId[id]`, so the new `checkins` array flows
   through reactively to `PolicyCheckins` without any extra wiring.
5. On failure, `checkinsError` is set and shown inline in the tab; the previously-loaded rows stay
   visible.

## Error Handling

- Initial load failure: existing `StatusMessage`/`policies.error` handling, unchanged — an error
  here blocks the whole page (both tabs), same as today.
- Refresh failure: scoped to `checkinsError`/`checkinsLoading`, surfaced only inside
  `PolicyCheckins`, doesn't affect the `Details` tab or clear existing check-in rows.

## Testing

- `Tabs.spec.js` (new): renders tab strip, click switches active slot content, defaults to first
  tab when `?tab` is absent/invalid, updates `route.query.tab` on click via `replace`.
- `PolicyCheckins.spec.js` (new): rows render sorted by `last_seen_at` desc, empty state, `Refresh`
  click emits `refresh`, disabled while `loading`, `error` renders without clearing rows.
- `StoragePolicyView.spec.js` (new, mirrors `BackupPolicyView.spec.js`): Details tab field
  assertions, Edit opens `StorageEditModal`, Delete confirms and navigates, Checkins tab wiring.
- `BackupPolicyView.spec.js`: update for the new `Tabs` wrapper; existing `DetailList` assertions
  move under the `Details` tab.
- `StorageView.spec.js`: update name-column assertion from opening the modal to navigating to
  `storage-detail`.
- `policies.spec.js` / `storagePolicies.spec.js`: `refresh(id)` bypasses the `byId` cache (calls
  the API even when already cached) and updates both `byId` and `list`.
- `router.spec.js`: new `storage-detail` route.

## Documentation

Per `.claude/CLAUDE.md`'s feature-change rule:
- `docs/components/web.md` — update the `/policies/:id` bullet (tabs, Check-ins content) and the
  `/storage` bullet (drop "no detail or form routes of its own — list and modal only", document
  the new `/storage/:id` route and its own tabs).
- `README.md` — checked for references to the old Storage Policies list/modal-only behavior;
  updated if any exist.
- `CHANGELOG.md` — entry added before merge.
