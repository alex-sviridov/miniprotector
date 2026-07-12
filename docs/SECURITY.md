# Security Model

This document is the canonical reference for how authentication, authorization, and revocation
work across miniprotector today. It describes the system as it currently is, not a changelog of
how it got here — see `docs/superpowers/specs/` for the historical design rationale if you want
that.

## mTLS everywhere

Every gRPC connection between components in this project — backup traffic (`brfs`↔`bwfs`,
`bwfs`↔`rwfs`), catalog replication (`catalogsync`↔`catalog`), and certificate lifecycle traffic
(`certclient`↔step-ca, `certclient`↔`issuer`) — is mutual TLS. Both sides present a certificate
signed by the same root CA and verify the other's chain; there is no unauthenticated RPC surface
anywhere in the mesh.

One exception to "gRPC": `log-gateway`'s push endpoint is plain HTTP, since it proxies to Loki's
own HTTP push API. The transport is still genuine mTLS (`common/mtls.ServerTLSConfig`, the same
operating-tier verification `LoadServerCredentials` gives every gRPC server, just not wrapped for
gRPC), and the same rule holds: caller identity is always the verified peer certificate
(`mtls.PeerHostnameFromConnState`), never a request field.

Whenever a server-side handler needs to know which node is calling it, that identity is **always**
derived from the verified mTLS peer certificate — never from a field the caller supplies on the
request. `common/mtls.PeerHostname` is the single implementation of this: it reads the first SAN
entry (falling back to `Subject.CommonName`) off the peer certificate gRPC's transport credentials
already verified against the CA's root. `issuer`'s `RequestOperatingCert` and `DescribeSANs` both
work this way, and so does `catalog`'s handler for `catalogsync`'s uploads. A node cannot claim to
be a different hostname than the one embedded in its own certificate; there is no request field to
lie in.

`common/mtls` and `common/connection` support presenting an identity other than the standard
`client.crt`/`client.key` pair (`LoadClientCredentialsWithIdentity`, `ConnectWithIdentity`) —
additive, parameterized on which cert/key filenames to load — which is what makes the two-tier
credential model below possible without touching every existing caller's code path.

## The two-tier credential model

Every enrolled node holds **two** distinct certificates in the same certs directory
(`MP_CONFIG_PATH/certs`), sharing one `ca.crt`:

| | Bootstrap credential | Operating credential |
|---|---|---|
| Files | `bootstrap.crt` / `bootstrap.key` | `client.crt` / `client.key` |
| Lifetime | Long, governed entirely by the CA provisioner's own claims today — `BootstrapCertTTLSec` (~90 days by default) is parsed and defaulted but not yet consumed by any request path (tracked follow-up) | Short (`OperatingCertTTLSec`, 1 hour by default) |
| Obtained via | `certclient bootstrap` (redeems a one-time enrollment token from `client-manager`), refreshed by `certclient renew` (step-ca's stock `/renew`) | `certclient operating-refresh`, authenticated *with* the bootstrap credential, talking to `issuer` |
| Consumed by | Only `certclient operating-refresh`'s connection to `issuer` | Every other component's mTLS transport (`common/mtls`'s hardcoded `client.crt`/`client.key`) — `bwfs`, `brfs`, `rwfs`, `catalogsync`, `catalog`, `log-gateway` |
| Scheduled by | `agent`'s `bootstrap-refresh` policy (`BootstrapCertRefreshIntervalSec`, daily by default) | `agent`'s `operating-refresh` policy (`OperatingCertFetchIntervalSec`, every 15 minutes by default) |

This split exists because of three constraints in step-ca itself (confirmed against the pinned
`smallstep/certificates` v0.30.2 source), not an arbitrary design preference:

1. **Renewal can't carry new content.** step-ca's `/renew` (`authority/tls.go`'s `renewContext`)
   builds the renewed certificate by copying the old certificate's fields and extensions
   byte-for-byte — no template evaluation, no external call. Only the initial `Sign` evaluates a
   template or embeds fresh data. So a certificate obtained via cheap, no-decision `/renew` can
   never pick up a revoke, an attribute change, or a SAN change — only a fresh `Sign` can.
2. **Revocation is keyed by certificate serial number**, universally (RFC 5280), never by
   subject/hostname — because one hostname has many certificate instances over its lifetime.
   Nothing in this project tracks a live certificate's serial (the node generates its own keypair
   and CSR locally), and asking a compromised, uncooperative node to self-report its own serial for
   revocation is useless exactly when it matters most.
3. **`/renew` never re-checks authorization.** It only checks the presented certificate is still
   validity-windowed and not on the serial-revocation list — no fresh decision from anyone. A
   certificate that only ever renews, never re-`Sign`s, could be renewed forever regardless of a
   later revoke.

The resolution: a certificate that actually gates mesh access (the operating credential) must be
obtained via a fresh `Sign` — not `/renew` — on every refresh, so revocation and attribute/SAN
changes are consulted every cycle rather than occasionally. The bootstrap credential exists
precisely so that this frequent, fresh-`Sign` round trip doesn't also require redeeming a new
one-time enrollment token every cycle: it's a long-lived, cheaply-`/renew`-able identity whose only
job is authenticating the node to `issuer` when asking for a fresh operating certificate.

`certclient operating-refresh`'s CSR always requests `DNSNames` of `[hostname] + sans`, where
`sans` comes from `issuer`'s `DescribeSANs` RPC, called immediately beforehand — see
[Issuer Protocol: why `DescribeSANs` exists](protocols/issuer.md#why-describesans-exists) for the
exact-match validation constraint that makes this call necessary rather than optional.

## Revocation and its trust-model costs

`client-manager revoke <hostname>` sets a flag in `client-manager`'s own SQLite database.
`client-manager` itself has no network interface — it never enforces anything directly. Real
enforcement happens in `issuer`, which shares that same database file: on the revoked node's next
`RequestOperatingCert` call, `issuer` refuses outright, and the node's current operating
certificate simply expires without a replacement, typically within `OperatingCertFetchIntervalSec`
of the check (bounded from above by `OperatingCertTTLSec`, since that's the certificate's own
validity window). `attribute`/`san` changes propagate the same way, on the same schedule, since
every operating-refresh is a fresh `Sign` with a fresh CSR.

`attribute` values land in the certificate itself as a real, non-critical X.509 extension (OID
`1.3.6.1.4.1.61183.1.1`, JSON-encoded), not just in the `Sign` request sent to the CA — see
[issuer](components/issuer.md#behavior). Nothing in this codebase yet reads or enforces that
extension; it exists so a future authorization check can, without another round of
certificate-issuance changes.

Stated plainly, this design has real costs, not just benefits:

- **`issuer` becomes a hard dependency for the entire fleet's mesh access.** This is not an
  occasional admin-tool outage — if `issuer` is unreachable for longer than a node's
  `OperatingCertTTLSec`, that node (and, at fleet scale, every node) loses mesh access
  simultaneously. This is inherent to "revoke by refusing the next reissuance" — the same trade-off
  Vault PKI, SPIFFE/SPIRE, and Teleport all accept for the same reason — not a defect specific to
  this project. `OperatingCertTTLSec` is therefore a load-bearing operational choice: long enough
  to tolerate realistic `issuer` maintenance windows, short enough to give the revocation latency
  actually wanted.
- **No HA for `issuer` yet.** It's a single instance backed by SQLite. Its own availability *is*
  the fleet-wide outage risk described above; scaling it out is future work.
- **The bootstrap credential is now cryptographically confined to only reaching `issuer`.** A
  bootstrap certificate carries `extKeyUsage: ["clientAuth"]` only (never `serverAuth`) plus a
  custom Extended Key Usage OID, `1.3.6.1.4.1.61183.1.3` (named `EKUIssuerCaller` in code —
  deliberately not named around "server"/"client", already overloaded elsewhere in this codebase),
  identifying it as a bootstrap/issuer-caller credential. `common/mtls.LoadServerCredentials` —
  used by `bwfs` and `catalog` — rejects any peer certificate carrying that marker;
  `mtls.LoadIssuerServerCredentials` — used only by `issuer`'s own listener — rejects any peer
  certificate that *doesn't* carry it. A leaked bootstrap credential can now only ever authenticate
  to `issuer`, exactly as intended. See
  [Design: Credential Tier Enforcement](superpowers/specs/2026-07-05-credential-tier-enforcement-design.md).

A brief `issuer` outage degrades mesh access temporarily but never destroys a node's identity or
requires re-enrollment to recover: the bootstrap credential keeps renewing independently via
step-ca's stock `/renew`, and normal operating-refresh traffic resumes automatically once `issuer`
is reachable again.

## See Also

- [Issuer Protocol](protocols/issuer.md) — `RequestOperatingCert`/`DescribeSANs` RPC shapes and
  authorization rules
- [issuer](components/issuer.md), [certclient](components/certclient.md), [agent](components/agent.md),
  [client-manager](components/client-manager.md) — the components that implement this model
- [Architecture](ARCHITECTURE.md)
- [Design: Client Manager Phase 2](superpowers/specs/2026-07-04-client-manager-phase2-design.md),
  [Design: Client Manager Phase 2c](superpowers/specs/2026-07-05-client-manager-phase2c-design.md)
  — historical design rationale, superseded as a reference by this document going forward
