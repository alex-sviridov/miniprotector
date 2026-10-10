# listener

The single network entry point for a host's control-plane gRPC services. It terminates mutual TLS on
one port (`listener_port`, default **9300**), verifies the client certificate, and transparently proxies
each call over a Unix socket to the local service that serves it. **Control-plane component** — one
runs on every host that runs a fronted service; hosts may run any mix of services, or all of them.

Fronted services: [catalog](./catalog.md), [policy-server](./policy-server.md),
[clientmanager-api](./clientmanager-api.md), [clientmanager-admin-api](./clientmanager-admin-api.md),
and [api-server](./api-server.md)'s job-status and storage-status gRPC services. Not fronted:
[bwfs](./bwfs.md) (backup data plane), [issuer](./issuer.md), `step-ca`, [log-gateway](./log-gateway.md)
and `api-server`'s REST port — each keeps its own port.

Design: [listener design](../superpowers/specs/2026-10-10-listener-design.md).

## Usage

```
listener [--port N] [--debug]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `listener_port` config value (default 9300) | Port to listen on |
| `--debug` | false | Enable debug logging (successful proxied calls are logged at debug level) |

The `listener` needs this node's operating certificate (`ca.crt`, `client.crt`, `client.key` in the certs
directory) like any other mesh member, and reloads it as `agent` refreshes it.

## How It Works

1. A client connects to `<host>:9300` and completes the mTLS handshake. The `listener` verifies the client
   certificate against `ca.crt` and rejects bootstrap-tier certificates, exactly as every other mesh
   server does.
2. The called gRPC method (`/pkg.Service/Method`) names the service. The `listener` looks for
   `<run_dir>/services/pkg.Service.sock`.
3. It forwards the call, byte for byte (it never decodes a message, so unary and streaming RPCs of any
   service pass through), adding the caller's verified certificate chain in the `x-mp-peer-cert-bin`
   metadata header — always replacing any value the caller sent.
4. The backend re-verifies that chain against its own `ca.crt` and then runs its normal per-RPC role
   authorization from the certificate's attributes. Authorization stays in each service.

### What the caller sees

| Situation | Result |
|---|---|
| Missing, invalid or wrong-tier client certificate | TLS handshake fails; the call never reaches the proxy |
| No socket for that service on this host | `UNIMPLEMENTED: service <name> is not served on this host` |
| Socket exists but nothing accepts (service crashed) | `UNAVAILABLE` (normal client retry/backoff applies) |
| The service's own status (e.g. a role denial) | Passed through unchanged (`PERMISSION_DENIED`, …) |

## Configuration Keys

- `listener_port` — port the `listener` serves, and the port every client dials on a service's
  `*_host` *(default: 9300)*
- `run_dir` — the directory whose `services/` subdirectory holds the sockets *(default: `<var_path>/run`)*.
  The `listener` and every service it fronts on one host must resolve to the same directory and run as
  the same OS user.

## Operations

- **Start order.** Services create their socket when they start and remove it on a clean shutdown; the
  `listener` finds sockets per call, so either may start first. In the control-plane containers
  [agent](./agent.md#running-services) starts and supervises the `listener` (and the service) from
  `running_service=` lines in `local.conf`, restarting either on crash.
- **Several services, one Docker host.** Every client dials port 9300, so two fronted services on one Docker
  host need different host addresses. `deploy/control-plane/docker-compose.yml` publishes each on its own
  bind address (`CATALOG_BIND`, `POLICY_SERVER_BIND`, `CLIENTMANAGER_API_BIND`; defaults `127.0.0.2`–`127.0.0.4`).
  Separate hosts need nothing special.
- **Socket path length.** A Unix socket path is limited to about 108 bytes; keep `run_dir` short.
- **Cost.** A small unary call through the `listener` costs roughly twice a direct mTLS call (extra hop plus
  chain verification, which the backend caches per client), and a 256 KiB call about 1.5×; both are
  negligible for control-plane traffic. See `BenchmarkUnary` in `src/common/svcproxy`.

## Security

The `listener` is now the single mTLS terminator for the services behind it, so its operating
certificate and process are high-value. Backends trust a forwarded identity only on their Unix-socket
server (which has no TCP listener), the socket directory is mode `0700` and each socket `0600`, and the
backend re-verifies the forwarded chain against `ca.crt`, so a local process forging the header still
needs a valid certificate. See [Security Model](../SECURITY.md#listener-trust-boundary).

## See Also

- [Design: listener](../superpowers/specs/2026-10-10-listener-design.md)
- [Architecture](../ARCHITECTURE.md)
- [Security Model](../SECURITY.md)
