# Design: storage policy type

**Date:** 2026-07-28
**Status:** Approved for planning

## Problem

`policy-server` currently supports exactly one policy type, `"backup"`, and its `Policy` type is a
single flat Go struct mixing fields genuinely shared across any future policy type (metadata,
client filters, on-disk path) with fields that only ever mean something for a backup policy
(`object_filters`, `rpo`, `backup_window`, `destination`). `2026-07-20-policy-type-subfolders-design.md`
anticipated a second type and built the type-derived-from-subfolder plumbing for it, but explicitly
deferred two things until a second type actually existed: a `type` selector on the write RPCs, and
any decision about how a non-backup policy's schema would coexist with the backup one.

That second type is now needed: a `"storage"` policy, requiring `hostname`, `port`, and an opaque
`config` JSON blob. It describes where a future storage server should run and how it should be
configured; nothing in this change teaches any component to actually run one. `policy-server`'s job
here is only to load, validate, cache, and serve `storage` policies the same way it already does for
`backup` — not to build the consumer.

## Approach: a `Policy` interface, not a wider flat struct

Rather than adding `Hostname`/`Port`/`Config` as more optional fields on the existing flat struct,
`Policy` becomes an interface implemented by two concrete types — `BackupPolicy` and
`StoragePolicy` — each embedding a common `PolicyBase`. This was chosen over widening the flat
struct because the two types share almost nothing behaviorally beyond identity and matching
(metadata, client-filter matching, on-disk bookkeeping); a flat struct would let a `storage` policy
carry a stray `rpo` with no meaning, and would only get more awkward as a third type arrives. The
interface makes each type's schema and validation self-contained, and makes adding a third type in
the future a matter of writing a new type and registering its parser — not editing shared code.

This is a **Go-internal** change only. The gRPC wire schema deliberately stays flat/additive (see
"Proto stays flat" below) so nothing outside `policy-server` is forced to adopt the same shape.

## `PolicyBase` and the `Policy` interface

```go
type Metadata struct {
    ID        string    `json:"-"`
    Name      string    `json:"name"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type ClientFilters struct {
    Hostnames []string          `json:"hostnames"`
    Labels    map[string]string `json:"labels"`
}

// PolicyBase holds everything genuinely shared across every policy type:
// identity, client-filter matching, and on-disk bookkeeping. Embedded by
// value in every concrete policy type; never used on its own.
type PolicyBase struct {
    Metadata      Metadata      `json:"metadata"`
    ClientFilters ClientFilters `json:"client_filters"`
    SourcePath    string        `json:"-"`
    Type          string        `json:"-"`
}

func (b PolicyBase) Meta() Metadata        { return b.Metadata }
func (b PolicyBase) Filters() ClientFilters { return b.ClientFilters }
func (b PolicyBase) Path() string           { return b.SourcePath }
func (b PolicyBase) Kind() string           { return b.Type }

// Matches -- today's filter.go logic, unchanged in behavior, moved onto
// PolicyBase since it depends only on shared fields.
func (b PolicyBase) Matches(hostname string, labels map[string]string) bool { ... }

type Policy interface {
    Meta() Metadata
    Filters() ClientFilters
    Path() string
    Kind() string
    Matches(hostname string, labels map[string]string) bool
    Validate() error
    Clone() Policy
    ToProto(includeClientFilters bool) *pb.Policy
}
```

`Kind()` (not `Type()`) avoids colliding with `PolicyBase.Type`, the embedded field — Go doesn't
allow a method and field of the same name on the same type.

## `BackupPolicy` and `StoragePolicy`

```go
// backup_policy.go
type BackupPolicy struct {
    PolicyBase
    ObjectFilters []ObjectFilter `json:"object_filters"`
    RPO           string         `json:"rpo"`
    BackupWindow  []string       `json:"backup_window"`
    Destination   string         `json:"destination"`
}
```

```go
// storage_policy.go
type StoragePolicy struct {
    PolicyBase
    Hostname string          `json:"hostname"`
    Port     int             `json:"port"`
    Config   json.RawMessage `json:"config"` // opaque; never parsed or interpreted beyond well-formedness
}
```

Each implements its own `Validate()`, `Clone()`, and `ToProto()`:

- `validateCommon(base PolicyBase) error` (in `policy.go`) keeps today's rules: `metadata.name`
  required, every `client_filters.hostnames`/`object_filters` include/exclude pattern must be a
  syntactically valid `path.Match` glob.
- `BackupPolicy.Validate()` = `validateCommon` + today's object-filter pattern checks (unchanged
  from current `validatePolicy`).
- `StoragePolicy.Validate()` = `validateCommon` + `hostname` non-empty, `port` in `1..65535`,
  `config` non-empty and well-formed JSON (`json.Valid`) — content is never interpreted further.
- `Clone()` deep-copies exactly the fields that need it (slices/maps), same shape as today's manual
  copy in `Cache.Policies()`, just distributed per type instead of one function branching on
  everything at once.
- `ToProto(includeClientFilters bool) *pb.Policy` fills the shared fields (id, name, timestamps,
  type, and `client_filters` when `includeClientFilters`) plus only its own type's fields, leaving
  the other type's proto fields at zero value.

File layout: `policy.go` keeps `Metadata`/`ClientFilters`/`PolicyBase`/`Policy`/`validateCommon` plus
the parser registry (below); `backup_policy.go` and `storage_policy.go` hold one concrete type each.
`policy_test.go` splits the same way.

## Parsing: a registry instead of one fixed struct

```go
var policyParsers = map[string]func(data []byte) (Policy, error){
    "backup":  parseBackupPolicyJSON,
    "storage": parseStoragePolicyJSON,
}
```

`parsePolicyFile(filePath, policyType string)` looks up `policyType` in `policyParsers`, unmarshals
into that type's concrete struct, sets `SourcePath`/`Type`/the computed deterministic `Metadata.ID`
(and, for `BackupPolicy`, each `ObjectFilter.ID`) directly on the concrete value before wrapping it
as a `Policy`, then calls `Validate()`. A third type in the future means writing
`parseFooPolicyJSON` and adding one registry entry — `Cache.Reload`'s directory-walking logic is
unchanged.

**Behavior change from `2026-07-20-policy-type-subfolders-design.md`:** that design loaded an
unrecognized subfolder generically, tagging it with its literal name, since the old flat struct
didn't care what a policy's `type` string was. Typed parsing has no schema to unmarshal an unknown
type into, so a subfolder name absent from `policyParsers` is now **logged and skipped** — the same
"loud skip, don't block the rest of the directory" treatment already given to a malformed file or a
stray flat `*.json` directly under `policies/`. This is a deliberate, documented narrowing: deciding
what an unrecognized type means is no longer deferred to a downstream consumer, because
`policy-server` itself now needs to know a type's shape just to load it.

## `Cache`

`Cache.policies` becomes `[]Policy`. `Cache.Policies()`'s current ad-hoc deep-copy collapses to:

```go
out := make([]Policy, len(c.policies))
for i, p := range c.policies {
    out[i] = p.Clone()
}
```

`FindByID`/`FindBySourcePath` use `p.Meta().ID` / `p.Path()` instead of struct field access —
otherwise unchanged.

## RPC handlers (`server.go`)

`GetPolicies`/`ListPolicies` call `p.Matches(...)` and `p.ToProto(...)` polymorphically, replacing
today's free functions `toProtoPolicy`/`toProtoPolicyAdmin` (which become each type's own
`ToProto` method, still with an `includeClientFilters` bool distinguishing the node-facing
`GetPolicies` response — which never includes `client_filters` — from the admin-facing
`ListPolicies`/`CreatePolicy`/`UpdatePolicy` responses, exactly as today).

## Proto: stays flat, not a `oneof`

The wire schema does **not** mirror the Go-side polymorphism. `Policy` gains three additive fields:

```proto
message Policy {
  // ...existing fields unchanged, same numbers...
  string hostname = 11; // storage only; unset/empty for a backup policy
  int32  port     = 12; // storage only
  string config   = 13; // storage only -- opaque JSON text, verbatim passthrough
}
```

This is a deliberate divergence from the Go-side design: a `oneof` on `Policy` would force every
existing reader of `pb.Policy` outside `policy-server` (`api-server`'s `toPolicyDTO`, `agent`'s
cached-policy handling, the web UI) to restructure just to keep compiling, which is out of scope for
"support this type inside policy-server." Flat/additive fields mean every existing reader keeps
compiling and behaving exactly as today — they just never see `hostname`/`port`/`config` populated
for a `"backup"`-typed policy, and vice versa.

`CreatePolicyRequest` gains a **required** `string type = 11` (pointless with one type, now
necessary) plus the same three additive fields:

```proto
message CreatePolicyRequest {
  // ...existing fields unchanged, same numbers...
  string type     = 11; // "backup" or "storage" -- required
  string hostname = 12;
  int32  port     = 13;
  string config   = 14;
}
```

`UpdatePolicyRequest` gains the three additive fields but **not** `type` — a policy's type is
immutable via `UpdatePolicy`, derived from the existing record it's overwriting, matching today's
behavior for `SourcePath`:

```proto
message UpdatePolicyRequest {
  // ...existing fields unchanged, same numbers...
  string hostname = 8;
  int32  port     = 9;
  string config   = 10;
}
```

Proto3 scalar fields have no explicit presence, so "set" below means non-default (non-empty
string/slice, non-zero int) — indistinguishable from omission at the wire level, which is fine here
since every field these checks apply to is already required to be non-default for its own type
(`storage`'s `port` must be `1..65535`, never `0`; `backup`'s `rpo`/`destination` must be non-empty).

## `CreatePolicy`/`UpdatePolicy` (`write.go`)

`CreatePolicy` switches on `req.GetType()`:

- `"backup"`: build a `BackupPolicy` from `object_filters`/`rpo`/`backup_window`/`destination`;
  reject the request (`InvalidArgument`) if `hostname`/`port`/`config` are also set — no silent
  drops.
- `"storage"`: build a `StoragePolicy` from `hostname`/`port`/`config`; reject the request if any
  backup-only field is also set.
- anything else: `InvalidArgument`, "unknown policy type".

It writes into `filepath.Join(s.policiesDir, req.GetType())` (creating the subdirectory if missing),
generalizing today's hardcoded `policies/backup/`. `UpdatePolicy` looks up the existing policy by
id (as today), builds the same concrete type as the existing record (its `Kind()` decides which
fields from the request apply), and rejects backup/storage field mismatches the same way.

## Required `api-server` touch-up (not a feature)

`api-server`'s `handleCreatePolicy` (`src/cmd/api-server/policies.go`) builds a
`pb.CreatePolicyRequest` with no `type` field today. Once `type` is required, this call site needs
`Type: "backup"` added — one line — to keep creating (backup) policies at all; `api-server` has no
storage-policy input path, so it never sends `type: "storage"`. This is unavoidable fallout from the
proto change, not new `api-server` functionality. `toPolicyDTO`/`handleListPolicies`/
`handleGetPolicy`/`policyInput`/`handleUpdatePolicy` are untouched — the flat/additive proto keeps
them compiling and behaving as today.

## Out of scope

- `api-server` support for creating/editing storage policies (`policyInput`, DTOs, endpoints) beyond
  the one-line `type: "backup"` fix needed to keep building.
- Web UI: no storage-policy form, list, or detail rendering. `PolicyFormView`/`PoliciesListView`/
  `PolicyDetailView` are untouched and unaware storage policies exist.
- Anything about a future consumer (a storage server, or whatever component ends up reading
  `storage`-typed policies) actually using `hostname`/`port`/`config` to run anything.
- Validating `config`'s contents beyond well-formed JSON — it is, and stays, opaque to
  `policy-server`.
- Auto-migrating or reconciling existing on-disk policies — not applicable here, no schema for
  existing files changes.

## Testing plan

- **`cache_test.go`**: `Reload` dispatches `policies/backup/*.json` to `BackupPolicy` and
  `policies/storage/*.json` to `StoragePolicy`, tagging each with the right `Kind()`; a subfolder
  name absent from `policyParsers` is logged and skipped, not fatal to the reload; a malformed file
  within a recognized type subfolder is still skipped per-file, not per-directory.
- **`backup_policy_test.go`** / **`storage_policy_test.go`** (replacing `policy_test.go`):
  `Validate()` per type — storage requires non-empty hostname, port in range, well-formed non-empty
  config; `Clone()` deep-copies correctly (mutating a clone never affects the original).
- **`write_test.go`**: `CreatePolicy` with `type: "storage"` writes into `policies/storage/`,
  creating it if missing; rejects an unknown `type`; rejects a request mixing backup-only and
  storage-only fields for either type. `UpdatePolicy` on a storage policy round-trips
  hostname/port/config; a storage policy's `Kind()` is unchanged after update (type is immutable).
- **`server_test.go`**: `GetPolicies`/`ListPolicies` return a storage policy's `hostname`/`port`/
  `config` correctly via the flat proto fields; a storage policy's `client_filters` matching
  (hostname/label glob rules) behaves identically to a backup policy's.
- **`api-server`**: `policies_test.go`'s existing `CreatePolicy`/`UpdatePolicy` fixtures updated for
  the new required `type` field (one-line addition, `Type: "backup"`).

## Documentation

- `docs/protocols/policy-server.md` — document the three new `Policy` fields, the new
  `CreatePolicyRequest.type` field, and that `UpdatePolicyRequest` has no `type` (immutable).
- `docs/components/policy-server.md` — directory-layout section: `storage` is now a second real
  type (`policies/storage/*.json`); the read-path parser registry and third-type extensibility;
  the unrecognized-subfolder behavior change (logged + skipped, not loaded generically); `type` is
  now required on `CreatePolicy` and dictates which fields are valid together.
- `CHANGELOG.md` — one dated entry: adds the `storage` policy type to `policy-server`, notes the
  unrecognized-subfolder behavior change as a breaking change (consistent with the precedent set by
  the type-subfolders change), and that `CreatePolicy` now requires `type`.
- `README.md` / `docs/ARCHITECTURE.md` — no changes; no new running component or topology change in
  this stage.
