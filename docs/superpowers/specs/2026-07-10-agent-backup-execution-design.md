# Agent Backup Execution — Design

> Builds on `docs/superpowers/specs/2026-07-10-agent-policy-update-job-design.md` (fetches and
> caches backup policies into `policies-cache.json`) and `docs/superpowers/specs/
> 2026-07-10-policy-server-design.md` (the policy schema itself: `client_filters`, `object_filters`,
> `rpo`, `backup_window`). Both explicitly stopped short of acting on the cache: "nothing yet reads
> the cache to schedule or run a backup; that remains separate, later work." This is that work —
> `agent` gains the ability to actually execute `brfs` runs derived from its cached policies, on a
> schedule gated by `backup_window` and `rpo`.

## Problem

`agent` fetches and caches this node's applicable backup policies (`policies-cache.json`) on a
schedule, but nothing reads that cache to decide when to run a backup or what to run. A policy's
`object_filters` (paths to back up), `rpo` (max staleness), and `backup_window` (cron slots a
backup may start in) are all present in the cache today, entirely inert. Separately, the policy
schema itself has no notion of *where* to back up to — `brfs` requires `--destination host:port`,
which no existing field carries.

## Goals

- A policy's `object_filters` paths are actually backed up via `brfs`, automatically, without
  operator intervention beyond authoring the policy on `policy-server`.
- A backup only starts inside one of its policy's `backup_window` cron slots, and only when the
  path's last successful backup is older than the policy's `rpo` — both conditions required.
- Each `object_filters` path is tracked and retried independently; one path's persistent failure
  never blocks or delays sibling paths in the same policy.
- A long-running backup never delays `agent`'s existing credential-refresh policies
  (`bootstrap-refresh`, `operating-refresh`), which gate mesh access and must keep their own
  cadence regardless of what backups are doing.
- `agent serve` picks up policy changes (new/removed policies, changed paths) on its own next
  cache read, without needing a restart — the same "no separate mechanism" property phase 2's
  attribute/SAN propagation already established for credentials.

## Non-Goals (this pass)

- **No pruning of stale per-path state.** If a policy or a path is removed from
  `policies-cache.json`, its `agent-state.json` entry is simply never read again — orphaned, not
  actively cleaned up. Harmless if wasteful, the same direction already accepted for agent v1's
  cache-write-failure handling and phase 2c's SAN-changed-mid-request retries.
- **No cross-node coordination or locking.** If two nodes' policies somehow resolved to the same
  destination/path (not a supported configuration today), nothing prevents both from backing up
  concurrently.
- **No backpressure or queueing beyond the existing per-task backoff and a blunt concurrency cap.**
  If more paths are simultaneously due than `MaxConcurrentBackupJobsInt` allows, the excess simply
  wait for the next reconcile tick and are retried then — no priority, no queue.
- **No changes to `brfs`, `bwfs`, or the backup protocol.** This phase only adds a caller;
  `--job-id`, `--destination`, and job tracking already exist and are used as-is.
- **No enforcement of `destination`'s format.** Like `rpo` and `backup_window`, `policy-server`
  stores and returns it as an opaque string; `brfs` is what actually validates it (as it already
  does for any `--destination` value today).
- **No exact-instant scheduling.** `agent`'s reconcile loop is a poll, not a timer; a cron trigger
  is only ever noticed on the next tick after it fires, same as every other policy today.

## Architecture

### Policy schema: add `destination`

`policy-server`'s `Policy` message gains a seventh field, `destination` (a `host:port` string),
alongside `rpo`/`backup_window` as another field `policy-server` stores and returns verbatim
without interpreting:

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
}
```

Operator-authored policy JSON files (`$MP_CONFIG_PATH/policies/*.json`) gain a
`"destination": "host:port"` key. `cmd/policy-server/policy.go`'s `Policy` struct,
`server.go`'s proto conversion, and `cmd/policyclient/fetch.go`'s cache struct all gain the
matching field — the cache file's shape becomes:

```json
[
  {
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": ["/var/lib/postgres", "/etc/postgres"],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"],
    "destination": "bwfs-east.internal:8080"
  }
]
```

### `agent`: backup tasks derived from the cache, alongside the existing three policies

Today `policies(conf)` returns a fixed `[]Policy` computed once at `agent` startup, and every
`Policy`'s due-check is the same hardcoded interval comparison (`isDue` in `reconcile.go`). This
phase generalizes both:

1. **Pluggable due-check.** `Policy` gains a due-check function in place of the bare `Interval`
   comparison, so the three existing policies (`bootstrap-refresh`, `operating-refresh`,
   `policy-update`) keep their exact current interval-based behavior — this is a refactor of
   `isDue`'s call site, not a behavior change for them.
2. **Dynamic backup tasks.** On every reconcile tick (not just at startup — `policies-cache.json`
   is refreshed independently by the existing `policy-update` policy on its own schedule, so a
   stale in-memory copy would miss policy changes), `agent` re-reads the cache and derives one
   **backup task** per `(policy, object_filters path)` pair — not one per policy. Each task gets a
   unique `Policy.ID` (`backup:<policy-name>:<path>`), giving it its own independent entry in
   `agent-state.json`: its own `LastSuccessAt`, its own backoff, its own retry schedule. A
   persistently-failing path never blocks or delays any other path, including siblings in the same
   policy.
3. **Backup task due-check.** A backup task is due when **both**:
   - **Window is open**: at least one of the policy's `backup_window` cron expressions has a
     trigger at-or-after `now - BackupWindowGraceSec` that is not after `now` — i.e., a trigger
     fired within the last `BackupWindowGraceSec` (new config, default `3600`, one hour) and the
     window hasn't closed yet. Computed via `github.com/robfig/cron/v3` (new dependency,
     standard 5-field parsing), using its `Schedule.Next(t)` to find the next trigger at-or-after
     `now - BackupWindowGraceSec` and checking it's `<= now`. This tolerates `agent` being busy,
     backing off, or briefly down without silently missing a slot.
   - **RPO elapsed**: `now - LastSuccessAt > rpo` for that specific path (or never succeeded).
   
   A window passing without an RPO breach is a no-op (nothing to do yet); an RPO breach outside
   any open window waits for the next window rather than running early — matching "window gates,
   RPO decides" exactly.

### Execution: `brfs`, backgrounded, bounded

When a backup task is due, `agent` execs:

```
brfs <path> --destination <destination> --job-id backup:<policy-name>:<slug(path)>:<unix-ts>
```

The explicit `--job-id` embeds the policy name and path into `bwfs`'s own `backup_jobs` records —
today those only carry `source_host` and timestamps, so this is the only way an operator querying
`bwfs`'s database can tell *which policy* produced a given job. `slug(path)` replaces `/` with `-`
and strips the leading separator (e.g. `/var/lib/postgres` → `var-lib-postgres`) — cosmetic only,
since `job-id` is opaque metadata to both `brfs` and `bwfs`; it never needs to round-trip back to a
literal path.

Unlike the three existing policies, a backup task's exec is launched in a **background goroutine**
rather than run synchronously in the reconcile loop — a large backup can run far longer than a
credential refresh, and must never delay `bootstrap-refresh`/`operating-refresh`, which gate mesh
access on their own tight cadence. Consequences:

- `agent-state.json`'s in-memory `Cache` map gains a mutex, since goroutines now complete and
  write results asynchronously, concurrently with the main reconcile loop's own reads/writes for
  the three synchronous policies.
- Concurrency is bounded by a semaphore sized from a new config key, `MaxConcurrentBackupJobsInt`
  (default `2`), so a burst of simultaneously-due paths can't all fire at once and saturate the
  node's disk/network. Tasks that don't acquire a slot this tick simply remain due and are
  reconsidered next tick — no queue, no priority.
- Each backup goroutine's `exec.Command` becomes `exec.CommandContext`, tied to `agent serve`'s own
  shutdown context — a `SIGTERM` cleanly terminates in-flight `brfs` processes rather than
  orphaning them as detached background processes. An interrupted backup is treated as "job did
  not complete," the same outcome `bwfs`'s own job tracking already assigns to a crashed `brfs` —
  no new failure mode, just a different cause of an already-handled one.

## Data Flow

**Policy authoring (unchanged in shape, one new field):**
```
operator writes policies/daily-db-backup.json (now including "destination": "bwfs-east:8080")
  -> touches policies/.changed -> policy-server hot-reloads
```

**Ongoing reconcile (`agent serve`, every `ReconcileIntervalSec`):**
```
tick:
  -> the three existing policies checked/exec'd synchronously, unchanged
  -> read policies-cache.json fresh
  -> for each (policy, path) pair:
       window open (cron trigger within BackupWindowGraceSec) AND rpo elapsed for this path?
         no  -> skip, unchanged state
         yes -> acquire concurrency slot (up to MaxConcurrentBackupJobsInt)
                 -> goroutine: exec brfs <path> --destination <dest> --job-id backup:<policy>:<path>:<ts>
                    -> success: LastSuccessAt = now, ConsecutiveFailures = 0 (mutex-guarded write)
                    -> failure: ConsecutiveFailures++, backoff as today (mutex-guarded write)
```

**Revocation / attribute change (unaffected):** unchanged from phase 2/2c — this phase adds no new
interaction with credentials; `brfs` authenticates with the same operating credential
(`client.crt`/`client.key`) every other component already uses.

## Configuration

New `local.conf` keys, following the existing `_host`/`_port`/`*Sec`/`*Int` convention:

| Key | Default | Used by |
|---|---|---|
| `BackupWindowGraceSec` | `3600` (1 hour) | How long after a `backup_window` cron trigger the window stays "open" |
| `MaxConcurrentBackupJobsInt` | `2` | Upper bound on simultaneously in-flight `brfs` execs launched by `agent` |

No changes to existing config keys; `PolicyFetchIntervalSec` (governing how often the cache itself
refreshes) is unaffected and orthogonal to how often `agent` re-reads that cache for due backup
tasks (every reconcile tick, i.e. `ReconcileIntervalSec`).

## Error Handling

- **`policies-cache.json` missing or unparseable**: zero backup tasks derived that tick — the same
  fail-safe direction `policyclient`/`agent` already use elsewhere (on any doubt, assume nothing to
  do, never assume success). Not an `agent serve` error; the three existing policies are
  unaffected.
- **A policy missing `destination`** (e.g. authored before this phase, or a bug on
  `policy-server`'s side): the derived backup task's `brfs` exec fails immediately (empty
  `--destination`), recorded as an ordinary failure with the existing per-path backoff — no
  special-casing.
- **`brfs` exits non-zero** (unreachable `bwfs`, hash mismatch on `BackupCommit`, revoked
  credential, etc.): recorded as a failure on that specific path's state; other paths (same or
  different policies) are entirely unaffected, per the per-path tracking decision.
- **Concurrency cap reached**: a due task that can't acquire a slot this tick is simply left due;
  it's reconsidered (and, if still due, retried) on the next tick. Not recorded as a failure — it
  never ran.
- **`agent serve` receives `SIGTERM` mid-backup**: in-flight `brfs` processes are terminated via
  the shared shutdown context; their jobs show as incomplete (`finished_at` stays `NULL`) in
  `bwfs`, matching the existing "client crash" handling already documented in the backup job
  tracking design — no new state to reason about.
- **Same path due again while its previous run is still in flight**: cannot happen — a task only
  becomes eligible to run again once `LastSuccessAt`/backoff state reflects the *previous*
  attempt's outcome, which is only written after that attempt's goroutine completes.

## Testing

- Unit: cron window-open check (`robfig/cron`-backed) — a trigger just inside
  `BackupWindowGraceSec` reports open; one just outside reports closed; multiple `backup_window`
  entries, only one of which recently triggered, still reports open.
- Unit: backup-task due-check — window-open-but-RPO-not-elapsed is not due; RPO-elapsed-but-window-
  closed is not due; both true is due; matches "window gates, RPO decides" exactly.
- Unit: cache-to-task derivation — a policy with N `object_filters` paths yields N distinct tasks
  with distinct, stable IDs; a policy/path disappearing from the cache simply stops being derived
  (no crash, no special handling required for the orphaned state entry).
- Unit: per-path independence — one path's `ConsecutiveFailures`/backoff advancing has no effect on
  a sibling path's state, including siblings in the same policy.
- Unit: concurrency cap — with `MaxConcurrentBackupJobsInt` set low in a test, more due tasks than
  the cap doesn't launch more than the cap's worth of goroutines simultaneously (fake `runner` with
  a blocking channel to observe concurrency directly).
- Unit: `--job-id` construction — given a policy name and path, the built argument list matches the
  expected `backup:<policy>:<slug>:<ts>` shape.
- Unit: shutdown terminates in-flight execs — a fake long-running `runner` observes context
  cancellation when the reconcile loop's context is cancelled.
- Integration (extends the existing e2e harness): a real `policy-server` serving a policy with a
  `backup_window` set to "always due" (e.g. `* * * * *`) and a zero `rpo`, a real `bwfs`, and a real
  `agent serve` — confirms a `brfs` run actually happens and `bwfs` records a job whose `job_id`
  contains the policy name.
- Integration: a policy whose `backup_window` never matches — confirms no backup task ever runs,
  across several reconcile ticks.

## Documentation Impact

Per `.claude/CLAUDE.md`'s protocol-change and feature-change rules:

- **`docs/protocols/policy-server.md`** (exists) — document the new `destination` field on `Policy`.
- **`docs/components/policy-server.md`** (exists) — document `destination` in the policy file
  format section.
- **`docs/components/policyclient.md`** (exists) — update the `policies-cache.json` example to
  include `destination`.
- **`docs/components/agent.md`** (exists) — document that `policy-update`'s cache is now acted on:
  the backup-task derivation, the window+RPO due-check, the two new config keys, and that
  `list-policies` now also lists backup tasks (`backup:<policy>:<path>` rows) alongside the three
  static policies.
- **`docs/components/brfs.md`** (exists) — note that `agent`-driven runs use a
  `backup:<policy>:<path>:<timestamp>` job-id convention, for operators grepping job history.
- **`README.md`** — update `agent`'s one-line component description if it's gone stale ("nothing
  yet acts on the cache" is no longer true after this phase).
- **`docs/ARCHITECTURE.md`** — update the `agent` component-table row; this closes the last
  "separate, later work" item the policy-server/policyclient/agent-policy-update trio's rows
  currently point to.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

## Relationship to Prior Work

This is the piece both the 2026-07-10 policy-server design and the 2026-07-10 agent policy-update
job design explicitly deferred. Before this phase, a policy is authored, served, fetched, and
cached — and then nothing happens. After this phase, an authored policy with a `destination`,
`object_filters`, `rpo`, and `backup_window` results in real, scheduled, tracked `brfs` runs against
real `bwfs` targets, closing the loop from "operator writes a policy file" to "backup actually
happens" without any additional manual step.
