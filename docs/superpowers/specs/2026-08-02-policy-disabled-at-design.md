# Design: generic `disabled_at` field on policies

**Date:** 2026-08-02
**Status:** Approved for planning

## Problem

Every policy in `policy-server` is permanent once created — the only way to stop it applying is an
explicit `DeletePolicy`. That blocks a one-time/ad hoc backup (create a policy that fires once, then
stops mattering on its own, without an operator or a second RPC call tearing it down afterward) and
any other temporary-policy workflow (pause a policy without losing it, a policy that's only meant to
apply for a limited window) — none of which fit today's "exists until deleted" model.

This design adds one generic primitive — a `disabled_at` timestamp, settable on any policy type —
that makes a policy stop being served/acted on once that time passes, without `policy-server` or
`agent` needing to know *why* it's disabled. A one-time backup becomes an ordinary `"backup"` policy
that happens to carry a near-future `disabled_at`: `agent`, `policy-server`, and the on-disk schema
require no "adhoc" concept anywhere. Composing that (a `backup_window` full of `*`, an `rpo` equal to
the desired timeout, `disabled_at = now + timeout`) into a convenience "create adhoc policy" call is
`api-server`'s job, and is **explicitly deferred** — this design only adds the primitive.

## Scope

- `policyserver.proto` / `docs/protocols/policy-server.md`: add `disabled_at` to `Policy`,
  `CreatePolicyRequest`, `UpdatePolicyRequest`.
- `policy-server`: `Metadata` gains `DisabledAt time.Time`; `GetPolicies` excludes any policy whose
  `disabled_at` has passed, checked at request time against wall-clock time (not baked in at
  `Reload`/load time); `ListPolicies` is unaffected (still shows everything, disabled or not — it's
  the admin/`api-server` visibility surface).
- `policyclient`: `CachedPolicy` carries `disabled_at` through to `policies-cache.json`, same additive
  convention as `type`/`port`/`config`.
- `agent`: `backupTasks`/`storageTasks` skip any cached policy whose `disabled_at` has passed,
  evaluated fresh every reconcile tick against wall-clock time (not just at fetch time) — this is a
  one-line addition to each function's existing skip logic, and needs no other change: since a
  disabled policy already contributes zero task IDs to the tick's `currentIDs`, the *existing*
  `prune()` call in `reconcile.go` removes its `agent-state.json` entry automatically, exactly the
  same way it already does for a policy that's been deleted outright.

## Out of scope

- The `api-server` "create adhoc backup policy" convenience endpoint that composes `backup_window`/
  `rpo`/`disabled_at` for a one-time backup. Deferred — this design only adds the primitive it would
  build on.
- Validation rejecting a `disabled_at` already in the past at create/update time. A request that sets
  one just produces an already-inert policy — harmless, no different in spirit from `ListPolicies`
  returning an empty list for an unrecognized `type` filter rather than erroring.
- Cleanup of on-disk policy files whose `disabled_at` has long since passed. They keep existing (and
  showing up in `ListPolicies`) until an operator deletes them — pure housekeeping, not a correctness
  concern for anything in this design.
- Any UI/CLI surface for setting `disabled_at` directly.
- Partial/merge update semantics for `UpdatePolicy`. It stays full-replace, exactly like every other
  field today (`ClientFilters`, `Name`, ...) — a caller that wants an existing `disabled_at` to survive
  an unrelated edit must echo it back explicitly in the request, the same way it already must for
  `client_filters`. (`CreatedAt` is the one field the server itself preserves across an update,
  unprompted — `disabled_at` is operator-settable like every other editable field, not
  system-computed like `CreatedAt`, so it does not get that special treatment.)
- The other three ad hoc actions from the original brainstorm (stop-job, verify, restore) — those
  target `bwfs` directly (already a live, addressable, mTLS-authenticated server) via new admin RPCs
  proxied through `api-server`, unrelated to `policy-server`'s pull model. Separate future work.

## `policyserver.proto`

```proto
message Policy {
  // ...existing fields 1-13 unchanged...
  google.protobuf.Timestamp disabled_at = 14;
}

message CreatePolicyRequest {
  // ...existing fields 1-10 unchanged...
  google.protobuf.Timestamp disabled_at = 11;
}

message UpdatePolicyRequest {
  // ...existing fields 1-10 unchanged...
  google.protobuf.Timestamp disabled_at = 11;
}
```

`disabled_at` is a `Timestamp` *message* field (not a string, unlike `rpo`) specifically so "unset" is
representable as a nil field on the wire, distinct from an explicit zero timestamp — needed for the
nil-vs-zero handling described below.

## `policy-server`: schema and conversion

`Metadata` (`policy.go`) gains one field:

```go
type Metadata struct {
    ID         string    `json:"-"`
    Name       string    `json:"name"`
    CreatedAt  time.Time `json:"created_at"`
    UpdatedAt  time.Time `json:"updated_at"`
    DisabledAt time.Time `json:"disabled_at,omitempty"`
}
```

(`omitempty` has no effect on a zero-value `time.Time` — Go's `encoding/json` doesn't recognize a
zero struct as "empty" — so an unset `disabled_at` still marshals as `"0001-01-01T00:00:00Z"` on disk.
That's fine; nothing reads the on-disk JSON directly except `parsePolicyFile`, which round-trips it
through the same `time.Time` type either way. `PolicyBase.clone()` needs no change — it already copies
`Metadata` by value, which now includes `DisabledAt`.)

**Nil-vs-zero pitfall on the proto boundary:** `(*timestamppb.Timestamp).AsTime()` is nil-safe, but a
nil receiver maps to the **Unix epoch** (`1970-01-01`), not Go's zero `time.Time` (`0001-01-01`) —
calling it directly on `req.GetDisabledAt()` would silently turn "field not set" into "disabled since
1970," which is disabled forever, immediately, for any create/update that doesn't mention the field.
Both conversion sites need an explicit helper instead of calling `.AsTime()` directly:

```go
// disabledAtFromProto converts a possibly-nil disabled_at field to time.Time,
// treating "field not set" as the zero time -- distinct from AsTime()'s own
// nil-safe behavior, which maps a nil Timestamp to the Unix epoch (1970), not
// Go's zero time.Time (year 1).
func disabledAtFromProto(ts *timestamppb.Timestamp) time.Time {
    if ts == nil {
        return time.Time{}
    }
    return ts.AsTime()
}
```

`write.go`: `buildPolicyForCreate`/`buildPolicyForUpdate` add
`DisabledAt: disabledAtFromProto(req.GetDisabledAt())` to their `Metadata{...}` literal (alongside
`Name`/`CreatedAt`/`UpdatedAt`) — a base-level field, not type-specific, so `buildPolicy` and the
`policyFieldsGetter` interface it dispatches on need no changes.

`backup_policy.go` / `storage_policy.go`: both `ToProto` methods add, mirroring `CreatedAt`/`UpdatedAt`
but only setting the field when non-zero (so "never disabled" stays a nil field on the wire, not an
explicit epoch timestamp):

```go
if !p.Metadata.DisabledAt.IsZero() {
    pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
}
```

## `policy-server`: `GetPolicies` filtering

`server.go`'s `GetPolicies` gains one check per candidate policy, evaluated against `time.Now()` at
request time (not cached from `Reload`, so a policy crossing its `disabled_at` disappears from the
very next `GetPolicies` call with no reload/`.changed`-touch needed):

```go
func isDisabled(m Metadata, now time.Time) bool {
    return !m.DisabledAt.IsZero() && !m.DisabledAt.After(now)
}
```

```go
for _, p := range s.cache.Policies() {
    if isDisabled(p.Meta(), time.Now()) {
        continue
    }
    if !p.Matches(hostname, labels) {
        continue
    }
    matched = append(matched, p.ToProto(false))
}
```

`ListPolicies` is untouched — it keeps returning every loaded policy regardless of `disabled_at`,
consistent with it being the full-visibility admin/debugging surface `api-server` proxies.

## `policyclient`: cache schema

`CachedPolicy` (`fetch.go`) gains one additive field:

```go
type CachedPolicy struct {
    // ...existing fields unchanged...
    DisabledAt time.Time `json:"disabled_at,omitempty"`
}
```

`toCachedPolicies` populates it via the same nil-safe helper (`disabledAtFromProto`, duplicated here —
`policyclient` can't import `cmd/policy-server`'s `main` package, same reason `agent`'s `cachedPolicy`
already duplicates schema rather than sharing it):

```go
DisabledAt: disabledAtFromProto(p.GetDisabledAt()),
```

## `agent`: skipping a disabled policy

`backup.go`'s `cachedPolicy` gains `DisabledAt time.Time \`json:"disabled_at,omitempty"\``. Both
`backupTasks` (`backup.go`) and `storageTasks` (`storage.go`, which already calls the same
`readCachedPolicies`/`cachedPolicy` from `backup.go`) add one skip check, alongside their existing
type/parse skips:

```go
if !p.DisabledAt.IsZero() && !p.DisabledAt.After(time.Now()) {
    continue
}
```

Both functions are already called fresh every reconcile tick (`run()`'s `policiesFunc`/
`storageTasksFunc`), so this check is re-evaluated against current wall-clock time every 30 seconds by
default — it does not depend on `policyclient fetch` having run recently, only on the (possibly stale,
up to `PolicyFetchIntervalSec`) cached copy's `disabled_at` value, which is exactly the intended
"catch it locally even if the cache hasn't refreshed since the server started excluding it too"
behavior.

**No `reconcile.go` change is needed for cleanup.** `run()` already builds each tick's `currentIDs`
from whatever `backupTasks`/`storageTasks` return and calls `rs.prune(currentIDs)` — a disabled
policy simply stops contributing any ID to that set the moment the skip above kicks in, so its
`agent-state.json` entry (and, for a storage policy, its supervised `bwfs`/`catalogsync` processes)
is torn down through the exact same path already used for a policy that's been deleted outright. This
is a direct consequence of the skip living inside `backupTasks`/`storageTasks` rather than being
bolted onto `isDue` — the disabled policy never produces a task at all, rather than producing one that
"isn't due."

## Data flow (a policy expiring on its own)

1. Operator (today: via `CreatePolicy`/`UpdatePolicy` directly; later: via `api-server`'s deferred
   adhoc-create convenience call) sets a policy's `disabled_at` to some future time.
2. Until that time passes, the policy behaves exactly as it does today — `GetPolicies` keeps returning
   it to every matching node, `agent` keeps deriving tasks/supervision from it normally.
3. Once `disabled_at` passes: the very next `GetPolicies` call from any node stops including it
   (checked live against `time.Now()`, no reload needed). Any node whose cache still holds a
   pre-expiry copy also independently stops acting on it at its next reconcile tick, via the same
   check evaluated locally against its own clock.
4. `agent`'s next reconcile tick after that no longer sees the policy's task ID in `currentIDs`;
   `prune()` removes its `agent-state.json` entry (and, for a storage policy, its supervisor stops the
   running `bwfs`/`catalogsync` the same way an edited-away or deleted storage policy already does).
5. The policy's on-disk file and its entry in `ListPolicies` persist until an operator deletes it —
   no automatic cleanup, by design (see "Out of scope").

## Testing plan

- **`policy-server`**:
  - `write_test.go`: `CreatePolicy`/`UpdatePolicy` round-trip `disabled_at` — unset request field →
    zero `time.Time` (not the Unix epoch); a set field round-trips exactly; a `disabled_at` already in
    the past is accepted without a validation error.
  - `server_test.go`: `GetPolicies` excludes a cached policy whose `disabled_at` has passed, includes
    one whose `disabled_at` is unset or still in the future; `ListPolicies` includes a disabled policy
    either way, unchanged from today.
- **`policyclient`**: `fetch_test.go` — `toCachedPolicies` carries `disabled_at` through correctly; a
  nil `Policy.disabled_at` produces a zero `time.Time` in `CachedPolicy`, not the Unix epoch.
- **`agent`**:
  - `backup_test.go`: `backupTasks` contributes zero tasks for a cached policy whose `disabled_at` has
    passed; unaffected by one that's unset or still in the future.
  - `storage_test.go`: same check for `storageTasks`.
  - `reconcile_test.go`/`integration_test.go`: a task derived from a policy that later flips to
    disabled (between two ticks, same cache file) has its `agent-state.json` entry pruned on the next
    tick — confirms the existing `prune()` path is sufficient with no dedicated disabled-handling code
    in `reconcile.go` itself.

## Documentation

- `docs/protocols/policy-server.md` — add `disabled_at` to the `Policy`/`CreatePolicyRequest`/
  `UpdatePolicyRequest` message listings; note in Behavior that `GetPolicies` excludes an
  already-disabled policy (checked live, not cached) while `ListPolicies` does not.
- `docs/components/policy-server.md` — new short paragraph introducing `disabled_at` as a generic,
  type-agnostic field, its motivating use case (a one-time backup being an ordinary backup policy with
  a near-future `disabled_at`), and that `policy-server`/`agent` never encode an "adhoc" concept
  themselves — only `api-server`'s future create-endpoint would.
- `docs/components/agent.md` — one sentence each in "Policy-driven backup execution" and
  "Storage-policy supervision" noting a policy past its `disabled_at` contributes no task, pruned the
  same way a removed policy is.
- `docs/components/policyclient.md` — note `disabled_at` now carried through to
  `policies-cache.json`, same additive convention as `type`/`port`/`config`.
- `CHANGELOG.md` — one dated entry: policies (any type) can now carry a `disabled_at` timestamp,
  after which `policy-server` stops serving them and `agent` stops acting on them — the primitive a
  future one-time/ad hoc backup capability will build on.
