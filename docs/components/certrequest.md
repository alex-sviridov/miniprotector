# certrequest

Mints a one-time enrollment token for a node, so it can bootstrap an mTLS identity via
`certclient`. **Control-plane tool** — run on or near the CA host; never deployed onto an agent
host or bundled into an agent Docker image.

## Usage

```
certrequest <hostname> [--san alias]... [--ca-url url] [--defaults-file path] [--root path] [--provisioner name] [--password-file path]
```

```bash
certrequest node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
```

Prints the token to stdout. Relay it to the target node out-of-band (SSH, etc.) as the
`MP_CERT_TOKEN` environment variable for `certclient`.

| Flag | Default | Description |
|------|---------|-------------|
| `--san` | | Additional SAN alias for the token (repeatable) |
| `--ca-url` | read from `--defaults-file` | CA URL, e.g. `https://localhost:9000` |
| `--defaults-file` | `deploy/control-plane/ca/data/config/defaults.json` | Path to step-ca's `defaults.json`, used to default `--ca-url` when it isn't given explicitly |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate, used to trust the connection to the CA |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |

`hostname` is a required positional argument — the subject name embedded in the minted token. It
is automatically included as a SAN alongside any `--san` values.

## How it works

Minting a token requires the CA to be reachable: `certrequest` fetches the named provisioner's
encrypted key from the CA over HTTPS (`GET /provisioners`, `GET /provisioners/{kid}/encrypted-key`
— stock step-ca endpoints, no new server-side surface), decrypts it locally with the password, and
signs the token locally. The decrypted key never touches disk. Token validity/SAN authorization is
bounded by the provisioner's own claims configured on the CA.

Anyone able to run `certrequest` with network access to the CA and the provisioner password has
full token-minting authority for any hostname — equivalent to CA-admin privilege. This is why
`certrequest` stays a control-plane-only tool.

## `serve` mode

```bash
certrequest serve
```

Runs as a persistent process, still on/near the CA host, holding the provisioner password and
exposing exactly one mTLS-authenticated RPC: `MintEnrollmentToken(hostname, sans)`. This is now
the highest-value target in the system (network-reachable, CA-admin-equivalent privilege), so its
surface stays deliberately minimal — no other RPCs, no revoke-forwarding, no query API.

It trusts exactly one caller: whoever's mTLS-verified hostname matches `client_manager_host` from
its own `local.conf`, checked via the same `mtls.PeerHostname` derivation `catalog` already uses
for `source_node`. Any other caller is rejected outright, regardless of whether its certificate is
otherwise valid — unlike the rest of the mesh, which trusts any CA-signed cert.

[`client-manager`](./client-manager.md) is the intended (and, after initial bootstrap, only)
caller — see its docs for the `add`/`re-enroll` flow this powers. This exists specifically so
`client-manager` never has to hold the CA's provisioner password itself: minting stays confined to
wherever `certrequest serve` runs, same as today's one-shot CLI.

| Flag | Default | Description |
|------|---------|-------------|
| `--debug` | false | Enable debug logging |
| `--ca-url`, `--defaults-file`, `--root`, `--provisioner`, `--password-file` | same as the one-shot CLI | Provisioner credentials used to mint tokens on callers' behalf |

## Configuration Keys (serve mode)

| Key | Default | Description |
|-----|---------|-------------|
| `client_manager_host` | | The sole hostname `certrequest serve` trusts to call `MintEnrollmentToken` |
| `certrequest_port` | 9100 | Port `certrequest serve` listens on |

## Building

```bash
make build
```

## See Also

- [certclient](./certclient.md) — redeems the token this mints
- [client-manager](./client-manager.md) — the intended caller of `serve` mode
- [Enrollment Broker Protocol](../protocols/enrollment-broker.md)
- [control plane setup](../../deploy/control-plane/README.md)
- [Architecture](../ARCHITECTURE.md)
