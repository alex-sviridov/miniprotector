# Policy-Driven Backup Reconciliation — Design

> Proposal #2, building on `2026-07-03-agent-job-dispatch-design.md` (proposal #1) rather than
> replacing it. That doc's job queue and cert-renewal loop are retained for irregular/imperative
> work (vacuum, restore, ad hoc immediate backups) and agent upkeep; this doc adds recurring,
> declarative backup scheduling as a third trigger source feeding the same `agent` executor.

## Problem

Proposal #1 explicitly deferred scheduling ("no scheduler component"). Recurring backup schedules
("back up `/var/log/audit` hourly, Mon-Fri, on every listed prod host") don't fit that model
naturally — expressing them as discrete published jobs would require *something* to repeatedly
compute due-ness and publish, which reintroduces the centralized-decision/SPOF concern the queue
design was trying to avoid, just for scheduling instead of execution. This doc proposes a
declarative, reconciliation-style alternative for recurring policies specifically, where each
agent knows its own schedule and needs no round trip to decide when to run.

## Goals

- Operators express backup requirements declaratively ("this path, on these hosts, on this
  schedule") instead of manually publishing individual jobs.
- Recurring backups keep firing on schedule even when the policy server is unreachable, once
  policies have been fetched at least once.
- No "when did I last back this up" tracking or store — the schedule itself, not remembered
  history, determines when the next run fires.
- Stay within the same agent shape as proposal #1: one generic executor; new logic lives only in
  how it's triggered, not in a new execution path.

## Non-Goals (v1)

- No fleet/tag-based selectors. Policies list explicit target hostnames, matched against the
  agent's own verified mTLS identity — the same derivation `bwfs` already uses for `source_host`
  (see `docs/superpowers/specs/2026-07-02-backup-job-tracking-design.md`). Group-based targeting
  ("all prod servers") is future work, likely via a verified role/tag claim embedded in the cert
  by `certrequest`, not a client-declared value.
- No policy CRUD API or admin UI. The policy server serves policies from a static config file an
  operator edits directly on its host — mirrors proposal #1's choice to skip a producer/scheduler
  service for v1.
- No catch-up/backfill for a missed scheduled run (agent was down, or the previous run for that
  policy was still in flight). The next natural tick is the retry; there is no "make up for lost
  time" logic.
- No catalog dependency for scheduling decisions — established during design discussion: `catalog`
  is never on the agent's decision path. Policies plus locally-materialized timers are
  self-sufficient.

## Architecture

New component:

- **Policy server** — new control-plane component, alongside `ca`/`catalog`/`nats`. Serves a
  static, operator-maintained policy list over mTLS. Deliberately dumb: no scheduling logic, no
  per-host due-time computation — it only answers "what policies target this host."

Agent changes (extends the `agent` binary from proposal #1, does not replace it):

- **Policy-fetch loop**: ticker at `PolicyFetchIntervalSec`, calls the policy server for policies
  targeting this host's verified hostname.
- On every successful fetch: write the returned list to a local cache file *before* doing anything
  else — this is the only new persisted local state this design needs.
- **Reconcile step**: diff the fetched (or cache-loaded, on startup) list against currently
  registered in-memory cron entries:
  - new `policy_id` → register a cron entry for its schedule
  - existing `policy_id`, schedule unchanged → leave alone
  - existing `policy_id`, schedule changed → replace the entry
  - previously-registered `policy_id` no longer present → deregister it
- **On startup**, before the first network fetch completes, load the local cache file (if present)
  and reconcile from it immediately — this closes the "agent restarts while policy server is
  unreachable" gap.
- **On each cron fire**: acquire a per-policy `flock` (reusing `github.com/gofrs/flock`, already a
  dependency) at a well-known path derived from `policy_id`; skip (log only) if already held;
  otherwise call the same `Execute("brfs", argsFromPolicy)` used by proposal #1, releasing the
  lock when it returns regardless of outcome.

Depends on a cron-expression library (e.g. `robfig/cron`) — new dependency, used only for parsing
schedule strings and firing in-process callbacks; no persistence or distributed behavior of its
own.

## Data Flow

```
Policy server (static config file, operator-maintained)
  → agent, every PolicyFetchIntervalSec, requests policies for its own hostname
       success: write list to local policy_cache.json, then reconcile timers
       failure: keep using whatever's currently registered in memory; retry next tick

agent startup:
  → load policy_cache.json if present, reconcile timers from it immediately
  → then proceed with the normal fetch loop above

cron fire for policy P (e.g. Tue 14:00, "0 * * * mon-fri"):
  → flock(lockPathFor(P))
       held already (previous run still going)? → log skip, done
       acquired? → Execute("brfs", ["--destination", bwfsHost, P.path])
                   → release flock regardless of exit code
```

Note there's no `job_id`-reuse trick here the way proposal #1 needed for queue redelivery — there
is no redelivery in this design. If a run fails or is skipped, the next scheduled tick is the
retry; `brfs` generates its own `job_id` per invocation as it already does today.

## Schema

```go
// policy server's static config, one entry per policy
type Policy struct {
    PolicyID string   `yaml:"policy_id"`
    Hosts    []string `yaml:"hosts"`    // explicit hostnames this policy targets
    Path     string   `yaml:"path"`     // source path brfs should back up
    Schedule string   `yaml:"schedule"` // cron expression
}

// agent's local cache, written after every successful fetch
type PolicyCache struct {
    FetchedAt time.Time `json:"fetched_at"`
    Policies  []Policy  `json:"policies"` // already filtered to this host
}
```

## Config (new `local.conf` keys)

- `policy_host` / `policy_port` — policy server to fetch from
- `PolicyFetchIntervalSec` — how often the agent re-fetches policies *(default: 600)*
- `policy_cache_path` — where the local cache file lives *(default: under the agent's state dir)*

## Deployment

New `policy-server` service in `deploy/control-plane/docker-compose.yml`, alongside
`ca`/`catalog`/`nats`. Reads its policy list from a mounted config file; no database.

## Error Handling

- **Policy server unreachable**: agent keeps running whatever's currently registered (from cache
  or a prior fetch); only *new* policy changes fail to propagate until it's reachable again. No
  interruption to already-scheduled recurring backups.
- **Agent restart while policy server is unreachable**: closed by the local cache — timers are
  re-materialized from `policy_cache.json` before the first network attempt.
- **Two runs overlapping for the same policy**: guarded by `flock`; the later tick is skipped, not
  queued or retried early — it fires again at its next natural scheduled time.
- **Agent crash mid-`brfs`-execution**: the `flock` is released automatically when the process
  holding it exits (OS-level), so a restarted agent won't be blocked by a stale lock. If the
  orphaned `brfs` child is still running when the next tick fires, this can briefly result in two
  concurrent runs against the same policy — the same accepted, dedup-absorbed limitation already
  noted in proposal #1's Error Handling, not new to this design.
- **Malformed or unparseable schedule string in a fetched policy**: that single policy is skipped
  (logged loudly), not the entire fetch — one bad policy shouldn't disable every other schedule on
  the host.

## Testing

- Unit: the reconcile step correctly adds/updates/removes cron entries given two policy-list
  snapshots.
- Unit: `PolicyCache` JSON round-trip.
- Unit: a malformed schedule string is skipped without affecting other policies in the same fetch.
- Integration: agent started with a pre-seeded `policy_cache.json` and no reachable policy server
  still fires scheduled runs.
- Integration: two rapid cron fires for the same policy (simulated) result in exactly one
  `Execute` call in flight at a time; the second is skipped, not queued.
- Integration: a policy removed from the served list on the next fetch results in its cron entry
  being deregistered (verified by asserting it no longer fires).

## Documentation Impact

Per `.claude/CLAUDE.md`:

- Add `docs/components/policy-server.md`; update `docs/components/agent.md` (from proposal #1)
  with the policy-fetch/reconcile loop.
- Update `docs/ARCHITECTURE.md` — policy server joins the control-plane row; mermaid diagram gains
  the policy-fetch edge.
- Add `docs/protocols/policy-fetch.md` documenting the request/response shape and the
  `Policy`/`PolicyCache` schemas.

## Relationship to Proposal #1

This doc narrows, not replaces, `2026-07-03-agent-job-dispatch-design.md`'s scope: recurring
backups are now policy-driven (this doc), while the NATS job queue from proposal #1 remains the
mechanism for irregular, imperative, one-off work — vacuum, restore, and ad hoc immediate backups
outside of any schedule. Both trigger sources, plus the cert-renewal ticker, feed the same
`Execute(action, args)` primitive defined in proposal #1; nothing about that executor or its
allowlist changes.
