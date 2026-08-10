# Design: restore cart submission

**Date:** 2026-08-10
**Status:** Approved for planning

## Problem

Two pieces already exist independently and don't talk to each other yet:

- The **restore cart** (`docs/superpowers/specs/2026-08-09-restore-cart-design.md`) lets an operator
  select files/folders in the catalog UI into a compact rule list (`{path, host, include}`), but
  `RestoreView.vue` only renders that list — there's no way to actually submit it.
- The **restore policy type** (`docs/superpowers/specs/2026-08-09-restore-policy-type-design.md`)
  gives `policy-server` a `"restore"` policy (`client_filters` targeting the destination node,
  `source_store` — a single `host:port` of the bwfs to restore from, and `config` — an opaque JSON
  blob whose format was explicitly left "TBD" pending this design) and `api-server` a creation
  endpoint, `POST /api/v1/restore`. Nothing calls it yet.

This design connects them: it defines `config`'s format and builds the submission path from the
restore cart to one or more created `"restore"` policies. Actual execution — `agent` fetching
`"restore"`-typed policies and driving a (not yet built) `rwfs restore` — remains out of scope, the
same way it was deliberately deferred by the restore-policy-type design. This is frontend-only work:
no changes to `policy-server`, `api-server`, `bwfs`, or any `.proto` file are needed — every step
below is served by REST endpoints that already exist (`GET /api/v1/catalog`, `GET
/api/v1/policies?type=storage`, `GET /api/v1/clients`, `POST /api/v1/restore`).

## Scope

- `web`: resolve the restore cart's rules into concrete files, group them by physical storage node,
  resolve each group's dial address, and submit one `"restore"` policy per group.
- `web`: redesign `RestoreView.vue` from a placeholder list into a working submission page (remove
  entries, pick a destination host, submit, see per-policy results).
- Docs: `docs/components/web.md`, `CHANGELOG.md`.

## Out of scope

- Restore execution — `agent` picking up `"restore"`-typed policies and driving `rwfs restore`.
  Future spec, as already called out by the restore-policy-type design.
- Any backend/proto change. `source_store`/`config`/`POST /api/v1/restore` already exist exactly as
  needed.
- Rename/move-on-restore (restoring to a different path than the original) — deferred; a restore
  targets the same path it was backed up from.
- Version-specific restore — a submitted file entry is a `(source_host, path)` pair, not a pinned
  version; whichever version is latest when the (future) execution side actually runs is what gets
  restored.
- Any mechanism for a folder selection to keep tracking newly-added files after submission — see
  "Config format" below for why this is a deliberate, accepted trade-off.
- Cart persistence across page reload (already out of scope per the restore-cart design; unchanged
  here).

## Config format

Each generated `"restore"` policy's `config` is a flat, already-resolved file list:

```json
{
  "files": [
    { "source_host": "database", "path": "/var/lib/dbdata/dump.sql" },
    { "source_host": "database", "path": "/var/lib/dbdata/schema.sql" }
  ]
}
```

Not the cart's folder-wildcard rule shape. Splitting cart selections by physical storage node (see
below) requires knowing, per file, which node it's actually stored on — a folder rule doesn't carry
that, so grouping by store forces a one-time resolve-to-concrete-files step at submit time anyway.
Once resolved, keeping the flat list (rather than re-deriving wildcard rules from it) is simpler and
requires no rule-resolution logic on the future execution side.

**Accepted trade-off:** a folder selection becomes "restore exactly these files that existed at
submit time," not "restore whatever's under this folder whenever the policy executes." A file added
to a selected folder between submission and (future) execution isn't picked up. This was confirmed
acceptable — restore is a one-shot, bounded action, not a recurring rule.

Each entry stays path-based (not a pinned `file_uuid`/version), so the *value* restored is still
whichever version is latest at execution time — only the *set of paths* is fixed at submission.

## Submission flow

Triggered by "Submit restore" on the redesigned `RestoreView.vue`. All steps run client-side in
`web`, using only existing REST endpoints.

### 1. Resolve rules to concrete files

New `web/src/utils/restoreResolve.js`, independent of the `catalog` store's own reactive filter
state (browsing-view filters like date range are a browsing convenience, not a restore-scope
constraint — resolution always considers the full, unfiltered catalog):

- For every `include: true` rule not already covered by a broader positive rule (the cart's own
  pruning invariant already guarantees no redundant positive rules exist — see
  `restoreRules.js`), fetch matching catalog entries: `GET /api/v1/catalog?pattern=<path>` (plus
  `source_host=<rule.host>` when the rule is file-scoped), paginating via `starting_after`/`has_more`
  the same way `catalog.js`'s `search()` already does.
- `pattern` is a substring match on the entry's underlying ID, not an anchored prefix match — filter
  the returned entries again client-side, checking the real `path` field is exactly `rule.path` or a
  true path-segment descendant of it (reusing the ancestor-chain logic already in `restoreRules.js`),
  to avoid a false-positive match like `/var/lib/dbdata2` when the rule path is `/var/lib/dbdata`.
- Drop any resulting entry that falls under a more specific `include: false` rule in the same cart
  (same longest-matching-rule resolution `restoreRules.js` already implements for the UI's checkbox
  state — reused here against concrete entries instead of hypothetical paths).
- A rule that resolves to zero files (e.g. a stale selection referencing files no longer in the
  catalog) is silently dropped — nothing to restore, not an error.

Output: a flat list of `{ sourceHost, path, storeHost }` (each entry's `store_host`, already present
on catalog entries, carried through for the next step).

### 2. Group by physical store

Group the resolved list by `storeHost`. This is what "one policy per store" means in practice, and
it falls out naturally at the file level — no special-casing needed for the edge case of one source
host's files being split across two different storage destinations over time; each file just lands
in whichever group its own `storeHost` puts it in.

### 3. Resolve each store's dial address

New helper (same module or `web/src/utils/storeAddress.js`): fetch `GET
/api/v1/policies?type=storage` (via `useStoragePoliciesStore().fetchAll()`) and, for each `storeHost`
group, find a storage policy with a `checkins[]` entry whose `hostname` matches `storeHost`, pairing
it with that policy's `port` to build `source_store = "hostname:port"` — the same computation
`policy-server`'s `attachDestination` already does server-side for backup policies, done here
client-side since no endpoint returns it pre-joined for a storage policy.

If no storage policy has a matching checkin, that group can't be resolved — it's reported as a
per-group error (see step 5) and excluded from submission; other groups still proceed.

### 4. Submit one policy per group

New `web/src/stores/restorePolicies.js`, matching the existing per-type store pattern
(`policies.js`/`storagePolicies.js`): a `create(input)` action `POST`ing to `/restore`. For each
resolvable group:

```json
{
  "name": "restore-<UTC timestamp>-<storeHost>",
  "client_filters": { "hostnames": ["<destinationHost>"], "labels": {} },
  "source_store": "<resolved host:port>",
  "config": "{\"files\":[...]}"
}
```

`client_filters.hostnames` is always a single-element array — the operator-chosen destination,
mirroring `StorageEditModal`'s existing single-hostname pattern. No `disabled_at` — nothing consumes
`"restore"` policies yet, so there's no auto-expiry concern to encode.

### 5. Report results

Each group's create call succeeds or fails independently. The submit view shows one line per group:
created policy name/id, or the specific error (unresolved store address, or the `POST` failing
validation/network). No all-or-nothing rollback — a partial submission (some groups created, one
failed to resolve) is visible and actionable rather than silently discarded.

## `RestoreView.vue` redesign

Replaces the current placeholder list:

- Each cart entry (`path/*` or `path (host)`, as today) gets a **Remove** button, dispatching to the
  existing `restoreCart.toggleFolder(path)` / `toggleFile(host, path)` actions (removing a rule is
  exactly toggling it back off — no new cart-mutation logic needed, just a UI affordance for the
  existing actions from outside the catalog checkboxes).
- **Destination host** — a `<select>` populated from `useClientsStore()`/`GET /api/v1/clients`
  (already fetched elsewhere in the app, just not wired into a form yet), single choice.
- **Submit restore** button — disabled until the cart is non-empty and a destination is chosen.
  Runs the flow above and renders the per-group results in place.
- Empty-state text unchanged ("No files selected for restore yet.").

## Testing plan

- `web/src/utils/restoreResolve.spec.js`: pattern-match false-positive filtering (`/var/lib/dbdata2`
  not matched by a `/var/lib/dbdata` rule), exception-pruning against resolved entries, zero-match
  rule dropped silently, pagination across multiple pages, grouping by `storeHost` splits correctly
  including the same-source-host-different-stores edge case.
- `web/src/utils/storeAddress.spec.js` (or co-located): matches a `storeHost` to the right storage
  policy's `hostname:port`; returns unresolved when no checkin matches.
- `web/src/stores/restorePolicies.spec.js`: `create()` posts the expected body to `/restore`,
  matching the existing store test patterns (`policies.spec.js`/`storagePolicies.spec.js`).
- `web/src/views/RestoreView.spec.js`: Remove button calls the right toggle action; destination
  select populated from `useClientsStore`; Submit disabled with an empty cart or no destination;
  submit renders per-group success/error results; a group with an unresolved store address is
  reported without blocking other groups.

## Documentation

- `docs/components/web.md` — extend the restore cart mention (already added per the restore-cart
  design) to cover submission: resolve/group/submit flow, and that it creates one `"restore"` policy
  per physical store with no backend changes involved.
- `README.md` — no change (no new component, no quick-start impact).
- `docs/ARCHITECTURE.md` — no change (frontend-only, no topology/data-flow change; `"restore"`
  policies already exist in the model, this just gives `web` a way to create them).
- `CHANGELOG.md` — one dated entry: the restore cart can now be submitted, creating one `"restore"`
  policy per physical storage node with a resolved flat file list, still with no execution consumer
  yet.
