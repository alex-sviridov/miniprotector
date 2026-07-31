# Design: agent supervises catalogsync, demo drops its shell-script process juggling

**Date:** 2026-07-31
**Status:** Approved for planning

## Problem

`2026-07-28-agent-storage-supervision-design.md` made `agent` the first consumer of a cached
`"storage"`-typed policy: every reconcile tick it derives one ensure-running task per such policy
and supervises a `bwfs server` process for it. That covered `bwfs` only — `catalogsync` (which
replicates a `bwfs` node's `file_versions` to the central catalog) is untouched, still started by
hand from `demo/backup-host/entrypoint.sh`, gated on `STORAGE_PATH` and on polling `bwfs`'s port
before starting.

That shell script exists to coordinate three processes (`certclient`, `agent`, and conditionally
`bwfs` + `catalogsync`) around two hazards that predate `agent`'s storage supervision:

1. `bwfs`/`catalogsync` must not start before `agent`'s first `client.crt` exists (the script polls
   the filesystem for it).
2. `catalogsync`'s read-only open of `bwfs`'s `metadata.db` must not race `bwfs`'s own first-time
   schema/WAL initialization (the script polls `bwfs`'s gRPC port as a proxy signal).

Neither hazard survives once `agent` starts both processes itself. Hazard 1 disappears because
`agent`'s own reconcile loop already runs `operating-refresh` (which produces `client.crt`) before
`policy-update` (which is what makes a storage task exist at all) in the same tick — a storage task
can only ever appear after a successful `operating-refresh`, so nothing agent-spawned can race the
cert. Hazard 2 turns out not to need active coordination either: `catalogsync`'s
`wfs.OpenReplicaReader` opens SQLite with the `mode=ro` URI flag, which the driver enforces strictly
— on a database that doesn't exist yet, that's a clean, immediate error, not a partial read or a
corrupting write. A `catalogsync` that starts before `bwfs` has created `metadata.db` just fails and
gets crash-restarted with backoff, the same as any other transient exec failure this file already
tolerates without special-casing (see the predecessor design's "Out of scope" section on port
conflicts).

With both hazards gone, the shell script's entire reason to exist — sequencing multiple processes
around races — goes with them. This design extends `agent`'s existing storage supervision to also
cover `catalogsync`, then collapses the demo's `backup-host` entrypoint down to "bootstrap a cert,
then `exec agent serve`."

## Scope

- `agent`: `storageTask` gains a `Binary` field; `storageTasks()` derives **two** independent tasks
  per cached storage policy (`bwfs` and `catalogsync`) instead of one; `storageManager` drops its
  single `binary` field and becomes generic over whatever `(ID, Binary, Args)` tuples it's given.
- `demo/backup-host/entrypoint.sh`: drop the entire `STORAGE_PATH` branch (manual `bwfs`/`catalogsync`
  spawn, the port-poll wait, the `BWFS_PID`/`CATALOGSYNC_PID` bookkeeping) and the `client.crt` poll
  loop (no longer needed — nothing in the script starts a process that depends on it anymore).
  The script becomes: create/renew the bootstrap credential, then `exec ./agent serve`.
- `demo/docker-compose.yml`: remove `STORAGE_PATH=/data/storage` from the `store` service (nothing
  reads it once the shell script stops branching on it — `catalog`'s own unrelated `STORAGE_PATH`
  usage, for its own database directory, is untouched).
- New `demo/policy-server/policies/storage/store.json`: a storage policy targeting `store`, replacing
  what the removed env var used to imply.
- Documentation: `docs/components/agent.md`, `docs/ARCHITECTURE.md`, `demo/README.md`,
  `CHANGELOG.md`.

## Out of scope

- Any change to `bwfs` or `catalogsync` themselves. Both already behave correctly when started
  independently; this is purely about who starts them and when.
- Any explicit coordination between the two supervised processes (ordering, waiting, shared
  readiness signals). Explicitly rejected in favor of treating them as two independent ensure-running
  tasks, each reconciled on its own — a `catalogsync` that loses the cold-start race simply retries,
  per the Problem section above.
- Changing `storageStabilityWindow`, `backoffBase`/`backoffMax`, or any other existing supervisor
  tuning. `catalogsync` reuses `storageSupervisor` exactly as `bwfs` does today.
- `reconcile.go` and `list.go`. Both already operate generically enough on `[]storageTask` (build a
  prune-set union; render one table row per ID) that two tasks per policy instead of one requires no
  changes to either file.
- Any change to how a storage policy is authored, validated, or targeted (`client_filters`,
  `port`/`config` shape) — unchanged from the predecessor design.

## `agent`: two independent tasks per storage policy

`storageTask` (`src/cmd/agent/storage.go`) gains a `Binary` field:

```go
type storageTask struct {
    ID     string
    Binary string
    Args   []string
}
```

`storageTasks()` takes the two resolved binary paths as parameters and, for each cached policy that
still passes today's guard (`type == "storage"`, `backend == "filesystem"`, non-empty `root`),
appends two tasks instead of one:

```go
func storageTasks(policiesCachePath string, logger *slog.Logger, bwfsBinary, catalogsyncBinary string) ([]storageTask, bool) {
    // ... existing read/parse/skip logic unchanged ...
    tasks = append(tasks,
        storageTask{ID: storageTaskID(p.Name), Binary: bwfsBinary,
            Args: []string{cfg.Root, "server", "--port", strconv.Itoa(int(p.Port))}},
        storageTask{ID: catalogsyncTaskID(p.Name), Binary: catalogsyncBinary,
            Args: []string{cfg.Root}},
    )
}

// catalogsyncTaskID mirrors storageTaskID's "storage:<name>" convention with a
// suffix, so the two tasks derived from one policy are related-but-distinct
// IDs in agent-state.json / list-policies, and prune/reconcile treat them as
// two ordinary, independent entries.
func catalogsyncTaskID(policyName string) string {
    return storageTaskID(policyName) + ":catalogsync"
}
```

`storageManager` (same file) drops its `binary` field — `newStorageManager` takes only a logger.
`reconcile()`'s "start a supervisor for a newly-appeared task" branch uses `t.Binary` instead of
`m.binary`; everything else (the single `supervisors`/`args` map, the changed-args
stop-and-restart path, `StopAll()`) is unchanged, because it was already written generically enough
to not care what it's supervising. There is no cross-task logic added: `bwfs`'s and `catalogsync`'s
supervisors for the same policy know nothing about each other, start independently, and are
reconciled independently — the only thing they share is both disappearing together when the
originating policy is deleted (because `storageTasks()` stops emitting both of their IDs in the same
tick, and each vanishes through the existing "no longer in `wanted`" path on its own).

`main.go` resolves `catalogsyncBinary := resolveExecPath("catalogsync")` alongside today's
`bwfsBinary` resolution, and both `serve`'s `storageTasksFunc` closure and `list-policies`'s
`storageTasks(...)` call pass both through.

**Why not gate `catalogsync` on `bwfs`'s stability signal instead:** an earlier iteration of this
design used `storageSupervisor`'s existing `onOutcome(nil)` (fired once a spawn has stayed running
past `storageStabilityWindow`) to trigger starting `catalogsync` only after `bwfs` first stabilized.
Rejected: it reintroduces exactly the kind of inter-process sequencing this design is trying to
delete, for a hazard (the SQLite open race) that plain independent retry-with-backoff already
handles safely, per the Problem section's confirmation that `mode=ro` can't corrupt or partially
read anything.

## `agent list-policies`

No code changes. `renderPolicies` already takes `[]storageTask` and prints one row per `t.ID` with
no assumption about what kind of task it is; `storageTasks()` now simply hands it twice as many
entries. A fresh storage policy shows both `storage:<name>` and `storage:<name>:catalogsync` rows
from the first tick onward (both start as "never run" until each independently succeeds).

## `reconcile.go`

No code changes. `run()` already builds its prune-set union by iterating whatever `storageTaskList`
it's given and calls `storageMgr.reconcile(ctx, rs, storageTaskList)` with that same flat list —
both already handle an arbitrary number of tasks per tick, so returning two per policy instead of
one falls out for free.

## Demo: `backup-host` entrypoint

`demo/backup-host/entrypoint.sh` shrinks to:

```sh
#!/bin/sh
set -e

if [ -f /data/certs/bootstrap.crt ]; then
    ./certclient renew
else
    ./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

exec ./agent serve
```

This applies to all three `backup-host`-target services (`database`, `webserver`, `store`) — the
first two never used the `STORAGE_PATH` branch anyway, so this is a pure simplification for them;
`store` is the one service where behavior actually changes (see below).

Removed entirely: the `client.crt` poll loop (nothing left in this script depends on it — `agent
serve` handles its own certificate sequencing internally), the `STORAGE_PATH`-gated `bwfs`/
`catalogsync` spawns, the port-poll wait, and the `trap`/`wait`/PID-tracking machinery (`exec`
replaces the shell process with `agent`, so `agent` becomes PID 1 and receives `SIGTERM` directly —
no forwarding needed).

## Demo: `store` service configuration

`demo/docker-compose.yml`: remove `STORAGE_PATH=/data/storage` from `store`'s `environment` block —
nothing reads it once the shell script above no longer branches on it.

New `demo/policy-server/policies/storage/store.json`:

```json
{
  "metadata": {
    "name": "store",
    "created_at": "2026-07-31T00:00:00Z",
    "updated_at": "2026-07-31T00:00:00Z"
  },
  "client_filters": {
    "hostnames": ["store"]
  },
  "port": 8080,
  "config": {"backend": "filesystem", "root": "/data/storage"}
}
```

Port and root match the previous hardcoded values (`8080`, `/data/storage`) so every existing
backup policy's `"destination": "store:8080"` keeps working unchanged. Since `policy-server`'s
`Cache.Reload` walks every immediate subdirectory of `policies/` generically (not a hardcoded
`backup/`), and `demo/docker-compose.yml` already bind-mounts the whole
`./policy-server/policies` directory into `policy-server`'s container, no compose or mount change is
needed for this new file to be picked up.

## Data flow (cold start of the `store` node, post-change)

1. `store`'s container starts: `certclient bootstrap`, then `exec ./agent serve`.
2. Tick 1: `bootstrap-refresh` and `operating-refresh` run synchronously in order, producing
   `client.crt`. `policy-update` runs immediately after in the same tick (certs already exist),
   fetching `store.json`'s storage policy into `policies-cache.json`. `storageTasksFunc()` was
   already called earlier in this same tick (before `policy-update` ran), so it still sees no
   storage tasks this tick — same lag the predecessor design already documented.
3. Tick 2 (`ReconcileIntervalSec` later, 30s in this demo): `storageTasksFunc()` now reads the
   populated cache and returns two tasks. `storageManager.reconcile` starts two independent
   supervisors: `bwfs /data/storage server --port 8080` and `catalogsync /data/storage`.
4. Both are started at essentially the same instant. If `catalogsync` wins the race and starts before
   `bwfs` has created `metadata.db`, `OpenReplicaReader` fails cleanly; `catalogsync`'s supervisor
   records the failure and retries after backoff (tens of seconds) — by then `bwfs` has long since
   finished its (sub-second) startup, and the retry succeeds.
5. `agent list-policies` on `store` shows `storage:store` and `storage:store:catalogsync` once each
   has recorded its first success.

## Testing plan

- **`agent`**:
  - `storage_test.go`: every existing `storageTasks` test updates its call site for the new
    `(bwfsBinary, catalogsyncBinary)` parameters and asserts on `len(tasks) == 2` (one per binary)
    where it previously asserted `1`, checking both IDs/Binaries/Args. A cached policy that's
    skipped (unsupported backend, missing root, unparseable config) still contributes zero tasks of
    either kind.
  - `storageManager` tests: update every `newStorageManager(script, ...)` call site to the new
    single-logger constructor and give each `storageTask` literal an explicit `Binary` field. Add a
    case with two tasks sharing a policy-derived ID prefix (one `storage:x`, one
    `storage:x:catalogsync`) confirming they're supervised, restarted, and stopped fully
    independently — in particular, that one crashing and backing off doesn't affect the other.
  - No changes needed to `reconcile_test.go` or `list_test.go` beyond whatever call-site churn the
    `storageTasks`/`storageManager` signature changes force — their own logic isn't touched.
  - `integration_test.go`: extend the existing storage-task tick assertion to also cover
    `catalogsync`'s task appearing/running/pruning alongside `bwfs`'s.
- **Demo**: manual verification via `make demo-up` — confirm `store`'s `agent list-policies` shows
  both `storage:store` and `storage:store:catalogsync` as `ok`, and that
  `docker compose logs -f store` shows both processes' log lines (this repo has no automated
  end-to-end test harness for the demo compose stack; this matches how the predecessor storage
  supervision work was itself verified).

## Documentation

- `docs/components/agent.md`'s "Storage-policy supervision" section: note that a storage policy now
  yields two independent ensure-running tasks (`bwfs` and `catalogsync`), both supervised the same
  way, with no ordering between them.
- `docs/ARCHITECTURE.md`: extend the existing `agent -.-> bwfs` "supervises" edge with a matching
  `agent -.-> catalogsync` edge.
- `demo/README.md`: mention `demo/policy-server/policies/storage/` alongside the existing
  "Backup policies" section (both are now host-mounted, editable demo config); note that `store`'s
  `bwfs`/`catalogsync` come up automatically via that policy rather than unconditionally at container
  start, so there's a brief (`ReconcileIntervalSec`-scale) delay after `make demo-up` before
  `docker compose logs -f store` shows either process running.
- `CHANGELOG.md`: one dated entry — `agent` now supervises `catalogsync` the same way it already
  supervises `bwfs` for a storage policy (two independent ensure-running tasks, not one); the demo's
  `backup-host` containers no longer run a multi-process shell script, just `agent serve` directly.
