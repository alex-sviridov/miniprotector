# log-gateway

An mTLS-terminating HTTP reverse proxy in front of Loki — the enforcement point for
[Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md).
Loki's push API has no concept of mTLS peer identity, and this project never trusts a
caller-asserted identity field (see [Security Model](../SECURITY.md)); `log-gateway` closes that
gap by deriving `hostname` from the verified peer certificate and overwriting whatever value the
caller sent, before forwarding to Loki.

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

`POST /loki/api/v1/push` (see [protocol](../protocols/log-gateway.md)) is `log-gateway`'s only
endpoint. The caller's hostname is always the verified mTLS peer identity, never a request field.
For every stream in the pushed body: the `hostname` label is force-overwritten with the verified
value (creating it if the caller omitted one), every other label passes through unchanged, and the
rewritten body is forwarded to Loki's own push endpoint. A caller presenting no verified peer
certificate, or malformed JSON, is rejected outright — nothing is forwarded. A Loki-side failure
(unreachable, or a non-2xx response) is surfaced back to the caller (`502` if unreachable, Loki's
own status/body proxied through otherwise) rather than swallowed.

`log-gateway`'s listener requires an operating-tier peer certificate — the same
`mtls.ServerTLSConfig`/`ServerTLSConfig`-equivalent tier check `bwfs`/`catalog` already enforce
(via `common/mtls.LoadServerCredentials`) rejects a bootstrap/issuer-caller credential outright.

## Building

```bash
make log-gateway
```

## See Also

- [Fleet Log Aggregation Protocol: log-gateway](../protocols/log-gateway.md)
- [Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md)
- [Security Model](../SECURITY.md)
- [Architecture](../ARCHITECTURE.md)
