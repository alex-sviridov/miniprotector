# certclient

Manages a node's mTLS bootstrap credential (`bootstrap.crt`/`bootstrap.key`) and, via
`operating-refresh`, its short-lived operating certificate (`client.crt`/`client.key`) obtained
from `issuer`. `bwfs`/`brfs`/`rwfs` read the operating certificate via `common/mtls` (`ca.crt`,
`client.crt`, `client.key` under `MP_CONFIG_PATH/certs`). **Agent tool** — bundled onto every node
also running `bwfs`/`brfs`/`rwfs`.

## Usage

```bash
certclient bootstrap
MP_CERT_TOKEN=<token> certclient bootstrap
certclient bootstrap --token <token>

certclient renew
certclient operating-refresh

certclient --debug <subcommand>
```

| Flag | Subcommand | Default | Description |
|------|------------|---------|-------------|
| `--debug` | root (applies to all subcommands) | `false` | Enable debug logging |
| `--token` | `bootstrap` | | Enrollment token for first-time bootstrap. Least safe of the three sources (visible via `ps`) — prefer `MP_CERT_TOKEN` or the stdin prompt on shared hosts |

`bootstrap`/`renew` require `ca_host` set in `local.conf` (the CA's `host:port`).
`operating-refresh` requires `issuer_host` (`issuer_port` defaults to `9200`).

## Behavior

- **`bootstrap`**: redeems a one-time enrollment token for a long-lived bootstrap credential,
  writing `ca.crt` and `bootstrap.crt`/`bootstrap.key`. Gets the token from `--token`, then
  `MP_CERT_TOKEN`, then an interactive stdin prompt, in that order. Trust in the CA is established
  from the token's embedded root fingerprint claim (no separately-distributed root cert needed for
  this step). The redemption's sign request carries `TemplateData {"tier": "bootstrap"}`, which the
  CA's custom leaf template turns into a certificate with `extKeyUsage: ["clientAuth"]` only plus
  the custom `EKUIssuerCaller` marker — see
  [Security Model](../SECURITY.md#the-two-tier-credential-model).
- **`renew`**: renews the existing bootstrap credential via the CA's mTLS-authenticated renew
  endpoint — authenticated by the existing `bootstrap.crt`/`bootstrap.key`, no token needed. Reuses
  the existing private key; only `bootstrap.crt` is rewritten. Always renews when invoked; there's
  no expiry check — run it on a schedule (`agent`'s `bootstrap-refresh` policy, or a bare
  cron/systemd timer) if periodic renewal is wanted.
- **`operating-refresh`**: dials `issuer` authenticated with the bootstrap credential
  (`connection.ConnectWithIdentity`, presenting `bootstrap.crt`/`bootstrap.key`), derives this
  node's hostname from the bootstrap certificate's `Subject.CommonName`, calls `DescribeSANs` to
  learn its current SAN aliases, generates an operating keypair the first time (`client.key`,
  reused byte-for-byte on every later run — only the certificate is re-obtained each cycle), builds
  a CSR whose `DNSNames` are `[hostname] + sans` (the CA's authorization check validates
  `CommonName` and `DNSNames` independently, so the hostname must appear in both — see
  [Issuer Protocol](../protocols/issuer.md#why-describesans-exists)), and calls
  `RequestOperatingCert` to get back a certificate chain written to `client.crt`. On any failure,
  `client.crt` is left untouched. Always refreshes when invoked; there's no expiry check — run it
  on a schedule (`agent`'s `operating-refresh` policy, or a bare cron/systemd timer) if periodic
  refresh is wanted.

Note the credential split: `bootstrap`/`renew` only ever read/write `ca.crt` and
`bootstrap.crt`/`bootstrap.key`. `client.crt`/`client.key` — the pair every other component
actually consumes via `common/mtls` — are written exclusively by `operating-refresh`.

## Building

```bash
make build
```

## See Also

- [client-manager](./client-manager.md) — mints the token `bootstrap` redeems
- [issuer](./issuer.md) — issues the operating certificate `operating-refresh` obtains
- [Issuer Protocol](../protocols/issuer.md) — `DescribeSANs`/`RequestOperatingCert` RPC details
- [agent](./agent.md) — runs `bootstrap-refresh`/`operating-refresh` as scheduled policies
- [bwfs](./bwfs.md), [brfs](./brfs.md), [rwfs](./rwfs.md) — the services that consume the identity this writes
- [Security Model](../SECURITY.md)
- [Design: Credential Tier Enforcement](../superpowers/specs/2026-07-05-credential-tier-enforcement-design.md)
- [Architecture](../ARCHITECTURE.md)
