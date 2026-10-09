# Design: Retention Policies and Web UI (Retention, Part 1)

> Part 1 of the retention feature. [Part 2](2026-10-05-retention-expiry-stamping-design.md) already
> stamps `expire_at` on every file version from a per-job rule matrix, but today that matrix only
> ever holds the built-in default. This part adds the real rules: a `retention` policy type, its
> management API, `agent` consuming it, and a web UI to manage and reorder it. Cleanup (acting on
> `expire_at`) stays out of scope.

## Decisions

- **One retention policy = one rule**, a separate policy type next to `backup`/`storage`/`restore`
  (own subfolder `policies/retention/`). Rules match clients with the same `client_filters`
  (hostnames glob + labels) as every other type, and carry:
  - `backup_type` — what is being backed up; only `"filesystem"` exists today and is the only
    accepted value (validated against a small allowed set, so a new workload type is one entry).
  - `path` — absolute, slash-separated path **prefix**, matched on a segment boundary.
  - `include` — optional glob list that must also match. **Basename globs only** (`*.log`, `tmp?`):
    a pattern containing `/` is rejected, because "relative to what?" has no good answer once a
    rule's prefix can sit above or below a job's root.
  - `keep_seconds` — how long versions are kept; `0` = never expire. The UI works in whole days.
  - `priority` — explicit integer; lower is evaluated first; **first match wins** after matching
    on host/attributes, type and path. Server-assigned, never typed by an operator.
  - `disabled_at` — the existing generic mechanism; a disabled rule is not served to nodes.
- **Priority is server-managed.** `CreatePolicy` appends a new rule at `max(priority)+1`;
  `UpdatePolicy` never changes it; a new `ReorderRetentionPolicies` RPC is the only way to change
  order. It takes the **complete ordered list** of retention policy ids and rewrites priorities
  `1..n`; a list that is missing an id, has an unknown one or a duplicate is rejected
  (`FailedPrecondition`/`InvalidArgument`) so a stale UI can never silently drop or reorder rules
  it didn't see. Reorder holds the same write lock as the other write RPCs.
- **Built-in default stays in `agent` config** (`RetentionDefaultDays`, default 7) as the last row
  of every job's matrix; the UI shows it as a fixed, non-reorderable last row with an explanation.
- **`agent` consumes real rules** by implementing the seam Part 2 left (`retentionRulesFrom`):
  cached policies of type `retention`, `backup_type == "filesystem"`, not disabled, sorted by
  `(priority, id)`, mapped to `retention.Rule{Prefix: path, Include, KeepSeconds}`. Nothing else in
  the matrix flow changes.

## Wire and storage

`policyserver.proto`:

```proto
message RetentionRule {
  string backup_type  = 1; // "filesystem"
  string path         = 2; // absolute prefix
  repeated string include = 3; // basename globs, optional
  int64  keep_seconds = 4; // 0 = never expire
  int32  priority     = 5; // server-assigned; lower evaluated first
}
// Policy.retention = 23; CreatePolicyRequest.retention = 17; UpdatePolicyRequest.retention = 13
// (CreatePolicyRequest/UpdatePolicyRequest ignore retention.priority.)

rpc ReorderRetentionPolicies(ReorderRetentionPoliciesRequest) returns (ReorderRetentionPoliciesResponse);
message ReorderRetentionPoliciesRequest  { repeated string ids = 1; }
message ReorderRetentionPoliciesResponse { repeated Policy policies = 1; } // new order
```

On disk: `policies/retention/<slug>.json` with `metadata`, `client_filters`, `backup_type`, `path`,
`include`, `keep_seconds`, `priority`. `ListPolicies(type="retention")` returns them sorted by
`priority`. `GetPolicies` serves matching, non-disabled retention policies like any other type.
`ReorderRetentionPolicies` joins the `control-plane`-only RPCs.

## REST (`api-server`)

- `GET /api/v1/policies?type=retention` (existing) — the `policyDTO` gains
  `retention: {backup_type, path, include, keep_seconds, priority}`.
- `POST /api/v1/retention-policies`, `PUT /api/v1/retention-policies/{id}` — body
  `{name, client_filters, retention: {backup_type, path, include, keep_seconds}, disabled_at?}`.
- `POST /api/v1/retention-policies/reorder` — body `{ids: [...]}`, returns the policies in the new
  order. Delete uses the existing `DELETE /api/v1/policies/{id}`.

## Web UI

New **Retention** page (sidebar entry, route `/retention`):

- An ordered list, one row per rule: position, name, target (hostnames/labels summary), backup type,
  path (+ include globs), keep (`N days` / `Forever`), enabled state, edit/delete.
- **Reorder** by drag-and-drop on a handle, and by up/down buttons on every row (keyboard and touch
  accessible, and what the unit tests drive). Reordering applies immediately via the reorder
  endpoint; on failure the list reverts and shows the error (a stale-list rejection triggers a
  refetch).
- A fixed last row, "Everything else — built-in default (7 days unless the node's
  `RetentionDefaultDays` says otherwise)", not draggable.
- Create/edit modal following `BackupPolicyFormModal`: name, target hostnames and labels, backup
  type (select, Filesystem), path, include globs (tag input), keep (days number input or a
  "Keep forever" checkbox), disabled-at is not exposed (same as the storage form).
- Client-side validation mirrors the server's (absolute path, no `..`, no `/` in globs, positive
  integer days).

## Error handling and testing

Server validation errors surface as `InvalidArgument` → HTTP 400 → shown in the modal's server-error
slot. Tests: policy-server (parse/validate/Create/Update/Delete/Reorder/ListPolicies ordering/
GetPolicies matching/authz), `policyclient` mapping, `agent` rule mapping and an end-to-end matrix
case, `api-server` handlers, and vitest for the store, view (render, reorder up/down/drag, revert on
error) and modal (validation, payload).

## Out of scope

Cleanup/deletion driven by `expire_at`; per-file rules with `/` in `include`; non-filesystem backup
types; editing the default's duration from the UI (it is per-node agent config); showing
`expire_at` in the catalog UI; e2e (Playwright) coverage, which needs the demo lab.
