# Agent Backup State Hygiene — Design

> Builds on `docs/superpowers/specs/2026-07-10-agent-backup-execution-design.md` (backup tasks
> derived fresh from `policies-cache.json` every reconcile tick, one `agent-state.json` entry per
> `(policy, path)` pair). That design explicitly accepted two gaps as non-goals: "no pruning of
> stale per-path state" and no mention of failure-reason visibility beyond a bare count. This spec
> closes both, without touching execution, scheduling, or the wire protocol.

## Problem

`agent-state.json` never shrinks. A policy or `object_filters` path removed from
`policies-cache.json` simply stops being read — its `PolicyState` entry (`LastSuccessAt`,
`ConsecutiveFailures`, `NextRetryAt`) lingers forever, orphaned. Harmless but silent, and it never
self-corrects.

Separately, a persistently-failing backup task is only visible as a number
(`ConsecutiveFailures`) in both the cache and `list-policies`' output — the actual error
(unreachable `bwfs`, hash mismatch, revoked credential, bad `--destination`) is logged once via
`slog` at failure time and then lost; there's no way to see *why* a task is failing without
digging through logs.

## Goals

- A policy/path pair removed from `policies-cache.json` has its `agent-state.json` entry cleaned
  up automatically, within a bounded number of reconcile ticks, without operator action.
- Pruning never deletes a *live* task's history because of a transient, self-inflicted problem —
  specifically, a momentarily missing or corrupt `policies-cache.json` (e.g. read mid-write by the
  `policy-update` job) must never be mistaken for "everything was removed."
- `list-policies` (and the underlying cache) surfaces the last failure's actual error message, not
  just a count.

## Non-Goals (this pass)

- **No recovery from total `agent-state.json` loss** (disk wipe, corruption, redeployed node).
  Considered and deliberately rejected: the actual cost of losing all local state is bounded and
  cheap, not catastrophic. A backup task's `Due` check requires an open `backup_window`, not just
  an elapsed RPO (`backup.go`'s `windowOpen(...) && rpoElapsed(...)`) — losing state does not cause
  an immediate stampede, only makes the *next* scheduled window's run unconditional instead of
  possibly-skipped. And that run itself is cheap: `bwfs`'s file-level pre-filtering
  (`SEND_FILE`/`SKIP_FILE`, `docs/protocols/backup.md`) skips hashing and chunk transfer entirely
  for files the server already has, so a redundant post-loss run is mostly a directory scan plus a
  metadata round-trip per file, not a data re-transfer. Building a recovery mechanism against this
  bounded cost isn't worth the complexity it would add.
- **No distributed source of truth for "last successful backup."** Both `catalog` (receive-only
  today, no query API, replicates asynchronously on a poll interval, and only tracks
  `file_versions` rows — not job-level success/failure at all) and `bwfs` (has the data in
  `backup_jobs`, but no query RPC exposed for it) were considered and rejected for the same reason:
  a per-tick due-check depends on `agent-state.json` being a fast, local, dependency-free read.
  Making that decision depend on a network call to an optional or unqueryable service would be a
  much larger change than this pass's actual problem calls for.
- **No pruning of the `inFlight` in-memory map.** Unaffected by this design — it already only ever
  holds entries for currently-running background tasks and is never persisted.
- **No structured error taxonomy.** `LastError` is the raw `error.Error()` string from the failed
  `runner` call, not a categorized/coded failure reason. Good enough for an operator reading
  `list-policies`; anything more structured is future work if it turns out to be needed.

## Architecture

### Pruning, gated on a confirmed-good read

`readCachedPolicies` (`backup.go`) currently collapses two different situations into the same
`nil` return: "the file doesn't exist or is corrupt" and "the file exists and legitimately lists no
policies." Pruning needs to tell these apart, so the function (and `backupTasks`, which wraps it)
gains a second return value:

```go
func readCachedPolicies(policiesCachePath string) (policies []cachedPolicy, ok bool)
func backupTasks(policiesCachePath string, conf *config.Config) (tasks []Policy, ok bool)
```

`ok` is `true` exactly when the cache file was present and parsed as valid JSON — regardless of
whether the resulting list (or the derived task list) is empty. It's `false` when the file is
missing or fails to unmarshal. This is the one bit pruning needs: "was this tick's view of the
world trustworthy enough to delete things based on it?"

`run`'s `policiesFunc` parameter grows the same shape — `func() (policies []Policy, ok bool)`.
The three static policies never fail to be produced (no file read involved), so the combined `ok`
for a tick is just `backupTasks`'s `ok`; `main.go`'s implementation of `policiesFunc` becomes a
thin combinator: fixed policies, plus `backupTasks`'s result, `ok` passed through unchanged.

Each tick, `run` does the prune **before** dispatching that tick's due checks:

```
policies, ok := policiesFunc()
if ok {
    rs.prune(idsOf(policies))   // deletes any cache entry not in this tick's full policy list
}
for _, p := range policies {
    ... existing isDue / dispatch logic, unchanged ...
}
```

`prune` needs no per-entry reasoning about *why* an ID is missing — a policy removed outright, a
policy present but currently contributing zero tasks (bad `rpo`/`backup_window` this tick), and a
path dropped from `object_filters` all look the same: absent from a confirmed-good tick's list, and
all get the same bounded-cost treatment already accepted in Non-Goals if they reappear later
(one extra, cheap, window-gated run). `prune` is a plain map-diff under `reconcileState`'s existing
mutex, writing the cache back only if it actually removed something, to avoid gratuitous disk
churn on every tick.

**Accepted race:** a task pruned while its own previous-tick run is still in flight gets its
`PolicyState` entry resurrected by `recordOutcome`, which writes unconditionally by ID once that
goroutine completes — regardless of whether the entry it's writing into still exists. If the task
is genuinely gone, that one resurrected entry is simply pruned again on the next confirmed-good
tick. No special-casing needed; self-healing within one extra tick.

### Last-error tracking

`PolicyState` gains one field:

```go
type PolicyState struct {
    LastSuccessAt       *time.Time `json:"last_success_at"`
    LastAttemptAt       *time.Time `json:"last_attempt_at"`
    ConsecutiveFailures int        `json:"consecutive_failures"`
    NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
    LastError           string     `json:"last_error,omitempty"`
}
```

`recordOutcome` (`reconcile.go`) is already the single place both synchronous and background paths
update state, so it's the only place that needs to change: on failure, `state.LastError =
attemptErr.Error()`; on success, `state.LastError = ""` — cleared the same tick a run succeeds, so
the field always reflects the *current* failure streak, never a stale error from a since-resolved
problem.

`list.go`'s `renderPolicies` gains an `ERROR` column, truncated (a fixed character cap, ellipsis on
overflow) to keep the tabwriter output readable — the full, untruncated string remains in
`agent-state.json` for anyone reading the file directly.

## Data Flow

**Reconcile tick, prune step (new, runs before dispatch):**
```
tick:
  -> policies, ok := policiesFunc()
  -> if ok: prune agent-state.json entries not present in policies (by ID)
  -> (existing) for each policy in policies: isDue? -> dispatch, record outcome
```

**Failure recording (existing path, one field added):**
```
task fails -> recordOutcome(id, err, now):
  -> ConsecutiveFailures++, NextRetryAt = now + backoff(...)   (unchanged)
  -> LastError = err.Error()                                    (new)
  -> write cache
```

**Success recording (existing path, one field added):**
```
task succeeds -> recordOutcome(id, nil, now):
  -> LastSuccessAt = now, ConsecutiveFailures = 0, NextRetryAt = nil   (unchanged)
  -> LastError = ""                                                    (new)
  -> write cache
```

## Error Handling

- **`policies-cache.json` missing or unparseable this tick**: `ok=false`, pruning skipped entirely
  for the tick; the three static policies and (if the file was previously readable) zero backup
  tasks are still evaluated for dispatch as today — no crash, no special-casing beyond the existing
  fail-safe "assume nothing to do."
- **Every `*.json`-derived backup task legitimately absent this tick** (operator deleted all
  policies, or emptied `object_filters`): `ok=true`, an empty task list is trustworthy, pruning
  proceeds and removes every backup-task entry — this is the intended behavior, not a failure mode.
- **A task pruned mid-flight**: see "Accepted race" above — resurrected once, pruned again next
  confirmed-good tick.

## Testing

- `readCachedPolicies`/`backupTasks`: missing file → `ok=false`, nil tasks; corrupt JSON →
  `ok=false`, nil tasks; valid file with zero policies → `ok=true`, nil tasks (distinguished from
  the corrupt case only by `ok`, both currently return a nil slice); valid file, one policy with an
  unparseable `rpo` → `ok=true`, that policy contributes no tasks but others in the same file still
  do.
- `prune`: given a cache with entries `{A, B, C}` and a confirmed-good tick's ID set `{A, C}`, `B`
  is removed and the cache is rewritten; given the same cache and `ok=false`, nothing is removed
  and no write happens.
- `prune` race: an in-flight task's entry, pruned mid-run, is resurrected by `recordOutcome` on
  completion, then pruned again on the next confirmed-good tick — exercised via the existing fake
  `runner`-with-blocking-channel pattern already used for concurrency-cap tests.
- `recordOutcome`: `LastError` is set to the failing error's message on failure, cleared on the
  next success, and retains the most recent (not first) message across multiple consecutive
  failures.
- `list.go`: `renderPolicies` includes the `ERROR` column; a `LastError` longer than the truncation
  cap is shown truncated with an ellipsis; an empty `LastError` renders as blank/`-` consistent with
  other empty fields.

## Documentation Impact

Per `.claude/CLAUDE.md`'s protocol-change and feature-change rules:

- **`docs/components/agent.md`** (exists) — note that `agent-state.json` entries for removed
  policies/paths are now pruned automatically, and that failure entries now carry a `LastError`
  message, surfaced in `list-policies`' `ERROR` column.

No protocol, RPC, or on-disk schema for any *other* component changes — this is entirely internal
to `agent`'s own state file and CLI output.

## Relationship to Prior Work

Closes both gaps the 2026-07-10 agent backup execution design explicitly deferred ("no pruning of
stale per-path state," and failure visibility limited to a bare count) without revisiting any of
that design's execution, scheduling, or concurrency decisions. The state-loss-recovery question
raised during this design's discussion (querying `catalog` or `bwfs` to rehydrate lost state) was
evaluated and explicitly rejected — see Non-Goals — as disproportionate to the bounded, already-
cheap cost of the scenario it would address.
