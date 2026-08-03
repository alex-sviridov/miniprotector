# Design: link backup policies to storage policies by ID

**Date:** 2026-08-03
**Status:** Approved for planning

## Problem

A backup policy's `destination` (the target `bwfs`, `"host:port"`) is a plain opaque string an
operator types by hand. A storage policy already describes a concrete target: its
`client_filters.hostnames[0]` and `port` are exactly a `host:port` pair. There is no relationship
between the two on either side — a backup policy pointed at `"store:8080"` has no way to know that
string came from the storage policy named `store`, and nothing stops it from drifting out of sync if
that storage policy's hostname or port later changes.

This came up while redesigning `BackupPolicyFormModal.vue` to pick a destination from the storage
policies the operator already configured: the only frontend-only fix available is a heuristic —
compute every storage policy's `hostname:port` and string-match it against the stored `destination`
to guess which one an existing backup policy meant. That guess breaks the moment two storage
policies ever share a `host:port`, or a storage policy is edited after a backup policy was pointed at
it. This design replaces the guess with a real reference: a backup policy stores
`storage_policy_id`, and `destination` becomes a value `policy-server` derives from it, not something
an operator sets directly. No backward compatibility is being preserved — existing on-disk backup
policy files and their raw `destination` strings are migrated outright, not grandfathered.

## Approach

`BackupPolicy` gains `StoragePolicyID string` in place of the free-text `Destination` it stores
today. `destination` remains on the wire (`pb.Policy`) — `agent`/`brfs`/`policyclient` all still
parse a `"host:port"` string via `common.ParseDestination` and none of that changes — but it becomes
a **derived, read-only** value: `policy-server` resolves it from the referenced storage policy at the
point it builds a response, rather than storing it verbatim.

```go
// backup_policy.go
type BackupPolicy struct {
    PolicyBase
    ObjectFilters   []ObjectFilter `json:"object_filters"`
    RPO             string         `json:"rpo"`
    BackupWindow    []string       `json:"backup_window"`
    StoragePolicyID string         `json:"storage_policy_id"`
}
```

`Destination` is gone from the struct entirely — there is nothing to store. `ToProto` leaves
`pb.Policy.destination` unset; a new resolution step (below) fills it in afterward.

## Validation: `storage_policy_id` is required, checked uniformly

`BackupPolicy.Validate()` adds one rule alongside its existing checks: `StoragePolicyID` must be
non-empty. This applies uniformly wherever `Validate()` runs — both `Cache.Reload`'s load path
(`parsePolicyFile`) and the `CreatePolicy`/`UpdatePolicy` write path call the same method, and
neither gets special-cased. A backup policy file without `storage_policy_id` is invalid, full stop;
on reload it gets the same "logged and skipped, doesn't block the rest of the directory" treatment
already given to any other malformed file — consistent with, not a new exception to, `policy-server`'s
existing error philosophy.

**What `Validate()` still can't check:** whether `storage_policy_id` actually refers to an existing
`"storage"`-typed policy. `Validate()` has no access to the rest of the loaded set — during
`Cache.Reload`, each file is parsed and validated independently, and there's no guaranteed load order
between the `backup` and `storage` subfolders that would make "does this id exist yet" a meaningful
question at load time. This isn't new: it's the same reason `policy-server` has never validated that
`client_filters.hostnames` refer to real clients. Referential existence is instead checked only where
a full, current cache is actually available: the write RPCs.

## Resolution: dynamic, at read time

`Cache` gains:

```go
// ResolveDestination looks up storagePolicyID and, if it names a currently-
// loaded "storage" policy, returns its "hostname:port" from
// ClientFilters.Hostnames[0]/Port. ok is false if storagePolicyID doesn't
// resolve to a storage policy at all (unknown id, or a dangling reference
// left by hand-editing policy files outside the write RPCs — DeletePolicy
// itself blocks creating one, see below).
func (c *Cache) ResolveDestination(storagePolicyID string) (dest string, ok bool)
```

A small helper in `server.go`, called right after `ToProto` at all four call sites that produce a
`pb.Policy` (`GetPolicies`, `ListPolicies`, `CreatePolicy`, `UpdatePolicy`):

```go
func attachDestination(pp *pb.Policy, cache *Cache) {
    if pp.GetType() != "backup" || pp.GetStoragePolicyId() == "" {
        return
    }
    if dest, ok := cache.ResolveDestination(pp.GetStoragePolicyId()); ok {
        pp.Destination = dest
    }
}
```

Resolving at read time (rather than snapshotting a string once at write time) means editing a storage
policy's hostname or port automatically updates every backup policy linked to it, with no re-save
needed — there is exactly one place `host:port` for a given storage policy is computed.

**Dangling reference:** if `ResolveDestination` returns `ok == false` (only reachable by hand-editing
policy files outside the API, since `DeletePolicy` blocks removing a referenced storage policy —
below), `pp.Destination` is left unset. Downstream, `common.ParseDestination("", "localhost",
defaultPort)` silently defaults rather than erroring, so a dangling reference degrades to "backs up
to `localhost` on the default port" instead of a loud failure. That's a pre-existing sharp edge in a
shared helper several components use; fixing it is out of scope here.

## Delete-cascade: block, don't orphan

`DeletePolicy` (`write.go`), before removing a storage policy's file, scans the current cache for any
backup policy whose `StoragePolicyID` matches the id being deleted:

```go
if existing.Kind() == "storage" {
    if inUse := referencingBackupPolicies(s.cache, req.GetId()); len(inUse) > 0 {
        return nil, status.Error(codes.InvalidArgument,
            fmt.Sprintf("storage policy in use by: %s", strings.Join(inUse, ", ")))
    }
}
```

Deleting a backup policy is unaffected — nothing references a backup policy by id.

## Proto (`policyserver.proto`)

`Policy` gains one field; `destination` (field 7) stays but its doc comment changes to reflect that
it's now derived, never set directly:

```proto
message Policy {
  // ...existing fields unchanged, same numbers...
  // Derived, read-only. For a "backup" policy, resolved from
  // storage_policy_id at read time -- never stored or settable directly.
  // Unset for a "storage" policy, as before.
  string destination = 7;
  // backup policy only. References a "storage"-typed Policy.id; destination
  // is resolved from it. Required for every backup policy.
  string storage_policy_id = 15;
}
```

`CreatePolicyRequest`/`UpdatePolicyRequest` **drop** `destination` (field 6 / field 7 respectively)
as a writable field entirely — for both policy types, since storage policies never used it either —
and gain `storage_policy_id`:

```proto
message CreatePolicyRequest {
  // ...existing fields unchanged...
  // reserved: destination is no longer a writable field.
  reserved 6;
  reserved "destination";
  string storage_policy_id = 12; // backup only, required
}

message UpdatePolicyRequest {
  // ...existing fields unchanged...
  reserved 7;
  reserved "destination";
  string storage_policy_id = 12; // backup only, required
}
```

`buildPolicy` (`write.go`)'s type-mismatch guard extends: `backupFieldsSet` drops its
`destination != ""` check (nothing to check — the field no longer exists on the request) and gains
`storagePolicyID != ""`, so a `"storage"`-type request that sets `storage_policy_id` is rejected the
same way one setting `rpo` already is. The backup-required check (`storage_policy_id` must be
non-empty for a `"backup"`-type request) lives in `BackupPolicy.Validate()`, not here — `buildPolicy`
only rejects fields that don't belong to the type at all, not required-but-missing fields, matching
how `object_filters`/`rpo` emptiness isn't checked here either today.

## `api-server`

`src/cmd/api-server/policies.go` passes `storage_policy_id` through in the same shape as every other
field (JSON in/out, proto in/out) — no new logic. `destination` in its response DTOs stays, still
populated from `pb.Policy.GetDestination()`, just no longer accepted as create/update input;
`policyInput`'s JSON struct drops its own `Destination` field and gains `StoragePolicyID`.

## Demo fixtures

The three existing backup policy files under `demo/policy-server/policies/backup/` (`audit-logs`,
`database-backup`, `webserver-backup`) currently store `"destination": "store:8080"`. They're
rewritten to `"storage_policy_id": "<store.json's id>"` instead (that id is deterministic —
`uuid.NewSHA1(policyIDNamespace, []byte("storage/store.json"))`, the same computation
`parsePolicyFile` already does — so it can be computed directly rather than round-tripped through a
running server). `demo/policy-server/policies/storage/store.json` itself is unchanged.

## Out of scope

- Fixing `common.ParseDestination`'s silent-default-on-empty behavior.
- Any change to `agent`, `brfs`, `rwfs`, or `policyclient` — they keep reading `destination` off
  `pb.Policy` exactly as today; only where that string comes from changes.
- The frontend (`BackupPolicyFormModal.vue`) — this is the backend half of that redesign, brainstormed
  separately once this lands, since the form can then just read `storage_policy_id` directly instead
  of guessing.
- Any UI or API for resolving/repairing a dangling `storage_policy_id` beyond preventing new ones via
  the delete-cascade block.

## Testing plan

- **`backup_policy_test.go`**: `Validate()` rejects a `BackupPolicy` with empty `StoragePolicyID`
  (both a request-built one and one parsed from a file — same code path, one test each to keep the
  "load path and write path share validation" property honest).
- **`cache_test.go`**: `ResolveDestination` returns the right `"host:port"` for a known storage
  policy id; `ok == false` for an unknown id and for an id that resolves to a `"backup"`-typed
  policy (wrong kind).
- **`write_test.go`**: `CreatePolicy`/`UpdatePolicy` reject a backup request that also sets
  `port`/`config`; reject a storage request that sets `storage_policy_id`; `DeletePolicy` on a
  storage policy referenced by ≥1 backup policy fails with `InvalidArgument` naming them; succeeds
  once nothing references it.
- **`server_test.go`**: `GetPolicies`/`ListPolicies` return a backup policy's `destination` resolved
  live from its `storage_policy_id`; a backup policy with a dangling `storage_policy_id` (constructed
  directly in the test cache, bypassing the write RPCs) comes back with `destination` unset rather
  than erroring.
- **`api-server`**: `policies_test.go` fixtures updated for `storage_policy_id` replacing
  `destination` as create/update input.

## Documentation

- `docs/protocols/policy-server.md` — `destination`'s new derived/read-only semantics,
  `storage_policy_id`'s required-for-backup semantics, the delete-cascade behavior on
  `DeletePolicy`.
- `docs/components/policy-server.md` — schema section: `storage_policy_id` replaces `destination` as
  backup policies' on-disk/writable field; `destination` is now server-computed; deleting a
  referenced storage policy is rejected.
- `docs/components/api-server.md` — `storage_policy_id` replacing `destination` in create/update
  request bodies.
- `docs/api/rest-v1.md` — updated request/response examples.
- `README.md` — cross-link the updated protocol doc from the Documentation section.
- `docs/ARCHITECTURE.md` — no changes; no new component or topology/data-flow change, purely a
  schema change internal to `policy-server`'s existing role.
- `CHANGELOG.md` — one dated entry: backup policies now link to a storage policy by id instead of a
  free-text destination; this is a breaking change to `CreatePolicy`/`UpdatePolicy` (no
  `destination` input, `storage_policy_id` required) and to any hand-maintained policy JSON file.
