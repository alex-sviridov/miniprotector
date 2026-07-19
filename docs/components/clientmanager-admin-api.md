# clientmanager-admin-api

CA-admin-equivalent gRPC writes onto `client-manager`'s enrolled-client data: issue/re-enroll
enrollment tokens, revoke/unrevoke, and manage description/attribute/SAN metadata — reachable
over the network via [`api-server`](./api-server.md), unlike `client-manager`'s CLI (which stays a
single-operator, no-network tool; this is an additional path, not a replacement). **Control-plane
component**, holds the CA provisioner password directly (same requirement as
[`client-manager`](./client-manager.md)), and shares `clientmanager.sqlite` with `client-manager`,
[`issuer`](./issuer.md), and [`clientmanager-api`](./clientmanager-api.md).

Deliberately a separate binary from `clientmanager-api`, packaged in the *same container* (see
Deployment, below): `clientmanager-api` stays completely password-free and read-only; this service's
RPC surface is fixed and small (seven RPCs, no unrelated read/query features), which is what keeps a
process holding CA-admin-equivalent access auditable. See
[Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
for the full reasoning, including what packaging both services in one container trades away
(filesystem-level isolation, in exchange for one shared `agent`/enrollment instead of two).

## Usage

```bash
clientmanager-admin-api --port 9501 --ca-url https://step-ca:9000 --root /data/root_ca.crt \
    --provisioner admin@backup.internal --password-file /data/secrets/password
```

| Flag | Default | Description |
|------|---------|--------------|
| `--port` | `clientmanager_admin_api_port` config value (default: 9501) | Port to listen on |
| `--ca-url` | `https://localhost:9000` | CA URL |
| `--root` | `deploy/control-plane/ca/data/certs/root_ca.crt` | Path to the CA's root certificate |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `deploy/control-plane/ca/data/secrets/password` | Path to the provisioner password file |
| `--debug` | false | Enable debug logging |

## How It Works

No new business logic: every RPC calls the same `storage/clientmanager.Store` methods and
`common/certmint.Mint` function `client-manager`'s CLI already uses. See the
[ClientManagerAdmin protocol](../protocols/clientmanager-admin.md) for the full RPC behavior.

## Configuration Keys

- `clientmanager_admin_api_port` — port to listen on *(default: 9501)*
- `var_path` — must point at the same directory `client-manager`'s SQLite database lives in (shared
  volume with `client-manager`/`issuer`/`clientmanager-api`)

## Certificates

Same mTLS pattern as every other mesh component: identity bootstrapped/renewed via `certclient`
against `MP_CONFIG_PATH/certs` — shared with `clientmanager-api`, since both binaries run in the same
container and use the same `agent`-managed identity (see Deployment).

## Deployment

Ships in the *same container* as `clientmanager-api` (one Dockerfile, one `entrypoint.sh`, one
`agent` process, one mesh enrollment) rather than a separate service — see
[`deploy/control-plane/README.md`](../../deploy/control-plane/README.md) and the design spec's
"Packaging" section for why. Additionally mounts the CA's root certificate and provisioner password
file read-only, the same two mounts [`issuer`](./issuer.md) already has.

## Building

```bash
make clientmanager-admin-api
```

## See Also

- [clientmanager-api](./clientmanager-api.md) — the read-only sibling sharing this container
- [client-manager](./client-manager.md) — the CLI this service's write logic mirrors
- [issuer](./issuer.md) — enforces `revoked`, reads live `attribute`/SAN values; unaffected by this service
- [api-server](./api-server.md) — the only intended caller
- [ClientManagerAdmin Protocol](../protocols/clientmanager-admin.md)
- [REST API v1](../api/rest-v1.md)
- [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)
- [Security Model](../SECURITY.md)
- [Architecture](../ARCHITECTURE.md)
