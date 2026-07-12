# log-gateway Protocol

Already-bootstrapped node's log shipper → `log-gateway`'s `POST /loki/api/v1/push`, mTLS
(`common/mtls`'s operating-tier verification — the same transport check `bwfs`/`catalog` already
enforce, applied to a plain `net/http.Server` instead of gRPC since Loki's own push API is HTTP,
not gRPC).

## Request

Loki's own push API shape (a strict subset `log-gateway` cares about — everything else passes
through untouched):

```json
{
  "streams": [
    {
      "stream": { "<label>": "<value>", ... },
      "values": [["<unix-nano-timestamp-string>", "<line>"], ...]
    }
  ]
}
```

## Authorization

The caller's hostname is always derived from its verified mTLS peer identity
(`mtls.PeerHostnameFromConnState`) — never a field on the request. `log-gateway` overwrites the
`hostname` label on every stream in the body with that verified value before forwarding — a caller
cannot claim to be a different hostname than the one in its own certificate, in logs any more than
anywhere else in this project (see [Security Model](../SECURITY.md)).

## Response

Whatever Loki's own push endpoint returns, proxied through unchanged (`204 No Content` on success,
per Loki's own convention). `502 Bad Gateway` if Loki itself is unreachable. `401 Unauthorized` if
no verified peer certificate was presented. `400 Bad Request` for a malformed body. `405 Method Not
Allowed` for anything other than `POST`. `500 Internal Server Error` in the (expected-never)
case where re-marshaling the rewritten body itself fails.

## See Also

- [log-gateway](../components/log-gateway.md)
- [Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md)
