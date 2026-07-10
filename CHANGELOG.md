# Changelog

All notable changes to this project are documented here, most recent first.

## 2026-07-10 — Backup policy serving (policy-server)

Added `policy-server`, a new control-plane binary that serves backup policies — static,
operator-authored JSON files under `$MP_CONFIG_PATH/policies/` — filtered to whatever a requesting
client's hostname and attribute labels match. It holds no database and calls no other service: a
client's attribute labels are read directly off its own mTLS certificate, since `issuer` already
embeds them there as a custom X.509 extension on every operating certificate it mints. Policies are
cached in memory and hot-reloaded as a single atomic swap whenever an operator touches a
`policies/.changed` sentinel file after editing one or more policy files. A client-side consumer
(`agent` fetching and acting on policies) is deliberately deferred to later, separate work.

## 2026-07-06 — Self-contained demo lab environment, updated for the current architecture

The 2026-07-03 demo lab design predated `issuer`, the two-tier bootstrap/operating credential
split, and `client-manager` (it assumed the now-retired `certrequest`) — it could no longer be run
as written. `demo/` now stands up `ca`, `issuer`, `catalog`, and two backup-capable nodes
(`client`, `store`) with one command (`make demo-up`), fully self-contained: no host ports
published, no host bind-mounts of secrets, and no dependency on `deploy/control-plane`'s own
compose file or volumes. `catalog`'s image is reused directly rather than duplicated; the CA's
leaf template is read straight from `deploy/control-plane/ca/templates/leaf.tpl` at build time so
the two deployments can't silently drift apart. Building this as a genuine, fully-automated cold
start (rather than a hand-run walkthrough) surfaced and fixed several previously-unknown gaps that
never showed up in the unit/e2e suites or manual deployments: `issuer`'s self-mint requesting a
longer certificate duration than step-ca's default provisioner claims allow (`deploy/control-plane/ca/entrypoint.sh`
has the identical gap — no `--x509-max-dur` flag on its provisioner — so a genuinely fresh
control-plane deployment would hit the same 90-day-request-vs-24h1m-default rejection; not fixed by
this change); `issuer` running as root and corrupting shared SQLite file ownership on a cold boot; `ConnectionTimeOutSec` and
`FileLockTimeoutSec` both lacking defaults in `config.ParseConfig`, silently zeroing out
connection/file-lock timeouts when a deployment's `local.conf` doesn't set them explicitly (as
`deploy/control-plane`'s own config files also don't — a latent gap there too, not fixed by this
change). `deploy/control-plane/catalog/Dockerfile` also gains the `sqlite3` CLI, which its image
never actually had despite being the documented way to inspect its database directly.

## 2026-07-05 — Bootstrap credentials can no longer reach bwfs/catalog

`common/mtls` trusted any CA-signed certificate regardless of which of the two credential tiers
issued it — a leaked bootstrap credential (whose only intended use is authenticating to `issuer`)
could authenticate to `bwfs`/`catalog` exactly as well as an operating credential, something
`docs/SECURITY.md` already flagged as a known, unenforced gap. Bootstrap certificates now carry
`extKeyUsage: ["clientAuth"]` only plus a custom Extended Key Usage marker (`EKUIssuerCaller`, OID
`1.3.6.1.4.1.61183.1.3`); `common/mtls.LoadServerCredentials` (used by `bwfs`/`catalog`) rejects any
peer certificate carrying that marker, and a new `mtls.LoadIssuerServerCredentials` (used only by
`issuer`) rejects any peer certificate that doesn't. Certificates issued before this change lack
the marker and won't pass either check — the demo lab (`deploy/control-plane`) needs its CA and
client-manager volumes wiped and the enroll walkthrough re-run after upgrading.

## 2026-07-05 — Attributes now land in the certificate, not just the Sign request

`issuer` has passed `attribute` key/value pairs to the CA via `TemplateData` on every `Sign` call
since the operating-certificate work landed, but step-ca's default template ignored the field
entirely — the data reached the wire and was silently dropped. A new CA-side template
(`deploy/control-plane/ca/templates/leaf.tpl`, wired in by `ca/entrypoint.sh`) now embeds those
attributes as a real, non-critical X.509 extension (OID `1.3.6.1.4.1.61183.1.1`, JSON-encoded,
present only when a client has attributes set), closing the gap phase 2's design explicitly
deferred. Nothing yet reads or enforces this extension — that remains separate, later work — but
the round-trip from `client-manager attribute set` to an actual certificate field now provably
works end to end, per a new Docker-backed e2e assertion in `src/cmd/issuer/e2e_test.go`.

## 2026-07-05 — Fixed the control-plane docker-compose demo; issuer self-mints its own identity (phase 2d)

Phase 2c's `certclient`/`agent` split broke `deploy/control-plane`'s docker-compose demo: `catalog`
crash-looped, since its container invoked bare `certclient` the old, single-shot way, which no
longer matched the new two-tier bootstrap/operating-refresh model. Fixing this properly meant
closing a gap phase 2c left open — `issuer` itself had no mTLS identity of its own, and couldn't
get one the normal way, since obtaining one via `certclient`/`agent` would mean either a second
daemon running on the CA host or `issuer` depending on itself. `issuer` now mints and signs its own
server certificate directly at startup, reusing the CA provisioner access it already holds for
`RequestOperatingCert`, and re-mints it on an internal ticker while running
(`IssuerSelfCertTTLSec`/`IssuerSelfCertRefreshIntervalSec`, defaulting to a 90-day certificate
refreshed daily); a startup failure is fatal, but a refresh failure just logs and keeps the
existing certificate. `catalog`'s image now bundles `agent` (not just `certclient`), so it runs as
an ordinary `agent`-managed enrolled node with continuously-refreshed bootstrap and operating
credentials for as long as its container runs, instead of a one-shot bootstrap redeemed only at
container start. `docker-compose.yml` gained an `issuer` service and a real, persistent, shared
`client-manager` database volume, so the demo's enrollment commands actually persist across runs
instead of writing to a throwaway container filesystem. `deploy/control-plane/README.md` was
rewritten around a real, verified enroll→connect→revoke smoke test, replacing the stale
instructions that had gone unnoticed as broken.

## 2026-07-05 — Agent-driven operating-certificate refresh (phase 2c)

`agent` now obtains and refreshes operating certificates through `issuer` on a schedule, closing
the loop phase 2's design opened: revocation, live attributes, and SAN changes now actually reach
a node automatically, end to end, without an operator re-enrolling it. This required splitting a
node's mTLS identity into a two-tier credential model — a long-lived bootstrap credential
(`bootstrap.crt`/`bootstrap.key`, obtained/renewed via `certclient bootstrap`/`renew`) used only to
authenticate to `issuer`, and the short-lived operating credential (`client.crt`/`client.key`,
everything else's mTLS identity) obtained fresh via the new `certclient operating-refresh`
subcommand. `agent` accordingly runs two independent-cadence, config-driven policies —
`bootstrap-refresh` and `operating-refresh` — instead of its previous single `cert-refresh` policy.
While wiring this, a design gap surfaced and was closed: step-ca's OTT provisioner validates a
CSR's requested SANs against the signing token's authorized set with an exact match, not a subset,
so a new `issuer.DescribeSANs` RPC was added for a node to learn its own current SAN alias list
before building its CSR — without it, SAN propagation silently failed to actually reach issued
certificates. Also added: `docs/SECURITY.md`, a canonical reference for the mTLS/two-tier-credential/
revocation model, consolidating what had been scattered across dated design docs.

## 2026-07-05 — Operating-certificate issuance (issuer)

Added `issuer`, a new CA-host-local binary that mints short-lived "operating certificates" for
already-enrolled nodes and enforces revocation on every issuance, refusing outright for a revoked
or unknown hostname, by sharing `client-manager`'s SQLite database directly rather than querying it
over the network. Attributes are embedded via the sign request's `TemplateData` field rather than
custom JWT claims, since the CA client library's signing key is unexported and inaccessible outside
its own package. `client-manager`'s `list`/`show` now display real `last_seen` data instead of a
placeholder. Agent-side integration (actually calling `issuer` on a schedule) and a CA-side custom
certificate template (to actually bake attributes into certificate extensions) are deliberately
deferred to a later, separate piece of work.

## 2026-07-03 — Node agent v1 (embedded cert-refresh reconciliation)

Added `agent`, a node-level process intended to replace the bare cron entry for `certclient` with a
small reconcile loop: on a configurable interval it checks whether the (currently single,
compiled-in) `cert-refresh` policy is due, execs `certclient` if so, and records the outcome to a
local JSON cache — failures back off with jittered delays instead of retrying every tick. `agent
list-policies` reads that same cache to show each policy's health and estimated next run without
needing a running daemon. Also added `var_path` to `common/config`, a general directory for this
kind of runtime/variable data, defaulting to the running binary's own directory when unset. This
is the first concrete slice of a broader `agent` design that will later add queue-dispatched and
policy-server-fetched work on top of the same reconcile primitives.

## 2026-07-03 — Backup catalog service (catalog)

Added `catalog`, the receiving end of `catalogsync`'s replication pipeline: a standalone gRPC
service that persists replicated `bwfs` file-version batches to its own SQLite database, keyed by
`(source_node, job_id, object_id)` — `source_node` comes from the CA-verified mTLS client
certificate, never the payload, so a single catalog can safely receive from a fleet of `bwfs`
nodes. `catalogsync` gained a real `GrpcSender` (config-gated by `catalog_host`/`catalog_port`),
replacing the `LoggingSender` stand-in whenever a catalog is configured and reachable. `catalog`
ships its own `docker compose` deployment (`catalog/`), using the same `certclient`-bootstrapped
mTLS identity every other node uses. Also fixed a pre-existing gap in `common/mtls`: server and
client identity certificates are now re-read from disk on every new connection instead of once at
startup, so a certificate renewed by a scheduled `certclient` run is picked up without restarting
the long-running process — this benefits `bwfs`/`brfs`/`rwfs` too, not just this new pair.

## 2026-07-02 — Async catalog replication (catalogsync)

Added `catalogsync`, a new standalone component that tails a `bwfs` node's `file_versions` table
and forwards new rows to a future backup catalog, independently of `bwfs`'s own availability.
`catalogsync` opens `bwfs`'s SQLite database strictly read-only and tracks its own replication
progress in a small local cursor file, retrying with backoff whenever the catalog (represented
today by a logging stand-in `Sender`) is unreachable — nothing is marked replicated until a batch
is confirmed sent, so an outage or restart never loses data. This required replacing
`file_versions`' synthetic `UUID` primary key with a real `INTEGER PRIMARY KEY AUTOINCREMENT`
`seq` column (immune to the row-number reuse a bare SQLite `rowid` allows after a failed job's
rows are purged) and its natural `(job_id, object_id)` identity for external consumers.

## 2026-07-02 — Backup job completion verification

`bwfs` no longer treats a job as finished just because its streams closed. Added a `BackupCommit`
RPC: after a backup run's streams close, `brfs` submits a hash of the files it believes it sent,
and `bwfs` independently recomputes the same hash from its own catalog before marking the job
`success` — a mismatch marks it `failure` and purges that job's incomplete catalog entries. A
background watchdog now fails jobs that go silent past a configurable timeout (`JobTimeoutSec`,
default 30s), and `bwfs` reconciles any jobs left `in_progress` by an unclean shutdown on restart.
`backup_jobs` gained an explicit `status` column (`in_progress`/`success`/`failure`) as the source
of truth for job outcome.
