# Client Manager Phase 2: Enforced Revocation & Live Attributes — Design

> Builds on `docs/superpowers/specs/2026-07-04-client-manager-design.md` (phase 1: enrollment,
> descriptions, attributes, revoked flag — all storage-only, none of it enforced). This spec makes
> revocation and attributes actually take effect, on a materially different architecture than
> phase 1 anticipated — phase 1's `certrequest serve` broker and client-manager's separate-host
> placement are superseded here; see "Relationship to Phase 1" at the end.

## Problem

Phase 1 shipped a client list, descriptions, attributes, and a revoked flag — but none of it does
anything. `revoke` only sets a database flag; nothing blocks a renewal. `attribute` values are
never embedded in a certificate. `last_seen` is always `"unknown"`. Closing these gaps turned out
to hinge on a real constraint in the CA itself, confirmed directly against the pinned
`smallstep/certificates` v0.30.2 source:

- **Renewal cannot carry new content.** `authority/tls.go`'s `renewContext` builds the renewed
  certificate by copying the old certificate's fields and extensions byte-for-byte
  (`RawSubject: oldCert.RawSubject`, `for _, ext := range oldCert.Extensions { ... }`) — there is no
  template evaluation, no webhook call, nothing external consulted. Renewal's own doc comment says
  it plainly: "creates a new Certificate identical to the old certificate, except with a validity
  window that begins 'now'." Only the initial `Sign` (`AuthorizeSign`) evaluates a template or
  calls webhooks.
- **Revocation is keyed by certificate serial number, universally** (RFC 5280, not a step-ca
  quirk) — never by subject/CN, because one hostname can have multiple certificate instances over
  its lifetime. `client-manager` never observes the actual signing exchange (the node generates its
  own keypair and CSR locally and redeems its enrollment token directly against step-ca), so it has
  no trustworthy way to learn a live certificate's serial — and asking the node to self-report is
  useless precisely when revocation matters most (a compromised, uncooperative node).
- **`/renew` never re-checks authorization.** It only verifies the presented certificate is still
  valid and not on the serial-revocation list. A compromised node can renew forever, indefinitely,
  regardless of certificate lifetime, because renewing requires no fresh decision from anyone.

The resolution — confirmed against how SPIFFE/SPIRE, Vault PKI, and Teleport all handle this same
problem — is that real, timely revocation against a possibly-compromised, uncooperative node
requires the trust authority to be consulted on *every* reissuance, not occasionally. That's the
architecture this spec builds.

## Goals

- Revoking a client takes effect within one certificate-refresh cycle (minutes-to-hours,
  operator-tunable), including against a compromised node that doesn't cooperate.
- Attribute changes are automatically reflected in a client's certificate on its next refresh — no
  separate mechanism, no manual re-enroll required.
- SAN aliases can be added/removed after enrollment and take effect the same way, on the client's
  next refresh — no manual re-enroll required.
- `last_seen` becomes real data, not a placeholder.
- `step-ca` stays completely stock: no webhooks, no admin API, no server-side code changes — one
  plain config file addition (a certificate template).
- A brief outage of the new listening service (below) degrades mesh access temporarily; it never
  destroys a node's identity or requires re-enrollment to recover.

## Non-Goals (this pass)

- **No cryptographic isolation of the bootstrap credential from the wider mesh.** In principle the
  long-lived bootstrap certificate (below) should be restricted to only ever reach the listening
  service — today, `common/mtls`'s server-side check trusts any CA-signed certificate, so this is
  an operational expectation, not an enforced boundary. Closing this (a custom extension + one
  shared `common/mtls` check) is a small, well-understood follow-up, deferred here.
- **No high availability for the listening service.** Single instance, backed by SQLite. Its own
  availability is now load-bearing for the whole fleet's mesh access (see Security Evaluation) —
  scaling that out is future work, not solved by this spec.
- **No policy-manager / multi-tenant admin backend.** `client-manager` remains a single-operator
  CLI tool, used directly on the CA host. A future admin backend serving multiple (including
  tenant-scoped) human administrators is an entirely separate design track, deliberately kept out
  of client-manager's own code and trust boundary.
- **No key rotation policy beyond "the operating cert gets a fresh CSR each cycle."** Whether the
  underlying keypair itself rotates every cycle or is reused across cycles is an implementation
  detail for the plan stage, not a security-relevant decision this spec needs to pin down.

## Architecture

### Components

1. **`client-manager`** (CLI binary, no network listener) — the admin tool, run directly by the
   cluster admin on the CA host. Holds the CA's provisioner password directly, replacing
   `certrequest` entirely (see "Relationship to Phase 1"). Commands:
   - `add <hostname> [--san alias]` — mints a one-time enrollment token (redeemed once, by the
     node, into its long-lived **bootstrap credential** — see below). Relayed manually, same
     out-of-band step as today. Records `{hostname, added_at}`.
   - `revoke` / `unrevoke <hostname>` — flips a local flag. No step-ca call, no serial number
     involved. All enforcement lives in the listening service.
   - `description set/unset <hostname> k=v` — free-form human annotation, never touches a
     certificate. Unchanged from phase 1.
   - `attribute set/unset <hostname> k=v` — now genuinely live: read by the listening service on
     the client's very next operating-cert refresh.
   - `san add <hostname> <alias>` / `san remove <hostname> <alias>` — manage a client's SAN list
     after enrollment, stored on the same `ClientRecord` phase 1 already added for `re-enroll`
     reuse. Also genuinely live: since every operating-cert refresh is a fresh `Sign` with a fresh
     CSR, an added/removed SAN takes effect on the client's very next refresh, the same way an
     attribute change does — no re-enroll required. (List semantics, not key/value, hence
     `add`/`remove` rather than `set`/`unset`.)
   - `list` / `show` — local reads; `list`'s `LAST_SEEN` column now reflects real data, updated by
     the listening service on every successful issuance.

2. **The listening service** (new binary, shares `storage/clientmanager` and the token-minting
   package with `client-manager` — same source tree, separate `main`) — small, narrow, always-on.
   Its entire job: accept a request from an already-bootstrapped node (mTLS-authenticated by that
   node's bootstrap credential), read the client's `revoked` flag and current `attribute` values
   from the same local database, and either mint a fresh short-lived **operating certificate**
   (attributes embedded via a custom claim) or refuse. Also stamps `last_seen`. Deliberately minimal
   surface: one request type, mostly a local, fast SQLite read plus one call into step-ca's own
   stock `/sign` endpoint.

3. **`step-ca`** — untouched, stock. Gains one configuration addition: a custom X.509 template
   (`options.x509.templateFile`, a plain file in `ca.json`, no admin API) that reads
   `.Token.<claim>` to embed the attribute data the listening service put in the token. This reuses
   the same token-extensibility already present in the pinned library — `cli-utils/token`'s
   `WithClaim`/`ExtraClaims`, and `authority/provisioner/jwk.go`'s `data.SetToken(v)`, which exposes
   the full parsed token to the template — both confirmed directly against source, not assumed.

### Two-tier node credentials, one certs directory

Every enrolled node holds two distinct certificates, but in **one** certs directory (`ca.crt`
shared by both, since they're issued by the same CA and the root is only ever established once,
during bootstrap — see the "CA root delivery" note below):

- **Operating credential** — `client.crt`/`client.key`, the existing, unchanged filenames
  `common/mtls` already hardcodes and every other component (`bwfs`/`brfs`/`rwfs`/`catalogsync`/
  `catalog`) already expects at the standard certs path. Nothing about those components changes.
  Short-lived (minutes-to-hours, operator-tunable), obtained fresh from the listening service every
  cycle — this is the certificate revocation actually gates, and the one that carries current
  attribute/SAN values.
- **Bootstrap credential** — a second filename pair in the *same* directory (`bootstrap.crt`/
  `bootstrap.key`), used only by `agent` to authenticate to the listening service. Long-lived
  (months). Obtained once via the same `ca.Bootstrap`/`CreateSignRequest`/`Sign` flow `certclient`
  already uses today, then renewed independently and cheaply via step-ca's native `/renew` — no
  dependency on the listening service, so it survives that service being down for extended periods.

This needs one small, additive change to `common/mtls`: a variant of `LoadClientCredentials` that
accepts explicit cert/key filenames instead of assuming `client.crt`/`client.key`, used only by
`agent`'s bootstrap-credential renewal and its calls to the listening service. Every other
component's usage of `common/mtls` is untouched.

If the listening service is unreachable when an operating-cert refresh is due, the node simply
doesn't get a new one — it loses mesh access until the service is reachable again, but its
bootstrap identity is untouched (renewed independently) and recovery is automatic, no
re-enrollment needed.

**CA root delivery, for clarity:** `ca.crt` is downloaded exactly once, during the initial
bootstrap redemption (`ca.Bootstrap(token)` fetches step-ca's stock `/roots` endpoint and pins
trust via the fingerprint claim embedded in the enrollment token) — unchanged from phase 1. The
operating credential, obtained later and repeatedly from the listening service, is signed by the
same CA and never triggers a fresh root download; it reuses the `ca.crt` already on disk.

## Data Flow

**Enrollment (unchanged in shape from phase 1):**
```
operator: client-manager add node-east-01 --san node-east-01.internal
  -> mints one-time enrollment token, records {hostname, added_at}
  -> operator relays token out-of-band to the node
  -> node redeems it (ca.Bootstrap/CreateSignRequest/Sign) -> bootstrap credential
```

**Ongoing operating-cert refresh (new; replaces `agent`'s phase-1 use of `certclient`'s `/renew`
for this purpose):**
```
agent, on its own schedule:
  -> keeps the bootstrap credential renewed: direct step-ca /renew, cheap, no listening-service
     dependency
  -> separately, more frequently: generates a fresh CSR, calls the listening service
     (authenticated via the bootstrap credential)
       listening service: read revoked flag + current attributes for this hostname
         revoked -> refuse; agent's operating cert is not renewed, mesh access lapses
         not revoked -> mint token (attributes as claims), sign CSR against step-ca,
                         return cert; stamp last_seen
  -> agent writes the returned operating certificate to disk
```

**Revoke:**
```
operator: client-manager revoke node-east-01
  -> sets revoked=true locally (client-manager's own DB, no network call)
  -> takes effect on node-east-01's very next operating-cert refresh attempt (minutes-to-hours,
     whatever the operating-cert TTL is configured to) -- the listening service simply refuses,
     regardless of whether the node cooperates
```

## Security Evaluation

**What this achieves:** revocation that's effective within one refresh cycle, against a
compromised node that doesn't cooperate, without any serial-number tracking; attribute freshness
as a side effect of the same mechanism, with no separate subsystem; `step-ca` remains completely
stock; "control plane only receives connections" holds throughout (the listening service never
dials out; `client-manager` has no network surface at all).

**What this costs, stated plainly rather than deferred by omission:**

- The listening service becomes a **hard dependency for the entire fleet's mesh access**, not an
  occasional admin convenience — if it's down longer than the operating-cert TTL, every node loses
  mesh access simultaneously. This is inherent to "revoke by refusing the next reissuance" (the
  same trade-off Vault/SPIRE/Teleport all accept), not a defect specific to this design. The
  operating-cert TTL is therefore a load-bearing operational choice — long enough to tolerate
  realistic maintenance windows, short enough to give the revocation latency actually wanted —
  not a value to default casually.
- No HA story for the listening service yet (see Non-Goals) — its single instance being down *is*
  the fleet-wide outage described above.
- The bootstrap credential is not yet cryptographically prevented from reaching the wider mesh
  (see Non-Goals) — today that's an operational expectation, not an enforced boundary.

## Configuration

New `local.conf` keys, following phase 1's `_host`/`_port` convention:
- `issuer_host` / `issuer_port` (default `9200`) — where the listening service runs; read by
  `agent` (to request operating certs) and set on the CA host (where the listening service binds).
- `OperatingCertTTLSec` (default `3600`, one hour) — requested validity for operating certs,
  bounding both how often `agent` must reach the listening service and the worst-case latency
  before a revoke takes effect.
- `BootstrapCertTTLSec` (default `7776000`, ~90 days) — requested validity for the bootstrap
  credential, renewed independently via plain `/renew`.
- `OperatingCertFetchIntervalSec` (default `900`, 15 minutes) — how often `agent` requests a fresh
  operating cert; kept comfortably shorter than `OperatingCertTTLSec` so a missed attempt or two
  doesn't lapse mesh access.

These are starting defaults tunable per deployment, not hardcoded ceilings — the actual values are
a real operational trade-off (Security Evaluation) between revocation latency and reissuance load,
to be validated against real step-ca provisioner claim limits during planning.

## Testing

- Unit: listening service's revoked/attribute lookup and refusal logic (mirrors phase 1's
  `broker_server_test.go` pattern — fabricated peer identity, stubbed store).
- Unit: token claim embedding (`token.WithClaim`) round-trips through a parsed JWT.
- Integration: real step-ca instance, custom template configured, confirming an issued operating
  certificate actually contains the expected attribute-derived extension.
- Integration: revoke a hostname, confirm the listening service refuses its next request; confirm
  an *un*-revoked hostname's request still succeeds.
- Integration: bootstrap credential renewal succeeds via plain `/renew` even with the listening
  service intentionally stopped, proving the "identity survives, only mesh access lapses" property.

## Documentation Impact

- New `docs/components/` entry for the listening service.
- Update `docs/components/client-manager.md` for the now-enforced `revoke`/`attribute` behavior
  and the removal of `certrequest`.
- Update `docs/ARCHITECTURE.md`'s control-plane section and data-flow diagram for the two-tier
  credential model.
- New `docs/protocols/` entry for the listening service's request/response shape.

## Relationship to Phase 1

This supersedes phase 1's `certrequest serve` broker and client-manager's separate-host placement:
`certrequest` is retired, its role fully absorbed into `client-manager`, which now runs on the CA
host directly rather than a separate host (see the design discussion that led here — client-manager
was originally kept separate specifically to isolate it from the CA, but that reasoning assumed
client-manager would eventually back a multi-tenant, human-facing admin surface; that concern is
now explicitly out of scope, addressed by a *future*, separate admin-backend component instead of
by client-manager itself). Phase 1's schema (`clients`, `client_kv`) and CLI surface
(`add`/`re-enroll`/`list`/`show`/`revoke`/`unrevoke`/`description`/`attribute` `set`/`unset`)
carry forward essentially unchanged; what changes is what enforces `revoked` and delivers
`attribute` values, and where `client-manager` is deployed.
