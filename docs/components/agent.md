# agent

Node-level agent that reconciles local state against a small, config-driven set of policies.
It runs three embedded, statically-configured policies — `bootstrap-refresh`, `operating-refresh`,
and `policy-update` — the first two keep this node's two-tier mTLS credential (see
[Security Model](../SECURITY.md)) fresh via `certclient`; the third fetches this node's applicable
backup policies from `policy-server` (see [policy-server](./policy-server.md)) into a local cache
via `policyclient`. On top of those three, `agent` also derives a dynamic **backup task** for every
`(cached policy, object_filters path)` pair in that cache, and executes `brfs` for each one on its
own schedule — see "Policy-driven backup execution" below.

## Usage

```bash
# Run the reconcile loop (long-lived)
agent serve

# Inspect policy state without running anything
agent list-policies
```

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` (serve only) | false | Enable debug logging |

## Behavior

`agent serve` ticks every `ReconcileIntervalSec` seconds. On each tick, for every policy it checks
whether the policy is due — a healthy policy is due once its own `Interval` has elapsed since the
last success; a policy that's currently failing is due once a jittered backoff period (computed
once per failure, not re-derived on every check) has elapsed instead, decoupled from `Interval`.
When due, `agent` execs the policy's binary and records the outcome — success or failure, and a
running count of consecutive failures — to a local JSON cache file.

`agent`'s three policies:

| Policy | Execs | Interval | Refreshes |
|--------|-------|----------|-----------|
| `bootstrap-refresh` | `certclient renew` | `BootstrapCertRefreshIntervalSec` | The long-lived bootstrap credential (`bootstrap.crt`) via the CA's `/renew` |
| `operating-refresh` | `certclient operating-refresh` | `OperatingCertFetchIntervalSec` | The short-lived operating certificate (`client.crt`/`client.key`) via `issuer` |
| `policy-update` | `policyclient fetch` | `PolicyFetchIntervalSec` | The local backup-policy cache (`policies-cache.json`) via `policy-server` |

`agent list-policies` reads that same cache file and prints each policy's health and estimated
next run time, without executing anything or requiring a running `agent serve` process:

```
POLICY              STATE               LAST SUCCESS         LAST ATTEMPT         FAILURES  NEXT RUN
bootstrap-refresh    ok                  2026-07-03 14:32:10  2026-07-03 14:32:10  0         2026-07-04 14:32:10
operating-refresh    ok                  2026-07-05 09:10:00  2026-07-05 09:10:00  0         2026-07-05 09:25:00
```

The cache file lives at `<var_dir>/agent-state.json`, where `<var_dir>` is `var_path` from
`local.conf` if set, otherwise the directory containing the running binary (see `common/config`).
A missing or corrupt cache is treated as empty — every policy then looks "never run" and executes
on the next tick, the same fail-safe direction used everywhere else in this component.

## Policy-driven backup execution

Every reconcile tick, `agent` re-reads `policies-cache.json` fresh (so it notices `policy-update`
refreshing the cache without needing a restart) and derives one backup task per
`(policy, object_filters path)` pair. Each task is tracked independently in `agent-state.json`
(ID: `backup:<policy-name>:<path>`) — one path's failures and backoff never affect any other path,
including a sibling path in the same policy.

A backup task is due when **both**:
- a `backup_window` cron slot is currently open (a trigger fired within the last
  `BackupWindowGraceSec`, and hasn't closed yet), **and**
- the path's last successful backup is older than the policy's `rpo` (or it has never succeeded).

When due, `agent` execs `brfs <path> --destination <destination> --job-id
backup:<policy>:<slug(path)>:<timestamp>` — the explicit job-id lets an operator correlate a
`bwfs` job record back to the policy and path that produced it. Unlike the three static policies,
backup task execs run in a background goroutine rather than the synchronous reconcile loop, so a
long-running backup never delays `bootstrap-refresh`/`operating-refresh`. Concurrency is bounded by
`MaxConcurrentBackupJobs`; a due task that can't acquire a slot this tick simply stays due and is
retried next tick. On `agent serve` shutdown (`SIGTERM`), in-flight backup execs are terminated
cleanly rather than orphaned — the resulting `bwfs` job simply never completes, the same outcome
already assigned to a crashed `brfs`.

A policy with an unparseable `rpo`, or no valid `backup_window` entry at all, contributes no tasks.
A missing or invalid `destination` is not checked in advance — the task is still created, and its
`brfs` exec simply fails (recorded as an ordinary failure with backoff), the same as any other exec
failure.

`agent list-policies` shows backup tasks as additional rows (`backup:<policy>:<path>`) alongside
the three static policies; a task's "NEXT RUN" reflects its next `backup_window` occurrence rather
than a fixed interval.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `var_path` | binary's own directory | Directory for runtime/variable data (the cache file) |
| `ReconcileIntervalSec` | 30 | How often `agent serve` checks whether any policy is due |
| `BootstrapCertRefreshIntervalSec` | 86400 (1 day) | How often the `bootstrap-refresh` policy runs `certclient renew` |
| `OperatingCertFetchIntervalSec` | 900 (15 minutes) | How often the `operating-refresh` policy runs `certclient operating-refresh` |
| `PolicyFetchIntervalSec` | 900 (15 minutes) | How often the `policy-update` policy runs `policyclient fetch` |
| `BackupWindowGraceSec` | 3600 (1 hour) | How long after a `backup_window` cron trigger a backup task's window stays "open" |
| `MaxConcurrentBackupJobs` | 2 | Upper bound on simultaneously in-flight `brfs` execs launched by backup tasks |
| `BootstrapCertTTLSec` | 7776000 (90 days) | Intended requested validity for the bootstrap credential. Parsed and defaulted by `common/config`, but not yet consumed by any request path — `certclient bootstrap`/`renew` don't currently pass a requested TTL to the CA, so actual bootstrap credential lifetime is governed entirely by the CA provisioner's own claims today |

## Building

```bash
make agent
```

## See Also

- [brfs](./brfs.md) — the binary backup tasks exec
- [certclient](./certclient.md) — the binary both of `agent`'s credential-refresh policies exec
- [issuer](./issuer.md) — what `operating-refresh` ultimately talks to
- [policyclient](./policyclient.md) — the binary `agent`'s `policy-update` policy execs
- [policy-server](./policy-server.md) — what `policyclient fetch` ultimately talks to
- [Security Model](../SECURITY.md) — the two-tier credential model these policies maintain
- [Architecture](../ARCHITECTURE.md)
- [Design: Agent v1](../superpowers/specs/2026-07-03-agent-v1-cert-refresh-design.md)
