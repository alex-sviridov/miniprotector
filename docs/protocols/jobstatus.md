# Job Status Protocol

`policy-server`'s restore-cleanup sweep (`cmd/policy-server/restore_cleanup.go`) → `api-server`'s
`GetPolicyJobStatus` RPC, mTLS (`common/mtls`, same transport every other gRPC call in this project
uses). This is `api-server`'s only gRPC surface — its REST API (see
[REST API v1](../api/rest-v1.md)) is a deliberately separate, non-mTLS surface for browsers/admin
tools, guarded by a bearer token instead of a peer certificate, and remains unaffected by this
protocol.

## RPC

```proto
service JobStatusService {
  rpc GetPolicyJobStatus(GetPolicyJobStatusRequest) returns (GetPolicyJobStatusResponse);
}

message GetPolicyJobStatusRequest {
  string job_id = 1;
  // Lower bound for the Loki query. api-server clamps this to at most
  // maxJobsWindow (168h) before "now" -- a policy legitimately older than
  // that is still searched, just over a narrower recent window, since a
  // job that finishes at any point is always inside a future tick's
  // window regardless of how old the policy is.
  google.protobuf.Timestamp policy_created_at = 2;
}

message GetPolicyJobStatusResponse {
  bool finished = 1;
  // Set iff finished is true.
  google.protobuf.Timestamp finished_at = 2;
}
```

## Authorization

Like every other gRPC surface in this project, the caller's identity comes from its verified mTLS
peer certificate — there is no field on `GetPolicyJobStatusRequest` carrying it. `GetPolicyJobStatus`
is restricted to the `"control-plane"` role (`cmd/api-server/authz.go`'s `roleRequirements()`,
mirroring `cmd/policy-server/authz.go`'s identical pattern) — the sole caller today is
`policy-server`'s restore-cleanup sweep, itself enrolled with `authz-role=control-plane`. See
[Design: Role-Based gRPC Authorization](../superpowers/specs/2026-08-22-role-based-grpc-authz-design.md).

## Behavior

- `job_id` is required — a request that omits it is rejected with `INVALID_ARGUMENT` before any Loki
  query runs.
- `GetPolicyJobStatus` answers exactly one question: has `job_id`'s `event="finish"` line been
  observed? It reuses `jobs.go`'s existing `queryEvent` unchanged — no new Loki-query logic — against
  the label selector `{binary=~"agent|policy-server"} | job_id="<job_id>"`, filtered to
  `event="finish"`. `policy-server` is part of that selector (not just `agent`) because a restore
  policy's lifecycle now spans both binaries under the one shared `job_id` — `policy-server` logs
  `event="created"`/`event="deleted"` lines under it, and `agent` logs the eventual `event="finish"`
  line once `rwfs verify`/`rwfs restore` completes.
- The query window is `since = max(policy_created_at, now - maxJobsWindow)`, `until = now`
  (`maxJobsWindow` is `jobs.go`'s existing `168 * time.Hour`, the same constant `GET /jobs` enforces
  as a caller-facing 400 on an over-wide window). Here it's a *clamp*, not a rejection: a restore
  policy legitimately older than the cap must still be searched for, just over a narrower recent
  window — and since `until` is always "now" on every sweep tick, a job that finishes at any point is
  guaranteed to fall inside a *future* tick's clamped window, so a finish is never permanently missed,
  only found on a later tick if the policy is unusually old.
- `finished` is `true` iff at least one matching `event="finish"` line was found in the window;
  `finished_at` is set from that line's timestamp and left unset (zero value) when `finished` is
  `false`. A caller (`policy-server`'s sweep) must not read `finished_at` unless `finished` is `true`.
- No match in the window (job hasn't finished yet, or its finish line hasn't reached Loki) returns
  `finished: false`, not an error — an error is reserved for a genuine query failure (Loki
  unreachable, malformed request), which returns `codes.Internal`.

## See Also

- [api-server](../components/api-server.md) — serves this RPC; see its Job-Status gRPC Listener
  section for the listener's port/config/certificate details
- [policy-server](../components/policy-server.md) — the sole caller, via its restore-cleanup sweep
  (see its Restore policy lifecycle section)
- [REST API v1](../api/rest-v1.md) — `api-server`'s separate, non-mTLS surface
- [Design: Restore Policy Lifecycle](../superpowers/specs/2026-08-23-restore-policy-lifecycle-design.md)
