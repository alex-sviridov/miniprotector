# clientmanager-api

Read-only gRPC access to `client-manager`'s enrolled-client data (`clientmanager.sqlite`) — the
same file [`issuer`](./issuer.md) already shares. **Control-plane component**, runs on the CA host
(same requirement as `issuer`: needs filesystem access to the shared SQLite file).

`client-manager` itself stays exactly as it was before this component existed: a network-surface-free
CLI tool an operator runs by hand, holding the CA's provisioner password directly (see
[Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
for why that's a deliberate security property, not an oversight). `clientmanager-api` never writes —
`client-manager` (CLI) and `issuer` remain the only writers to `clientmanager.sqlite`.

## Usage

```bash
clientmanager-api --port 9500
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `clientmanager_api_port` config value (default: 9500) | Port to listen on |
| `--debug` | false | Enable debug logging |

## How It Works

`ListClients` and `GetClient` are the only RPCs. Both read directly from the same
`storage/clientmanager` store `client-manager`'s CLI and `issuer` use — no caching, no independent
state. `GetClient` returns `NotFound` for an unknown hostname.

## Configuration Keys

- `clientmanager_api_port` — port to listen on *(default: 9500)*
- `var_path` — must point at the same directory `client-manager`'s SQLite database lives in (shared
  volume with `client-manager`/`issuer`)

## Certificates

Same mTLS pattern as every other mesh component: identity bootstrapped/renewed via `certclient`
against `MP_CONFIG_PATH/certs`.

## Deployment

Ships as part of the combined control-plane `docker compose` stack, alongside `issuer` — see
[`deploy/control-plane/README.md`](../../deploy/control-plane/README.md).

## Building

```bash
make clientmanager-api
```

## See Also

- [client-manager](./client-manager.md) — the CLI tool sharing this component's database
- [issuer](./issuer.md) — the existing precedent for a daemon sharing client-manager's database
- [api-server](./api-server.md) — the only intended caller of this service today
- [Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)
- [Architecture](../ARCHITECTURE.md)
