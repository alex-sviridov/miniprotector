# log-gateway

An mTLS-terminating HTTP reverse proxy in front of Loki, for
[Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md).
Only a caller with a valid, non-revoked operating certificate can push anything through it at all;
`log-gateway` never parses the push body (JSON, or — Vector's own default — snappy-compressed
protobuf) to do so, so a stream's `hostname` label is trusted as whatever the shipper itself set
(see [Security Model](../SECURITY.md)).

Deployed exactly like [catalog](./catalog.md)/[policy-server](./policy-server.md): an ordinary
`agent`-managed enrolled node, not a self-minting one like `issuer`.

## Usage

```bash
log-gateway --loki-url http://localhost:3100
```

| Flag | Default | Description |
|------|---------|-------------|
| `--loki-url` | `http://localhost:3100` | Base URL of the Loki instance to forward pushes to |
| `--debug` | false | Enable debug logging |

## Behavior

`POST /loki/api/v1/push` (see [protocol](../protocols/log-gateway.md)) is `log-gateway`'s primary
endpoint. A caller must present a verified mTLS peer certificate to push at all; given one, the
request body is forwarded to Loki's own push endpoint completely unexamined and byte-for-byte
unmodified (`Content-Type`/`Content-Encoding` headers included, so Vector's default
snappy-compressed protobuf pushes work exactly like a hand-built JSON body would). A caller
presenting no verified peer certificate is rejected outright — nothing is forwarded. A Loki-side
failure (unreachable, or a non-2xx response) is surfaced back to the caller (`502` if unreachable,
Loki's own status/body proxied through otherwise) rather than swallowed.

`log-gateway`'s listener requires an operating-tier peer certificate — the same
`mtls.ServerTLSConfig`/`ServerTLSConfig`-equivalent tier check `bwfs`/`catalog` already enforce
(via `common/mtls.LoadServerCredentials`) rejects a bootstrap/issuer-caller credential outright.

`log-gateway` also proxies Loki's read path: `GET /loki/api/v1/query_range`, gated by the same
operating-tier mTLS check, forwarding query parameters unmodified. See
[log-gateway Protocol](../protocols/log-gateway.md) and
[Design: /jobs REST Endpoint](../superpowers/specs/2026-07-19-jobs-endpoint-design.md).

`log-gateway` also proxies Loki's live path: `GET /loki/api/v1/tail`, a WebSocket upgrade rather
than a plain request, gated by the same operating-tier mTLS check. `log-gateway` dials Loki's own
tail endpoint with the caller's query parameters forwarded unmodified and relays frames
byte-for-byte in both directions until either side disconnects — it never parses a tail frame, same
as every other route here. See [log-gateway Protocol](../protocols/log-gateway.md) and
[Design: Live Job & Log Updates](../superpowers/specs/2026-08-17-live-job-updates-design.md).

## Building

```bash
make log-gateway
```

## See Also

- [Fleet Log Aggregation Protocol: log-gateway](../protocols/log-gateway.md)
- [Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md)
- [Design: Live Job & Log Updates](../superpowers/specs/2026-08-17-live-job-updates-design.md)
- [Security Model](../SECURITY.md)
- [Architecture](../ARCHITECTURE.md)
