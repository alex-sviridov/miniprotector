# Design: listener — one mTLS port per host for control-plane gRPC

Date: 2026-10-10

## Goal

Every host exposes **one** network port (`listener_port`, default 9300) for control-plane gRPC. A new
`listener` binary terminates mTLS on it, verifies the client certificate, and proxies each call to the
backend service on the same host that serves the called gRPC service. Backends no longer open TCP
ports; they listen on Unix sockets only. Infrastructure placement is unchanged: services may live on
separate hosts or all on one host, and a host's listener answers for exactly the services running there.

## Scope

In v1 (fronted by the listener): `catalog`, `policy-server`, `clientmanager-api`,
`clientmanager-admin-api`, and `api-server`'s `JobStatusService` / `StorageStatusService` (formerly
`:8091`).

Out of scope for v1 (unchanged): `bwfs` (data plane; a proxy hop would cost backup throughput),
`issuer` (accepts bootstrap-tier certificates), `step-ca` (owns its own TLS), `log-gateway`, and
`api-server`'s REST port (HTTP). See [Future work](#future-work).

## Architecture

```
client ──mTLS──► listener :9300 ──Unix socket──► <run_dir>/services/<fully.qualified.Service>.sock
                   1. handshake, verify client cert (operating tier)
                   2. service name from the gRPC method path
                   3. no socket  → UNIMPLEMENTED; socket refuses → UNAVAILABLE
                   4. proxy raw frames; set x-mp-peer-cert-bin = verified cert chain (DER, leaf first)
```

- **Transparent proxy.** `grpc.UnknownServiceHandler` plus a passthrough codec. The listener imports
  no service proto; unary and streaming RPCs (e.g. `catalogsync`'s streams) work unchanged and a new
  service needs no listener change.
- **Discovery by socket directory.** A backend announces itself by creating
  `<run_dir>/services/<ServiceName>.sock`. No route table.
- **Identity.** The listener always overwrites `x-mp-peer-cert-bin` (client-supplied copies are
  dropped). Backends read the identity from it only on their Unix-socket server, which has no TCP
  listener.
- **Supervision.** `listener` is a separate binary. In the control-plane containers it is started by the
  container's `entrypoint.sh` in a restart loop, alongside the service (services there are started by the
  entrypoint, not by `agent`); `agent` does not supervise it. It uses the same hot-reloaded operating-tier
  certificate as every other node.

## Backend-side changes

1. `common/connection`: add `StartServiceServer(ctx, logger, certsDir, runDir, roleRequirements,
   register, opts...)`. It creates one socket (mode 0600) per service the `register` callback registered
   (read from `GetServiceInfo()`), removes a stale socket on start (refusing if the service is live) and its
   own sockets on shutdown, installs the forwarded-identity interceptor, and uses no TLS credentials.
   The six fronted backends switch to it; `bwfs` and `issuer` keep `StartServer`.
2. `common/mtls/peer.go`: `peerCert(ctx)` falls back to `x-mp-peer-cert-bin` when the connection has
   no TLS peer, parses the DER, and re-verifies the chain against `ca.crt` (operating tier) as defence
   in depth against a forged header from a local process. All identity reads (`PeerHostname`,
   `PeerAttributes`, `PeerNotAfter`, `RequireRoles`) go through `peerCert`, so none else changes.
3. Config (hard cutover, no dual-listen): add `listener_port` (default 9300) and `run_dir`; remove
   `catalog_port`, `policy_server_port`, `clientmanager_api_port`, `clientmanager_admin_api_port`,
   `api_server_job_status_port`. Clients dial `<service>_host:<listener_port>`; the `*_host` keys stay.
   `policy-server` leaves 9300 and creates a socket instead.
4. Deployment: the entrypoint of every container running a fronted service starts `listener` in a restart
   loop. Compose files and the demo publish only 9300; on a single Docker host each fronted container's 9300
   is published on its own bind address (`<SERVICE>_BIND` env, defaults 127.0.0.2-127.0.0.5), because clients
   all dial `<host>:9300`.

## Errors

| Situation | Result |
|---|---|
| Missing/invalid/wrong-tier client cert | Handshake fails; call never reaches the proxy |
| No socket for the service on this host | `UNIMPLEMENTED: service <name> is not served on this host` |
| Socket exists, nothing accepts (stale) | `UNAVAILABLE` (normal client retry/backoff) |
| Backend status/trailers | Passed through unchanged (role denials stay `PERMISSION_DENIED` from the backend) |
| Client sends its own `x-mp-peer-cert-bin` | Dropped and overwritten |
| Local process forges the header on a backend socket | Chain re-verification rejects it; socket mode 0600 is the first defence |
| Listener shutdown | Graceful stop, as every other server in `common/connection` (no separate drain bound) |

The listener logs one structured line per call (peer hostname, service, method, outcome), never payloads:
debug level on success, warn on failure.

## Testing

- `common/mtls`: `peerCert` fallback — valid forwarded cert, missing header, malformed DER, cert from a
  different CA.
- `common/connection`: stale-socket cleanup and socket permissions.
- `cmd/listener`: routing by method path, both error codes, header overwrite, unary and
  client/server/bidi streaming passthrough.
- Integration: listener + the real `catalog` server on a temp run dir; `SyncFileVersions` through the
  proxy (store role passes, client role is refused by catalog's own authz).
- e2e/demo: compose files publish only 9300; every fronted service reachable; start order and the
  listener-prerequisite handling work.
- Regression guard: no fronted backend opens a TCP port.
- Benchmark: a unary call via the listener vs a direct mTLS server (`common/svcproxy` `BenchmarkUnary`);
  `mpbench` (expected unchanged — `bwfs` untouched), reported against baseline.

## Documentation obligations

New `docs/components/listener.md`; update the six backends' component docs, `docs/ARCHITECTURE.md`
(table, prose, mermaid), `README.md`, `docs/SECURITY.md` (trust boundary), `CHANGELOG.md`, and
`backlog.md`. No `.proto` changes, so no `docs/protocols/` change.

## Future work

Behind the same port later, each its own spec: `log-gateway` and REST (HTTP, dispatch on ALPN
`http/1.1`), `issuer` (needs the bootstrap-tier certificate check), `step-ca` (owns its TLS; needs an
SNI-based raw passthrough). `bwfs`: peek the ClientHello (`MSG_PEEK`), route on ALPN/SNI, hand the raw
fd to `bwfs` over a Unix socket (`SCM_RIGHTS`) so `bwfs` does its own handshake and the listener leaves
the data path — Linux-only, needs clients to set the routing hint. Per-service rate limiting.
