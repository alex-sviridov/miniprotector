# Restore Policy Lifecycle: One-Shot Execution, Job Correlation, and Automatic Cleanup — Design

## Problem

A restore policy (`RestorePolicy`, `src/cmd/policy-server/restore_policy.go`) is a one-shot directive:
`{storage_policy_id, rules[], mode: "verify"|"restore", overwrite}`, written as a JSON file under
`policies/restore/` the same way backup/storage policies are. `agent` (`cmd/agent/restore.go`) derives
one task per cached restore policy targeted at it and dispatches `rwfs verify`/`rwfs restore`, treating
the task as due until it succeeds once (`Due: LastSuccessAt == nil`) and never again after — but it
retries on every reconcile tick for as long as it keeps failing.

This has two problems in production:

1. **No way to remove a completed restore policy.** `DeletePolicy` works generically for any policy
   type, but nothing ever calls it for a restore policy, and no UI page even lists them (`GET
   /policies` is only ever queried with `?type=backup`; there is no restore-policy list view). A restore
   policy — a one-shot instruction that has already run — sits in `policies/restore/` forever.
2. **A restore policy that keeps failing retries forever**, with no way to know, from `policy-server`'s
   side, whether it ever succeeded — that signal (`LastSuccessAt`) lives only in the executing node's
   local `agent-state.json`, never reported centrally. `policy-server`'s only other source of
   centrally-visible state, check-in records, capture "a node fetched this policy," not its outcome.
3. **No correlation between "this restore was requested" and "this restore ran."** The Jobs UI only
   shows a restore/verify job once the target node actually starts executing it (`agent`'s own
   `event=start`) — a policy waiting on an offline or slow-to-check-in node is invisible until it finally
   runs, with no visible "pending" state in between.

This design addresses all three by changing the task's retry semantics to run-once-ever, giving
`agent`/`policy-server` a shared job identity for a restore policy's full lifecycle, and giving
`policy-server` an active, centrally-informed sweep that removes a restore policy once its one-shot job
has reached a terminal state.

**Out of scope:** who is authorized to *create* a restore policy (there is currently no user
authentication in `api-server` at all — anyone reaching the web UI can submit a destructive
`mode=restore, overwrite=true` policy unilaterally). That is a separate, larger design (an approval/authz
model), deliberately not addressed here.

## Goals

- A restore/verify task runs at most once per policy, ever — success or failure, no retry.
- `policy-server` can determine, without trusting any single node's local state, whether a restore
  policy's task has reached a terminal state.
- A restore policy is automatically deleted once its task has finished (successfully or not) and a
  grace period has elapsed — no policy lingers forever needing manual cleanup.
- The Jobs UI shows a restore/verify job as `in_progress` from the moment the policy is created, not
  only once a node has connected and started running it.
- `policy-server`'s own lifecycle actions (created, deleted) are visible in that same job's log/timeline,
  under the same `job_id` the eventual `rwfs` run uses.

## Non-Goals

- Any approval/authorization gate on restore policy creation (separate design).
- Any change to `verify`/`restore` execution itself (`rwfs`), rule resolution, or the restore cart UI.
- A general-purpose job-query API — `GetPolicyJobStatus` (below) is purpose-built for this one caller.
- Manual deletion UI for restore policies. With run-once-ever semantics, every restore policy reaches a
  terminal state eventually and the automatic sweep removes it — there is no more "stuck forever"
  case that would need a manual safety valve.

## Architecture

```
policy-server: CreatePolicy(restore)
  -> generates job_id, stores it on the RestorePolicy record
  -> logs event="created" under that job_id ("restore policy created, waiting for client to connect")

agent: restore.go
  Due: LastAttemptAt == nil        [was: LastSuccessAt == nil]
  -> uses the policy's own JobID   [was: generates restoreJobID(name, mode, now) itself]
  -> runs rwfs verify/restore exactly once, ever; records LastAttemptAt regardless of outcome
  -> agent's own event="start" is suppressed for restore/verify (reconcile.go); event="finish" unchanged

policy-server: restore_cleanup.go (new, background tick, mirrors checkin.go's shape)
  every <interval>:
    for each cached "restore"-type policy:
      call api-server.GetPolicyJobStatus(job_id, policy_created_at)   [new mTLS gRPC, "control-plane" role]
      if finished && now - finished_at > <grace period>:
        log event="deleted" ("restore policy deleted as job <job_id> found")
        DeletePolicy(id)   [in-process, existing method, unchanged]

api-server: jobstatus.proto + jobstatus_server.go (new; api-server's first gRPC server)
  GetPolicyJobStatus(job_id, policy_created_at) -> {finished: bool, finished_at}
  reuses existing queryEvent/pairJobEvents against Loki — no new Loki-query logic
```

## Component Changes

### 1. `src/api/jobstatus.proto` (new)

api-server's first gRPC service. mTLS, restricted to the `"control-plane"` role via the same
`roleRequirements()`/interceptor pattern every other control-plane service already uses (see
`cmd/policy-server/authz.go`).

```proto
service JobStatusService {
  rpc GetPolicyJobStatus(GetPolicyJobStatusRequest) returns (GetPolicyJobStatusResponse);
}

message GetPolicyJobStatusRequest {
  string job_id = 1;
  google.protobuf.Timestamp policy_created_at = 2;
}

message GetPolicyJobStatusResponse {
  bool finished = 1;
  google.protobuf.Timestamp finished_at = 2; // set iff finished
}
```

`job_id` alone determines the Loki query (`| job_id="<id>" | event="finish"`); `policy_created_at` sets
the query's lower time bound.

### 2. `api-server` changes

- New `jobstatus_server.go` implementing `GetPolicyJobStatus`: queries Loki for an `event="finish"` line
  matching `job_id`, windowed `since = max(policy_created_at, now - maxJobsWindow)`, `until = now` (reuses
  `maxJobsWindow` from `jobs.go` — this is a *clamp*, not a rejection: unlike `GET /jobs`'s caller-facing
  400 on an over-wide window, a policy legitimately older than the cap must still be found, and since
  `until` is always "now" on every sweep tick, a job that finishes at any point is guaranteed to fall
  inside a future tick's clamped window, so nothing is ever missed — only searched-for-late).
- api-server gains its first gRPC server (new port/listener, mTLS via `common/mtls.ServerTLSConfig`,
  alongside its existing plain-HTTP listener — the HTTP listener is unchanged, still the deliberate
  non-mesh, browser-facing surface it always was).
- `jobEventAccumulator` (`jobs.go`) gains `ApplyCreated`: sets `StartedAt`/`State="in_progress"` from an
  `event="created"` line, but — unlike `ApplyStart` — never touches `SourceHost`.
- `ApplyFinish` is extended: for `Kind == "restore"` or `Kind == "verify"`, also set `SourceHost` from
  the finish line's hostname (mirrors the existing `Kind == "backup"` rule that sets `StoreHost` from
  finish).
- `binariesForKind("restore")`/`binariesForKind("verify")` change from `"agent"` to `"agent|policy-server"`.
- Three label-selector sites gain `policy-server`: `handleGetJobLogs`'s selector (`jobs.go`),
  `jobs_aggregator.go`'s `reconcile()` selector, and its `tailLoop()` tail selector.
- `jobs_aggregator.go`'s `ingestTailMessage` event whitelist (`event != "start" && event != "finish"`)
  gains `"created"`, folding through the new `ApplyCreated` path exactly like `ApplyStart`/`ApplyFinish`
  do today.
- The `event="deleted"` audit line needs no special handling anywhere: it is plain text, already visible
  through the existing unfiltered `GET /jobs/{id}/logs` (which returns every raw line for a `job_id`, no
  event filtering) once the label selector above includes `policy-server`. It never touches `jobDTO`
  state.

### 3. `policy-server` changes

- `RestorePolicy` (`restore_policy.go`) gains a `JobID string` field, generated once at `CreatePolicy`
  time (stable id, no longer timestamped-per-attempt — retries no longer exist, so there is exactly one
  attempt to identify).
- `CreatePolicy` (`write.go`) logs, via its existing `slog` logger (already shipping `policy-server.log`
  through the same Vector pipeline every control-plane binary uses — confirmed policy-server nodes
  already run their own `agent`+Vector for `operating-refresh`/`policy-update`), one line:
  `logger.Info("restore policy created, waiting for client to connect", "policy", ..., "job_id", ...,
  "event", "created")`.
- New `restore_cleanup.go`, structurally mirroring `checkin.go`'s existing background-tick pattern
  (`runCheckinCleanup`): fixed interval, iterates the policy cache's restore-type entries, calls
  `GetPolicyJobStatus` via a new mTLS gRPC client dialing `api-server`, and on `finished &&
  now-finished_at > gracePeriod`, logs `event="deleted"` (`"restore policy deleted as job <job_id>
  found"`) then calls the existing in-process `DeletePolicy` path. A query failure (api-server
  unreachable, etc.) is logged and skipped — retried next tick, same as every other best-effort cleanup
  in this codebase.
- New mTLS gRPC client config (certs dir, api-server host/port) — same shape as `policy-server`'s
  existing outbound clients (e.g. to `catalog`/`clientmanager-api`).

### 4. `agent` changes

- `restore.go`: `restoreTasks` uses the cached policy's own `JobID` (new field, mirrored from
  `policy-server`'s `RestoreRule`/`RestorePolicy` the same way other fields already are — `agent` can't
  import `cmd/policy-server` directly) instead of calling `restoreJobID(name, mode, now)`. `Due` becomes
  `func(s PolicyState, now time.Time) bool { return s.LastAttemptAt == nil }` — `PolicyState.LastAttemptAt`
  already exists and is already set unconditionally on every attempt, success or failure
  (`reconcile.go`'s `recordOutcome`, line 216, predates this design), so this is a one-line change to
  `restore.go`'s `Due` function, nothing in `cache.go`/`reconcile.go`'s state tracking needs to change.
- `reconcile.go`: new `isRestorePolicy(p Policy) bool` (prefix check on `"restore:"`/`"verify:"`,
  parallel to the existing `isBackupPolicy`). `logExecStart` suppresses `event="start"` for these
  (mirroring how it's already suppressed for backups) — `logExecCompletion`/`event="finish"` is
  **unchanged** for restore/verify (only the start marker moves to `policy-server`, not the finish
  marker).

## Data Flow (end to end)

```
1. web: user submits restore cart -> POST /api/v1/restore -> policy-server.CreatePolicy
2. policy-server: writes RestorePolicy{JobID: "restore:<slug>:<id>", ...} to disk
   logs event=created under that job_id
   [Jobs UI: job now visible, state=in_progress, no source_host yet]
3. target node's agent eventually checks in, GetPolicies returns the policy
   agent: restoreTasks() builds a Policy{JobID: cached.JobID, Due: LastAttemptAt==nil}, dispatches rwfs
   agent: (no event=start logged) -> rwfs runs -> agent logs event=finish, status=success|failure
   [Jobs UI: state updates to success|failure, source_host now populated from the finish line]
4. policy-server: next sweep tick calls api-server.GetPolicyJobStatus(job_id, created_at)
   api-server: finds the finish line -> {finished: true, finished_at: ...}
5. policy-server: gracePeriod has elapsed -> logs event=deleted -> DeletePolicy(id)
   [Jobs UI: job's log view now also shows the "deleted" audit line; job list unaffected -- state
    was already terminal from step 3]
```

## Error Handling

- `GetPolicyJobStatus` query failure (network, api-server down): logged, sweep tick skipped for that
  policy, retried next tick — no different from any other best-effort reconcile-loop failure in this
  codebase.
- A restore policy whose target node never checks in at all: never attempted, `finished=false`
  indefinitely, never swept — matches today's behavior for an unreachable destination (no task exists
  to run in the first place if `Destinations` is empty; if the node itself is simply offline, the task
  stays due until it finally runs, exactly once, whenever the node returns).
- `DeletePolicy` failure after a positive `GetPolicyJobStatus` result: existing `DeletePolicy` behavior
  is unchanged (best-effort check-in cleanup already tolerates partial failure); the policy remains for
  the next sweep tick to retry.
- Loki ingestion lag (the reason a grace period exists at all): a job's `finish` line may not be
  queryable immediately after `rwfs` completes. The grace period is deliberately generous relative to
  typical Vector-to-Loki latency, and re-querying every sweep tick means a delayed line is simply picked
  up on a later tick — never a correctness problem, only a delay in cleanup.

## Testing

- `policy-server`: `restore_policy_test.go` (JobID generation/persistence), `write_test.go`
  (`CreatePolicy` logs `event=created`), new `restore_cleanup_test.go` (sweep logic against a fake
  `JobStatusService` client: finds-and-deletes, not-yet-finished skip, grace-period boundary,
  query-failure skip).
- `api-server`: new `jobstatus_server_test.go` (`GetPolicyJobStatus` against a fake Loki querier:
  finished/not-finished, window clamping at `policy_created_at` and at `maxJobsWindow`), `jobs_test.go`
  updates (`ApplyCreated`, `SourceHost`-from-finish for restore/verify, widened `binariesForKind`),
  `jobs_aggregator_test.go` updates (widened selectors, `"created"` in the tail whitelist).
- `agent`: `restore_test.go` (uses policy-provided `JobID`, `Due` on `LastAttemptAt`),
  `reconcile_test.go` (`isRestorePolicy` suppresses only `event=start`, not `event=finish`).
- e2e: existing `live-job-updates.spec.js`/restore e2e specs extended to assert a restore job shows
  `in_progress` immediately after policy creation (before the target node has run anything), and that it
  disappears from `policies/restore/` after completion + grace period.

## Documentation Impact

Per `.claude/CLAUDE.md`:

- New `docs/protocols/jobstatus.md` (mirrors `docs/protocols/policy-server.md`'s structure), cross-linked
  from `README.md`'s Documentation section and from `docs/components/{api-server,policy-server}.md`'s
  See Also sections.
- `docs/components/policy-server.md`: restore policy lifecycle (JobID, created/deleted logging, the
  cleanup sweep, its config keys).
- `docs/components/agent.md`: restore/verify task's run-once-ever semantics, retired `event=start`.
- `docs/components/api-server.md`: new gRPC listener, `GetPolicyJobStatus`.
- `docs/ARCHITECTURE.md`: topology change — api-server now accepts an inbound mTLS connection (from
  policy-server), not just outbound ones; update the mermaid diagram and the component table.
- `README.md`: update if the component list's api-server/policy-server descriptions reference
  protocol/topology details that become stale.
- `CHANGELOG.md`: entry before merge.

## Open Questions / Follow-ups

- **Who decides if a restore should actually happen** (authorization/approval before `mode=restore,
  overwrite=true` executes) is explicitly out of scope here and needs its own design — today, anyone
  reaching the web UI can submit one unilaterally, and this design does not change that.
- Grace period and sweep interval default values are left for the implementation plan to pick (likely
  config keys on `policy-server`, following `CheckinRetentionSec`'s existing convention) rather than
  fixed here.
