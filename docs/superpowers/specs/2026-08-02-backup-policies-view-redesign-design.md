# Backup Policies View Redesign — Design

## Context

`POST /api/v1/policies/adhoc` (added in `673c965`) lets the API server fire a one-time backup without a caller having to hand-craft `rpo`/`backup_window`/`disabled_at` — the server computes those from `adhocPolicyTimeout` and ignores any caller-supplied values for those three fields. There is currently no UI for it.

Separately, the backup policies pages (`PoliciesListView.vue`, `PolicyFormView.vue`, `PolicyDetailView.vue`) predate the list+modal pattern already established for Storage Policies (`StorageView.vue` + `StorageEditModal.vue`): a single list view owns a modal for both create and edit, rather than routing to dedicated `/new` and `/:id/edit` pages. This change brings Backup Policies to the same pattern, renames the two remaining views for symmetry, and adds a "Run now" action that calls the adhoc endpoint.

## Scope

- Collapse `/policies/new` and `/policies/:id/edit` into a single shared modal, reached from both the list page and the detail page.
- Rename `PoliciesListView.vue` → `BackupPoliciesView.vue` and `PolicyDetailView.vue` → `BackupPolicyView.vue` (list/detail symmetry; the detail page's route, content, and read-only behavior are otherwise unchanged).
- Add `BackupPolicyFormModal.vue` under a new `components/backup_policies/` directory, replacing `PolicyFormView.vue`.
- Add a "Run now" button to the modal that fires an adhoc backup independent of Save.
- Add `policies.runAdhoc()` to the Pinia store, calling `POST /api/v1/policies/adhoc`.
- Update `router.js` to drop `policy-new` and `policy-edit`, and repoint `policies`/`policy-detail` at the renamed view files.
- Update/rename the affected spec files.

Out of scope: any change to the adhoc endpoint itself, to `PolicyDetailView`'s read-only field layout, or to Storage Policies (already on this pattern).

## Views

**`BackupPoliciesView.vue`** (renamed from `PoliciesListView.vue`, path unchanged at `web/src/views/`)
- Same list/table as today (`PageHeader`, `StatusMessage`, `DataTable`, name column linking to the detail route, Delete action).
- The header action button is renamed **"New backup"** and now opens `BackupPolicyFormModal` in create mode (`editingPolicy = null`) instead of navigating to `/policies/new`.
- Owns `showModal`/`editingPolicy`/`serverError` refs, mirroring `StorageView.vue`'s `openCreate`/`closeModal`/`save` functions.
- On successful create, closes the modal and navigates to `{ name: 'policy-detail', params: { id } }` — unlike Storage (which stays on the list), Backup Policies keeps a detail page worth visiting.

**`BackupPolicyView.vue`** (renamed from `PolicyDetailView.vue`, path unchanged)
- Content, route (`policy-detail`), and read-only field rendering (`DetailList`, object filters slot) are unchanged.
- The "Edit" button changes from a `router-link` to `/policies/:id/edit` into a button that opens `BackupPolicyFormModal` in edit mode (`editingPolicy = policy.value`).
- On successful update, the modal closes; no navigation is needed since the store's `update()` already writes through to `byId[id]`, which the detail page's computed `policy` already reads from.
- On Run now, see below — behaves identically whether the modal was opened from here or from the list.

## Component: `components/backup_policies/BackupPolicyFormModal.vue`

Follows the exact shape of `StorageEditModal.vue`:
- Props: `policy` (object or `null` — `null` means create), `serverError` (string).
- Emits: `close`, `save` (payload), `run-now` (payload).
- Escape-to-close and click-outside-to-close via the same `onKeydown`/`@click.self` pattern.
- Local `errors.message` ref for client-side validation feedback, shown above the form alongside `serverError`.

**Fields** — identical to today's `PolicyFormView.vue`, reusing `RepeatableFieldList` from `components/ui/`:
- Name (text, required)
- Hostnames (repeatable)
- Labels (repeatable key/value)
- Object filters (repeatable path/include/exclude)
- RPO (text)
- Backup window (repeatable cron expressions)
- Destination (text)

**`toFormShape`/`buildPayload`** move from `PolicyFormView.vue` into this component unchanged.

**Footer buttons:**
- **Save** (`type="submit"`, label `"Create Backup Policy"` / `"Save Changes"` depending on `policy` prop) — submits the `<form>`, triggering native HTML5 validation (the `required` attribute on Name), then emits `save(buildPayload())`.
- **Run now** (`type="button"`) — placed next to Save. Calls `formEl.reportValidity()` directly (since a plain button doesn't trigger native form validation on click) and, if valid, emits `run-now(buildPayload())`. Uses the same `buildPayload()` as Save — the adhoc endpoint already ignores `rpo`/`backup_window`/`disabled_at`, so no separate payload shape is needed.
- **Cancel** — emits `close`, as today.

Run now is available in both create and edit mode, and never touches the saved policy — it is a fire-and-forget action independent of Save, per the earlier decision.

## Data flow: Run now

1. User fills the form (from either "New backup" or "Edit") and clicks **Run now**.
2. Modal validates via `reportValidity()`, then emits `run-now(payload)`.
3. Parent view (`BackupPoliciesView` or `BackupPolicyView`, whichever hosts the open modal) calls `policies.runAdhoc(payload)`.
4. **Success:** parent closes the modal and `router.push({ name: 'jobs' })`, so the user lands on the Jobs list to watch the resulting job(s) appear.
5. **Failure:** parent sets `serverError` from the store's error state; modal stays open so the user can retry or adjust fields.

## Store: `web/src/stores/policies.js`

New action, sibling to `create`/`update`:

```js
async runAdhoc(payload) {
  return withRequest(this, async () => {
    return apiFetch('/policies/adhoc', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    })
  })
},
```

It does not write to `list` or `byId` — the created adhoc policy is a server-side implementation detail (it self-disables after `adhocPolicyTimeout`) and isn't something the UI needs to track. If the user later revisits the policies list, `fetchAll()` will naturally include it until it expires, same as any other backup-type policy — no special-casing needed on the frontend.

## Router: `web/src/router.js`

```js
{ path: '/policies', name: 'policies', component: () => import('./views/BackupPoliciesView.vue') },
{ path: '/policies/:id', name: 'policy-detail', component: () => import('./views/BackupPolicyView.vue') },
```

`policy-new` and `policy-edit` routes are removed entirely.

## Error Handling

- Client-side: native HTML5 validation (`required` on Name) gates both Save and Run now via `reportValidity()`.
- Server-side: both `save()` and `runAdhoc()` catches set `serverError`/`policies.error`, surfaced in the modal without closing it, matching the existing `StorageEditModal` convention.

## Testing

- `BackupPoliciesView.spec.js` (renamed from `PoliciesListView.spec.js`): update button label assertion to "New backup"; assert it opens the modal rather than navigating.
- `BackupPolicyView.spec.js` (renamed from `PolicyDetailView.spec.js`): update Edit button assertion to open the modal rather than navigate; existing read-only rendering assertions unchanged.
- `BackupPolicyFormModal.spec.js` (replaces `PolicyFormView.spec.js`, styled after `StorageEditModal.spec.js`): covers create submit, edit submit, and new Run now cases — validation gate, `runAdhoc` call with the built payload, redirect to `jobs` on success, and modal-stays-open-with-error on failure.
- `router.spec.js`: drop assertions for the removed `policy-new`/`policy-edit` routes; update component references for the renamed files.

## Documentation

Per `.claude/CLAUDE.md`'s feature-change rule, `docs/components/` (whichever file documents the policies UI, if any) and `README.md` will be checked and updated if they reference the old view names, routes, or the "New Policy" button label. A `CHANGELOG.md` entry will be added before merge.
