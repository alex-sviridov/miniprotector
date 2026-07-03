# Agent v1: Embedded Cert-Refresh Reconciliation — Design

> First concrete slice of the `agent` component. Builds toward, but does not yet implement,
> proposal #1 (`2026-07-03-agent-job-dispatch-design.md`, NATS job queue) or proposal #2
> (`2026-07-03-policy-reconciliation-design.md`, policy-server-fetched schedules). This version
> has exactly one hardcoded policy (cert refresh) and no network dependency beyond what
> `certclient` itself needs — it exists to prove the reconcile-loop shape before adding a queue or
> a policy server on top of it.

## Problem

Cert renewal today is a bare cron entry calling `certclient`, with no state, no differentiated
retry behavior beyond cron's fixed cadence, and no way to inspect whether it's healthy without
reading logs. This is also the smallest possible proof of the reconciliation-loop pattern intended
for proposal #2, without taking on a policy server or a cron-expression dependency yet.

## Goals

- One `agent` binary, `agent serve`, running a fixed-interval reconcile loop.
- Exactly one embedded (hardcoded, not fetched) policy: refresh the mTLS identity via `certclient`
  on interval.
- Local, on-disk state records the outcome of the last attempt/success so a restart doesn't lose
  track of whether a refresh is due.
- A failed attempt doesn't retry on every subsequent tick — it backs off with randomized jitter,
  decoupled from the normal refresh interval.
- `agent list-policies` — a read-only CLI mode, reads the same on-disk state the daemon writes,
  prints each policy's health and estimated next run time. No IPC with a running daemon; both
  daemon and CLI read/write the same cache file.
- Variable/runtime data (the cache file) lives in a standard, configurable directory shared by
  future components, not hardcoded next to the config file.

## Non-Goals (v1)

- No NATS queue, no policy server, no cron-expression scheduling — those are proposals #1/#2.
- No policy fetched over the network; the (single) policy is a Go literal compiled into the
  binary.
- No config for adding more policies yet — the reconcile loop and cache are written generically
  enough (`[]Policy`, keyed cache) that adding a second hardcoded policy later is a small diff, but
  that's not built now.

## Architecture

```go
type Policy struct {
    ID       string
    Binary   string
    Args     []string
    Interval time.Duration
}

var policies = []Policy{
    {ID: "cert-refresh", Binary: "certclient", Interval: 5 * time.Minute},
}
```

```go
type PolicyState struct {
    LastSuccessAt       *time.Time `json:"last_success_at"`
    LastAttemptAt       *time.Time `json:"last_attempt_at"`
    ConsecutiveFailures int        `json:"consecutive_failures"`
}
type Cache map[string]PolicyState // keyed by Policy.ID, persisted as one JSON file
```

`agent serve` runs a `time.Ticker` at `ReconcileIntervalSec`. On each tick, for every policy in
`policies`, it loads that policy's state from the cache and asks `isDue` (see below) whether to
act. If due: run `exec.Command(policy.Binary, policy.Args...)`, record the outcome, persist the
cache (atomic write: temp file + rename — the same pattern `catalogsync` already uses for its
cursor file).

`agent list-policies` loads the cache and, for each hardcoded policy, computes and prints the same
`isDue`-derived state the daemon would use, without executing anything or requiring a running
daemon.

## Variable Data Directory (`var_path`)

New `common/config` addition, not agent-specific — a standard directory for runtime/variable data
(cache files, state files) that future components can also adopt, distinct from `local.conf`'s own
location and the mTLS certs directory:

```go
// Config gains:
VarPath string // parsed from "var_path" in local.conf

// analogous to the existing ResolveCertsDir:
func ResolveVarDir(cfg *Config) (string, error) {
    if cfg.VarPath != "" {
        return cfg.VarPath, nil
    }
    exePath, err := os.Executable()
    if err != nil {
        return "", fmt.Errorf("failed to determine executable path: %w", err)
    }
    return filepath.Dir(exePath), nil
}
```

If `var_path` is unset in `local.conf`, it defaults to the directory containing the running
binary — mirroring `ResolveBaseDir`'s own fallback, but resolved independently of
`MP_CONFIG_PATH`, since variable data and config-file location are orthogonal concerns that happen
to share the same default today.

The agent's cache file lives at `<var_dir>/agent-state.json`.

## Due-ness and Backoff

```go
func isDue(p Policy, s PolicyState, now time.Time) bool {
    if s.ConsecutiveFailures == 0 {
        if s.LastSuccessAt == nil {
            return true // never succeeded, run immediately
        }
        return now.Sub(*s.LastSuccessAt) >= p.Interval
    }
    return s.LastAttemptAt == nil || now.Sub(*s.LastAttemptAt) >= backoff(s.ConsecutiveFailures)
}

func backoff(failures int) time.Duration {
    const base, max = 30 * time.Second, 10 * time.Minute
    d := base * time.Duration(1<<min(failures, 8)) // cap exponent, avoid overflow
    if d > max {
        d = max
    }
    // half jitter: never near-zero, still spreads retries across a fleet
    return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}
```

Two different cadences, chosen deliberately: a **healthy** policy is due strictly on its own
`Interval` (predictable, e.g. every 5 minutes); a **failing** policy backs off exponentially with
jitter, capped at `max`, decoupled from `Interval` — this stops a persistent failure (e.g. CA
down) from hammering `certclient` every reconcile tick, and the jitter prevents every agent in a
fleet with the same failure from retrying in lockstep.

`ReconcileIntervalSec` (the loop's own tick cadence) must be shorter than any policy's `Interval`
or `backoff` floor to actually observe due-ness promptly — not a hard requirement the code
enforces, just an operational expectation worth documenting.

## Data Flow

```
agent serve:
  varDir := ResolveVarDir(cfg)
  ticker every ReconcileIntervalSec:
    for each policy in policies:
      state := cache[policy.ID]  // zero value if absent
      if isDue(policy, state, now):
        err := exec.Command(policy.Binary, policy.Args...).Run()
        state.LastAttemptAt = &now
        if err == nil:
          state.LastSuccessAt = &now
          state.ConsecutiveFailures = 0
        else:
          state.ConsecutiveFailures++
          log.Error(...)
        cache[policy.ID] = state
        persist cache to <varDir>/agent-state.json (temp file + rename)

agent list-policies:
  varDir := ResolveVarDir(cfg)
  cache := load(<varDir>/agent-state.json)
  for each policy in policies:
    state := cache[policy.ID]
    print policy.ID, health(state), state.LastSuccessAt, state.LastAttemptAt,
          state.ConsecutiveFailures, estimatedNextRun(policy, state)
```

`health(state)`: `"never run"` if `LastSuccessAt == nil && LastAttemptAt == nil`; `"retrying
(N failures)"` if `ConsecutiveFailures > 0`; else `"ok"`.

`estimatedNextRun`: `LastSuccessAt.Add(Interval)` in the healthy case, or
`LastAttemptAt.Add(backoff(ConsecutiveFailures))` in the failing case — the same expressions
`isDue` itself compares `now` against, just surfaced as a timestamp instead of a boolean, so the
CLI can never drift from what the daemon would actually do.

## Config (new `local.conf` keys)

- `var_path` — standard directory for variable/runtime data; unset means "the running binary's own
  directory" (`common/config`, not agent-specific — see above)
- `ReconcileIntervalSec` — daemon tick cadence *(default: 30)*

The cert-refresh policy's own `Interval` (5 minutes) is a compiled-in constant for this iteration,
not a config key — it becomes config-driven (or policy-server-driven) in proposal #2's iteration,
not this one.

## CLI Shape

Following the existing subcommand convention (`bwfs <path> server`/`list`, `rwfs list`/`verify`)
rather than flags:

- `agent serve` — run the reconcile loop (the long-lived daemon).
- `agent list-policies` — one-shot, read-only, prints the table below and exits.

```
POLICY         STATE               LAST SUCCESS         LAST ATTEMPT         FAILURES  NEXT RUN
cert-refresh   ok                  2026-07-03 14:32:10  2026-07-03 14:32:10  0         2026-07-03 14:37:10
```

## Error Handling

- **`certclient` exits non-zero**: `ConsecutiveFailures` increments, `LastAttemptAt` updates,
  `LastSuccessAt` untouched; next attempt governed by `backoff`, not `Interval`.
- **`certclient` binary missing/not executable**: `exec.Command` returns an error before running
  anything; treated identically to a non-zero exit — same backoff path, logged loudly since it's a
  misconfiguration.
- **Cache file missing or corrupt on startup**: treated as an empty cache (every policy looks
  "never run", so it executes on the first tick) — same fail-safe default direction chosen in
  proposal #2 ("on any doubt, assume not yet done, never assume done").
- **`var_path` directory doesn't exist**: created on first write (`os.MkdirAll`), consistent with
  how `bwfs` already creates its own storage directories.
- **Agent restart mid-backoff**: no special handling needed — `LastAttemptAt` and
  `ConsecutiveFailures` are already durable in the cache, so `isDue` computes the same answer
  after a restart as it would have without one.
- **Cache write failure (e.g. disk full)**: logged as an error; the in-memory tick loop continues
  regardless, but the next tick may redundantly re-run the same policy since its success wasn't
  durably recorded — same accepted, harmless-if-wasteful direction as everywhere else in this
  design.

## Testing

- Unit: `isDue` — never-run policy is due; healthy policy is due only after `Interval` elapses;
  failing policy is due only after `backoff` elapses, not `Interval`.
- Unit: `backoff` — monotonically non-decreasing with `ConsecutiveFailures` up to `max`, jittered
  output falls within `[d/2, d]`.
- Unit: `ResolveVarDir` — returns `cfg.VarPath` when set, else the executable's directory.
- Unit: cache JSON round-trip; corrupt/missing file loads as empty, not an error.
- Integration: `agent serve` with a stubbed `certclient` that fails N times then succeeds — assert
  `ConsecutiveFailures` resets to 0 and `LastSuccessAt` updates only after the real success.
- Integration: `agent list-policies` against a pre-seeded cache file produces the expected table
  without invoking `certclient` at all (read-only, no side effects).

## Documentation Impact

Per `.claude/CLAUDE.md`:

- Add `docs/components/agent.md` — new component doc: what it is, `serve`/`list-policies`, the
  embedded cert-refresh policy, `var_path`/cache file location and format.
- Update `docs/ARCHITECTURE.md` — `agent` joins the Agents column, replacing the bare cron entry
  for `certclient` in that description.
- Update `README.md` component list.

## Relationship to Proposals #1 and #2

This is the first concrete implementation slice. Proposal #1's queue consumer and proposal #2's
policy-fetch loop are both additional trigger sources meant to be added onto this same `agent`
binary later, feeding the same shape of `Execute`-and-record-outcome logic this version
establishes for a single hardcoded policy. Nothing here is expected to be thrown away — the
`Policy`/`PolicyState`/`isDue`/`backoff`/`var_path` primitives should carry forward largely
unchanged when policies become fetched-over-the-network instead of compiled-in, and when a
queue-triggered action needs the same "run this binary, record what happened" execution
primitive.
