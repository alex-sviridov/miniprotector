# Fleet Log Aggregation — Design

> Builds on the two-tier credential model in `docs/SECURITY.md` and the `issuer`/`agent` machinery
> described in `docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md` — this spec adds
> no new credential type, reusing the existing operating certificate end to end.

## Problem

`agent` execs `certclient`, `policyclient`, and `brfs` on a schedule (see
`docs/components/agent.md`), and each of those — plus `agent serve` itself and every long-running
server (`catalog`, `policy-server`, `bwfs`) — already writes structured JSON logs locally via
`common/logging`, gated by `LogFolder`, a required `local.conf` key. Nothing ships those logs
off-node: an operator who wants to see why a backup task is failing, or confirm a revoke actually
took effect, has to know which of potentially dozens of enrolled hosts to SSH into and which of
many per-invocation log files (`common/logging` currently names one file per process, per PID) to
read. There is no fleet-wide, near-real-time view of backup state.

Separately, today's local log files have no rotation or retention at all — `common/logging` opens
a new file per process invocation and nothing ever deletes an old one. Harmless while logs are
purely a local debugging aid; becomes a real concern once local files are the spool an aggregation
pipeline reads from continuously.

## Goals

- An operator can observe backup state — and any agent-exec'd binary's output — across the fleet,
  near-real-time, from one place, without SSHing to individual nodes.
- Every subprocess `agent` execs (`certclient`, `policyclient`, `brfs`), `agent` itself, and every
  long-running server bundled with an `agent` sidecar (`catalog`, `policy-server`, `bwfs`) is
  covered uniformly, with no per-component special-casing.
- Log identity (which node a log line came from) is cryptographically derived from the same mTLS
  peer certificate every other authenticated call in this project already trusts — never
  self-asserted by the shipping node, matching `docs/SECURITY.md`'s "identity from cert, never a
  request field" rule.
- Revoking a node cuts off its ability to ship logs within one operating-cert refresh cycle, the
  same bound `docs/SECURITY.md` already gives every other operating-cert-gated capability — with no
  new revocation mechanism to build.
- A brief outage of the log-collection backend degrades log visibility temporarily; it never blocks
  or fails an actual backup, cert-refresh, or policy-fetch operation, and recent logs survive the
  outage rather than being silently dropped the instant it starts.
- Local log storage is bounded (rotated/retained), not unbounded.

## Non-Goals (this pass)

- **No coverage for `issuer` or `client-manager`.** `issuer` mints its own mTLS identity outside
  the usual `agent`-bundled flow and could be added later using that self-minted cert;
  `client-manager` has no mTLS identity at all by design (`docs/SECURITY.md`) and cannot
  participate in an mTLS-authenticated pipeline without a design change to `client-manager` itself,
  which is out of scope here. Both stay local-file-only for this pass.
- **No metrics or traces.** Logs only.
- **No custom query/visualization UI.** Grafana (or `logcli`) against Loki is the query interface —
  consistent with this project's existing all-CLI admin ergonomics (`client-manager`,
  `agent list-policies`, `rwfs list`); building a bespoke viewer would duplicate a solved problem.
- **No log-content redaction, sanitization, or injection defense.** A compromised-but-not-yet-
  revoked node can write arbitrary content into its own local log files and have it shipped
  as-is — inherent to log aggregation, not something this design can prevent, and not materially
  different from the trust already extended to any enrolled, non-revoked node.
- **No HA for Loki or the new `log-gateway` component.** Single instance each, the same accepted
  trade-off `issuer`'s design made for its own listening service.
- **No per-job debug-level control.** Today's `--debug` flag toggles debug logging for an entire
  process; there's no way to request verbose logging for one specific troublesome `job_id` (e.g.
  one flaky backup task) without turning on debug output for every invocation of that binary
  fleet-wide. A future mechanism — `agent` recognizing some per-job debug request and passing an
  elevated log level to just that one exec — is worth exploring later; deferred here, noted so it
  isn't lost.
- **No in-application TLS hot-reload for the log shipper.** Confirmed against Grafana's own docs
  and Prometheus's long-standing, still-open behavior (both `loki.write`'s underlying HTTP client
  config and Prometheus's identical lineage): reloading configuration does not reliably reload an
  in-use client certificate — only a full process restart reliably picks up a new one. Handled by
  restarting the shipper on a timer (see Architecture), which is deployment configuration, not
  something this design solves in Go.

## Architecture

### New components

1. **Loki** — internal-only central log store. Runs in single-binary, filesystem-storage mode,
   appropriate to this project's current scale (no S3/ring/HA complexity). Never directly reachable
   from any agent-managed node — the only thing that talks to it is `log-gateway`, on the same host
   or a trusted internal network segment.

2. **`log-gateway`** (new binary, control plane) — a small mTLS-terminating HTTP reverse proxy
   sitting in front of Loki's push endpoint. It is the only new network-facing binary this design
   introduces, and it exists for exactly one reason: Loki's push API has no concept of mTLS peer
   identity, and this project never trusts a caller-asserted identity field (`docs/SECURITY.md`).
   `log-gateway`:
   - Terminates TLS using the same operating-tier verification `bwfs`/`catalog` already use
     (`common/mtls`'s `requireOperatingTier` check, rejecting any peer presenting an
     issuer-caller/bootstrap credential — a leaked bootstrap credential must remain confined to
     `issuer`, exactly as `docs/SECURITY.md` already establishes) — extended to a plain
     `net/http.Server` (`common/mtls` already returns a bare `*tls.Config`; this needs no gRPC-
     specific machinery, just an HTTP-flavored constructor next to the existing one).
   - Derives `hostname` from the verified peer certificate's SAN, the same field
     `mtls.PeerHostname` already reads for every gRPC call.
   - Force-overwrites (never trusts) the `hostname` label on every proxied push request before
     forwarding it to Loki — a node cannot claim to be a different hostname than the one in its own
     certificate, in logs any more than anywhere else in this project.
   - Forwards the corrected request to Loki's real push endpoint.

3. **Grafana Alloy sidecar** — bundled into the same image/deployment as `agent`, so it runs
   wherever `agent` already runs (`agent` itself, `catalog`, `policy-server`, `bwfs`/`brfs`/`rwfs`
   hosts). Tails the standardized log directory (below), batches new lines, and pushes them to
   `log-gateway` over mTLS using the node's existing operating certificate
   (`client.crt`/`client.key`) — no new credential type. Alloy's own on-disk WAL provides local
   buffering across a `log-gateway`/Loki outage; once the WAL's configured bound is exceeded, oldest
   entries are dropped rather than blocking or growing without limit.

   **Cert rotation vs. Alloy's TLS reload:** the operating cert is refreshed in place roughly every
   `OperatingCertFetchIntervalSec` (15 minutes by default) and expires within `OperatingCertTTLSec`
   (1 hour by default). Alloy's `loki.write` component will not pick up a rotated cert file without
   a process restart (see Non-Goals). The fix is deployment-level, not application code: the Alloy
   sidecar process is restarted on a timer shorter than `OperatingCertTTLSec` (matching
   `OperatingCertFetchIntervalSec` is the natural choice, since that's already the cadence at which
   a fresh cert becomes available) via ordinary process supervision (a systemd timer, or a
   restart-loop wrapper in the agent image). Alloy resumes cleanly from its positions file across
   this restart, so scheduled restarts don't lose buffered-but-unsent lines. This also means a
   revoked node's log-shipping ability lapses on its next scheduled Alloy restart — no separate
   revocation path needed, the same property `operating-refresh` already gives every other
   capability.

### Standardized local logging

`common/logging` changes in two ways, applied uniformly to every component (no per-binary
special-casing):

- **Path:** every process writes to `<log_dir>/<binary-name>.log` — one stable filename per binary,
  not one file per process invocation. `log_dir` is a renamed `local.conf` key (was `logfolder`,
  same required-field status, same resolution rules) — no new configuration mechanism introduced.
- **Rotation:** the file handler is backed by a rotation-aware writer (`gopkg.in/natefinch/
  lumberjack.v2` — a standard, widely-used choice for exactly this) instead of a raw
  `os.OpenFile(O_APPEND)` handle, bounded by size/age/backup-count. No cleanup code is added to
  `agent`; rotation is entirely `common/logging`'s responsibility, the same way every component
  already gets file logging for free from that package today.

One consequence, stated plainly: multiple concurrent processes of the *same* binary (e.g. two
`brfs` backup tasks running simultaneously under `MaxConcurrentBackupJobs`) now append to one
shared file rather than each getting its own. This is safe under ordinary POSIX semantics — each
log line is written via a single `Write()` call (`log/slog`'s handlers build a full line into a
buffer and issue one `Write` per record), and a single `write()` syscall on an `O_APPEND`-opened
file descriptor is atomic — so concurrent processes' lines interleave cleanly rather than
corrupting each other. The one edge case worth naming: if lumberjack rotates the file (rename +
reopen) while another process still holds its old file descriptor open, that process's remaining
lines land in the just-rotated backup file until it next reopens (i.e., until its next invocation).
Nothing is lost — Alloy tails rotated files by glob, following both the active and recently-rotated
file, which is standard log-shipper behavior — logs from one invocation can just end up briefly
split across two files in this narrow window.

### Correlation IDs, extended uniformly — including across hosts

`brfs` already does more than locally tag its own logs: it sends `job-id` as outgoing gRPC metadata
(`metadata.AppendToOutgoingContext`), auto-generating a UUID if `--job-id` was omitted, and `bwfs`'s
server *requires* that metadata, extracts it, and tags its own logs with the identical value
(`bwfs/server.go`'s `jobIDFromMetadata`) — which then threads through `catalogsync` into `catalog`'s
own logs too (`JobID` carried on `FileVersionRecord`, through to `catalog/server.go`). A single
`job_id` today already glues `brfs` (source host) → `bwfs` (destination host) → `catalog` (central)
logs together end-to-end. This is pre-existing, not part of this design — but it's the precedent
this design extends, not a new pattern invented for it.

`certclient`/`policyclient` currently have no equivalent, and neither `issuer` nor `policy-server`
reads any request-scoped ID from incoming metadata — so without this extension, cert-refresh and
policy-fetch operations would only be locally correlatable, unlike backups. This design closes that
gap by applying the identical pattern to both remaining paths:

- `certclient` and `policyclient` gain a `--job-id` flag with the same auto-generate-if-omitted
  behavior as `brfs` (a UUID when invoked without one), used both for local log tagging
  (`ctx.Value("jobId")`, as `brfs` already does) and sent as outgoing `job-id` gRPC metadata to
  `issuer`/`policy-server` respectively.
- `issuer` and `policy-server` extract that metadata and require it (same enforcement `bwfs`
  already applies), tagging their own log lines with the identical `job_id`.
- `agent` passes an explicit job-id on every exec, not just backup tasks: for the three static
  policies (`bootstrap-refresh`, `operating-refresh`, `policy-update`), `<policy-id>:
  <unix-timestamp>`, mirroring the shape `brfs`'s job-id already has for backup tasks
  (`backup:<policy>:<slug(path)>:<timestamp>`) — giving every invocation, not just every policy, a
  distinguishable identity, on both ends of the call.
- Since `issuer` and `policy-server` now need the identical metadata-extraction logic `bwfs`
  already has, that logic moves out of `cmd/bwfs` into a small shared helper (e.g.
  `common/mtls` or a new minimal package) — three independent copies of the same extraction/
  requirement logic would be exactly the kind of duplication worth fixing while touching this code,
  not left to drift.

`job_id` stays in log line content, never becomes a Loki label (see Data Flow) — it's
per-invocation and would blow up label cardinality if indexed. Its value is what lets an operator
pivot from one host's log line to the corresponding line on the other end of any cross-host call in
this system, backup or otherwise.

### No stdout capture in `agent` — one log per binary, not two

An earlier direction for this design had `agent` itself capture and forward each subprocess's
stdout/stderr. That's now unnecessary and explicitly dropped: every subprocess already writes its
own structured, job-id-tagged log via `common/logging` (Standardized local logging, above), and
Alloy already ships it. Having `agent` *also* capture and re-log the same subprocess's console
output would duplicate that content under a different, less structured path for no benefit —
`realExec` stays exactly as it is today (`exec.CommandContext(...).Run()`, no `Stdout`/`Stderr`
wired up).

What `agent` *does* still need, and doesn't fully have today: its own log
(`<log_dir>/agent.log`) should record that it started and finished each exec, not just failures.
Today `reconcileState.recordOutcome` (`cmd/agent/reconcile.go`) only logs on failure
(`rs.logger.Error("policy execution failed", ...)`) — a successful run leaves no trace in `agent`'s
own log beyond a silent cache update. This design adds:
- An `Info`-level log line when `agent` dispatches an exec (`policy`/`binary`/`job_id`), before
  calling `execute`.
- An `Info`-level log line when it completes, on both success and failure (`job_id`, exit code —
  available for free from the `*exec.ExitError` a non-zero exit already returns, no stdout capture
  required — and duration), not only the existing failure-only `Error` line.

Both carry the same `job_id` `agent` now passes to the exec (Correlation IDs, above), so `agent`'s
own lifecycle log line for a given run and that run's own subprocess log file (and, for
cert-refresh/policy-fetch/backups, the corresponding server-side log line on the other host) all
share one value an operator can pivot between.

## Data Flow

```
subprocess exec (as today, via agent's realExec)
  -> JSON log line appended to <log_dir>/<binary-name>.log
     (fields already include app, pid, and now job_id on every exec, not just brfs)
  -> for certclient/policyclient/brfs, the same job_id also rides outgoing gRPC metadata to
     issuer/policy-server/bwfs, whose own logs carry the identical value -- one job_id glues
     both hosts' log lines together for any cross-host call, not just backups

Alloy (sidecar, same host) tails <log_dir>
  -> extracts `binary` label from the filename (low-cardinality: a handful of binary names)
  -> batches lines, pushes to log-gateway over mTLS using client.crt/client.key
     (buffered to Alloy's own disk-backed WAL if log-gateway/Loki is unreachable;
      oldest entries drop only once the WAL's configured bound is exceeded)
  -> restarted on a timer inside OperatingCertTTLSec to keep picking up the rotating operating cert

log-gateway
  -> verifies the peer cert is operating-tier (rejects a bootstrap/issuer-caller credential,
     same rule bwfs/catalog already enforce)
  -> derives `hostname` from the verified peer cert's SAN
  -> force-overwrites the `hostname` label on the request (never trusts a client-supplied value)
  -> forwards to Loki's push endpoint

Loki
  -> stores/indexes by label (hostname, binary); job_id/pid/timestamp stay in line content,
     queryable via LogQL, not indexed as labels
  -> retention is an operator-tunable Loki config value, not hardcoded here

operator
  -> queries/tails near-real-time via Grafana (or logcli) against Loki
```

## Security Evaluation

**What this achieves:** near-real-time, fleet-wide log visibility with the same trust properties
every other authenticated data path in this project already has — mTLS transport throughout,
identity derived strictly from the verified peer certificate, and Loki itself never directly
reachable from any agent-managed node. Revoking a node cuts off its logging ability within roughly
one operating-cert refresh/Alloy-restart cycle, reusing the existing credential and revocation
machinery entirely — no new revocation path to build or reason about.

**What this costs, stated plainly:**

- `log-gateway` (and Loki behind it) is a new always-on dependency; its outage doesn't block backup
  operations (per Goals), but it does mean recent logs stop arriving until it's reachable again,
  bounded by Alloy's WAL capacity.
- Log line *content* is not verified against the sending node's actual state or identity — only the
  *label* (`hostname`) is cryptographically bound. A compromised, not-yet-revoked node can write
  misleading log content and have it shipped faithfully. This is a pre-existing level of trust
  already extended to any non-revoked node elsewhere in this system, not a new exposure specific to
  logging.
- No HA for `log-gateway`/Loki (Non-Goals) — a prolonged outage is a fleet-wide loss of *log
  visibility*, not of any operational capability (unlike `issuer`, where an outage costs mesh
  access itself).
- The Alloy sidecar's periodic restart (needed for cert-rotation pickup, see Architecture) means a
  short, deployment-config-driven gap in live shipping around each restart — not a gap in captured
  data, since local files and Alloy's WAL both persist across it.

## Configuration

New/changed `local.conf` keys, following this project's existing `_host`/`_port` and required-field
conventions:
- `log_dir` (renamed from `logfolder`, same required-field status) — where every component writes
  its own `<binary-name>.log`.
- `log_gateway_host` / `log_gateway_port` (default `9400`, following `issuer`'s `9200` and
  `policy-server`'s `9300`) — where `log-gateway` runs; read by the Alloy sidecar's push config and
  set on the host `log-gateway` binds to.

Deployment-level, not `local.conf` keys (operator-tunable at the infrastructure layer, the same way
Loki's own retention already is):
- Alloy's restart interval (recommended: `OperatingCertFetchIntervalSec`).
- Loki's retention period.

## Testing

- Unit: `log-gateway`'s peer-cert tier verification, hostname-label derivation/overwrite, and
  forwarding logic — fabricated peer identity + stubbed forward target, mirroring `issuer`'s
  `server_test.go` pattern.
- Unit: `common/logging`'s new path/rotation wiring — confirms `<log_dir>/<binary-name>.log`
  naming and that a rotation-triggering write produces a bounded set of files.
- Unit: `certclient`/`policyclient`'s new `--job-id` flag parsing, auto-generation when omitted,
  and logger tagging, mirroring `brfs`'s existing `arguments_test.go` coverage.
- Unit: the shared job-id metadata-extraction helper, plus `issuer`/`policy-server` each rejecting
  a request with no `job-id` metadata, mirroring `bwfs`'s existing
  `TestIntegration_MissingJobID_StreamRejected` coverage.
- Unit: `reconcile.go`'s new start/completion log lines (mirrors the existing `TestRun_*` coverage
  in `cmd/agent/reconcile_test.go`) — confirms a dispatched exec logs on start, logs on both
  success and failure completion (not only failure, as today), and that the logged `job_id` matches
  what was passed to `execute`.
- Integration: a real, throwaway Loki instance (own `docker-compose.yml` service, same pattern as
  `issuer`'s existing e2e test against a throwaway `step-ca`) — confirms a push through
  `log-gateway` round-trips with the gateway-enforced `hostname` label, and that a request
  attempting to spoof a different `hostname` label is overwritten, not honored.
- Demo: extend `demo/` to add `loki`, `log-gateway`, and per-node Alloy sidecars, confirming the
  demo's existing `database`/`webserver` nodes' logs are queryable centrally by hostname.

## Documentation Impact

- New `docs/components/log-gateway.md`.
- New `docs/protocols/log-gateway.md` (the proxied push shape and the hostname-label-override
  behavior).
- Update `docs/components/agent.md` for the standardized `<log_dir>/<binary-name>.log` path and the
  uniform `--job-id` convention.
- Update `docs/SECURITY.md` to note `log-gateway` as another operating-tier-only listener, alongside
  `bwfs`/`catalog`.
- Update `docs/ARCHITECTURE.md`'s components table and Control Plane vs. Agents table.
