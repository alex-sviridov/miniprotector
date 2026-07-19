# Design: clientmanager-admin-api Frontend

**Date:** 2026-07-19
**Status:** Approved for planning

## Overview

`web` (the Vue SPA in front of `api-server`) currently has a read-only `/clients` list and
`/clients/:hostname` detail page, backed by `GET /api/v1/clients` and `GET
/api/v1/clients/{hostname}`. The
[clientmanager-admin-api](2026-07-19-clientmanager-admin-api-design.md) work added seven write
endpoints under `/api/v1/clients` (enroll, re-enroll, revoke, unrevoke, description/attribute/SAN
management). This spec adds a UI for all seven, matching the shape `/policies` already
established for its own read+write surface — same store pattern, same list/detail/form
structure — rather than inventing a new one.

## Approach

Three shapes were possible for the store layer; the chosen one mirrors `stores/policies.js`
directly rather than introducing anything new:

- **(Chosen) Extend `stores/clients.js` with one action per RPC.** `enroll`, `reenroll`, `revoke`,
  `unrevoke`, `updateDescription`, `updateAttributes`, `updateSans` — each a thin `apiFetch` call
  updating `list`/`byHostname` from the response, identical in shape to `policies.js`'s existing
  `create`/`update`/`remove`. No new file, no new pattern.
- **A generic "patch" action** taking an arbitrary path/body. Rejected: loses the specific,
  readable call signatures the rest of the codebase uses (`policies.create(input)`, not
  `policies.patch('/policies', input)`), and hides validation/shape concerns each endpoint
  actually has (e.g. `updateSans` takes `add`/`remove` lists, `updateDescription` takes
  `set`/`unset` maps — different shapes, not interchangeable).
- **Separate stores per concern** (e.g. a `clientAdmin.js` alongside `clients.js`). Rejected as
  needless split — `policies.js` already mixes read and write actions in one store; there's no
  reason clients should differ.

## Store (`web/src/stores/clients.js`)

Seven new actions, each following `policies.js`'s existing `loading`/`error`/cache-update shape:

```js
async enroll(hostname, sans) {
  // POST /clients — returns { hostname, token }. Adds a minimal record
  // ({ hostname, revoked: false, revoked_at: 0, last_seen_at: 0 }) to
  // this.list so the list page reflects it without a full refetch.
}
async reenroll(hostname, sans) {
  // POST /clients/{hostname}/reenroll — returns { hostname, token }. No
  // cache update: re-enrolling doesn't change any displayed field.
}
async revoke(hostname) { /* POST .../revoke — updates byHostname[hostname] and the matching list row from the response */ }
async unrevoke(hostname) { /* POST .../unrevoke — same update shape as revoke */ }
async updateDescription(hostname, set, unset) { /* PATCH .../description — updates byHostname[hostname] from the response */ }
async updateAttributes(hostname, set, unset) { /* PATCH .../attributes — same shape as updateDescription */ }
async updateSans(hostname, add, remove) { /* PATCH .../sans — updates byHostname[hostname] from the response */ }
```

`enroll`/`reenroll`'s token is returned to the caller, not written into `list`/`byHostname`
(the fields those cache — hostname, revoked, timestamps — never include it). For the hand-off from
`/clients/new` to `/clients/:hostname` after a successful enroll, the store holds it in one
plain, non-persisted field, `pendingToken: { hostname, token } | null`: `enroll`/`reenroll` set it
on success; `ClientDetailView` reads it on mount (if `pendingToken.hostname` matches the current
route), shows the banner, and immediately sets it back to `null`. Simpler than routing the token
through navigation state, and no less safe — it's an in-memory field cleared right after its one
read, never written to the URL or to any persisted storage.

## Pages

### `/clients` (`ClientsListView.vue`) — extended

Same table (Hostname, Revoked, Last Seen) as today, wrapped with `simple-datatables` exactly like
`JobsListView.vue`: mount `new DataTable(tableRef.value)` in `onMounted` after `fetchAll()`
resolves, `destroy()` in `onBeforeUnmount`. Gives client-side search/sort for free — no
pagination logic needed, since the enrolled-client list is expected to stay small (same
assumption `GET /api/v1/clients` itself already makes by not paginating). A "New Client" link in
the header, next to the title, routes to `/clients/new` — same placement `PoliciesListView` uses
for "New Policy". No per-row actions on this page; revoke/unrevoke stays a detail-page action.

### `/clients/new` (new `ClientFormView.vue`)

Mirrors `PolicyFormView.vue`'s structure for the "new" case only (no edit mode — re-enrollment is
a detail-page action, not a form revisit):

- Hostname text input (required).
- A SANs add/remove sub-list — identical interaction pattern to `PolicyFormView`'s hostnames
  editor (`v-for` over a local array, per-row "Remove", an "Add SAN" button).
- Submit calls `clients.enroll(hostname, sans)`, which sets `clients.pendingToken` on success, then
  navigates to `/clients/:hostname`.

### `/clients/:hostname` (`ClientDetailView.vue`) — extended

**Actions row** (top of page, next to the hostname heading):
- **Revoke** / **Unrevoke** — single button reflecting current `revoked` state, `window.confirm`
  before calling the store action, matching `PoliciesListView`'s existing delete-confirm pattern.
- **Re-enroll** — button, calls `clients.reenroll(hostname)`, which sets `clients.pendingToken`
  the same way `enroll` does — the detail page picks it up identically whether it arrived via a
  post-enroll navigation or an in-page re-enroll, no separate code path needed.

**Token banner** — shown when `clients.pendingToken?.hostname` matches the current route's
hostname (checked on mount, and again right after a `reenroll` call resolves), styled distinctly
(e.g. a highlighted box, not inline prose) since it's the one piece of genuinely sensitive,
ephemeral data on the page: monospace token text, a "Copy" button
(`navigator.clipboard.writeText`), and a fixed warning line ("This token won't be shown again —
relay it to the node now"). Reading it clears `clients.pendingToken` back to `null`, so a
subsequent reload never re-shows it. No auto-dismiss timer; the user closes it explicitly.

**Description / Attributes** — two sections, each independently editable, same internal
structure for both (parameterized by which store action and KV kind they call):
- Existing key/value pairs rendered as rows, each with a "Remove" button — removing marks the row
  for deletion in the local draft, doesn't call the API yet.
- An "Add" row (key input + value input) appends a new pair to the local draft.
- Editing an existing row's value in place also just updates the local draft.
- A per-section **Update** button, disabled by default, enabled the moment the local draft differs
  from the last-saved snapshot (a plain deep-equal check against the snapshot object). Clicking it
  diffs draft vs. snapshot into `{set: {...changed/added}, unset: [...removed keys]}`, calls
  `updateDescription`/`updateAttributes`, and on success re-snapshots the draft from the returned
  client record (so the button disables again and further edits diff against the new baseline).

**SANs** — same inline-list-with-its-own-Update-button pattern as Description/Attributes, but for
a plain string list instead of key/value pairs: rows with "Remove", an "Add SAN" input, an
**Update** button enabled on draft-vs-snapshot difference, diffing into `{add: [...], remove:
[...]}` on click.

Each of the three sections (Description, Attributes, SANs) has its own independent draft/snapshot
and its own Update button — editing one doesn't gate or bundle with the others, since they're
three independent backend calls with no ordering dependency.

## Testing

Vitest + `@vue/test-utils`, mocking `apiFetch` (stores) or the store itself (views) exactly like
the existing `policies.spec.js` / `PolicyFormView.spec.js` / `PoliciesListView.spec.js` /
`JobsListView.spec.js` pairs:

- `clients.spec.js`: one test per new store action — success path (correct `apiFetch` call shape,
  cache updated from response), and an error path reusing the existing `error`-recording pattern.
- `ClientFormView.spec.js` (new): submitting enrolls, sets `pendingToken`, navigates to the new
  client's detail page; SAN add/remove interactions.
- `ClientDetailView.spec.js`: Revoke/Unrevoke button reflects and toggles `revoked` state; Re-enroll
  sets `pendingToken`; Update button starts disabled and enables only after a draft change for
  each of the three sections independently; clicking Update sends the correct `set`/`unset` or
  `add`/`remove` diff and re-disables after success; token banner shows when `pendingToken` matches
  the route on mount and clears it back to `null`; banner does *not* show for a mismatched or null
  `pendingToken`.
- `ClientsListView.spec.js`: extend the existing spec the same way `JobsListView.spec.js` verifies
  `simple-datatables` initialization/teardown (`DataTable` mocked via `vi.hoisted`); "New Client"
  link present and points to `/clients/new`.

## Documentation Impact

- `docs/components/web.md`: update the `/clients` and `/clients/:hostname` bullet points to note
  write capability; add `/clients/new` to the Pages list.
- `CHANGELOG.md` entry once implemented.
