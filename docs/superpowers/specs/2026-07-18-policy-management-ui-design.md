# Design: policy management UI (web frontend)

**Date:** 2026-07-18
**Status:** Approved for planning

## Problem

`api-server` now exposes full CRUD for policies (`GET`/`POST`/`PUT`/`DELETE` on `/api/v1/policies`,
see [Design: policy management via api-server](2026-07-18-policy-management-api-design.md)), but
`web` — the browser frontend over `api-server` (see
[Design: web frontend](2026-07-18-web-frontend-design.md)) — only has pages for `clients` and
`catalog`, both read-only. There is still no way to view or manage backup policies from the browser;
an operator has to `curl` `api-server` or hand-edit JSON files on `policy-server`'s host. This closes
that gap: policy list, detail, create, edit, and delete, reachable from the browser.

## Scope

**In scope:**
- `/policies` — list of all policies, with a Delete action per row and a link to create a new one
- `/policies/:id` — read-only detail view
- `/policies/new` — create form
- `/policies/:id/edit` — edit form (same component as create)
- A `policies` Pinia store wrapping the five `/api/v1/policies` endpoints
- A "Policies" sidebar link alongside the existing Clients/Catalog

**Out of scope:**
- Client-side semantic validation of `rpo` (duration), `backup_window` (cron), or glob syntax —
  `policy-server` already validates on write and returns a `400` with a message; the form surfaces
  that message rather than duplicating validation logic in the browser
- Optimistic-concurrency UI (e.g. conflict warnings on concurrent edits) — matches the API's own
  last-write-wins design
- RBAC or per-user identity — reuses the existing shared bearer token, same as every other write
  path in `api-server`
- Any change to `api-server` or `policy-server` — the REST surface this consumes already exists and
  is unchanged by this work

## Routing

Mirrors the existing list/detail split (`/clients`, `/clients/:hostname`):

| Route | Component | Purpose |
|---|---|---|
| `/policies` | `PoliciesListView.vue` | Table of all policies; Delete action per row; "New Policy" link |
| `/policies/new` | `PolicyFormView.vue` | Create form |
| `/policies/:id` | `PolicyDetailView.vue` | Read-only detail; Edit and Delete actions |
| `/policies/:id/edit` | `PolicyFormView.vue` | Edit form |

`PolicyFormView.vue` is shared between create and edit: mode is derived from whether
`route.params.id` is present. On mount in edit mode it calls `policies.fetchOne(id)` and
pre-populates the form; in create mode it starts from an empty policy shape.

`Sidebar.vue` gains a third `router-link` to `/policies`, alongside Clients and Catalog.

## Store (`web/src/stores/policies.js`)

Same shape as `stores/clients.js`: `list`, `byId`, `loading`, `error`.

- `fetchAll()` — `GET /policies`, populates `list`
- `fetchOne(id)` — `GET /policies/{id}`, cached in `byId` like `clients.fetchOne`
- `create(input)` — `POST /policies`; on success, pushes the returned policy into `list` and `byId`
- `update(id, input)` — `PUT /policies/{id}`; on success, replaces the entry in `list` and `byId`
- `remove(id)` — `DELETE /policies/{id}`; on success, removes the entry from `list` and `byId`

Write actions update the store in place from the response body rather than triggering a full
`fetchAll()` refetch, consistent with keeping the store as the single source of truth for already-
loaded views. All five actions follow the existing `loading`/`error` try/catch/finally pattern from
`clients.js`; `create`/`update`/`remove` rethrow on failure (like `clients.fetchOne`) so the calling
form/view can keep the user on the page and show the error inline instead of navigating away.

## Form fields (`PolicyFormView.vue`)

A `policyInput`-shaped local reactive object (`name`, `client_filters`, `object_filters`, `rpo`,
`backup_window`, `destination`), matching `api-server`'s `policyInput` wire shape exactly so submit
is a direct `JSON` post of the form state:

- **Plain text inputs:** `name`, `rpo` (e.g. `"1h"`), `destination` (e.g. `"store:8080"`)
- **`client_filters.hostnames`** (list of glob strings): add/remove rows of a single text input each
- **`client_filters.labels`** (key/value map): add/remove rows of a key text input + value text
  input pair
- **`backup_window`** (list of cron strings): add/remove rows of a single text input each
- **`object_filters`** (list of `{path, include, exclude}`): add/remove rows at the filter level;
  each row has a `path` text input plus two comma-separated text fields for `include`/`exclude`
  (split on `,`, entries trimmed, empty entries dropped before submit). Nested add/remove UI for
  `include`/`exclude` within each filter row was considered and rejected as disproportionate — these
  are typically short glob lists, and comma-separated text keeps the form flat.

All "add row" controls append an empty entry to the relevant array; each row gets a "remove" button.
Empty trailing rows are filtered out of list-shaped fields before submit (so an operator can add and
then abandon a row without sending a blank string).

On submit, the form calls `policies.create(input)` or `policies.update(id, input)` depending on
mode, and on success navigates to `/policies/:id` (the created or edited policy's detail page). On
failure, the store's `error` is rendered inline above the form (same placement/style as
`ClientsListView`'s error state) and the user stays on the form with their input intact.

## Delete

A "Delete" button appears on each row of `PoliciesListView` and on `PolicyDetailView`, guarded by
the browser's native `confirm()` (matches this app's existing minimal-dependency style — no custom
modal component exists yet and none is introduced for this). On confirm, calls
`policies.remove(id)`:
- From the list, the row is removed in place (no refetch, no navigation).
- From the detail view, the browser navigates to `/policies` after a successful delete.

## Error Handling

- **List/detail fetch failure:** inline error message in place of the table/detail content, same
  pattern as `ClientsListView`/`ClientDetailView`.
- **Unknown `id` on detail/edit (`404`):** inline "policy not found" message instead of a blank page
  (mirrors how `ClientDetailView` would need to handle a missing client, per the web frontend
  design).
- **Create/update validation failure (`400`):** the backend's `error` message is shown inline above
  the form; the user's entered data is preserved so they can fix and resubmit.
- **Delete failure:** inline error message on whichever view (list or detail) the delete was
  triggered from; the row/detail remains since the delete did not take effect.

## Testing

Following the existing `clients.spec.js` / `ClientsListView.spec.js` pattern (Vitest, mocked
`api/client.js`):

- **`stores/policies.spec.js`:** each of the five actions — success path (request shape, state
  update), failure path (`error` set, rethrow for the three write actions), and `fetchOne`'s cache
  behavior (second call for the same `id` doesn't refetch).
- **`views/PoliciesListView.spec.js`:** renders fetched policies; delete button (with `confirm()`
  mocked) calls `policies.remove` and the row disappears from the rendered table.
- **`views/PolicyDetailView.spec.js`:** renders a fetched policy's fields; delete triggers navigation
  to `/policies` on success.
- **`views/PolicyFormView.spec.js`:** create mode starts from an empty form and calls
  `policies.create` with the assembled `policyInput` shape on submit (including the comma-split
  `include`/`exclude` and empty-row-filtering behavior); edit mode pre-populates from
  `policies.fetchOne` and calls `policies.update`; a mocked `400` failure leaves the form's entered
  values intact and shows the inline error.
- **Manual/integration:** `make demo-up`, then in the browser create a policy, edit it, verify the
  change round-trips through `policy-server`'s file-backed storage (e.g. via `bwfs ... list` or by
  re-fetching), and delete it.

## Documentation

- `docs/components/web.md` — add `/policies`, `/policies/new`, `/policies/:id`,
  `/policies/:id/edit` to the routes list; note this is the app's first write-capable feature.
- `CHANGELOG.md` — one dated entry.
- No `docs/ARCHITECTURE.md` change (no new topology/data flow — `web` already talks to `api-server`
  for reads; this adds writes over the same path).

## Out of scope (restated)

- No client-side semantic validation of `rpo`/`backup_window`/glob syntax.
- No optimistic-concurrency/conflict UI.
- No RBAC or per-user identity.
- No changes to `api-server` or `policy-server`.
- No custom modal/confirm component — native `confirm()` only.
