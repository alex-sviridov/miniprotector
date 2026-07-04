# issuer

Mints short-lived, attribute-bearing **operating certificates** for already-enrolled nodes,
refusing to do so for a revoked hostname — the enforcement half of
[Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md).
Runs on the CA host, sharing `client-manager`'s SQLite database directly (same file, same host —
not synchronized over a network) and reusing `common/certmint` for token minting.

## Usage

```bash
issuer --ca-url https://localhost:9000 --root <path> --provisioner <name> --password-file <path>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--ca-url` | `https://localhost:9000` | CA URL |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |
| `--debug` | false | Enable debug logging |

## Behavior

`RequestOperatingCert` (see [protocol](../protocols/issuer.md)) is `issuer`'s only RPC. The
caller's hostname is always the verified mTLS peer identity, never a request field. For a known,
not-revoked hostname: mints a token via the same mechanism `client-manager` uses, signs the
caller's own submitted CSR against the CA directly (the caller's private key never reaches
`issuer`), embeds the hostname's current `attribute` values via the sign request's `TemplateData`,
and records `last_seen`. For a revoked or untracked hostname: refuses outright, no certificate
issued, `last_seen` untouched. A `last_seen` write failure is logged but never fails an otherwise-
successful request.

**Deployment note:** `issuer` and `client-manager` must be configured with the same `var_path` (or
otherwise resolve to the same `clientmanager.sqlite` file) — they share one database, not two kept
in sync.

**Not yet in this phase:** actually baking `attribute` values into a certificate's extensions
requires a custom X.509 template (`options.x509.templateFile` in the CA's `ca.json`) that reads
`.Insecure.User.<field>` — that template is deployment configuration for a CA operator to author,
not something this binary's code prescribes. This phase proves the data reaches `TemplateData`
correctly (see the e2e test); it does not ship a specific template.

## Configuration Keys

- `issuer_port` — port `issuer` listens on *(default: 9200)*
- `OperatingCertTTLSec` — requested validity in seconds for operating certificates *(default: 3600)*

## Building

```bash
make issuer
```

## See Also

- [client-manager](./client-manager.md) — owns the database `issuer` reads
- [Issuer Protocol](../protocols/issuer.md)
- [Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
- [Architecture](../ARCHITECTURE.md)
