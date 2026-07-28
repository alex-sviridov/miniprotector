# Design: storage policy type filtering + web UI

**Date:** 2026-07-28
**Status:** Approved for planning

## Problem

`2026-07-28-storage-policy-type-design.md` added a `"storage"` policy type to `policy-server`, but
explicitly left the web UI and `api-server`'s create/update paths untouched: `api-server`'s
`policyDTO`/`policyInput` only carry backup fields (`rpo`, `destination`, `object_filters`, ...) and
`handleCreatePolicy` hardcodes `Type: "backup"`, so there is currently no way to create or edit a
storage policy through the HTTP API, and therefore nothing for a web UI to call. `ListPolicies` also
returns every policy regardless of type, with no way to ask for just one kind.

This design adds: a type filter on `ListPolicies`, `api-server` support for storage policy
create/update, and a dedicated `Storage` section in the web UI, kept fully separate from the
existing `Policies` (backup) section.

**Behavior change from this doc's original draft:** `2026-07-28-agent-storage-supervision-design.md`
removes `StoragePolicy.Hostname` entirely — targeting a node is now `client_filters.hostnames` only,
the same mechanism a backup policy already uses. Every mention of a storage policy's `hostname` field
below is replaced with editing `client_filters.hostnames` in the modal instead of leaving it hardcoded
empty.

## Scope

- `policy-server`: optional `type` filter on `ListPoliciesRequest`.
- `api-server`: `?type=` query param on `GET /policies`; `port`/`config` on `policyDTO`;
  new `POST /storage-policies` / `PUT /storage-policies/{id}` endpoints.
- Web UI: new `Storage` nav section — one list view, modal-based create/edit — fully separate from
  the existing backup-only `Policies` section, which now requests `?type=backup` explicitly.

## Out of scope

- Labels editing in the storage modal (`client_filters.labels` stays `{}`, not user-editable here).
  Only `hostnames` is exposed, as a single required "Target hostname" field — see the modal section
  below.
- Any storage-type option beyond `filesystem` in the type selector. The dropdown is structured to
  make adding a second type a small addition (new `<option>` + new conditional field block), not a
  redesign — but no second type is implemented here.
- Anything about a storage server actually consuming `port`/`config` to run. This is still policy
  authoring only, same boundary the backend design drew — actually running `bwfs` is
  `2026-07-28-agent-storage-supervision-design.md`'s job, a separate consumer of this data.
- Changing `GetPolicies` (the node-facing RPC) — the type filter is admin-surface (`ListPolicies`)
  only; a node needs whatever matches it, regardless of type.

## `policy-server`: type filter on `ListPolicies`

`ListPoliciesRequest` gains one optional field:

```proto
message ListPoliciesRequest {
  // Optional. "backup" or "storage" -- when set, only policies of this type
  // are returned. Empty returns every type (today's behavior, unchanged).
  string type = 1;
}
```

`ListPolicies` (`server.go`) filters `s.cache.Policies()` by `p.Kind() == req.GetType()` before
converting to proto, only when `req.GetType() != ""`. An unrecognized type value simply matches
nothing (no error) — consistent with `Kind()` being whatever string the type subfolder produced,
not a closed enum at this layer. `GetPolicies` is unchanged.

## `api-server`: type-aware DTO, input, and endpoints

- `handleListPolicies` reads an optional `type` query parameter and passes it through as
  `ListPoliciesRequest.Type`. `GET /api/v1/policies` (no param) keeps returning everything.
- `policyDTO` gains `Port int32`, `Config string` fields, populated from
  `p.GetPort()`/`p.GetConfig()` in `toPolicyDTO` — zero for backup policies, same additive
  convention as the proto. No `Hostname` field — removed from `StoragePolicy` itself, targeting is
  `ClientFilters` only.
- New `storagePolicyInput` struct: `Name string`, `ClientFilters clientFiltersDTO`,
  `Port int32`, `Config string`.
- New handlers `handleCreateStoragePolicy` / `handleUpdateStoragePolicy`, parallel to the existing
  `handleCreatePolicy`/`handleUpdatePolicy` but building `CreatePolicyRequest`/`UpdatePolicyRequest`
  with `Port`/`Config` set and `Type: "storage"` (create only; update has no type field,
  matching the immutable-type rule from the backend design).
- New routes: `POST /api/v1/storage-policies`, `PUT /api/v1/storage-policies/{id}`.
- `handleGetPolicy` (`GET /api/v1/policies/{id}`) and `handleDeletePolicy`
  (`DELETE /api/v1/policies/{id}`) are reused as-is for storage policies — both are already
  type-agnostic (lookup/delete by id).
- Existing `handleCreatePolicy`/`handleUpdatePolicy` (backup) are unchanged in behavior.

## Web UI

### Store: `web/src/stores/storagePolicies.js`

A new Pinia store, separate from `usePoliciesStore`, so the two policy kinds never share one
flat-shaped list:

- State: `list`, `byId`, `loading`, `error` (same shape as `usePoliciesStore`).
- Actions: `fetchAll()` → `GET /policies?type=storage`; `fetchOne(id)` → `GET /policies/{id}`
  (cached in `byId`, same pattern as `usePoliciesStore.fetchOne`); `create(input)` →
  `POST /storage-policies`; `update(id, input)` → `PUT /storage-policies/{id}`; `remove(id)` →
  `DELETE /policies/{id}`.

`usePoliciesStore.fetchAll` is updated to request `GET /policies?type=backup`, so the existing
`Policies` section never sees a storage policy.

### View: `web/src/views/StorageView.vue`

The one storage view — list plus modal, no separate detail/form routes. Layout follows
`PoliciesListView.vue`: `PageHeader` with a "New Storage Policy" action, `StatusMessage` wrapping a
`DataTable` (columns: Name, Target Hostname, Port, Storage Type, actions — Target Hostname read from
`row.client_filters.hostnames[0]`, `—` if empty). Row's Name links/click opens the edit modal for
that row (rather than navigating to a detail route); "New Storage Policy" opens the same modal
empty. Delete button per row, same `window.confirm` pattern as `PoliciesListView`.

Storage type displayed in the table is derived client-side by parsing `row.config` and reading its
`backend` field (falls back to `—` if unparseable/absent) — matching the `backend`/`root` key names
already used for storage `config` throughout `policy-server`'s own tests (e.g.
`{"backend": "filesystem", "root": "/data/storage"}` in `storage_policy_test.go`).

### Modal: `web/src/components/storage/StorageEditModal.vue`

Modeled on `VersionsModal.vue`: full-screen overlay, `@click.self` to close, Escape key closes,
`close`/`save` emits. Props: `policy` (null for create, the storage policyDTO for edit).

Fields:
- **Name** — text, required.
- **Target hostname** — text, required. The one node this storage policy applies to; submits as
  `client_filters.hostnames = [value]` — this is now the sole targeting mechanism
  (`StoragePolicy.Hostname` no longer exists).
- **Port** — number input, required, 1-65535.
- **Storage type** — `<select>`, one option today: `filesystem`. Structured so a second `<option>`
  plus its own conditional field block is a small addition later.
- **Filesystem path** — text, required, shown only when storage type is `filesystem`.

On mount with a non-null `policy` prop, the form is pre-filled from
`policy.client_filters?.hostnames?.[0]`/`policy.port` and `JSON.parse(policy.config)`
(`backend`/`root`). On submit, builds `config: JSON.stringify({ backend: storageType, root: path })`
and `client_filters: { hostnames: [targetHostname], labels: {} }`, and calls
`storagePolicies.create`/`update` via the parent view (the modal emits `save` with the built payload;
`StorageView` owns the store calls and closes the modal on success, mirroring how `PolicyFormView`
owns `policies.create`/`update` today).

Client-side validation before emitting `save`: name/target hostname/path non-empty, port in range.
Server-side validation (`StoragePolicy.Validate()`) is the authority; client checks just avoid an
obviously bad round-trip. Note `StoragePolicy.Validate()` itself does not require
`client_filters.hostnames` to be non-empty (same as a backup policy) — this modal's own required-field
check is what actually prevents creating an untargeted (matches-every-node) storage policy through the
UI.

### Nav and routing

- `web/src/router.js`: new route `{ path: '/storage', name: 'storage', component: () =>
  import('./views/StorageView.vue') }`.
- `web/src/components/Sidebar.vue`: new "Storage" `router-link`, alongside the existing Policies
  link.

## Data flow (create example)

1. User clicks "New Storage Policy" on `StorageView` → modal opens empty.
2. User fills name/target hostname/port/type/path, submits → modal validates client-side, emits
   `save` with `{ name, port, config, client_filters: { hostnames: [target], labels: {} } }`.
3. `StorageView` calls `storagePolicies.create(payload)` → `POST /storage-policies`.
4. `api-server` `handleCreateStoragePolicy` builds `CreatePolicyRequest{ Type: "storage", ... }` →
   `policy-server.CreatePolicy`.
5. `policy-server` validates (`StoragePolicy.Validate()`), writes to `policies/storage/`, returns
   the created `Policy` proto.
6. `api-server` returns `policyDTO` (now including port/config) with `201`.
7. `StorageView` pushes the new row into `storagePolicies.list`, closes the modal.

Errors at any step (validation failure, backend unreachable) surface the same way
`policies.error` does today — recorded on the store, rendered via `StatusMessage`/inline text in the
modal.

## Testing plan

- **`policy-server`**: `ListPolicies` with `type: "storage"` / `type: "backup"` / unset — returns
  only matching-kind policies / everything, respectively; unrecognized type returns empty, not an
  error.
- **`api-server`**: `handleListPolicies` passes `?type=` through to `ListPoliciesRequest.Type`;
  `toPolicyDTO` surfaces port/config; `handleCreateStoragePolicy`/
  `handleUpdateStoragePolicy` build correctly-typed requests and reject/propagate backend
  validation errors the same way the backup handlers do.
- **Web UI**: `storagePolicies` store actions (fetchAll/create/update/remove) against a mocked
  `apiFetch`, same pattern as `stores/policies.spec.js`; `StorageView` renders the list (including the
  Target Hostname column derived from `client_filters.hostnames[0]`), opens the modal on "New" and on
  row click, calls the right store action on save; `StorageEditModal` pre-fills from an edit `policy`
  prop (including target hostname from `client_filters.hostnames[0]`), round-trips `config` JSON
  correctly, validates required fields (including target hostname) client-side before emitting
  `save`.

## Documentation

- `docs/protocols/policy-server.md` — document `ListPoliciesRequest.type`.
- `docs/components/policy-server.md` — note `ListPolicies` now supports type filtering.
- `docs/components/api-server.md` — document `?type=` on `GET /policies`, the new
  `POST /storage-policies` / `PUT /storage-policies/{id}` endpoints, and the extended `policyDTO`
  shape.
- `docs/components/web.md` — document the new `Storage` section: nav entry, `StorageView`, modal
  -based create/edit, and that it's separate from the `Policies` (backup) section.
- `CHANGELOG.md` — one dated entry: adds type-filtered `ListPolicies`, `api-server` storage-policy
  create/update support, and the web UI's `Storage` section.
