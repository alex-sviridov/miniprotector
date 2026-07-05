# Control-Plane Deployment for the Two-Tier Credential Model — Design

> Follow-up to `docs/superpowers/specs/2026-07-05-client-manager-phase2c-design.md` (phase 2c:
> agent/issuer wiring). Phase 2c's own Go code, tests, and docs are complete, reviewed, and
> correct — but manually exercising `deploy/control-plane/`'s docker-compose demo against that
> code (as part of finishing phase 2c's branch) revealed the demo/deployment tooling itself was
> never updated for the two-tier model and is now broken. This spec is that follow-up, scoped
> strictly to `deploy/control-plane/`.

## Problem

`deploy/control-plane/catalog/entrypoint.sh` calls bare `./certclient` to obtain `catalog`'s own
mTLS identity — this was phase 1's auto-detecting bootstrap-or-renew command. Phase 2c retired it:
`certclient` now requires an explicit subcommand (`bootstrap`/`renew`/`operating-refresh`), and
neither `bootstrap` nor `renew` writes `client.crt`/`client.key` anymore — those filenames are
now exclusively the operating credential, obtained only via `operating-refresh`, which itself
requires a running `issuer` to dial. Concretely, running `make control-plane-up` today produces a
`catalog` container that crash-loops forever:

```
catalog-1  | Arguments error: a subcommand is required: bootstrap, renew, operating-refresh
```

Beyond that one crash, the underlying gaps are structural, not a one-line typo:

- **`issuer` isn't a docker-compose service at all** — nothing in this deployment can satisfy
  `operating-refresh`'s dependency on a reachable `issuer`.
- **`issuer` has no defined way to obtain its own mTLS server identity.** `connection.StartServer`
  (which every gRPC service including `issuer` uses) hardcodes loading `client.crt`/`client.key`
  via `common/mtls.LoadServerCredentials` — but the only mechanism that produces those filenames is
  `operating-refresh`, which works by *dialing* `issuer`. `issuer` cannot bootstrap its own serving
  identity by calling itself.
- **`client-manager`'s demo enrollment command never persists its database.** The documented
  throwaway-container flow (`docker run --rm ... go run ./cmd/clientmanager add ...`) mounts no
  volume for `client-manager`'s SQLite file, so nothing durable is ever recorded — a pre-existing
  gap (not introduced by phase 2c) that now matters concretely, since `issuer` needs to read the
  *same*, persistent database `client-manager` writes to.

## Goals

- `docker compose up -d` (via `make control-plane-up`) brings up a working `step-ca` + `issuer` +
  `catalog` stack with no manual intervention beyond the documented enrollment token step.
- `catalog` obtains and continuously refreshes both its bootstrap and operating credentials without
  manual restarts — a genuine improvement over today's documented "renewal happens on restart"
  behavior.
- `issuer` obtains and continuously refreshes its own server identity with no new binary, no
  `certclient`, and no additional long-lived process on the CA host beyond `issuer` itself —
  preserving this project's existing "keep the CA host minimal" principle (the same reasoning
  that already gives `client-manager` no network interface at all).
- `client-manager`'s enrollment commands persist to a real, durable, shared database `issuer`
  reads from.
- The full lifecycle — enroll, connect, revoke — is demonstrable end-to-end against this compose
  stack, and documented as the deployment's canonical smoke test.
- Clean teardown: `docker compose down` (or `-v` for a full volume wipe) already works generically
  for the whole stack; no new tooling needed, just confirmed and documented.

## Non-Goals

- **No changes to `bwfs`/`brfs`/`rwfs`'s own enrollment.** Unaffected by this deployment gap;
  already correct after phase 2c.
- **No HA / multi-instance `issuer`.** Unchanged from every earlier phase's stated non-goals.
- **No cryptographic isolation of the bootstrap credential from the wider mesh.** Carried forward,
  still deferred.
- **No rewrite of `docs/superpowers/specs/2026-07-03-demo-lab-environment-design.md`.** That's a
  separate, larger, already-stale, never-implemented spec (a `demo/` stack with full `brfs`/`bwfs`/
  `rwfs` fleet nodes) — it references phase 1's retired `certrequest` and the old single-credential
  `certclient`/single-policy `agent` shapes, and needs its own full rewrite to match the two-tier
  model. Explicitly flagged here as a known, separate follow-up; not addressed by this spec.
- **No general-purpose `client-manager` service or admin UI.** The throwaway-container CLI pattern
  stays; it just gets a persistent volume.

## Architecture

Three independent identity paths, matched to what each component actually is — no single
one-size-fits-all mechanism:

### 1. `issuer` — self-mints its own identity

`issuer` already holds direct CA provisioner access (the same `certmint.Mint`/`(*ca.Client).Sign`
machinery `RequestOperatingCert` uses). On startup, it generates a keypair, builds a CSR for
itself, and mints+signs its own certificate directly — no enrollment token, no `certclient`, no
dependency on anything this deployment doesn't already give it. An internal `time.Ticker`
re-mints well before expiry while the process runs, so a long-lived container never silently
outlives its own certificate. Startup failure (CA unreachable, bad provisioner credentials) is
fail-fast (matches `issuer`'s existing error-handling style); a mid-run refresh failure logs and
keeps serving with the still-valid certificate, retrying next tick — a transient CA outage must
never take down an already-healthy `issuer`.

This keeps the CA host's process surface to exactly `step-ca` + `issuer` (plus occasional
`client-manager` CLI use) — no `agent`, no second daemon. This was a deliberate choice over
running `agent` alongside `issuer`: `agent` carries a broader, evolving feature surface (multiple
policy types, a reconcile loop, a cache file) that has no reason to exist on the one host this
project has consistently kept minimal elsewhere (`client-manager` itself has no network interface
at all, for the identical reason).

**issuer's own hostname/SAN.** Since `catalog` dials `issuer` at `issuer_host:issuer_port`
across a Compose network (not loopback), `common/mtls`'s existing hostname-verification rule
applies unchanged: the caller's `ServerName` must exactly match a SAN on `issuer`'s presented
certificate. `issuer`'s self-mint CSR therefore needs to embed the same hostname value operators
configure as `issuer_host` elsewhere (e.g. the Compose service name `issuer` in this deployment) —
a new CLI flag on `issuer serve` (e.g. `--hostname`, mirroring how `client-manager add`'s own
positional hostname argument already names the entity a cert is issued for) is the explicit,
unambiguous way to supply this, rather than trying to infer it from the environment.

### 2. `catalog` — becomes a genuinely ordinary enrolled node

Unlike `issuer`, `catalog` doesn't hold CA provisioner credentials — it must use real, token-based
enrollment like any fleet node. Its container now runs: `certclient bootstrap` (first run) or
`certclient renew` (subsequent restarts) to obtain/refresh `bootstrap.crt`/`bootstrap.key`, then
`agent serve` in the background — using its existing, **unmodified** two policies
(`bootstrap-refresh`, `operating-refresh`) from phase 2c — then `./catalog` in the foreground.
`catalog` needed no special-casing at all: it simply becomes another `agent`-managed node, which is
exactly what `agent` already exists to do. This is also a real behavioral improvement:
`catalog`'s certificate now refreshes continuously rather than only on container restart, and
`catalog` becomes revocable via `client-manager revoke`, same as any other fleet node.

### 3. Fleet nodes (bwfs/brfs/rwfs) — unchanged

Already correct after phase 2c; nothing here affects them.

### Deployment plumbing

- `docker-compose.yml` gains an `issuer` service: its own container, its own certs volume
  (populated by its internal self-mint, not mounted from anywhere else), the CA provisioner
  password file shared with `client-manager`'s existing throwaway-container pattern, and a
  read-write mount of `client-manager`'s now-persistent database volume.
- `client-manager`'s documented enrollment commands gain a real, persistent volume for their
  database (e.g. `./client-manager/data`), mounted read-write into both the throwaway
  `client-manager` container invocations and `issuer`'s container — the same SQLite file, same
  host, exactly the architecture every earlier phase's design already assumed but this deployment
  never actually wired in.
- `catalog`'s `Dockerfile` now also builds/bundles `agent` (currently `catalog`+`certclient`
  only); its `entrypoint.sh` becomes the three-step sequence above; its `local.conf` gains
  `issuer_host`/`issuer_port` and `agent`'s existing interval config keys
  (`BootstrapCertRefreshIntervalSec`, `OperatingCertFetchIntervalSec`).

## Data Flow

**Demo bring-up:**
```
docker compose up -d step-ca
  -> mint CA provisioner password (make control-plane-up already does this)

docker compose up -d issuer
  -> issuer self-mints its own server identity at startup (no token, no client-manager
     entry needed -- issuer isn't a "client", it's the listening service itself)
  -> reads/writes client-manager's shared, now-persistent SQLite volume

operator: client-manager add catalog-01 --san catalog.backup.internal --ca-url https://step-ca:9000
  (via a throwaway container mounting the same persistent client-manager volume issuer reads)
  -> mints a one-time enrollment token, relayed to catalog's own container via MP_CERT_TOKEN

MP_CERT_TOKEN=<token> docker compose up -d catalog
  -> entrypoint: certclient bootstrap (first run) or certclient renew (later restarts)
  -> agent serve & (background: bootstrap-refresh + operating-refresh, unmodified from phase 2c)
  -> exec ./catalog
  -> catalog's client.crt/client.key now stay continuously fresh via agent
```

**Revoke, now actually demonstrable end-to-end in this demo:**
```
operator: client-manager revoke catalog-01
  -> issuer refuses catalog's next operating-refresh
  -> catalog's operating cert lapses within OperatingCertFetchIntervalSec-to-OperatingCertTTLSec
  -> catalog's bootstrap credential and process are untouched; only mesh access lapses
```

**Teardown:**
```
docker compose down          # stop/remove containers and network; named data volumes persist
docker compose down --volumes  # full wipe, including client-manager's and issuer's own data
```
No new tooling needed — this is already generic Compose behavior; documented explicitly here so
it isn't assumed silently.

## Error Handling

- **`issuer`'s initial self-mint fails at startup** (CA unreachable, bad provisioner credentials):
  fail fast, exit non-zero — consistent with `issuer`'s existing startup error style; Docker's
  `restart: unless-stopped` already covers "retry via restart" elsewhere in this deployment
  (`catalog`'s own docs already document this same shape for its missing-token case).
- **`issuer`'s periodic self-refresh tick fails** (CA transiently unreachable, current cert not yet
  expired): log the error, keep serving with the still-valid certificate, retry next tick.
- **`catalog`'s agent-managed refresh fails**: unchanged from phase 2c — `agent`'s existing
  backoff; `catalog`'s cert simply expires without a replacement until `issuer`/network recovers;
  the process itself keeps running, mTLS handshakes fail until the cert refreshes.

## Testing

- Unit: `issuer`'s self-mint-and-persist logic, extracted and testable with a mocked CA client
  (mirrors how `mintAndSign` is already isolated from real network calls).
- Unit: the refresh ticker's due/not-due decision.
- e2e (real `step-ca`, build-tag gated, mirrors this feature's existing e2e pattern): `issuer`
  starts from nothing, self-mints, and successfully serves a real `DescribeSANs`/
  `RequestOperatingCert` call — proving the chain is actually bootable, not just internally
  consistent.
- Manual/documented verification: a full `docker compose up` walkthrough — enroll, connect,
  revoke — added to `deploy/control-plane/README.md` as this deployment's canonical smoke test
  (this is exactly the exercise that surfaced the problem this spec fixes).

## Documentation Impact

- `deploy/control-plane/docker-compose.yml` — new `issuer` service; `catalog`'s volumes/config
  updated.
- `deploy/control-plane/catalog/{Dockerfile,entrypoint.sh,local.conf}` — bundle `agent`, new
  entrypoint sequence, new config keys.
- `deploy/control-plane/README.md` — rewritten enrollment walkthrough; the enroll/connect/revoke
  smoke test.
- `docs/components/issuer.md` — document self-mint + internal refresh behavior.
- `docs/ARCHITECTURE.md` — the "Agent images bundle `certclient` only" row is now inaccurate
  (`catalog`'s image bundles `agent` too); correct it.
- `CHANGELOG.md` entry.
