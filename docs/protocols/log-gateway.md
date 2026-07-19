# log-gateway Protocol

Already-bootstrapped node's log shipper → `log-gateway`'s `POST /loki/api/v1/push`, mTLS
(`common/mtls`'s operating-tier verification — the same transport check `bwfs`/`catalog` already
enforce, applied to a plain `net/http.Server` instead of gRPC since Loki's own push API is HTTP,
not gRPC).

## Request

Whatever Loki's own push endpoint accepts, forwarded byte-for-byte unmodified — `log-gateway`
never parses the body. In practice this is either Loki's JSON push shape:

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

or Loki's native protobuf push format (`logproto.PushRequest`), snappy-compressed — Vector's own
`loki` sink sends this by default (`Content-Type: application/x-protobuf`,
`Content-Encoding: snappy`), regardless of the sink's `encoding.codec` setting, which only governs
per-line encoding within that format, not the outer wire format. `log-gateway` forwards both
`Content-Type` and `Content-Encoding` headers unchanged, so Loki decodes exactly what the caller
sent.

## Authorization

A caller must present a verified, non-revoked operating-tier mTLS certificate to push at all
(`common/mtls`'s standard peer verification) — that is the entire authorization check. Unlike
every other server in this project, `log-gateway` does not additionally derive an identity field
from that certificate: since it never parses the body, a stream's `hostname` label is whatever the
shipper itself set (see [Security Model](../SECURITY.md)).

## Response

Whatever Loki's own push endpoint returns, proxied through unchanged (`204 No Content` on success,
per Loki's own convention). `502 Bad Gateway` if Loki itself is unreachable. `401 Unauthorized` if
no verified peer certificate was presented. `413 Request Entity Too Large` if the body exceeds
`log-gateway`'s 10MB cap. `405 Method Not Allowed` for anything other than `POST`.

## `GET /loki/api/v1/query_range`

Same mTLS operating-tier gate as the push path. Query parameters are forwarded to Loki's real
`query_range` endpoint unmodified; the response body is forwarded back unmodified, capped at 10MB
(`502 Bad Gateway` if exceeded or if Loki is unreachable). `401 Unauthorized` if no verified peer
certificate was presented. `405 Method Not Allowed` for anything other than `GET`. Added for
`api-server`'s `GET /api/v1/jobs` and `GET /api/v1/jobs/{job_id}/logs` — see
[Design: /jobs REST Endpoint](../superpowers/specs/2026-07-19-jobs-endpoint-design.md).

## See Also

- [log-gateway](../components/log-gateway.md)
- [Design: Fleet Log Aggregation](../superpowers/specs/2026-07-11-fleet-log-aggregation-design.md)
