# Design: Restore Policy Type

> **Schema superseded 2026-08-10:** `source_store` and `config` as described in this document were
> replaced by `storage_policy_id` (live-resolved, same mechanism as backup) and a typed `rules`
> field. See [Design: Restore Policy Verification Execution](2026-08-10-restore-policy-verification-design.md)
> for the current schema and the reasoning behind the change.

**Date:** 2026-08-09
**Status:** Approved for planning

## Problem

`policy-server` currently supports two policy types — `"backup"` (what to back up and where) and
`"storage"` (how a storage server should be configured) — each a distinct Go type implementing a
shared `Policy` interface, living under its own `policies/<type>/` subfolder. `rwfs` (Restore
Writer for File System) exists today but only implements `list`/`verify`; full file restoration is
not yet built (see `docs/ARCHITECTURE.md`).

A restore is fundamentally a directive to a specific node: "pull these files from that storage
server and write them back locally." A human operator can't just SSH into an arbitrary mesh node
and run a restore CLI directly — the established pattern in this system for getting a node to do
something is `policy-server` (holds the directive) + `agent` (polls, picks it up, executes
locally), the same mechanism already used for backup and storage-server supervision. So even though
a restore is normally a one-off, ad hoc action rather than a recurring policy, it still needs to go
through the policy primitive to reach the node that will run it.

This design adds a third policy type, `"restore"`, to `policy-server` and a creation endpoint to
`api-server`. It intentionally stops at the schema and API surface — the consumer side (`agent`
picking up `"restore"`-typed policies and driving a not-yet-built `rwfs restore`) is deferred to a
future design, the same way "Storage Policy Type" and "agent storage-policy supervision" were split
into two separate specs delivered the same day.

## Scope

- `policy-server`: new `"restore"` policy type (`RestorePolicy` Go type implementing `Policy`),
  registered alongside `BackupPolicy`/`StoragePolicy` in the existing type registry — no changes to
  the generic directory-walking, hot-reload, or RPC-handling code.
- `policyserver.proto`: one new field, `source_store`, added to `Policy`, `CreatePolicyRequest`. Not
  added to `UpdatePolicyRequest` — restore policies are not updatable (see below).
- `api-server`: one new endpoint, `POST /api/v1/restore`, and a same-type-rejection check added to
  the existing generic `PUT /api/v1/policies/{id}` handler.
- Docs: `docs/protocols/policy-server.md`, `docs/components/policy-server.md`,
  `docs/components/api-server.md`, `docs/api/rest-v1.md`, `CHANGELOG.md`.

## Out of scope

- Actual restore execution — `agent` fetching `"restore"`-typed policies from its cache and driving
  a local `rwfs restore`. Future spec.
- `rwfs restore` itself (currently "TBD" per the architecture doc) — the write-to-filesystem path.
- The restore spec's internal format (which files, path mapping, version selection, etc.) — carried
  as an opaque JSON blob for now (see below); its shape is defined when the execution side is
  designed.
- Any "run once, don't re-run" idempotency mechanism — an `agent`-side concern for the future
  execution spec, not something `policy-server`/`api-server` need to encode.
- Any adhoc-style auto-composition (e.g. server-computed `disabled_at`) — a plain create is enough;
  the caller sets `disabled_at` directly if they want auto-expiry.
- The relationship (if any) between `source_store` and the existing `"storage"` policy type / its
  checkin-derived `destinations` — `source_store` is a plain, independently-supplied `host:port`,
  not derived from a `storage_policy_id` reference the way a backup policy's `destinations` is.

## Schema

A `"restore"` policy has:

- **`metadata`** (`name`, `created_at`, `updated_at`, `disabled_at`) — shared with every policy
  type, unchanged semantics. `disabled_at` behaves exactly as it already does generically: unset
  means never disabled; once passed, `GetPolicies` stops returning the policy (checked live against
  current time on every call); `ListPolicies` keeps showing it regardless.
- **`client_filters`** (`hostnames`, `labels`) — shared mechanism, targets the node that will
  *execute* the restore (the destination for restored files), the same role it plays for backup
  (which node runs `brfs`) and storage (which node runs `bwfs`).
- **`source_store`** (string, `"host:port"`) — new field. The source `bwfs` to restore from.
  Validated as a syntactically valid `host:port` (`net.SplitHostPort`) at load and write time; not
  otherwise interpreted by `policy-server` (no reachability check, no cross-reference against any
  `"storage"` policy).
- **`config`** (existing field, string, opaque JSON) — reused, not duplicated. Currently
  storage-only; now also holds the restore spec (file list etc., format TBD). Same validation as
  today: well-formed JSON checked at load/write time, contents never interpreted by
  `policy-server`.

No `rpo`, `backup_window`, `object_filters`, `storage_policy_id`, or `port` — none apply to this
type. A restore policy has no recurring-schedule concept; it exists to be picked up once.

`config`'s doc comment/annotation (in both `docs/protocols/policy-server.md` and the `.proto` file)
needs updating from "storage policy only" to "storage and restore policies," since it's no longer
exclusive to one type.

## Directory layout, validation, lifecycle

- New subfolder `policies/restore/*.json`, one policy per file — a third type registered the same
  way `"backup"`/`"storage"` are. A malformed file is skipped and logged loudly, same as today; an
  unrecognized subfolder name is still skipped and logged, unchanged.
- Same hot-reload path: editing files by hand and touching `policies/.changed` triggers one atomic
  reload across every type subfolder, restore included.
- Same write path: `CreatePolicy` validates (non-empty `metadata.name`, valid `source_store`,
  well-formed `config`), atomically writes the file, then calls `Reload` synchronously in-process
  before responding — unchanged mechanism, restore just becomes a third type it can produce.
- **ID computation is unaffected and collision-safe**: `policy-server` computes each policy's `id`
  as `SHA1(policyType + "/" + basename)` (`parsePolicyFile`,
  `src/cmd/policy-server/policy.go:158`) — `policyType` is prepended before hashing, so
  `restore/foo.json` and `backup/foo.json` hash different inputs and get different, non-colliding
  IDs even with identical filenames. Adding `"restore"` as a third `policyType` value requires no
  change to this logic.
- Check-in tracking (`GetPolicies` upserting `(policy, hostname, last_seen_at)` on every call)
  applies to restore policies exactly as it does to every other type today — no special-casing,
  since it's already keyed generically on "any policy `GetPolicies` returns."
- `ListPolicies?type=restore` and `GET /policies?type=restore` fall out for free from the existing
  generic `type` filter — no new filtering logic.
- **Restore policies are not updatable.** `UpdatePolicyRequest` does not gain `source_store` (nor
  reuse `config` for restore's purposes) — a request to update a `"restore"`-typed policy is
  rejected with `INVALID_ARGUMENT`, the same way a request that sets fields belonging to the wrong
  type is already rejected on create. This is enforced in `policy-server` (source of truth), not
  just by `api-server` omitting a route to it.

## `policyserver.proto` changes

```proto
message Policy {
  // ...existing fields unchanged...
  string source_store = 18; // restore policy only
}

message CreatePolicyRequest {
  // ...existing fields unchanged...
  string source_store = 13; // restore policy only
}

// UpdatePolicyRequest: unchanged — no source_store field. Updating a "restore"-typed
// policy is rejected regardless of which fields the request sets.
```

`type = "restore"` in `CreatePolicyRequest` selects this shape; a request that also sets
backup/storage-only fields (`object_filters`, `rpo`, `backup_window`, `storage_policy_id`, `port`)
is rejected, the same validation already applied between `"backup"` and `"storage"`.

## `api-server` REST surface

- **`POST /api/v1/restore`** — the sole creation path for restore policies. Body: `name`,
  `client_filters`, `source_store`, `config`, optional `disabled_at`. Internally issues the same
  `CreatePolicyRequest{type: "restore", ...}` gRPC call the generic policy-create path would, just
  under a dedicated, action-oriented route rather than `POST /restore-policies` — reflecting that
  this is meant to be launched, not managed as a long-lived resource.
- **No update endpoint.** `PUT /api/v1/policies/{id}` (generic, existing route) now explicitly
  rejects the request with `400` if the target policy is `"restore"`-typed, surfacing
  `policy-server`'s `INVALID_ARGUMENT` rather than attempting a request shape that was never valid
  for this type.
- **`GET /api/v1/policies/{id}`** and **`DELETE /api/v1/policies/{id}`** — unchanged, type-agnostic,
  apply to restore policies automatically (status check and cancel/cleanup respectively).
- **`GET /api/v1/policies?type=restore`** — filters via the existing generic `type` query param.
- **DTO**: `policyDTO` gains `source_store`; `config` (already on the DTO for storage) is now also
  populated for restore. Fields that don't apply to a given policy's type are left zero/omitted,
  the existing convention.

## Testing plan

- `policy-server`: `parsePolicyFile`/`RestorePolicy` unit tests — valid file parses correctly;
  invalid `source_store` (not a valid `host:port`) rejected; invalid `config` JSON rejected; ID
  computed as `SHA1("restore/" + basename)`, distinct from a same-named `backup`/`storage` file
  (extend existing `TestParsePolicyFile_ComputesDeterministicPolicyID`-style coverage).
- `policy-server`: `CreatePolicy` with `type: "restore"` — rejects a request that also sets
  backup/storage-only fields; rejects empty `name`; rejects malformed `source_store`/`config`.
- `policy-server`: `UpdatePolicy` against an existing `"restore"`-typed policy — always rejected
  with `INVALID_ARGUMENT`, regardless of which fields the request sets.
- `policy-server`: `GetPolicies`/`ListPolicies` — restore policies match `client_filters` and
  check-in-track the same as backup/storage; `disabled_at` hides a restore policy from
  `GetPolicies` once passed, same as other types; `ListPolicies?type=restore` filters correctly.
- `api-server`: `handleCreateRestore` (`POST /api/v1/restore`) against a fake
  `policyServiceClient` — composes `CreatePolicyRequest{type: "restore", ...}` correctly; malformed
  JSON returns `400`; backend validation failure maps through `writeGRPCError`.
- `api-server`: `PUT /api/v1/policies/{id}` against a `"restore"`-typed policy returns `400`.
- `api-server`: `toPolicyDTO` includes `source_store`/`config` for a restore policy, omits
  backup/storage-only fields.

## Documentation impact

Per this project's standing documentation rules (new/changed proto, new endpoint):

- `docs/protocols/policy-server.md`: document the `"restore"` type, `source_store`, `config`'s
  now-shared (storage + restore) role, the no-update rule, and the proto field additions.
- `docs/components/policy-server.md`: extend "Policy types and directory layout" to cover
  `"restore"`, its directory, and its validation rules.
- `docs/components/api-server.md`: document `POST /api/v1/restore`, the `PUT` rejection behavior
  for restore-typed policies, and the `GET /policies?type=restore` filter.
- `docs/api/rest-v1.md`: new `## POST /api/v1/restore` section with example request/response.
- `docs/ARCHITECTURE.md`: no topology change — `policy-server` already serves multiple policy
  types; this is additive within its existing role. No diagram change needed.
- `CHANGELOG.md`: dated entry summarizing the new restore policy type and why (the primitive needed
  to route a restore directive to a specific mesh node, ahead of building actual restore
  execution).
