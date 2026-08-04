# Design: resolve a backup policy's destinations from storage-policy checkins

**Date:** 2026-08-04
**Status:** Approved for planning

## Problem

`Cache.ResolveDestination` (`src/cmd/policy-server/cache.go:135-145`) builds a backup policy's
`destination` as `fmt.Sprintf("%s:%d", sp.ClientFilters.Hostnames[0], sp.Port)` — treating a storage
policy's `client_filters.hostnames` as if it were a literal server address. It isn't: `hostnames` is a
list of glob patterns used by `hostnameMatches` (`src/cmd/policy-server/filter.go:19-29`) to decide
which client nodes a policy applies to, the same mechanism every policy type uses for targeting, not
addressing. In practice this breaks whenever a storage policy's `client_filters.hostnames` is anything
other than exactly one literal hostname:

- a wildcard pattern (e.g. `"bwfs-*"`) gets baked verbatim into `destination` as `"bwfs-*:8080"` — not
  a resolvable address
- multiple literal hostnames (a replicated/clustered storage backend) silently collapse to just `[0]`,
  the rest dropped with no error

Nothing in `StoragePolicy.Validate()` (`src/cmd/policy-server/storage_policy.go:38-52`) enforces the
single-literal-hostname assumption `ResolveDestination` depends on.

Separately, `policy-server` already has a source of *concrete, resolved* hostnames for a storage
policy: `RecordCheckin` (`src/cmd/policy-server/server.go:84`) fires inside `GetPolicies` for every
matched policy, including `"storage"`-typed ones — a storage node itself calls `GetPolicies`, matches
its own storage policy via the same `client_filters`, and gets checked in by its real hostname
(`src/cmd/agent/storage.go:79-112` is the loop that makes this happen: fetch policies, find `"storage"`
type, spawn `bwfs`). Checkin records are therefore real hostnames of nodes actually running under a
storage policy — never an unmatched glob, and naturally covering every node a wildcard or multi-host
pattern matches, with no extra input from an operator.

## Approach

Replace `ClientFilters.Hostnames[0]` as the address source with the storage policy's checkin list, and
change `destination` from a single string to an ordered list, freshest-first, so an agent can try the
next entry later without another round trip to `policy-server`.

### Storage layer: order checkins by recency, not hostname

`src/storage/policyserver/store.go:41` — `CheckinsForPolicy` changes its query order from `hostname` to
`last_seen_at DESC`:

```go
err := s.db.Where("policy_id = ?", policyID).Order("last_seen_at DESC").Find(&out).Error
```

Same signature, same per-`(policy_id, hostname)` scoping — only the order changes. This is the single
source of truth for checkin order; nothing downstream re-sorts. `CheckinsForPolicy` is also used by
`attachCheckins` for the admin `ListPolicies` checkin display (`src/cmd/policy-server/server.go:122-134`),
so that view's order changes too, from alphabetical-by-hostname to most-recently-seen-first — a
side effect of sharing the method, and a reasonable one for an admin view, not something worth
forking the query to avoid.

### Resolution: combine cache + checkins in `attachDestination`

`Cache` has no access to the checkins store today — `*checkinstore.Store` is a separate dependency
held on `policyServerServer` alongside `*Cache`, not inside it. Rather than giving `Cache` a checkins
dependency, the combining logic moves into `attachDestination`
(`src/cmd/policy-server/server.go:106-113`), which already sits at the one place with access to both:

```go
// attachDestination resolves pp.Destinations for a backup policy from its
// StoragePolicyId's checkin list, using cache's live state and checkins'
// live check-in records. Called right after ToProto at every RPC that
// returns a pb.Policy (GetPolicies, ListPolicies, CreatePolicy, UpdatePolicy).
// A dangling reference, or a storage policy with no checkins yet, leaves
// pp.Destinations empty rather than erroring.
func attachDestination(pp *pb.Policy, cache *Cache, checkins *checkinstore.Store) {
    if pp.GetType() != "backup" || pp.GetStoragePolicyId() == "" {
        return
    }
    p, ok := cache.FindByID(pp.GetStoragePolicyId())
    if !ok || p.Kind() != "storage" {
        return
    }
    sp := p.(*StoragePolicy)
    records, err := checkins.CheckinsForPolicy(sp.Meta().ID)
    if err != nil {
        return
    }
    for _, r := range records {
        pp.Destinations = append(pp.Destinations, fmt.Sprintf("%s:%d", r.Hostname, sp.Port))
    }
}
```

`Cache.ResolveDestination` (`cache.go:135-145`) is deleted outright — its one caller is replaced, and
nothing else calls it. All four call sites that construct a `pb.Policy` for a backup policy
(`GetPolicies`, `ListPolicies`, `CreatePolicy`, `UpdatePolicy`) pass `s.checkins` alongside `s.cache`,
unchanged otherwise.

**Empty case:** a storage policy with zero checkins (brand new, or every check-in has aged out past
`CheckinRetentionSec`) leaves `pp.Destinations` empty — the same "unset, degrades to
`ParseDestination`'s existing silent-default" behavior the dangling-reference case has today per
`2026-08-03-backup-policy-storage-link-design.md`. Not solved here; same pre-existing sharp edge,
still out of scope.

**Staleness:** a checkin means "polled `policy-server` within `CheckinRetentionSec`" (default 24h,
`src/cmd/policy-server/checkin.go`), not "is running `bwfs` right now." A node that fetched policies
once and crashed before starting `bwfs` remains a listed destination for up to the retention window.
This is an existing property of the checkin mechanism, not a new risk introduced here.

### Proto (`src/api/policyserver.proto`)

```proto
message Policy {
  // ...existing fields unchanged, same numbers...
  reserved 7;
  reserved "destination";
  // backup policy only. Derived, read-only: one "host:port" entry per live
  // checkin against storage_policy_id, freshest first. Empty if the storage
  // policy has no checkins yet or storage_policy_id is dangling. Unset for a
  // "storage" policy, as before.
  repeated string destinations = 17;
}
```

No backward compatibility is preserved — every consumer of `destination` is inside this repo, and the
project already broke this exact field once in `2026-08-03-backup-policy-storage-link-design.md`
without a compat shim.

### Client-side plumbing

Three places carry the field from wire to `brfs`, all singular → list, no shim:

- `src/cmd/policyclient/fetch.go:56,147` — `CachedPolicy.Destination string` → `Destinations []string`,
  built from `p.GetDestinations()`.
- `src/cmd/agent/backup.go:34-40` — `cachedPolicy.Destination string` → `Destinations []string`.
- `src/cmd/agent/backup.go:226-229` — use `Destinations[0]` as `--destination` for `brfs`; an empty
  list takes the same skip/error path today's empty-string case takes. Trying `Destinations[1:]` on
  failure is deliberately left for later — the agent already has the full ordered list cached locally
  in `policies-cache.json`, so that enhancement needs no further `policy-server` change when it lands.

## Out of scope

- Any retry/failover logic that actually uses `Destinations[1:]` — `backup.go` only ever reads `[0]`
  for now.
- Fixing `common.ParseDestination`'s silent-default-on-empty behavior.
- Any change to checkin retention, or a shorter "currently alive" threshold distinct from
  `CheckinRetentionSec`.
- Revalidating a checked-in hostname still matches the storage policy's current `client_filters` if
  the policy was edited since that node's last poll — same eventual-consistency bound as the rest of
  the checkin mechanism.
- `rwfs`/`brfs` themselves — they still receive one `--destination host:port` per invocation, unchanged.

## Testing plan

- **`store_test.go`**: update the existing `CheckinsForPolicy` ordering assertion from hostname order
  to `LastSeenAt DESC`.
- **`server_test.go`**: replace `cache_test.go`'s `ResolveDestination` coverage with `attachDestination`
  tests — multiple checkins resolve to a freshest-first `Destinations` list; zero checkins leaves it
  empty; a dangling `storage_policy_id` leaves it empty rather than erroring (same cases
  `cache_test.go:197-231` covered, moved to where the logic now lives).
- **`policyclient/fetch_test.go`**: update fixtures (`fetch_test.go:56,81`) from `Destination` string to
  `Destinations` list.
- **`agent/backup_test.go`**: update fixtures (`backup_test.go:330,336`) from `Destination` to
  `Destinations`; add a case confirming `brfs` is started with `Destinations[0]`.

## Documentation

- `docs/protocols/policy-server.md` — `destination` → `destinations`, its checkin-derived/ordered
  semantics.
- `docs/components/policy-server.md` — schema section update; note `CheckinsForPolicy` ordering change
  and its effect on the admin checkin display.
- `docs/components/agent.md` — note destination selection now reads `Destinations[0]` from a list,
  with future entries reserved for later failover.
- `docs/api/rest-v1.md` — updated response examples (`destination` field → `destinations`).
- `README.md` — cross-link updates if the protocol doc's summary line changes.
- `docs/ARCHITECTURE.md` — no changes; no new component or topology/data-flow change.
- `CHANGELOG.md` — one dated entry: a backup policy's destination is now a list of `host:port` entries
  derived from its storage policy's live checkins, ordered most-recently-seen first, replacing the
  single-hostname-pattern resolution; breaking change to `pb.Policy` (`destinations` replaces
  `destination`) and to any hand-parsed `policies-cache.json`.
