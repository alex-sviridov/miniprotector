# agent

Node-level agent that reconciles local state against a small set of policies compiled into the
binary. **v1** has exactly one embedded policy — renew this node's mTLS identity via `certclient`
on a fixed interval — intended to replace the bare cron entry used previously. `agent` does not fetch
policies over the network yet; see the
[design doc](../superpowers/specs/2026-07-03-agent-v1-cert-refresh-design.md) for how this grows
into policy-server-fetched and queue-dispatched work in later iterations.

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

`agent serve` ticks every `ReconcileIntervalSec` seconds. On each tick, for every embedded policy
it checks whether the policy is due — a healthy policy is due once its own `Interval` has elapsed
since the last success; a policy that's currently failing is due once a jittered backoff period
(computed once per failure, not re-derived on every check) has elapsed instead, decoupled from
`Interval`. When due, `agent` execs the policy's binary (`certclient`, with no arguments, for the
embedded `cert-refresh` policy) and records the outcome — success or failure, and a running count
of consecutive failures — to a local JSON cache file.

`agent list-policies` reads that same cache file and prints each policy's health and estimated
next run time, without executing anything or requiring a running `agent serve` process:

```
POLICY         STATE               LAST SUCCESS         LAST ATTEMPT         FAILURES  NEXT RUN
cert-refresh   ok                  2026-07-03 14:32:10  2026-07-03 14:32:10  0         2026-07-03 14:37:10
```

The cache file lives at `<var_dir>/agent-state.json`, where `<var_dir>` is `var_path` from
`local.conf` if set, otherwise the directory containing the running binary (see `common/config`).
A missing or corrupt cache is treated as empty — every policy then looks "never run" and executes
on the next tick, the same fail-safe direction used everywhere else in this component.

## Configuration Keys

| Key | Default | Description |
|-----|---------|-------------|
| `var_path` | binary's own directory | Directory for runtime/variable data (the cache file) |
| `ReconcileIntervalSec` | 30 | How often `agent serve` checks whether any policy is due |

## Building

```bash
make agent
```

## See Also

- [certclient](./certclient.md) — the binary `agent`'s embedded `cert-refresh` policy execs
- [Architecture](../ARCHITECTURE.md)
- [Design: Agent v1](../superpowers/specs/2026-07-03-agent-v1-cert-refresh-design.md)
