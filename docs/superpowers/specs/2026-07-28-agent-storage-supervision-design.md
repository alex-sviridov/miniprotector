# Design: agent storage-policy supervision (activating bwfs)

**Date:** 2026-07-28
**Status:** Approved for planning

## Problem

`2026-07-28-storage-policy-type-design.md` added a `"storage"` policy type to `policy-server`,
describing where a `bwfs` storage server should run and how — but explicitly built no consumer:
nothing reads a cached storage policy and actually runs `bwfs`. `agent` today only derives dynamic
work from `"backup"`-typed cached policies (see `docs/components/agent.md`'s "Policy-driven backup
execution" section); a cached `"storage"` policy is silently skipped, contributing nothing.

This also surfaces a gap in how a storage policy targets a node. The shipped `StoragePolicy` type
carries its own `Hostname` field, separate from the `ClientFilters` (hostnames/labels) mechanism
`policy-server`'s `GetPolicies` already uses to decide which policies reach which node's cache. Using
`Hostname` for targeting would require `agent` to independently look up its own identity and compare
it against every cached storage policy — duplicating a scoping decision `policy-server` already made
before the policy ever reached the cache. Reusing `ClientFilters` instead means: if a storage policy
is in this node's `policies-cache.json` at all, it's already meant for this node, full stop — no
second check.

A second, unrelated gap: `bwfs` is the only gRPC server in this repo that never wires
`signal.NotifyContext` into its context, so `SIGTERM` kills it immediately instead of triggering the
existing `GracefulStop()` path in `common/connection/server.go`. That's dead code today because
nobody sends `bwfs` a `SIGTERM` in production — but `agent` is about to become exactly that sender,
both on its own shutdown and whenever a storage policy is edited or removed out from under a running
`bwfs`.

## Scope

- `policy-server`: remove `StoragePolicy.Hostname` (struct field, proto field, validation, `Clone`,
  `ToProto`) — targeting moves entirely to `client_filters.hostnames`.
- Web UI design spec (`2026-07-28-storage-policy-web-ui-design.md`, not yet implemented): drop the
  "Hostname" text field from `StorageEditModal`; add a required "Target hostname" field that submits
  `client_filters.hostnames = [value]` instead of today's hardcoded empty filters.
- `policyclient`: `CachedPolicy` (and `toCachedPolicies`) gain `Port`/`Config` passthrough for storage
  policies (currently dropped).
- `agent`: a new `storage.go` derives ensure-running tasks from cached `"storage"` policies, and a
  small supervisor (`storageSupervisor`/`storageManager`) keeps one `bwfs server` process alive per
  task — started, crash-restarted with backoff, stopped/restarted when its policy disappears or
  changes — sharing `agent-state.json`/`agent list-policies` with everything agent already tracks.
- `bwfs`: wire `signal.NotifyContext` into `main.go` so `SIGTERM` triggers a graceful drain.

## Out of scope

- Any storage `backend` besides `filesystem`. `config` parsing recognizes `{"backend": "filesystem",
  "root": "..."}`; any other or missing `backend` is a skip-with-log, not a new capability.
- Requiring `client_filters.hostnames` to be non-empty on a storage policy. An operator who leaves it
  empty still gets "runs on every node" — the same footgun that already exists for a backup policy
  with no hostname filter. Not solved here.
- Any cap on concurrently-supervised `bwfs` processes (unlike `MaxConcurrentBackupJobs`). These are
  long-lived daemons, not queued jobs; there's no queue to bound.
- Forwarding `agent`'s own `--debug` flag to a supervised `bwfs` (no existing precedent forwards it to
  `certclient`/`policyclient` either).
- The CA-pool (`ca.crt`) rotation gap shared by every `mtls`-based server in this repo. Real, but
  pre-existing and not specific to this work.
- Multi-storage-policy port/path conflict detection. Two policies targeting the same node with
  colliding ports simply fail to bind — an ordinary, already-handled exec failure with backoff, no new
  handling needed.

## `policy-server`: remove `StoragePolicy.Hostname`

**Behavior change from `2026-07-28-storage-policy-type-design.md`:** that design gave `StoragePolicy`
a `Hostname string` field (`storage_policy.go`), required by `Validate()`, and an additive proto field
(`Policy.hostname = 11`, `CreatePolicyRequest.hostname = 8`, `UpdatePolicyRequest.hostname = 8`). All
of that is removed:

- `storage_policy.go`: delete the field, its `Validate()` check ("hostname is required"), its `Clone`
  copy, and its `ToProto` assignment. `StoragePolicy.Validate()` now only requires `port` in
  `1..65535` and `config` non-empty/well-formed JSON, plus `validateCommon`.
- `policyserver.proto`: delete `Policy.hostname`, `CreatePolicyRequest.hostname`,
  `UpdatePolicyRequest.hostname` (field numbers retired, not reused — proto3 convention). Regenerate
  `policyserver.pb.go`.
- `write.go`: `storageFieldsSet`/`buildPolicy`'s storage case stop reading/checking `req.GetHostname()`.
- Tests: `storage_policy_test.go`, `write_test.go`, `server_test.go`, `cache_test.go` drop their
  `Hostname: "h"` fixtures and the missing-hostname validation test.
- This is a Go-internal-plus-proto change only; `api-server` never implemented hostname passthrough
  (confirmed: `policyDTO`/`policyInput` in `src/cmd/api-server/policies.go` have no such fields today),
  so nothing there needs touching.

Targeting a storage policy at one node is now identical to targeting a backup policy at one node:
set `client_filters.hostnames`. `policy-server`'s `GetPolicies` already applies
`ClientFilters.Matches(hostname, labels)` before returning anything — unchanged by this design.

## Web UI spec update (`2026-07-28-storage-policy-web-ui-design.md`)

Editing that spec directly since it describes unimplemented work:

- `StorageEditModal.vue` fields become: **Name**, **Target hostname** (text, required — submits as
  `client_filters: { hostnames: [value], labels: {} }`, replacing today's hardcoded empty filters),
  **Port**, **Storage type**, **Filesystem path**. The old **Hostname** field (which mapped to the
  now-removed `StoragePolicy.Hostname`) is deleted.
- Everything else in that spec (store shape, list view, routing, data flow, testing plan) is
  unchanged.

**Addendum (post-planning discovery):** the paragraph above, and the "Out of scope" bullet
declaring `api-server` untouched, assumed `2026-07-28-storage-policy-web-ui-design.md` described
unimplemented work — confirmed at the time by inspecting `src/cmd/api-server/policies.go` on the
branch this design was written against. That assumption turned out to be false: a complete,
working implementation of that spec existed on a separate, unmerged branch
(`storage-policy-web-ui`) the whole time, and has since been merged into `main`. It implements the
*original* spec — a raw `Hostname` field throughout `api-server`'s `policyDTO`/`storagePolicyInput`
and the web `StorageEditModal`/`StorageView` — which now needs the same `Hostname`-removal treatment
described above, applied to real code instead of a planning document. This is handled by the
implementation plan's Task 2 (`api-server`) and Task 3 (`web`), inserted specifically for this;
`policy-server`'s removal (this design's original scope) is otherwise unaffected.

## `policyclient`: cache schema

`CachedPolicy` (`src/cmd/policyclient/fetch.go`) gains two additive fields, populated in
`toCachedPolicies` from the proto response — mirroring the already-additive convention the backup
fields use:

```go
type CachedPolicy struct {
    // ...existing fields unchanged...
    Port   int32  `json:"port,omitempty"`
    Config string `json:"config,omitempty"`
}
```

No `Hostname` field — moot now that targeting is `ClientFilters`-only, and `policy-server` no longer
emits it.

## `agent`: cached-policy mirror and `storage.go`

Agent's own duplicate schema mirror (`cachedPolicy` in `backup.go`, duplicated rather than imported
per this file's existing documented convention — agent can't import `cmd/policyclient`) gains the
same two fields.

A new `storage.go` derives one ensure-running task per cached `"storage"` policy:

```go
// storageTask is one bwfs server this node should be running, derived from
// a cached "storage" policy. Unlike backupTasks, there is no per-node
// targeting check here: policy-server's GetPolicies already applied
// ClientFilters.Matches before this policy ever reached policies-cache.json,
// so anything with Type == "storage" in the cache is already scoped to this
// node.
type storageTask struct {
    ID   string
    Args []string // {root, "server", "--port", port}
}

func storageTaskID(policyName string) string {
    return fmt.Sprintf("storage:%s", policyName)
}

// storageConfig is the subset of a storage policy's opaque config this
// agent understands -- today, exactly one backend.
type storageConfig struct {
    Backend string `json:"backend"`
    Root    string `json:"root"`
}

// storageTasks mirrors backupTasks's read/skip/build shape: ok=false means
// this tick's cache read failed and callers must not treat that as "zero
// tasks" (see reconcile.go's prune). A policy whose config doesn't parse as
// filesystem-backend JSON, or has an empty root, is skipped with a logged
// error -- same fail-safe direction as an unparseable rpo.
func storageTasks(policiesCachePath string, logger *slog.Logger) ([]storageTask, bool) {
    cachedPolicies, ok := readCachedPolicies(policiesCachePath)
    if !ok {
        return nil, false
    }
    var tasks []storageTask
    for _, p := range cachedPolicies {
        if p.Type != "storage" {
            continue
        }
        var cfg storageConfig
        if err := json.Unmarshal([]byte(p.Config), &cfg); err != nil || cfg.Backend != "filesystem" || cfg.Root == "" {
            logger.Error("storage policy has unsupported or unparseable config, skipping", "policy", p.Name)
            continue
        }
        tasks = append(tasks, storageTask{
            ID:   storageTaskID(p.Name),
            Args: []string{cfg.Root, "server", "--port", strconv.Itoa(int(p.Port))},
        })
    }
    return tasks, true
}
```

## `agent`: supervision (`storageSupervisor` / `storageManager`)

`storageSupervisor` is structurally a copy of the existing `vectorSupervisor` (`vector.go`) — a
long-running child, not a due/execute/complete `Policy`, so it gets the same kind of small dedicated
supervise loop rather than being shoehorned into `reconcile.go`'s cycle:

- Crash-restart with the same jittered `backoff()` reconcile.go already exports.
- `Stop()` sends `SIGTERM` (now a graceful drain once the `bwfs` fix below lands) and marks the exit
  as deliberate, same as Vector's `Stop`.
- **No `TriggerRestart`.** Unlike Vector, `bwfs` already hot-reloads its mTLS identity cert on every
  handshake via `mtls.LoadServerCredentials`'s `GetCertificate` closure (confirmed:
  `src/common/mtls/mtls.go`) — restarting on `operating-refresh` would only add disruption with no
  benefit, so this trigger is deliberately not carried over from the Vector pattern.

`storageManager` holds `map[string]*storageSupervisor` keyed by task ID. Each reconcile tick, given
the current `[]storageTask`:

- A task ID with no existing supervisor: resolve the `bwfs` binary (colocated-with-`$PATH`-fallback,
  same resolution `realExec` already does for `certclient`/`policyclient`/`brfs` — unlike Vector's
  no-fallback rule, `bwfs` is a first-party binary, not a third-party tool with a foot-gun-shaped
  ambient version), start a new supervisor.
- An existing supervisor whose task ID is no longer present: `Stop()` it, remove from the map.
- An existing supervisor whose args changed (port/path edited on the same policy): `Stop()` the old
  one, start a new one with the new args — same "policy changed under a running task" handling
  `agent` doesn't yet need for backup tasks (those are short-lived execs, not persistent processes).

State persistence reuses `agent-state.json`/`reconcileState` as-is, no new fields:

- A successful `cmd.Start()` calls `rs.recordOutcome(id, nil, now)` immediately — "started
  successfully" is this task's notion of success, not "exited successfully" (which, for a server,
  only happens on deliberate stop).
- An unexpected exit calls `rs.recordOutcome(id, err, now)` — increments `ConsecutiveFailures`, sets
  backoff, exactly like any other policy failure.
- A deliberate `Stop()`-triggered exit records nothing; the last known state stands until pruned.

**Why not extend the existing `Background`-`Policy`/`run()` due-execute path instead:** that path
tracks "is this still running" as a boolean (`reconcileState.inFlight`), never holding the actual
process handle. Pruning a removed storage policy, or restarting one whose port/path changed, both
require signaling a *specific* running child — which needs a real handle. Hence the dedicated
supervisor, matching the precedent Vector already set for exactly this class of problem.

**Sharing `agent-state.json` safely:** `run()` currently constructs its own `reconcileState`
internally from `cachePath`. This needs a small refactor — `run()` accepts a pre-built
`*reconcileState` instead — so `main.go` constructs one and hands it to both `run()`'s reconcile loop
and `storageManager`. `reconcileState`'s methods are already mutex-guarded for concurrent callers
(that's the whole reason it exists — background backup-task goroutines already call `recordOutcome`
concurrently with the main loop), so this is pure reuse, not new synchronization.

**Pruning across two task sources:** each tick, `run()`'s loop now also calls `storageTasksFunc()`
alongside `policiesFunc()`. Both sets of IDs are unioned into the single `rs.prune(currentIDs)` call
already made once per tick — unchanged mechanism, just a bigger union — and `storageManager.reconcile`
is called with the storage task list to actually start/stop supervisors. If this tick's storage-cache
read fails (`ok=false`), the tick skips both pruning *and* `storageManager.reconcile` for storage —
never tears down a running `bwfs` based on a transient read glitch, same fail-safe direction backup
tasks already use.

**`main.go`:** resolves the `bwfs` binary path, constructs the shared `reconcileState`, builds
`storageMgr`, passes `storageTasksFunc` into `run()`, and defers `storageMgr.StopAll()` on shutdown
(alongside the existing `vectorSup.Stop()`) so agent's own `SIGTERM` cleanly drains every supervised
`bwfs` rather than orphaning them.

## `bwfs`: graceful shutdown fix

`src/cmd/bwfs/main.go` currently builds a plain, never-cancelled `context.Background()`-derived ctx
and passes it into `connection.StartServer`. Every other gRPC server in this repo
(`policy-server`, `catalog`, `clientmanager-api`, `clientmanager-admin-api`, `issuer`, `catalogsync`,
`api-server`, `log-gateway`, `agent`, `brfs`) wires `signal.NotifyContext(ctx, os.Interrupt,
syscall.SIGTERM)` first — `bwfs` is the one outlier, so `common/connection/server.go`'s existing
`<-ctx.Done()` → `grpcServer.GracefulStop()` goroutine is currently dead code for `bwfs` specifically.
This fix brings `bwfs/main.go` in line with every sibling server — no other behavior change, no proto
change, no new flag.

This matters now specifically because `agent` is about to become a process that sends `bwfs`
`SIGTERM` routinely (on prune, on config change, on its own shutdown) — without this fix, every one of
those would hard-kill any in-flight `BackupService`/`RestoreService` stream instead of letting `bwfs`'s
existing job-finalization paths (see `docs/components/bwfs.md`'s stall-watchdog/startup-cleanup logic)
handle it cleanly.

## `agent list-policies`: visibility

`renderPolicies` gains a second slice of storage-task rows, reusing `health()`/`formatTime`/
`formatError` unchanged (so "ok" / "retrying (N failures)" / "never run" mean exactly what they mean
for the three static policies and backup tasks), with `NEXT RUN` hardcoded to `-` — these are
ensure-running daemons, not scheduled jobs, so there is no next-run estimate to show.

## Data flow (a storage policy going live on its target node)

1. Operator creates a storage policy via the (updated) web UI: name, target hostname, port, backend,
   root path. Saved with `client_filters.hostnames = [target]`.
2. `policy-server` validates and writes it to `policies/storage/`.
3. On the target node, `policyclient fetch`'s next `GetPolicies` call matches
   `client_filters.hostnames` against this node's identity and receives the policy; every other node's
   `fetch` does not. `toCachedPolicies` now carries `port`/`config` into `policies-cache.json`.
4. `agent serve`'s next reconcile tick re-reads `policies-cache.json`, `storageTasks` builds one task
   from it, `storageManager.reconcile` finds no existing supervisor for that ID and starts one:
   `bwfs <root> server --port <port>`.
5. A successful start records success in `agent-state.json`; `agent list-policies` shows it as `ok`.
6. If the policy is later deleted, or edited (port/path changed): next tick's `storageTasks` output no
   longer contains that task ID (deletion) or contains it with different `Args` (edit).
   `storageManager.reconcile` stops the running `bwfs` (`SIGTERM`, now a graceful drain) and, for an
   edit, starts a new one with the new args.
7. If `bwfs` crashes unexpectedly, `storageManager` restarts it with backoff, recording each failure —
   visible in `list-policies` as `retrying (N failures)` with the last error.

## Testing plan

- **`policy-server`**: `storage_policy_test.go`/`write_test.go`/`server_test.go`/`cache_test.go`
  updated to drop `Hostname` fixtures; `StoragePolicy.Validate()` no longer requires it.
- **`policyclient`**: `fetch_test.go` — `toCachedPolicies` carries `port`/`config` through for a
  storage-typed policy.
- **`agent`**:
  - `storage_test.go`: `storageTasks` builds a task from a well-formed filesystem-backend config;
    skips (without error) a policy with an unsupported/missing backend, an empty root, or unparseable
    config JSON; `ok=false` on a cache read failure, matching `backupTasks`'s contract.
  - Supervisor tests mirroring `vector_test.go`'s existing patterns: starts the given binary/args,
    crash-restarts with backoff and records failures via the shared `reconcileState`, `Stop()` sends
    `SIGTERM` and suppresses the next backoff, `onSpawnForTest` hook for deterministic tests.
  - Manager tests: starts a supervisor for a newly-appeared task, stops one for a disappeared task,
    stops-and-restarts one whose args changed, never double-starts a task already supervised.
  - `reconcile_test.go` updated for `run()`'s new `*reconcileState`-accepting signature and the
    storage-task-ID union in `prune`.
  - `integration_test.go`: end-to-end tick showing a storage task moving from absent → running →
    pruned.
- **`bwfs`**: verify `signal.NotifyContext` wiring compiles and matches the sibling pattern; graceful
  shutdown itself is not unit-tested anywhere else in this repo either (it's an integration/e2e
  concern), so no new burden here beyond the existing convention.

## Documentation

- `docs/components/agent.md` — new "Storage-policy supervision" section (parallel to today's
  "Policy-driven backup execution"): ensure-running model, config parsing/backend support, crash
  backoff, pruning/restart-on-change, `list-policies` visibility.
- `docs/components/bwfs.md` — note graceful `SIGTERM` handling under Transport/behavior.
- `docs/components/policyclient.md` — note `port`/`config` now carried through to
  `policies-cache.json` for storage policies.
- `docs/components/policy-server.md` + `docs/protocols/policy-server.md` — remove `hostname` field
  documentation; note targeting is `client_filters`-only for every policy type.
- `docs/ARCHITECTURE.md` — new topology edge: `agent` supervises `bwfs` (distinct from the existing
  `brfs` → `bwfs` backup-stream edge).
- `CHANGELOG.md` — one dated entry: agent now supervises `bwfs server` for storage policies targeting
  it (ensure-running, not scheduled); removes `StoragePolicy.Hostname` in favor of `client_filters`;
  fixes `bwfs`'s missing graceful-shutdown wiring.
