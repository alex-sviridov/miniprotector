# certclient

Bootstraps or renews this node's mTLS identity from the CA, populating the certs directory that
`bwfs`/`brfs`/`rwfs` read via `common/mtls` (`ca.crt`, `client.crt`, `client.key` under
`MP_CONFIG_PATH/certs`). **Agent tool** — bundled onto every node also running
`bwfs`/`brfs`/`rwfs`.

## Usage

```bash
certclient
MP_CERT_TOKEN=<token> certclient
certclient --token <token>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | | Enrollment token for first-time bootstrap. Least safe of the three sources (visible via `ps`) — prefer `MP_CERT_TOKEN` or the stdin prompt on shared hosts |

Requires `ca_host` set in `local.conf` (the CA's `host:port`).

## Behavior

- **No identity present** (`ca.crt`/`client.crt`/`client.key` missing from the certs dir):
  bootstraps a new one. Gets a token from `--token`, then `MP_CERT_TOKEN`, then an interactive
  stdin prompt, in that order. Trust in the CA is established from the token's embedded root
  fingerprint claim (no separately-distributed root cert needed for this step).
- **Identity already present**: renews it via the CA's mTLS-authenticated renew endpoint —
  authenticated by the existing certificate, no token needed. Reuses the existing private key;
  only `client.crt` is rewritten. Always renews when invoked; there's no expiry check — run it on
  a schedule (cron/systemd timer) if periodic renewal is wanted.

## Building

```bash
make build
```

## See Also

- [client-manager](./client-manager.md) — mints the token this bootstraps from
- [bwfs](./bwfs.md), [brfs](./brfs.md), [rwfs](./rwfs.md) — the services that consume the identity this writes
- [Architecture](../ARCHITECTURE.md)
