# REST API v1

`api-server`'s REST surface: read-only, v1, no RBAC. See [api-server](../components/api-server.md)
for auth and deployment.

## Conventions

- Every response is JSON. Successful list responses are wrapped as `{"data": [...]}`, with
  `"has_more": bool` added when the endpoint paginates.
- Filters are plain query parameters (no `filter[...]` envelope).
- Pagination is cursor-based: `limit` (page size) + `starting_after` (the `id` of the last item on
  the previous page). Style follows Stripe/GitHub-list conventions, not JSON:API.
- Errors are `{"error": "<message>"}` with an appropriate HTTP status code.
- Every request must present `Authorization: Bearer <token>`; the demo lab's placeholder token is
  `dev-placeholder-token-change-me` (see `demo/local.conf` / `deploy/control-plane/api-server/local.conf`).

## `GET /api/v1/clients`

Returns every enrolled client. Not paginated (the enrolled-client list is expected to stay small).

```bash
curl -H "Authorization: Bearer dev-placeholder-token-change-me" http://localhost:8090/api/v1/clients
```

```json
{
  "data": [
    {
      "hostname": "webserver",
      "revoked": false,
      "revoked_at": 0,
      "last_seen_at": 1784318519,
      "sans": null,
      "attributes": {"role": "web"},
      "descriptions": null
    }
  ]
}
```

`sans`, `attributes`, and `descriptions` are `null` (not `{}`/`[]`) when a client has none set —
these fields are never defaulted to empty collections.

## `GET /api/v1/clients/{hostname}`

Returns one client's full record (same shape as one entry above). `404` if `hostname` isn't
enrolled.

## `GET /api/v1/catalog`

Query parameters (all optional):

| Param | Type | Description |
|-------|------|--------------|
| `source_host` | string | Exact match on the backing-up node's hostname |
| `pattern` | string | Substring match against the entry's underlying object ID (which embeds the original file path) |
| `limit` | int, 1–500 | Page size, default 100 |
| `starting_after` | int | Continue from this entry `id` (from a previous page's last entry) |

```json
{
  "data": [
    {
      "id": 42,
      "source_host": "database",
      "job_id": "backup:daily-db-backup:...",
      "object_id": "fs://database:f:/var/lib/dbdata/data.db:1752400000",
      "ctime": 1752400000,
      "source_created_at": 1752400000,
      "received_at": 1752400010,
      "path": "/var/lib/dbdata/data.db",
      "size": 8192,
      "mode": "-rw-r--r--",
      "owner": 999,
      "group": 999,
      "mod_time": 1752400000
    }
  ],
  "has_more": false
}
```

`400` if `limit` isn't an integer in `[1, 500]`, or `starting_after` isn't a non-negative integer.

## See Also

- [Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)
- [Catalog Sync Protocol](../protocols/catalog-sync.md) — the internal gRPC protocol `ListEntries` (this API's `/catalog` backend) is part of
