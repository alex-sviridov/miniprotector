# api-server

Unified REST API in front of the control plane's client, catalog, and policy data — for browsers
and admin tools that don't hold a mesh mTLS client certificate. Client and catalog access are
read-only; policies additionally support create/update/delete. **Control-plane component.**

`api-server` is the system's first REST surface; every other inter-component call in this project
is gRPC over mTLS, including api-server's own outbound calls to
[`clientmanager-api`](./clientmanager-api.md) and [`catalog`](./catalog.md). It is a thin translation
layer — each REST endpoint maps to exactly one backend gRPC call, no cross-service aggregation.

## Usage

```bash
api-server --port 8090 --token <bearer-token>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `api_server_port` config value (default: 8090) | Port the REST listener binds to |
| `--token` | `api_server_token` config value | Bearer token required on every REST request |
| `--debug` | false | Enable debug logging |

## Endpoints

See [REST API v1](../api/rest-v1.md) for the full endpoint reference. Every endpoint maps to exactly
one backend gRPC call except `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs`, which query
Loki (through `log-gateway`'s read-proxy route) and aggregate the result — the one deliberate
exception to that rule, documented in
[Design: /jobs REST Endpoint](../superpowers/specs/2026-07-19-jobs-endpoint-design.md).

## Authentication

Every request must present `Authorization: Bearer <token>`, checked against the single
config-supplied token; missing or mismatched returns `401`. This is the only auth layer today — no
RBAC, no per-user identity, including for the policy write endpoints (see
[Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md) and
[Design: Policy Management API](../superpowers/specs/2026-07-18-policy-management-api-design.md)).
Any node holding a valid mesh operating credential can still call
`clientmanager-api`/`catalog`/`policy-server`'s RPCs directly, bypassing this token — an accepted
continuation of this project's existing "any operating-tier cert may call any RPC it can reach"
convention, not a new gap.

## Configuration Keys

- `api_server_port` — port the REST listener binds to *(default: 8090)*
- `api_server_token` — bearer token required on every REST request
- `clientmanager_api_host` / `clientmanager_api_port` — where to dial `clientmanager-api`
- `catalog_host` / `catalog_port` — where to dial `catalog`
- `policy_server_host` / `policy_server_port` — where to dial `policy-server` *(default port:
  9300)*
- `log_gateway_host` / `log_gateway_port` — where to dial `log-gateway`'s Loki query-proxy route for `GET /api/v1/jobs*` *(default port: 9400)*

## Certificates

Enrolls like any other mesh node (bootstrap credential → `certclient` → `issuer` operating cert) for
its *outbound* gRPC calls to `clientmanager-api`/`catalog`. The REST listener itself is plain
HTTP, guarded only by the bearer token above — it is not part of the mTLS mesh.

## Deployment

Ships as part of the combined control-plane `docker compose` stack — see
[`deploy/control-plane/README.md`](../../deploy/control-plane/README.md).

## Building

```bash
make api-server
```

## See Also

- [clientmanager-api](./clientmanager-api.md) — one of the two backends this component reads from
- [catalog](./catalog.md) — the other backend
- [REST API v1](../api/rest-v1.md)
- [Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)
- [Architecture](../ARCHITECTURE.md)
