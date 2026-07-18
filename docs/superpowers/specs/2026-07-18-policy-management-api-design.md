# Design: policy management via api-server

**Date:** 2026-07-18
**Status:** Approved for planning

## Problem

`policy-server`'s only RPC, `GetPolicies`, is identity-scoped: it returns exactly the policies
matching the *caller's own* mTLS certificate (hostname + attribute labels), and nothing lets an
operator see or edit the full policy set from anywhere but hand-editing JSON files on
`policy-server`'s host. `api-server` is a read-only REST proxy in front of `clientmanager-api` and
`catalog` (see [Design: api-server](2026-07-14-api-server-design.md)) with no policy visibility at
all.

This adds full CRUD for policies, reachable from the browser via `api-server`, so the web UI (or
any REST client) can list, create, edit, and delete backup policies without SSHing to
`policy-server`'s host and hand-editing files.

## Approach

Three shapes were considered:

- **(Chosen) `policy-server` owns writes.** New RPCs (`ListPolicies`, `CreatePolicy`,
  `UpdatePolicy`, `DeletePolicy`) alongside the existing `GetPolicies`. `policy-server` already owns
  the on-disk file format, ID derivation, and validation (`parsePolicyFile`) — a write RPC reuses
  that same validation before ever touching disk, then writes the file and reloads its own cache
  in-process. `api-server` stays a thin, 1:1 REST-to-gRPC proxy, consistent with its existing
  convention (see `docs/components/api-server.md`).
- **`api-server` writes policy files directly**, sharing a filesystem volume with `policy-server`
  and bypassing it for writes (touching the `.changed` sentinel to trigger the existing async
  reload). Rejected: validation would only happen *after* the fact, during hot-reload — a bad
  policy is silently skipped and logged, with no synchronous error back to the caller. Also
  duplicates path/ID-derivation logic across two services.
- **A new DB-backed policy component**, `client-manager`-style (a separate daemon owning a real
  database, `policy-server` reading from it instead of flat files). Rejected as disproportionate:
  nothing about today's flat-file JSON policies is a bottleneck at this project's scale, and this
  would be a much larger change than the problem calls for.

## RPC surface (`src/api/policyserver.proto`)

Four new RPCs alongside the existing `GetPolicies` (unchanged):

```proto
service PolicyService {
  rpc GetPolicies(GetPoliciesRequest) returns (GetPoliciesResponse);
  rpc ListPolicies(ListPoliciesRequest) returns (ListPoliciesResponse);
  rpc CreatePolicy(CreatePolicyRequest) returns (Policy);
  rpc UpdatePolicy(UpdatePolicyRequest) returns (Policy);
  rpc DeletePolicy(DeletePolicyRequest) returns (google.protobuf.Empty);
}

message ListPoliciesRequest {}
message ListPoliciesResponse {
  repeated Policy policies = 1;
}

message ClientFilters {
  repeated string hostnames = 1;
  map<string, string> labels = 2;
}

message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  repeated ObjectFilter object_filters = 3; // id ignored if set; server-computed
  string rpo = 4;
  repeated string backup_window = 5;
  string destination = 6;
}

message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  repeated ObjectFilter object_filters = 4; // full replace, not a patch
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
}

message DeletePolicyRequest {
  string id = 1;
}
```

`Policy` gains a `client_filters` field:

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string id = 8;
  ClientFilters client_filters = 9;
}
```

`GetPolicies`'s response-building (`toProtoPolicy` as called from `GetPolicies`) keeps leaving
`client_filters` unset — a node shouldn't learn other nodes' targeting rules from a policy that
already matched it. `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy` populate it, since an
editor needs to see and change it. This means two response-building paths: the existing
`toProtoPolicy` (node-facing, `GetPolicies` only) and a new `toProtoPolicyAdmin` (or an added `bool`
parameter — implementation's call) used by the four new RPCs.

`Update`/`Delete` address a policy by its `id` (the existing deterministic UUID), never by name or
path — names aren't guaranteed unique across files today, and this doesn't change that.

No optimistic-concurrency field on `Update`/`Delete` — last-write-wins, consistent with this
project's generally lean approach elsewhere, and the realistic collision risk is low (infrequent
edits, effectively single-admin usage).

## `policy-server` internals

- **Validate before writing.** Extract `parsePolicyFile`'s validation (non-empty `metadata.name`,
  syntactically valid glob patterns for hostnames/include/exclude) into a standalone
  `validatePolicy(p Policy) error`, called both from `parsePolicyFile` (unchanged behavior) and from
  `CreatePolicy`/`UpdatePolicy` before anything touches disk. A request that fails validation
  returns `codes.InvalidArgument` (→ REST `400` via the existing `writeGRPCError`) and never reaches
  the filesystem.
- **Atomic writes.** Write to a temp file in the same directory as the target, then `os.Rename` over
  it — avoids `Cache.Reload` (or, in principle, an operator's own `cat`) ever observing a
  half-written file. The temp-file create/rename does generate fsnotify events, but
  `watchForReload` (`watch.go:38`) already filters every event down to the exact `.changed` path, so
  this produces no spurious/duplicate reload.
- **Filename for `Create`.** Slugify `name` (lowercase; non-`[a-z0-9-]` runs collapse to a single
  `-`; leading/trailing `-` trimmed) to build `<slug>.json`; on collision with an existing file,
  append `-2`, `-3`, etc. until one is free. This filename is permanent for that policy's lifetime —
  it's what the policy's `id` derives from (see
  [Design: deterministic IDs](2026-07-13-policy-object-filter-ids-design.md)) — but two policies may
  still share the same `metadata.name` after creation if one is renamed via `Update`, same as today.
- **File lookup for `Update`/`Delete`.** `Policy` (the in-memory type in
  `src/cmd/policy-server/policy.go`) gains an internal `SourcePath string` field, set at parse time
  alongside `ID`, never serialized to JSON or proto (mirrors how `ID` itself is `json:"-"`). `Cache`
  gains `FindByID(id string) (Policy, bool)`, giving `Update`/`Delete` the file to overwrite/remove.
  `Update` overwrites that same file — filename unchanged, so `id` stays stable, only content
  changes — preserving the existing `CreatedAt` and setting `UpdatedAt` to now. Reordering or
  inserting `object_filters` entries during an `Update` still changes the affected filters' IDs, the
  same already-documented trade-off from the deterministic-IDs design.
- **Reload after write.** After a successful write/delete, synchronously call the existing
  `cache.Reload(dir, logger)` before returning — the RPC only responds once the in-memory cache
  reflects the change. Because the write was already validated pre-write, this reload is expected to
  succeed. This bypasses the `.changed` sentinel entirely (that remains solely the mechanism for an
  operator's manual, possibly multi-file, batch edits); no incremental in-memory patch path is
  introduced — reusing the existing full-directory `Reload` keeps this simple, and directory size
  here (single-digit to low-hundreds of policies) makes a full reparse per write a non-issue.
- `UpdatePolicy`/`DeletePolicy` on an unknown `id` return `codes.NotFound` (→ REST `404`).

## `api-server` REST surface

New endpoints under `/api/v1/policies`, following every existing convention exactly: JSON responses,
list endpoints wrapped as `{"data": [...]}`, errors as `{"error": "<message>"}`, the same bearer
token guarding every request, the same `writeGRPCError` gRPC-code→HTTP-status mapping already used
by `/clients` and `/catalog`.

| Method | Path | Backend call | Success |
|---|---|---|---|
| `GET` | `/api/v1/policies` | `ListPolicies` | `200`, `{"data": [...]}` |
| `GET` | `/api/v1/policies/{id}` | (see below) | `200`, one policy; `404` if no match |
| `POST` | `/api/v1/policies` | `CreatePolicy` | `201`, created policy |
| `PUT` | `/api/v1/policies/{id}` | `UpdatePolicy` | `200`, updated policy |
| `DELETE` | `/api/v1/policies/{id}` | `DeletePolicy` | `204`, empty body |

- `GET /api/v1/policies/{id}` is served from the same `ListPolicies` call, filtered by `id` inside
  `api-server` itself — no dedicated single-item backend RPC, since policy counts are small and this
  keeps `policy-server`'s RPC surface minimal (mirrors why `GET /api/v1/clients/{hostname}` still
  goes through a real backend `GetClient` call only because `clientmanager-api` already offers one;
  `policy-server` doesn't need to grow an equivalent).
- `policyDTO` mirrors `clients.go`'s `clientDTO` pattern: a `toPolicyDTO(*pb.Policy) policyDTO`
  converter, JSON field names matching the on-disk policy schema (`id`, `name`, `created_at`,
  `updated_at`, `client_filters`, `object_filters`, `rpo`, `backup_window`, `destination`).
- `POST`/`PUT` request bodies decode into a smaller `policyInput` struct — no `id`/`created_at`/
  `updated_at`, since those are server-set. Malformed JSON returns `400` directly from `api-server`,
  before any backend call.
- `server.go`'s client-interface pattern extends: a new `policyServiceClient` interface (subset of
  `pb.PolicyServiceClient` — `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy`; `GetPolicies`
  is never called by `api-server`) is added to the `server` struct alongside the existing
  `clientManagerClient`/`catalogQueryClient`, same narrow-interface-for-testability convention.
- `api-server` needs a new outbound mTLS gRPC connection to `policy-server` (new
  `policy_server_host`/`policy_server_port` config keys; `policy-server`'s existing default port is
  `9300`), alongside its existing connections to `clientmanager-api` and `catalog`.

## Testing plan

- **`policy-server`**: `validatePolicy` unit tests (extracted from existing `parsePolicyFile`
  coverage); `CreatePolicy` — slug generation, collision-suffix behavior, atomic-write-then-reload,
  rejection of invalid input before any file is written; `UpdatePolicy` — overwrites the correct file
  via `FindByID`, preserves `CreatedAt`, stable `id` across a content-only update, `NotFound` on an
  unknown `id`; `DeletePolicy` — removes the file, reload reflects it, `NotFound` on unknown `id`;
  `ListPolicies` — returns all policies including `client_filters`, unfiltered by any identity; a
  regression test that `GetPolicies`'s response still omits `client_filters` after this change.
- **`api-server`**: one test per new endpoint against a fake `policyServiceClient`, covering the
  success path, the `404`/`400` gRPC-error-mapping paths, and malformed-JSON-body handling for
  `POST`/`PUT`.

## Documentation

- `docs/protocols/policy-server.md` — document the four new RPCs and the `client_filters` addition
  to `Policy`.
- `docs/components/policy-server.md` — note the write path exists, validation-before-write, and that
  writes bypass the `.changed` sentinel (reload directly, in-RPC).
- `docs/components/api-server.md` — update "read-only" framing (no longer fully read-only) and add
  `policy_server_host`/`policy_server_port` to Configuration Keys.
- `docs/api/rest-v1.md` — document the five new endpoints, request/response shapes.
- `CHANGELOG.md` — one dated entry.

## Out of scope

- Optimistic concurrency / conflict detection on `Update`/`Delete` (last-write-wins, per decision
  above).
- `metadata.name` uniqueness enforcement (unchanged from today — only filenames, and thus `id`s, are
  guaranteed unique).
- RBAC or per-user identity on `api-server`'s write endpoints — reuses the existing single shared
  bearer token, same as every read endpoint today.
- Any change to `agent`/`policyclient`'s consumption of `GetPolicies` — untouched by this design.
- Web UI changes to actually surface policy editing (a separate, follow-on piece of work once this
  API exists).
