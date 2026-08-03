# api-server

Unified REST API in front of the control plane's client, catalog, and policy data — for browsers
and admin tools that don't hold a mesh mTLS client certificate. Catalog access is read-only; policies
support create/update/delete; client data supports both read (via `clientmanager-api`) and writes —
enroll/re-enroll, revoke/unrevoke, description/attribute/SAN management (via
`clientmanager-admin-api`, see [Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)).
**Control-plane component.**

`api-server` is the system's first REST surface; every other inter-component call in this project
is gRPC over mTLS, including api-server's own outbound calls to
[`clientmanager-api`](./clientmanager-api.md) and [`catalog`](./catalog.md). It is a thin translation
layer — each REST endpoint maps to exactly one backend gRPC call, no cross-service aggregation,
with one documented exception (see Endpoints, below).

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

`policy-server` also supports a `"storage"` policy type (`port`/`config`).
`GET /policies` accepts an optional `?type=backup|storage` query parameter to filter by type;
without it, every policy of every type is returned, each with `port`/`config` populated
in the response DTO when applicable (zero for a `"backup"`-typed policy, and vice versa for
`rpo`/`storage_policy_id`/`object_filters`). A `"backup"` policy's `destination` in the response DTO
is always derived by `policy-server` from its `storage_policy_id` — it's never itself part of the
create/update input. Creating or updating a storage policy uses a separate pair of
endpoints, `POST /storage-policies` and `PUT /storage-policies/{id}`, since a storage policy's input
shape (`port`/`config`) shares nothing with a backup policy's
(`object_filters`/`rpo`/`backup_window`/`storage_policy_id`) beyond `name`/`client_filters` — which is
also how a storage policy targets a node (there is no separate `hostname` field; set
`client_filters.hostnames` the same way a backup policy would). `GET
/policies/{id}` and `DELETE /policies/{id}` are shared across both types. `GET /policies/{id}` is
fully type-agnostic, looking a policy up by `id` alone. `DELETE /policies/{id}` now has
type-specific behavior for storage policies: `policy-server` rejects the delete with `400` if the
`id` names a storage policy still referenced by any backup policy.

`POST /policies/adhoc` creates a one-time backup policy from the same fields as an ordinary create
(`name`/`client_filters`/`object_filters`/`storage_policy_id`) — `api-server` computes `backup_window`
(every minute), `rpo`, and `disabled_at` itself from the `AdhocPolicyTimeoutSec` config value, so a
caller never composes those three fields by hand to get a "run once on every matched node, then
expire" policy. See [Design: adhoc policy endpoint](../superpowers/specs/2026-08-02-adhoc-policy-endpoint-design.md)
and [Design: link backup policies to storage policies by id](../superpowers/specs/2026-08-03-backup-policy-storage-link-design.md).

## Authentication

Every request must present `Authorization: Bearer <token>`, checked against the single
config-supplied token; missing or mismatched returns `401`. This is the only auth layer today — no
RBAC, no per-user identity, including for the policy write endpoints (see
[Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md) and
[Design: Policy Management API](../superpowers/specs/2026-07-18-policy-management-api-design.md))
and for the client write endpoints, whose stakes are notably higher — a leaked token can mint
enrollment tokens or revoke arbitrary nodes, not just edit backup policies (see
[Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)).
Any node holding a valid mesh operating credential can still call
`clientmanager-api`/`clientmanager-admin-api`/`catalog`/`policy-server`'s RPCs directly, bypassing
this token — an accepted continuation of this project's existing "any operating-tier cert may call
any RPC it can reach"
convention, not a new gap.

## Configuration Keys

- `api_server_port` — port the REST listener binds to *(default: 8090)*
- `api_server_token` — bearer token required on every REST request
- `clientmanager_api_host` / `clientmanager_api_port` — where to dial `clientmanager-api`
- `clientmanager_admin_api_host` / `clientmanager_admin_api_port` — where to dial `clientmanager-admin-api` *(default port: 9501)*
- `catalog_host` / `catalog_port` — where to dial `catalog`
- `policy_server_host` / `policy_server_port` — where to dial `policy-server` *(default port:
  9300)*
- `log_gateway_host` / `log_gateway_port` — where to dial `log-gateway`'s Loki query-proxy route for `GET /api/v1/jobs*` *(default port: 9400)*
- `AdhocPolicyTimeoutSec` — how long a `POST /policies/adhoc`-created policy stays active (its `rpo`
  and how far past `now` its `disabled_at` is set) before disabling itself *(default: 3600)*. Set it
  comfortably larger than `PolicyFetchIntervalSec` (mesh nodes' policy poll interval, default `900`)
  so every matched node has a chance to poll and receive the adhoc policy before it expires.

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
- [clientmanager-admin-api](./clientmanager-admin-api.md) — the write-capable backend behind this component's client-write endpoints
- [catalog](./catalog.md) — the other backend
- [REST API v1](../api/rest-v1.md)
- [Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)
- [Architecture](../ARCHITECTURE.md)
