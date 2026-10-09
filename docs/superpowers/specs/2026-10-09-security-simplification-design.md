# Security Subsystem Simplification — Design

Less code means fewer failure modes. This design removes duplicated and test-only plumbing from the
certificate lifecycle code and drops one RPC. It builds on the provisioner split in
[Security Model](../../SECURITY.md#why-two-provisioners). Breaking wire changes are acceptable; there is
no migration.

## Scope

In: `common/mtls`, a new `common/identity`, `cmd/certclient`, `cmd/issuer`, the `IssuerService` proto,
`operating.tpl`.

Out: dropping bootstrap renewal (`certclient renew`, `bootstrap-refresh`, `GetNodeCertStatus`) — a larger
change with a security trade-off, left for a separate decision. The loopback hostname exemption in `mtls`
stays (removing it would require a `localhost` SAN on every certificate).

## A. `common/mtls`

- Public functions keep their names and signatures; no caller changes.
- Delete the private wrapper chains (`serverTLSConfig` → `…ForTier` → `…ForTierWithClock`, and the client
  equivalent) and the injectable clock.
- `cachedIdentity.Get`: stat both files; if both mtimes equal the cached ones, return the cached
  certificate. Otherwise reload the pair. If the reload fails and the cached certificate has not expired,
  log a warning and serve it, leaving the stored mtimes unchanged so the next handshake retries; if it has
  expired, return the error. No TTL, `validUntil` or `nextValidUntil`.
- Export `HostnameFromCert` (first DNS SAN, else CommonName). It is the only hostname rule; `PeerHostname`,
  `PeerHostnameFromConnState` and `certclient` all use it.

## B. `common/identity` (new)

- `LoadOrCreateKey(path)` — loads an ECDSA P-256 key, or generates and persists it (0600), via `atomicfile`.
- `NewCSR(hostname, key)` — `CN=hostname`, `DNSNames=[hostname]`.
- `WriteCertChain(path, pem)` — atomic write.
- `issuer` reuses a persistent `client.key` for its own identity instead of generating a new key on every
  refresh, as `certclient` already does for nodes. Key and certificate are never replaced as a pair, so a
  torn pair cannot occur; `commitClientIdentity` is deleted.
- `certclient` bootstrap and renew write `bootstrap.crt`, `bootstrap.key` and `ca.crt` through `atomicfile`.

## C. Drop `DescribeSANs`

- Delete the RPC and its messages from `issuer.proto`, regenerate the Go code, delete the server handler
  and the client call.
- `RequestOperatingCert`: mint the token for the hostname only; send
  `templateData {sans: [{type: dns, value: hostname}, aliases…], attributes}`. step-ca's exact-match check
  then compares the CSR (`CN=hostname`, `DNS=[hostname]`) to the token's `[hostname]` and passes.
- `operating.tpl` takes `"sans"` from `.Insecure.User.sans`. This is safe only because the operating
  provisioner's tokens are minted by `issuer` alone. `bootstrap.tpl` is unchanged and still ignores caller
  data.
- Feasibility confirmed against a real step-ca v0.30.2 (throwaway probe): token `[node1]`, CSR `[node1]`,
  `templateData` sans `[node1, alias.internal]` yields a certificate with `DNSNames = [node1 alias.internal]`.
- `operating-refresh` becomes a single RPC. Alias changes still reach a node on its next refresh.

## Testing

- Template test: operating template honours `templateData` sans; bootstrap template still ignores forged
  data.
- mtls cache tests use real files and real expired certificates instead of an injected clock: reload on
  mtime change, fallback to last good, error once expired.
- `identity` package unit tests (key reuse, atomic write leaves no partial file).
- Existing issuer and certclient tests updated; one real-CA check repeated for the single-RPC flow.

## Docs

`docs/protocols/issuer.md`, `docs/components/issuer.md`, `docs/components/certclient.md`,
`docs/SECURITY.md`, `docs/ARCHITECTURE.md` if it names the RPC, and a `CHANGELOG.md` entry.
