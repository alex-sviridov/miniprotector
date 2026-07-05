# issuer

Mints short-lived, attribute-bearing **operating certificates** for already-enrolled nodes,
refusing to do so for a revoked hostname — the enforcement half of
[Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md).
Runs on the CA host, sharing `client-manager`'s SQLite database directly (same file, same host —
not synchronized over a network) and reusing `common/certmint` for token minting.

## Usage

```bash
issuer --ca-url https://localhost:9000 --root <path> --provisioner <name> --password-file <path> --hostname <hostname>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--ca-url` | `https://localhost:9000` | CA URL |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |
| `--hostname` | *(required)* | This `issuer` instance's own hostname, embedded as the CommonName/SAN of its self-minted server certificate — must match whatever `issuer_host` other nodes are configured to dial |
| `--debug` | false | Enable debug logging |

## Behavior

`issuer` exposes two RPCs (see [protocol](../protocols/issuer.md)): `RequestOperatingCert` and
`DescribeSANs`. The caller's hostname is always the verified mTLS peer identity, never a request
field.

- **`RequestOperatingCert`**: for a known, not-revoked hostname, mints a token via the same
  mechanism `client-manager` uses, signs the caller's own submitted CSR against the CA directly
  (the caller's private key never reaches `issuer`), embeds the hostname's current `attribute`
  values via the sign request's `TemplateData`, and records `last_seen`. For a revoked or untracked
  hostname: refuses outright, no certificate issued, `last_seen` untouched. A `last_seen` write
  failure is logged but never fails an otherwise-successful request.
- **`DescribeSANs`**: returns the caller's own current SAN alias list, read live from the same
  database. No revoked check — it reveals nothing the caller isn't already entitled to know about
  itself, and mints/signs nothing. `certclient operating-refresh` calls this first and uses the
  result verbatim as its CSR's `DNSNames`, since step-ca's OTT provisioner validates a CSR's
  requested SANs against the signing token's authorized set with an exact match — see
  [protocol: why `DescribeSANs` exists](../protocols/issuer.md#why-describesans-exists).

### Self-identity: minting its own server certificate

Unlike every other node in the system, `issuer` does not obtain its mTLS server identity via
`certclient` or `agent` — there is no enrollment token, no bootstrap step, and no second daemon
running on the CA host to keep it fresh. Instead, `issuer` mints and signs its own server
certificate directly at startup, reusing the exact same CA provisioner access (`--ca-url`,
`--root`, `--provisioner`, `--password-file`) it already holds for `RequestOperatingCert`: it
generates a fresh ECDSA keypair, builds a self-submitted CSR for `--hostname`, signs it against the
CA, and writes `ca.crt`/`client.crt`/`client.key` into its certs directory — the same files
`common/mtls` reads for every other component. This is safe to repeat on every call: nothing else
in the system depends on `issuer`'s keypair staying stable across restarts or refreshes.

This mint happens twice in `issuer`'s lifecycle, with different failure handling for each:

- **At startup**, before the gRPC server starts listening — no certificate exists yet, so a
  failure here (unreachable CA, bad provisioner credentials, etc.) is fatal: `issuer` logs the
  error and exits rather than serving without a valid identity.
- **On an internal ticker** while running, every `IssuerSelfCertRefreshIntervalSec` (default
  `86400`, i.e. daily) — a failure here (e.g. a transient CA outage) is logged and otherwise
  ignored: the existing, still-valid certificate stays in place and `issuer` keeps serving with it,
  and the next scheduled tick tries again.

Both the startup mint and every refresh request the same validity period, `IssuerSelfCertTTLSec`
(default `7776000`, i.e. 90 days) — long enough that a transient refresh failure has ample time to
be retried (daily) before the certificate actually expires.

**Deployment note:** `issuer` and `client-manager` must be configured with the same `var_path` (or
otherwise resolve to the same `clientmanager.sqlite` file) — they share one database, not two kept
in sync.

**Agent integration:** `agent`'s `operating-refresh` policy execs `certclient operating-refresh` on
a schedule, which is the client of both of these RPCs — see
[certclient](./certclient.md#behavior) for the exact call sequence.

**Attribute extension:** `attribute` values are baked into the issued certificate as a real X.509
extension (OID `1.3.6.1.4.1.61183.1.1`, non-critical, JSON-encoded, present only when a client
has at least one attribute set), via a custom step-ca leaf template
(`deploy/control-plane/ca/templates/leaf.tpl`) wired into the CA's provisioner by
`deploy/control-plane/ca/entrypoint.sh` on first boot. See
[Design: Issuer Attribute Template](../superpowers/specs/2026-07-05-issuer-attribute-template-design.md)
for why the OID is a short, arbitrarily-chosen private-use OID rather than a standards-compliant
X.667 arc, and why nothing in this codebase yet reads or enforces the extension it embeds.

## Configuration Keys

- `issuer_port` — port `issuer` listens on *(default: 9200)*
- `OperatingCertTTLSec` — requested validity in seconds for operating certificates *(default: 3600)*
- `IssuerSelfCertTTLSec` — requested validity in seconds for `issuer`'s own self-minted server
  certificate *(default: 7776000, i.e. 90 days)*
- `IssuerSelfCertRefreshIntervalSec` — how often `issuer` re-mints its own server certificate while
  running *(default: 86400, i.e. daily)*

## Building

```bash
make issuer
```

## See Also

- [client-manager](./client-manager.md) — owns the database `issuer` reads
- [certclient](./certclient.md) — `operating-refresh` subcommand is the client of this service
- [agent](./agent.md) — schedules `certclient operating-refresh` via its `operating-refresh` policy
- [Issuer Protocol](../protocols/issuer.md)
- [Security Model](../SECURITY.md)
- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
- [Architecture](../ARCHITECTURE.md)
