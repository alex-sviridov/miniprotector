# agent

Node-level agent that reconciles local state against a small, config-driven set of policies.
It runs three embedded, statically-configured policies — `bootstrap-refresh`, `operating-refresh`,
and `policy-update` — the first two keep this node's two-tier mTLS credential (see
[Security Model](../SECURITY.md)) fresh via `certclient`; the third fetches this node's applicable
policies (backup and storage) from `policy-server` (see [policy-server](./policy-server.md)) into a
local cache via `policyclient`. On top of those three, `agent` derives four kinds of dynamic work
from that cache: a **backup task** for every `(cached policy, object_filters path)` pair, executed
via `brfs` on its own schedule (see "Policy-driven backup execution" below); a supervised
`bwfs server` process for every cached `"storage"`-typed policy, kept running rather than scheduled
(see "Storage-policy supervision" below); a one-shot **restore verification task** for every
cached `"restore"`-typed policy whose `mode` is unset or `"verify"`, executed via `rwfs verify`
(see "Policy-driven restore verification" below); and a one-shot **restore execution task**
instead, for every cached `"restore"`-typed policy whose `mode` is `"restore"`, executed via
`rwfs restore` (see "Policy-driven restore execution" below).

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
POLICY              STATE               LAST SUCCESS         LAST ATTEMPT         FAILURES  ERROR  NEXT RUN
bootstrap-refresh    ok                  2026-07-03 14:32:10  2026-07-03 14:32:10  0         -      2026-07-04 14:32:10
operating-refresh    ok                  2026-07-05 09:10:00  2026-07-05 09:10:00  0         -      2026-07-05 09:25:00
```

The cache file lives at `<var_dir>/agent-state.json`, where `<var_dir>` is `var_path` from
`local.conf` if set, otherwise the directory containing the running binary (see `common/config`).
A missing or corrupt cache is treated as empty — every policy then looks "never run" and executes
on the next tick, the same fail-safe direction used everywhere else in this component.

`agent-state.json` now has a second reader: `policyclient fetch` reads the `"bootstrap-refresh"`
entry's `last_attempt_at`/`last_error` out of it to report a stuck bootstrap renewal to
`policy-server` (see [policyclient](policyclient.md) and
[Design: Bootstrap Certificate Renewal](../superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md)),
so the file's shape — the task IDs used as top-level keys, and `PolicyState`'s JSON tags — is a
cross-binary contract, not purely `agent`-internal state; a change to either must account for that
reader, which cannot import `cmd/agent` and would otherwise silently start reporting every node as
healthy. `cache_test.go`'s
`TestWriteCache_BootstrapRefreshEntryMatchesPolicyclientReadShape` pins that contract.

## Policy-driven backup execution

Every reconcile tick, `agent` re-reads `policies-cache.json` fresh (so it notices `policy-update`
refreshing the cache without needing a restart) and derives one backup task per
`(policy, object_filters path)` pair, considering only cached policies whose `type` is `"backup"` —
today the `"storage"` type also exists, and a cached policy of any non-backup type is silently
skipped, contributing zero backup tasks, the same fail-safe direction already used for an
unparseable `rpo` or missing `backup_window` below; see
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md).
Each task is tracked independently in `agent-state.json`
(ID: `backup:<policy-name>:<path>:<short-filter-id>`, where `<short-filter-id>` is the first 8
characters of that object filter's `policy-server`-computed ID with dashes stripped) — one path's
failures and backoff never affect any other path, including a sibling path in the same policy. The
suffix exists so two object filters sharing the same `path` within one policy (e.g. one filtering
with `include`, one with `exclude`, both scoped to the same root) still get distinct task-tracking
entries instead of silently sharing one.

A backup task is due when **both**:
- a `backup_window` cron slot is currently open (a trigger fired within the last
  `BackupWindowGraceSec`, and hasn't closed yet), **and**
- the path's last successful backup is older than the policy's `rpo` (or it has never succeeded).

When due, `agent` execs `brfs <path> --destination <destinations[0]> --job-id
backup:<policy>:<slug(path)>:<short-filter-id>:<timestamp>`, appending `--include <patterns>`
and/or `--exclude <patterns>` (comma-joined) only when the object filter that produced this task
actually carries them — the explicit job-id lets an operator correlate a `bwfs` job record back to
the exact policy and object filter that produced it, even when two filters share a path.
Unlike the three static policies,
backup task execs run in a background goroutine rather than the synchronous reconcile loop, so a
long-running backup never delays `bootstrap-refresh`/`operating-refresh`. Concurrency is bounded by
`MaxConcurrentBackupJobs`; a due task that can't acquire a slot this tick simply stays due and is
retried next tick. Independently of that cap, a task still running from a previous tick is never
re-dispatched — each `(policy, path)` pair can have at most one in-flight `brfs` exec at a time.
On `agent serve` shutdown (`SIGTERM`), in-flight backup execs are terminated cleanly rather than
orphaned — the resulting `bwfs` job simply never completes, the same outcome already assigned to a
crashed `brfs`.

A policy with an unparseable `rpo`, or no valid `backup_window` entry at all, contributes no tasks.
A policy whose `destinations` is empty (its storage policy has no live checkins yet, or
`storage_policy_id` is dangling) likewise contributes no task, for any of its object filters — rather
than exec'ing `brfs` with an empty `--destination`, which would silently default to `localhost`
instead of failing loudly. Each skip is logged with the policy and would-be job id. Only
`destinations[0]` is ever used; retrying the rest of the list is not implemented.

A policy whose `disabled_at` has passed also contributes no tasks -- checked fresh every reconcile
tick against the current time, so a policy that becomes disabled between two ticks stops being acted
on at the very next one, without waiting for `policy-update` to refresh the cache. Its existing
`agent-state.json` entry is removed the same way a deleted policy's already is: it simply stops
appearing in that tick's task list, which the existing pruning in `reconcile.go` already handles.

A backup task's `agent-state.json` entry is removed automatically once its `(policy, path)` pair no
longer appears in `policies-cache.json` — checked every reconcile tick, but only acted on when that
tick's read of the cache file succeeded; a momentarily missing or corrupt cache file never triggers
pruning, so a transient read glitch can never be mistaken for "every policy was removed" and wipe a
live task's backoff/RPO history.

`agent list-policies` shows backup tasks as additional rows (`backup:<policy>:<path>:<short-filter-id>`) alongside
the three static policies; a task's "NEXT RUN" reflects its next `backup_window` occurrence rather
than a fixed interval. Each row's `ERROR` column shows the most recent failure's message (truncated
to 60 characters, `-` if there isn't one), cleared automatically on that policy/task's next success.

## Storage-policy supervision

Every reconcile tick, alongside deriving backup tasks, `agent` also derives two independent
**ensure-running** tasks per cached policy whose `type` is `"storage"` — unlike a backup task (or
the three static policies), neither is a due/execute/complete unit: one is "this `bwfs server`
process should be running," the other is "this `catalogsync` process should be running," each
checked and corrected every tick rather than scheduled on an interval. There is no per-node
targeting check here — `policy-server`'s `GetPolicies` already scoped `policies-cache.json` to this
node via `client_filters` (the same mechanism a backup policy uses), so every `"storage"`-typed
policy in the cache is already meant for this node.

A storage policy's `config` is opaque JSON to `policy-server`, but `agent` interprets one shape:
`{"backend": "filesystem", "root": "/data/storage"}`. Any other or missing `backend` value is
skipped with a logged error (contributing neither task), the same fail-safe direction as an
unparseable `rpo` or missing `backup_window` for backup tasks. A matching policy becomes two
processes: `bwfs <root> server --port <port>` and `catalogsync <root>`.

A storage policy whose `disabled_at` has passed is skipped the same way, contributing neither the
`bwfs` nor the `catalogsync` ensure-running task -- an already-running pair is stopped via the same
path used when the policy is edited or deleted outright.

The two tasks are supervised entirely independently, with no ordering or coordination between them
— not even at first startup. `catalogsync` opens `bwfs`'s database read-only
(`mode=ro`), which fails cleanly rather than corrupting anything if `catalogsync` happens to start
before `bwfs` has created it; that failure is handled by the same crash-restart-with-backoff path
described below, no differently than any other transient exec failure.

Each task is supervised under its own ID (`storage:<policy-name>` for `bwfs`,
`storage:<policy-name>:catalogsync` for `catalogsync`, mirroring the `backup:` prefix convention): a
start is recorded as success (not "exited successfully" — neither is expected to exit on its own)
only once the process has stayed running for a short stability window (a few seconds) — a crash
faster than that never resets the failure count, so a persistently crash-looping process accumulates
failures instead of bouncing back to "1 failure" on every restart. An unexpected exit is recorded as
a failure with the same jittered `backoff()` reconcile.go already uses elsewhere, and a policy that's
edited (port/path changed) or removed causes both running processes to be stopped (`SIGTERM`, a
graceful drain for `bwfs` — see [bwfs](./bwfs.md) — and for `catalogsync`, which already honors it)
and, for an edit, fresh ones started with the new arguments; a `Stop()` issued while a supervisor is
sitting out a crash-backoff wait takes effect immediately rather than waiting out the remaining
backoff. `agent list-policies` shows each supervised task as its own additional row, reusing the
same STATE/FAILURES/ERROR columns as everything else, with `NEXT RUN` always `-` since there's no
schedule to estimate.

See [Design: agent storage-policy supervision](../superpowers/specs/2026-07-28-agent-storage-supervision-design.md)
and [Design: agent catalogsync supervision](../superpowers/specs/2026-07-31-agent-catalogsync-supervision-design.md).

## Policy-driven restore verification

Every reconcile tick, alongside backup tasks and storage supervision, `agent` derives one
verification task per cached `"restore"`-typed policy whose `mode` is `""`/`"verify"` (ID:
`verify:<policy-name>`) — unlike a backup task, there is exactly one task per policy, not one per
rule or per host, since a restore policy's `rules` aren't cleanly partitionable by host (a folder
rule can be host-agnostic). A policy whose `destinations` is empty (its `storage_policy_id` has no
live checkins yet, or is dangling) contributes no task, logged the same way an unresolved backup
destination already is.

A restore task is **one-shot**: due until it first succeeds, retried with the same jittered
backoff every other failing policy uses, and never dispatched again afterward for as long as this
exact policy still appears in `policies-cache.json` (a restore policy is deletable — deleting it
removes its task the same way any orphaned task's `agent-state.json` entry is pruned).

When due, `agent` execs `rwfs verify <destinations[0]> --rules-stdin --job-id
verify:<policy>:<timestamp>`, piping the policy's `rules` as `{"rules": [...]}` on the child's
standard input — see [rwfs](./rwfs.md)'s `--rules-stdin` mode for how that's resolved into an
actual pass/fail. `list-policies` shows each restore task as an additional row
(`verify:<policy>`), same columns as everything else; a permanently-succeeded one-shot task's
`NEXT RUN` column reads "due now" even though it will never run again — a known, accepted display
quirk (see [Design: Restore Policy Verification Execution](../superpowers/specs/2026-08-10-restore-policy-verification-design.md)), not a functional bug.
This path has browser-driven integration coverage in `web/e2e/restore-verify.spec.js`
(`docs/superpowers/specs/2026-08-13-restore-verification-e2e-design.md`) — a real backed-up file
verifying successfully, and a rule naming a file that was never backed up failing — both read from
the real, rendered `/jobs/:job_id` log.

## Policy-driven restore execution

A `"restore"`-typed policy whose `mode` is `"restore"` gets a task with a `restore:<policy-name>`
ID instead -- otherwise identical to restore verification above (one task per policy, one-shot,
same failure backoff, `list-policies` row). `agent` execs `rwfs restore <destinations[0]>
--rules-stdin --job-id restore:<policy>:<timestamp>`, with `--overwrite` appended when the policy's
`overwrite` field is set, piping the same `{"rules": [...]}` payload verification uses.

`rwfs restore` resolves the policy's rules against the live store and creates the resolved
directory structure on the destination filesystem (parent before child, aborting on the first
failure) -- see [rwfs](./rwfs.md)'s `restore` section and [Design: Restore Directory Structure
Phase](../superpowers/specs/2026-08-16-restore-directory-structure-design.md). File content restore
is still log-only: for each resolved file it logs the source path and renamed destination path but
writes nothing; a future round adds that.

## Logging and correlation

Every binary `agent` execs writes structured JSON logs to `<log_dir>/<binary-name>.log` (one
stable, rotated file per binary — not one file per invocation), and every exec `agent` dispatches
now carries a `--job-id` (auto-generated per invocation if not explicitly set): `<policy-id>:
<unix-timestamp>` for the three static policies, `backup:<policy>:<slug(path)>:<short-filter-id>:<timestamp>`
for backup tasks, `verify:<policy>:<timestamp>` for restore *verification* tasks,
`restore:<policy>:<timestamp>` for restore *execution* tasks. That same job-id
rides as outgoing gRPC metadata to whatever server the
exec calls (`issuer` for `certclient operating-refresh`, `policy-server` for `policyclient`, `bwfs`
for `brfs`, and `bwfs` again for `rwfs verify`'s `ListFiles`/`RestoreFile` calls on the
restore-verification path — `bwfs`'s list/restore handlers don't read that metadata back yet, so
for now `rwfs`'s half of that pair correlates `agent`'s log with `rwfs`'s own, like
`bootstrap-refresh` below), and each of the other servers tags its own log lines with the identical value — so one
job-id correlates `agent`'s own start/completion log line, the exec's local log file, and the
corresponding log line on whichever remote host it called, end to end. The `bootstrap-refresh`
policy is the one exception: `certclient renew` talks to step-ca's stock `/renew` endpoint
directly, not one of this project's own instrumented servers, so its job-id correlates `agent`'s
own log and `certclient`'s local log only — there is no remote log line to find for it. `agent`'s
own log
(`<log_dir>/agent.log`) records a start and a completion line (success or failure, with exit code
when available) for every dispatched exec, not just failures.

`agent` also bundles, configures, and directly supervises a Vector process that tails `log_dir`
and ships every line to `log-gateway` over mTLS, using this node's own operating certificate --
restarted immediately after every successful `operating-refresh` (so a rotated cert is always
picked up promptly) and crash-restarted with backoff otherwise, the same `backoff()` failing
policies already use. Vector's own HTTP API is never enabled, so this adds no listening socket to
`agent`'s footprint, which stays outbound-only. `log-gateway` authenticates the push but never
inspects its body (see [Security Model](../SECURITY.md)), so `agent` is the one that sets each
shipped stream's `hostname` label -- read from this node's own `bootstrap.crt` `CommonName`, the
same source `certclient`'s `operating-refresh` already uses to know its own hostname. The Loki
sink's `encoding.codec: text` stores only each event's own log line (the app's slog JSON) as the
shipped line text, since `binary`/`hostname`/`job_id`/`event`/`status` are already carried
separately as Loki labels/structured metadata -- `codec: json` would instead wrap the whole Vector
event, including those already-duplicated fields, into the stored line. See
[Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md).

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
| `log_gateway_host` / `log_gateway_port` | none / 9400 | Where agent's supervised Vector process pushes logs, via `log-gateway` |

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
- [log-gateway](./log-gateway.md) — receives this node's shipped logs
- [Security Model](../SECURITY.md) — the two-tier credential model these policies maintain
- [Architecture](../ARCHITECTURE.md)
- [Design: Agent v1](../superpowers/specs/2026-07-03-agent-v1-cert-refresh-design.md)
