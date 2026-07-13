# Fleet Log Aggregation — Design

> Builds on the two-tier credential model in `docs/SECURITY.md` and the `issuer`/`agent` machinery
> described in `docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md` — this spec adds
> no new credential type, reusing the existing operating certificate end to end.

> **Implementation note (Phase 3):** this document's server-side `hostname`-label
> derivation/force-overwrite (below, and in the Testing Strategy section) was **not** built as
> written. Real testing against Vector's actual traffic found that Vector's `loki` sink sends
> snappy-compressed protobuf by default, not JSON — hand-decoding Loki's wire format inside
> `log-gateway` just to re-derive a label already reachable through mTLS auth was judged not worth
> the complexity it would add. `log-gateway` instead only authenticates (a valid, non-revoked
> operating certificate is required to push at all) and forwards the body completely unexamined;
> `agent`'s own Vector config sets the `hostname` label directly, read from the node's own
> `bootstrap.crt`. See `docs/SECURITY.md`'s log-gateway paragraph and
> `docs/protocols/log-gateway.md` for what's actually built.

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
- **No reliance on the log shipper's own TLS hot-reload behavior, whatever it is.** Several
  candidates in this space (confirmed for Grafana Alloy specifically, and true of Prometheus's
  identical HTTP-client-config lineage more broadly) don't reliably pick up a rotated client
  certificate without a process restart. Rather than audit and depend on one tool's exact behavior
  here, `agent` restarts the shipper itself on every successful `operating-refresh` regardless (see
  Architecture) — the shipper's own reload characteristics become a non-issue either way.

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
     `mtls.PeerHostname` already reads for every gRPC call. **Not built this way — see the
     Implementation Note at the top of this document.**
   - Force-overwrites (never trusts) the `hostname` label on every proxied push request before
     forwarding it to Loki — a node cannot claim to be a different hostname than the one in its own
     certificate, in logs any more than anywhere else in this project. **Not built this way — see
     the Implementation Note at the top of this document.**
   - Forwards the request to Loki's real push endpoint.

3. **Vector, bundled and supervised directly by `agent`** — chosen over the initially-considered
   Grafana Alloy specifically because Vector's own HTTP API/health server is disabled by default
   (`api.enabled: false`) and, left disabled, opens **no listening socket at all**. That matters
   here because every agent-managed node in this project already holds to one invariant for the
   `agent` process's own footprint: it only ever dials out (to `issuer`, `policy-server`, the CA),
   it never accepts inbound connections itself (unlike `bwfs`/`catalog`/`policy-server`, which are
   deliberately servers). Alloy has no way to turn its HTTP server off — confirmed against Grafana's
   own docs and an open upstream issue asking for exactly that — so it would have broken that
   invariant for the first time. Vector, with its API left at its default, doesn't.

   **Bundling and isolation** (so a pre-existing, unrelated log shipper on the same host, if any,
   is never touched or interfered with):
   - The Vector binary is copied into the same directory as `agent`/`certclient`/`policyclient`/
     `brfs` at image-build time, pinned to a specific released version — not assumed to already be
     on the host. `agent` resolves it via the same colocated-binary-resolution logic `realExec`
     already uses for its other execs (pulled into a shared helper, since this is now the second
     caller of that logic) — but, unlike those, **with no `$PATH` fallback**: if the colocated
     binary is missing, `agent` fails loudly rather than risking a silent, version-mismatched
     substitute from whatever else is installed on the host.
   - Vector's config (which files to tail, where to push, which certs to present) depends on that
     node's `local.conf`, so it can't be a static file baked into the image. `agent` renders it
     from a small template at `serve` startup and writes it to `<var_dir>/vector-config.yaml` —
     `var_dir` (`config.ResolveVarDir`), not the binary's own install directory, matching where
     `agent` already keeps its own generated runtime state (`agent-state.json`).
   - Vector's disk buffer (its equivalent of a WAL, for resilience across a `log-gateway`/Loki
     outage) is pointed at an explicit `<var_dir>/vector-buffer` path via its `buffer.type: disk`
     sink config — never Vector's own default location, so it can't collide with an unrelated
     instance's state even in the (already-avoided, per above) case where one exists.
   - No listening socket at all (above) — there's no port to isolate or firewall in the first
     place, unlike a tool whose admin/health server can only be relocated, not disabled.

   Vector tails the standardized log directory (below), batches new lines, and pushes them to
   `log-gateway` over mTLS using the node's existing operating certificate
   (`client.crt`/`client.key`) — no new credential type. Its disk buffer provides local resilience
   across a `log-gateway`/Loki outage; once its configured bound is exceeded, oldest entries are
   dropped rather than blocking or growing without limit.

   **Lifecycle:** unlike `certclient`/`policyclient`/`brfs` (short commands `agent` execs to
   completion via the existing `Policy`/`runner` model), Vector is long-running — `agent` starts it
   once at `serve` startup and keeps it alive for as long as `agent serve` runs, a genuinely
   different lifecycle from the due-and-complete `Policy` model, so it's handled by its own small
   supervision loop rather than shoehorned into that abstraction. `agent` restarts its supervised
   Vector process immediately after every *successful* `operating-refresh` (the same event
   `reconcileState.recordOutcome` already observes), event-driven rather than clock-driven, so a
   fresh cert is always picked up promptly regardless of whatever Vector's own TLS-reload behavior
   turns out to be (see Non-Goals). Vector resumes from its own on-disk checkpoint across this
   restart, so a restart never loses buffered-but-unsent lines. If Vector exits unexpectedly for
   any other reason (a crash, unrelated to cert rotation), `agent`'s supervision loop restarts it
   with the same jittered backoff (`backoff()`, `cmd/agent/reconcile.go`) already used for failing
   policies, rather than leaving log shipping silently dead until the next successful refresh. On
   `agent serve` shutdown (`SIGTERM`), `agent` terminates its Vector child cleanly, the same
   graceful-shutdown symmetry it already gives in-flight backup execs, rather than leaving it
   orphaned. This also means a revoked node's log-shipping ability lapses once its current
   operating cert expires and no further successful refresh (and therefore no further Vector
   restart) occurs — no separate revocation path needed, the same property `operating-refresh`
   already gives every other capability.

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
Nothing is lost — Vector tails rotated files by glob, following both the active and recently-rotated
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
Vector already ships it. Having `agent` *also* capture and re-log the same subprocess's console
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

Vector (bundled, config-generated, and supervised by agent -- no listening socket, api.enabled
left false) tails <log_dir>
  -> extracts `binary` label from the filename (low-cardinality: a handful of binary names)
  -> batches lines, pushes to log-gateway over mTLS using client.crt/client.key
     (buffered to its own disk buffer at <var_dir>/vector-buffer if log-gateway/Loki is
      unreachable; oldest entries drop only once the buffer's configured bound is exceeded)
  -> agent restarts it right after every successful operating-refresh (fresh cert available),
     and independently on any unexpected exit (crash-restart with the same backoff as a
     failing policy) -- never left running on a cert past its useful reload point

log-gateway
  -> verifies the peer cert is operating-tier (rejects a bootstrap/issuer-caller credential,
     same rule bwfs/catalog already enforce)
  -> forwards the request, unexamined, to Loki's push endpoint
  (the hostname-derivation/force-overwrite steps originally planned here were not built --
  see the Implementation Note at the top of this document)

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
one operating-cert refresh/Vector-restart cycle, reusing the existing credential and revocation
machinery entirely — no new revocation path to build or reason about. `agent`'s own network
footprint stays outbound-only throughout, including its supervised Vector process (Architecture).

**What this costs, stated plainly:**

- `log-gateway` (and Loki behind it) is a new always-on dependency; its outage doesn't block backup
  operations (per Goals), but it does mean recent logs stop arriving until it's reachable again,
  bounded by Vector's disk-buffer capacity.
- Log line *content* is not verified against the sending node's actual state or identity — only the
  *label* (`hostname`) is cryptographically bound. A compromised, not-yet-revoked node can write
  misleading log content and have it shipped faithfully. This is a pre-existing level of trust
  already extended to any non-revoked node elsewhere in this system, not a new exposure specific to
  logging.
- No HA for `log-gateway`/Loki (Non-Goals) — a prolonged outage is a fleet-wide loss of *log
  visibility*, not of any operational capability (unlike `issuer`, where an outage costs mesh
  access itself).
- Vector's restart-on-refresh (needed for cert-rotation pickup, see Architecture) means a short gap
  in live shipping around each restart — not a gap in captured data, since local files and Vector's
  disk buffer both persist across it.
- `agent` now supervises a second, long-running child process in addition to its own reconcile
  loop — a real increase in what `agent` itself is responsible for getting right (start-up
  ordering, crash-restart backoff, clean shutdown), not just a deployment-config addition.

## Configuration

New/changed `local.conf` keys, following this project's existing `_host`/`_port` and required-field
conventions:
- `log_dir` (renamed from `logfolder`, same required-field status) — where every component writes
  its own `<binary-name>.log`.
- `log_gateway_host` / `log_gateway_port` (default `9400`, following `issuer`'s `9200` and
  `policy-server`'s `9300`) — where `log-gateway` runs; read by `agent` when rendering Vector's
  generated push config and set on the host `log-gateway` binds to.

No separate Vector-restart-interval key is needed — restarts are event-driven off
`operating-refresh`'s own existing cadence (`OperatingCertFetchIntervalSec`), not an independently
configured timer (Architecture).

Deployment-level, not a `local.conf` key (operator-tunable at the infrastructure layer, the same
way it already is today): Loki's retention period.

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
- Unit: `agent`'s new Vector-supervision loop — starts Vector once at `serve` startup; restarts it
  after a successful `operating-refresh` outcome and *not* after a failed one; restarts it with
  backoff on an unexpected exit; terminates it cleanly on context cancellation; fails loudly at
  startup if the colocated Vector binary is missing rather than falling back to `$PATH`.
  Fabricated supervised-process stub, mirroring how `reconcile_test.go` already fakes `execute`
  rather than invoking a real binary.
- Unit: `agent`'s Vector-config template rendering — confirms the generated
  `<var_dir>/vector-config.yaml` reflects `local.conf`'s `log_dir`/`log_gateway_host`/
  `log_gateway_port`/certs path, and that Vector's API/listener config is never emitted.
- Integration: a real, throwaway Loki instance (own `docker-compose.yml` service, same pattern as
  `issuer`'s existing e2e test against a throwaway `step-ca`) — confirms a push through
  `log-gateway` round-trips and that an unauthenticated caller (no valid operating certificate) is
  rejected at the TLS layer. (The gateway-enforced-hostname/spoof-rejection property originally
  planned for this test was not built — see the Implementation Note at the top of this document;
  the actual e2e test, `TestE2E_AuthenticatedPushReachesLokiUnderClientDeclaredHostname` plus
  `TestE2E_UnauthenticatedPushRejected`, verifies what's actually built instead.)
- Demo: extend `demo/` to add `loki`, `log-gateway`, and per-node Vector processes (supervised by
  each node's own `agent`), confirming the demo's existing `database`/`webserver` nodes' logs are
  queryable centrally by hostname.

## Documentation Impact

- New `docs/components/log-gateway.md`.
- New `docs/protocols/log-gateway.md` (the proxied push shape and the hostname-label-override
  behavior).
- Update `docs/components/agent.md` for the standardized `<log_dir>/<binary-name>.log` path and the
  uniform `--job-id` convention.
- Update `docs/SECURITY.md` to note `log-gateway` as another operating-tier-only listener, alongside
  `bwfs`/`catalog`.
- Update `docs/ARCHITECTURE.md`'s components table and Control Plane vs. Agents table.
