# Client Manager Phase 2c: Agent/Issuer Wiring — Design

> Builds on `docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md` (phase 2: enforced
> revocation, live attributes — architecture only) and its phase 2b implementation (`issuer`, a
> real, callable, but as-yet-unused RPC service). This is the piece phase 2b's plan explicitly
> deferred: "this plan does not yet wire `agent` to call it (that's phase 2c, a separate follow-up
> plan)." It also completes agent v1
> (`docs/superpowers/specs/2026-07-03-agent-v1-cert-refresh-design.md`), whose single embedded
> `cert-refresh` policy (exec `certclient`, phase-1 single-credential model) this phase replaces.

## Problem

`issuer` (phase 2b) works and is tested end-to-end against a real CA, but nothing calls it.
`agent`'s only embedded policy still execs `certclient` in its original phase-1 shape: one
identity file pair (`client.crt`/`client.key`), auto-bootstrapped or auto-renewed by file
presence, talking to `step-ca` directly. That shape can't express the phase 2 design's two-tier
credential model:

- A long-lived **bootstrap credential**, renewed independently and cheaply via plain step-ca
  `/renew`, used only to authenticate to `issuer`.
- A short-lived **operating credential**, obtained from `issuer`'s `RequestOperatingCert`, the one
  every other component (`bwfs`/`brfs`/`rwfs`/`catalogsync`/`catalog`) already expects at the
  standard `client.crt`/`client.key` path via `common/mtls` — and the one revocation and live
  attributes actually gate.

Without this phase, `issuer`'s revocation enforcement has no caller and is inert in practice: a
node's `client.crt` is still whatever `certclient`'s old renew path last wrote, never refreshed
through `issuer`, never actually subject to a revoke.

## Goals

- `agent` runs two independent-cadence policies instead of one: `bootstrap-refresh` (long
  interval, plain `/renew`) and `operating-refresh` (short interval, via `issuer`).
- `certclient`'s role narrows to exclusively managing the bootstrap credential
  (`bootstrap.crt`/`bootstrap.key`); a new `certclient operating-refresh` subcommand owns talking
  to `issuer` and writing `client.crt`/`client.key`.
- The operating credential's keypair is generated once and reused across refreshes — only the
  certificate itself is re-obtained each cycle.
- `common/mtls`/`common/connection` gain an additive, explicit-filename dial path so the new code
  can authenticate with `bootstrap.crt`/`bootstrap.key` without touching any existing caller's
  behavior or signature.
- `certclient` gains a `--debug` flag and `common/logging`-based diagnostic logging, matching
  `agent`/`issuer`'s existing convention — today it has neither.
- A brief `issuer` outage degrades mesh access temporarily (operating-cert refresh fails, existing
  backoff takes over) without touching the bootstrap credential's independent renewal — the
  "identity survives, only mesh access lapses" property the phase 2 design specified is actually
  realized end-to-end by this phase, not just architected.
- SAN aliases genuinely reach the issued operating certificate (see "SAN Content" under
  Architecture, below) — not just attributes/revocation. This closes a gap the phase 2 design
  itself got wrong (it assumed SANs rode the same `TemplateData` mechanism as attributes; source
  verification during this phase's planning showed that's a separate, mandatory, exact-match CSR
  validator untouched by any template).

## Non-Goals (this pass)

- **No change to `issuer`'s `RequestOperatingCert` handler or minting/signing logic.** Phase 2b
  already built and tested those; this phase only builds their caller. (`issuer` does gain one new,
  small, read-only RPC — `DescribeSANs`, see Architecture — required to make SAN propagation
  actually work; this is additive and doesn't touch phase 2b's existing handler.)
- **No cryptographic isolation of the bootstrap credential from the wider mesh**, no HA for
  `issuer`, no CA-side custom X.509 template — all carried forward unchanged from phase 2's own
  Non-Goals; still out of scope here.
- **No migration tooling for already-bootstrapped nodes.** This is a lab/demo project with no live
  fleet; a node bootstrapped under the old `client.crt`/`client.key`-only shape simply gets
  re-bootstrapped (`certclient bootstrap <token>` again) rather than migrated in place.
- **No policy-server / dynamic policy fetch for `agent`.** Both policies remain compiled-in Go
  literals, per agent v1's own stated scope (proposals #1/#2's territory, unrelated to this phase).

## Architecture

### `common/mtls` and `common/connection`: additive explicit-filename dial path

`common/mtls` gains:
```go
func LoadClientCredentialsWithIdentity(certsDir, certFile, keyFile, host string) (credentials.TransportCredentials, error)
```
identical loopback-detection/chain-verification logic to today's `clientTLSConfig`, parameterized
on which cert/key filenames to load. The existing `LoadClientCredentials(certsDir, host)` becomes a
one-line wrapper calling it with `"client.crt"`/`"client.key"` — every existing caller
(`bwfs`/`brfs`/`rwfs`/`catalogsync`/`catalog`) is unaffected.

`common/connection` gains the parallel:
```go
func ConnectWithIdentity(host string, port, timeout int, certsDir, certFile, keyFile string) (*grpc.ClientConn, error)
```
mirroring `Connect`'s existing dial/keepalive/ready-wait logic (`grpc.NewClient` +
`checkConnection` polling `connectivity.Ready`), calling the new `mtls` function instead of the
hardcoded one. `Connect` becomes a wrapper calling it with the same two default filenames.

This is the smallest change that lets one new caller use different identity filenames while every
one of the five existing callers keeps its exact current signature and behavior.

### `issuer`: one new RPC, `DescribeSANs`

Confirmed directly against the pinned `smallstep/certificates@v0.30.2` source (see "SAN Content"
below for the full finding): a CSR's requested DNS SANs must **exactly match** the one-time
token's authorized SAN set for the JWK/OTT provisioner path `issuer` uses, or `Sign` rejects the
request outright; an empty-SAN CSR is silently accepted but yields a SAN-less certificate. Since
that authorized set is computed from `client-manager`'s database — which `certclient` cannot
read — `certclient` must learn a hostname's current SAN list from `issuer` *before* it builds the
CSR it will sign. `issuer.proto` therefore gains a second RPC, alongside `RequestOperatingCert`:

```proto
service IssuerService {
  rpc RequestOperatingCert(RequestOperatingCertRequest) returns (RequestOperatingCertResponse);
  rpc DescribeSANs(DescribeSANsRequest) returns (DescribeSANsResponse);
}

message DescribeSANsRequest {}

message DescribeSANsResponse {
  // Current SAN aliases for the caller's own hostname (never including the
  // hostname itself, which is always the CSR's Subject.CommonName) --
  // exactly the same client.SANsList() value RequestOperatingCert already
  // reads server-side to build its own signing token.
  repeated string sans = 1;
}
```

Server-side, `issuerServer` gains one small, read-only handler: derive the caller's hostname from
`mtls.PeerHostname(ctx)` (same as `RequestOperatingCert`, never a request field), look it up via
the existing `store.GetClient`, return `client.SANsList()`. No revoked check — this call issues
nothing and reveals nothing the caller isn't already entitled to know about itself; a revoked
host's actual certificate request is still refused by `RequestOperatingCert` regardless. Unknown
hostname → same "not tracked" error `RequestOperatingCert` already returns.

### `certclient`: subcommand split, bootstrap-credential rename, new `operating-refresh`

Today `certclient` has a single `main()` that branches on file presence (bootstrap if no identity
exists, else renew) against `client.crt`/`client.key`. It becomes an explicit-subcommand CLI, all
three subcommands sharing a persistent `--debug` flag wired through
`common/logging.NewLogger(ctx)` (mirroring `agent`/`issuer`'s existing convention — `certclient`
currently has neither a debug flag nor structured logging, only final `fmt.Fprintf` success/error
lines, which remain as user-facing output alongside the new internal diagnostic logging):

- **`certclient bootstrap <token>`** — today's `bootstrap()` logic, unchanged except its target
  filenames become `bootstrap.crt`/`bootstrap.key`/`ca.crt`. Still a one-time, manual, out-of-band
  operator action on a freshly-provisioned node.
- **`certclient renew`** — today's `renew()` logic, unchanged except the same filename rename.
  This is what `agent`'s new `bootstrap-refresh` policy execs.
- **`certclient operating-refresh`** (new) — the phase 2c core:
  1. Load `bootstrap.crt`/`bootstrap.key` via `mtls.LoadClientCredentialsWithIdentity` /
     `connection.ConnectWithIdentity`, dial `issuer` at `IssuerHost`:`IssuerPort`.
  2. Parse the node's own hostname from `bootstrap.crt`'s `Subject.CommonName` (safe and
     coordination-free — hostnames don't change post-enrollment).
  3. Call `DescribeSANs()` to learn the current SAN alias list for that hostname.
  4. Load `client.key` if it already exists; else generate a fresh ECDSA keypair and persist it
     (`0o600`, matching `writeIdentity`'s existing key-file convention) — generated once, reused on
     every subsequent call.
  5. Build a CSR from that key with `Subject.CommonName` = the parsed hostname and `DNSNames` =
     hostname plus the SAN list from step 3, in that order (an exact match against
     `certmint.Mint`'s own `append([]string{hostname}, sans...)` token claim is required — CommonName
     and DNSNames are validated independently, so CommonName alone does not satisfy the DNSNames
     check; confirmed against a real CA, see "SAN Content" below).
  6. Call `RequestOperatingCert(csr)` through a small `issuerClient` interface (satisfied by the
     real `pb.IssuerServiceClient`), mirroring the existing `signer`/`renewer` interfaces
     `bootstrap.go`/`renew.go` already use to isolate the CA client for testing.
  7. On success: write the returned chain to `client.crt` (plain `os.WriteFile`, matching
     `writeRenewedCert`'s existing convention — no atomic temp-file/rename dance is used anywhere
     else in this binary, so none is introduced here either).
  8. On failure at any step (unreachable, revoked, SAN list changed between steps 3 and 6 causing a
     mismatch, malformed response, etc.): non-zero exit, `client.crt` left untouched; no
     special-casing between failure kinds — `agent`'s existing backoff handles all of them
     identically, per the phase 2 design's "refuse outright, no partial success" model. A
     SAN-changed-mid-request failure simply retries next cycle with fresh, consistent data — same
     accepted "harmless if wasteful" direction used elsewhere in this project's design (e.g. agent
     v1's cache-write-failure handling).

### `agent`: two policies instead of one

```go
var policies = []Policy{
    {ID: "bootstrap-refresh", Binary: "certclient", Args: []string{"renew"},
     Interval: bootstrapCertRefreshInterval},
    {ID: "operating-refresh", Binary: "certclient", Args: []string{"operating-refresh"},
     Interval: operatingCertFetchInterval},
}
```
Both intervals are read from config (below) rather than compiled-in constants, unlike agent v1's
single policy. The existing generic `Policy`/`PolicyState`/`isDue`/`backoff`/cache machinery is
otherwise untouched — this is exactly the "adding a second policy is a small diff" case agent v1's
own design predicted, and needs no changes of its own.

## Data Flow

**Node bring-up** (unchanged in shape, changed target filenames):
```
operator: client-manager add node-east-01 --san node-east-01.internal
  -> mints one-time enrollment token, relayed out-of-band
operator, on the node: certclient bootstrap <token>
  -> ca.Bootstrap/CreateSignRequest/Sign -> writes ca.crt, bootstrap.crt, bootstrap.key
operator starts: agent serve
```

**Ongoing reconcile** (`agent serve`, both policies on independent cadences):
```
bootstrap-refresh (daily, BootstrapCertRefreshIntervalSec):
  exec certclient renew
    -> loads bootstrap.crt/bootstrap.key, calls step-ca /renew directly, overwrites both
    -> no dependency on issuer being reachable

operating-refresh (every 15 min, OperatingCertFetchIntervalSec):
  exec certclient operating-refresh
    -> loads bootstrap.crt/bootstrap.key (mtls.LoadClientCredentialsWithIdentity)
    -> dial issuer at IssuerHost:IssuerPort (connection.ConnectWithIdentity)
    -> parse own hostname from bootstrap.crt's CommonName
    -> call DescribeSANs() -> current SAN alias list
    -> load client.key if present, else generate + persist it
    -> build CSR: CommonName=hostname, DNSNames=[hostname]+SANs just fetched (exact match required)
    -> call RequestOperatingCert(csr)
    -> success: write returned chain to client.crt
    -> failure: non-zero exit, client.crt untouched; agent's existing backoff takes over
```

**Revoke** (unchanged from the phase 2 design, now actually reachable end-to-end):
```
operator: client-manager revoke node-east-01
  -> sets revoked=true locally, no network call
  -> issuer refuses node-east-01's next operating-refresh call
  -> bootstrap-refresh is untouched and keeps succeeding independently
  -> node's bootstrap identity survives; only mesh access lapses, until unrevoked
```

## Configuration

New `local.conf` keys, following the existing `_host`/`_port`/`*Sec` convention:

| Key | Default | Used by |
|---|---|---|
| `BootstrapCertRefreshIntervalSec` | `86400` (1 day) | `agent`'s `bootstrap-refresh` policy interval |
| `BootstrapCertTTLSec` | `7776000` (~90 days) | `certclient bootstrap`/`renew`'s requested validity |
| `OperatingCertFetchIntervalSec` | `900` (15 min) | `agent`'s `operating-refresh` policy interval |

`IssuerHost`/`IssuerPort`/`OperatingCertTTLSec` already exist from phase 2b — `operating-refresh`
is simply their first consumer. Daily bootstrap-cred renewal against a ~90-day TTL leaves large
slack for missed attempts or extended outages; step-ca's `/renew` has no "too early" restriction,
so attempting it daily is always safe.

## SAN Content — Resolved Against Source

Confirmed directly against the pinned `smallstep/certificates@v0.30.2` source (the same
"confirm against source, don't assume" standard phase 2b applied to `TemplateData`):

- **Enforcement point:** `authority/tls.go` (`signX509`) invokes every `provisioner.SignOption`
  implementing `CertificateRequestValidator` against the presented CSR; a non-nil error aborts the
  sign immediately.
- **The JWK/OTT provisioner path `issuer` uses** (`authority/provisioner/jwk.go`) installs a
  `dnsNamesValidator` (`authority/provisioner/sign_options.go`) seeded with the token's authorized
  SANs.
- **If the CSR's `DNSNames` is empty:** the validator short-circuits and passes unconditionally —
  but the issued certificate's SANs come straight from the CSR itself, so the result is a
  certificate with **no SANs at all**. This is silent, not an error: an empty-SAN CSR always
  "succeeds" and always omits every alias.
- **If the CSR's `DNSNames` is non-empty:** the validator requires an **exact set match**
  (`reflect.DeepEqual`) against the token's authorized SANs — not a subset. Any name in the CSR not
  in the token's claims, or any token SAN the CSR omits, causes `Sign` to reject the request
  outright (`errs.Forbidden`).
- A true subset-only validator (`dnsNamesSubsetValidator`) exists in the same package but is wired
  only into the cloud-attestation provisioners (AWS/Azure/GCP), not the JWK/OTT path `certmint.Mint`
  uses.

**Consequence:** there is no CSR shape `certclient` can construct unilaterally that both (a) is
guaranteed to be accepted and (b) actually carries the hostname's current SAN aliases onto the
certificate — the exact-match requirement means the CSR's `DNSNames` must be known, precisely,
before the CSR is built and signed. Since only `client-manager`'s database (which `certclient`
cannot read) knows the current list, a lookup RPC (`DescribeSANs`, above) is structurally
required — not a preference, a consequence of how the CA's own provisioner validates. This also
means the phase 2 design's original assumption ("SAN changes take effect the same mechanism as
attributes," i.e. via `TemplateData`) was incorrect: this validator runs unconditionally, before
and independent of any custom template.

**One further detail, confirmed against a real CA during implementation (not just source-reading):**
`certmint.Mint(hostname, sans, opts)`'s token authorizes `append([]string{hostname}, sans...)`, not
`sans` alone — the hostname itself must also be one of the CSR's `DNSNames`, even though it's
separately the CSR's `Subject.CommonName`. `CommonName` and `DNSNames` are validated by different,
independent checks (`commonNameSliceValidator` vs. `dnsNamesValidator`), so a CSR with `DNSNames`
containing only the SAN aliases (omitting the hostname) is rejected outright — a real, throwaway-CA
sign attempt built exactly that way failed with `certificate request does not contain the valid DNS
names`. `certclient`'s CSR-building must therefore use `DNSNames = append([]string{hostname},
sans...)`, matching `certmint.Mint`'s own construction.

## Error Handling

- `bootstrap.crt`/`bootstrap.key` missing when `operating-refresh` runs (agent started before the
  operator ran `certclient bootstrap`) → clear wrapped error, non-zero exit, handled by `agent`'s
  existing backoff like any other policy failure.
- `issuer` unreachable → surfaced by `connection.ConnectWithIdentity`'s existing timeout/ready-wait
  logic; non-zero exit.
- `issuer` refuses (revoked or untracked hostname) → non-zero exit, no special-casing versus other
  failure kinds, per the phase 2 design's refusal model.
- Key-generate-or-reuse and CSR-building are pure, network-free logic, isolated the same way
  `writeIdentity`/`writeRenewedCert` already are — independently unit-testable without touching a
  CA or `issuer`.
- A failure after the key was freshly generated but before the chain was written leaves the newly
  generated key in place for the next attempt to reuse (no regeneration, no partial/inconsistent
  state) — an inherent property of doing key generation before the network call, not extra logic.

## Testing

- Unit: `mtls.LoadClientCredentialsWithIdentity`/`connection.ConnectWithIdentity` against the
  existing `testdata/certs` fixtures, parameterized on filename (mirrors
  `TestLoadClientCredentials_Success`).
- Unit: `certclient`'s key generate-or-reuse logic — no existing key generates and persists one;
  an existing key is loaded and reused unchanged.
- Unit: `issuer`'s `DescribeSANs` handler — known hostname returns its exact current SAN list
  (mirrors `server_test.go`'s existing `fakeAuthContext` pattern); unknown hostname errors; no
  revoked-host special-casing (a revoked host's SANs are still readable, only issuance is refused).
- Unit: `operating-refresh`'s CSR-building step — given a fetched SAN list, the built CSR's
  `DNSNames` exactly equals it (a pure, network-free check, same style as `writeIdentity`).
- Unit: `operating-refresh`'s top-level flow against a fake `issuerClient` — a success response
  writes `client.crt`; a revoked/error response (from either `DescribeSANs` or
  `RequestOperatingCert`) leaves `client.crt` untouched and returns an error.
- Unit: `bootstrap`/`renew` tests updated only for the `bootstrap.crt`/`bootstrap.key` rename —
  their logic is otherwise unchanged.
- Integration/e2e (build-tag gated, mirrors `cmd/issuer/e2e_test.go`'s real-`step-ca`-via-
  `docker compose` pattern): a genuine `certclient bootstrap` against a throwaway CA, a genuine
  `issuer serve` instance, then a genuine `certclient operating-refresh` against it — confirming
  the full chain produces a `client.crt` a real mTLS handshake accepts, **and** that a hostname
  enrolled with a SAN alias (`client-manager add --san`) ends up with that alias actually present
  in the issued operating certificate's `DNSNames` — the concrete, end-to-end proof that the
  `DescribeSANs` round trip closes the gap this phase found.
- Integration: revoke a hostname via a real `client-manager` store, confirm the next
  `operating-refresh` fails while `bootstrap-refresh`'s `renew` still succeeds independently —
  proving the "identity survives, only mesh access lapses" property end-to-end, not just
  architecturally.

## Documentation Impact

Per `.claude/CLAUDE.md`'s feature-change and gRPC-protocol rules. Note this phase **does** modify
`issuer.proto` (adding `DescribeSANs`), which the earlier draft of this design incorrectly said it
wouldn't — per the protocol-change rule, `docs/protocols/issuer.md` must be updated accordingly:

- **`docs/protocols/issuer.md`** (exists) — add the `DescribeSANs` RPC: its request/response shape,
  its authorization model (identical to `RequestOperatingCert` — hostname always from verified mTLS
  peer identity), and why it exists (the CSR SAN exact-match constraint documented above).
- **`docs/components/certclient.md`** (exists) — rewrite for the subcommand split
  (`bootstrap`/`renew`/`operating-refresh`), the `bootstrap.crt`/`bootstrap.key` rename, and the
  new `--debug` flag.
- **`docs/components/agent.md`** (exists) — update for the two-policy list and the two new config
  keys.
- **`docs/components/issuer.md`** (exists) — note a real caller now exists (cross-link to
  `certclient.md`'s `operating-refresh`), removing phase 2b's "not yet wired" caveat.
- **`docs/components/client-manager.md`** (exists) — correct the phase 2b-added note that agent
  integration for revoke enforcement is "not yet built" — it now is.
- **`docs/SECURITY.md`** (new) — canonical home for the authentication/security model: the
  two-tier bootstrap/operating credential design, mTLS everywhere with hostname always derived
  from verified peer identity (never a client-supplied field), the revocation trust model and its
  costs (the "hard dependency on `issuer`" trade-off, no HA yet, bootstrap credential not yet
  cryptographically confined to the listening service). Consolidates the "Security Evaluation"
  content currently scattered across the phase 1/2/2b design docs into one living document instead
  of leaving it buried in dated specs.
- **`docs/ARCHITECTURE.md`** — add a cross-reference to `docs/SECURITY.md` near the credentials
  discussion; update the `agent`/`issuer` component-table rows (both currently say agent
  integration is "separate, later work" — no longer true after this phase) and the data-flow
  section for the two-tier credential model actually existing now.
- **`README.md`** — add `docs/SECURITY.md` to the Documentation list; update the `agent`/
  `certclient` one-line component descriptions if they've gone stale.
- **`CHANGELOG.md`** — entry before merge, per the standing rule.

## Relationship to Phase 2 / Phase 2b

This is phase 2's architecture and phase 2b's `issuer` binary, finally load-bearing: before this
phase, `issuer` is real and tested but has no caller, and `agent`'s only policy still runs phase
1's single-credential `certclient` shape. After this phase, revocation and live attributes are not
just architected and unit-tested in isolation — a node's actual `client.crt` is obtained through
`issuer` on a real, running schedule, and a `client-manager revoke` genuinely lapses that node's
mesh access within one `OperatingCertFetchIntervalSec`-to-`OperatingCertTTLSec` window, exactly as
phase 2's design set out to achieve.
