# REST API v1

`api-server`'s REST surface: v1, no RBAC. Catalog and job endpoints are read-only; policy and client
endpoints support create/update/delete (client writes — enroll/re-enroll, revoke/unrevoke,
description/attribute/SAN management — proxy to `clientmanager-admin-api`, see
[Design: clientmanager-admin-api](../superpowers/specs/2026-07-19-clientmanager-admin-api-design.md)).
See [api-server](../components/api-server.md) for auth and deployment.

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

## `POST /api/v1/clients`

Enrolls a new client and mints a one-time enrollment token for it. Body:

```json
{"hostname": "node-east-02", "sans": ["node-east-02.internal"]}
```

`201` with `{"hostname": "...", "token": "..."}` on success — the token is returned exactly once;
relay it to the target node out-of-band, the same as `client-manager add` today. `400` if `hostname`
is empty. `409` if `hostname` is already enrolled.

## `POST /api/v1/clients/{hostname}/reenroll`

Mints a fresh enrollment token for an already-tracked hostname. Body (optional):

```json
{"sans": ["override.internal"]}
```

`200` with `{"hostname": "...", "token": "..."}`. `sans`, if given, overrides the stored SAN list for
this token only — it is not persisted; use `PATCH .../sans` for a persistent change. `404` if
`hostname` isn't enrolled.

## `POST /api/v1/clients/{hostname}/revoke`

## `POST /api/v1/clients/{hostname}/unrevoke`

No body. `200` with the client's updated record (same shape as `GET /api/v1/clients/{hostname}`).
`404` if `hostname` isn't enrolled. Enforcement (refusing a revoked node's next operating-certificate
request) happens on the node's next credential refresh, not synchronously with this call.

## `PATCH /api/v1/clients/{hostname}/description`

## `PATCH /api/v1/clients/{hostname}/attributes`

Partial update — set then unset, per key (not a full-replace `PUT` like policies get). Body:

```json
{"set": {"owner": "alice"}, "unset": ["old-key"]}
```

`200` with the client's updated record. `404` if `hostname` isn't enrolled. `attributes` is this
system's "attribute labels" (the same key/value pairs `policy-server`'s `client_filters.labels`
matches against) — JSON field stays `attributes`, matching `GET /api/v1/clients`'s existing response
shape.

## `PATCH /api/v1/clients/{hostname}/sans`

Body:

```json
{"add": ["new.internal"], "remove": ["old.internal"]}
```

`200` with the client's updated record. `404` if `hostname` isn't enrolled. Adding an already-present
alias or removing an absent one is a no-op, not an error.

## `GET /api/v1/clients/{hostname}/cert-status`

Reports a node's most recent **bootstrap**-certificate renewal failure, if any. Proxies
`PolicyService.GetNodeCertStatus` on `policy-server` (see
[Design: bootstrap-cert-renewal](../superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md)).

Scope: only `agent`'s `bootstrap-refresh` task is reported here. `operating-refresh` failures are
an explicit non-goal of this design — `policyclient` reads exactly the `"bootstrap-refresh"` key
out of `agent-state.json` and nothing else. An empty `last_error` therefore says nothing about
operating-certificate renewal health, which this route does not monitor at all.

```json
{
  "hostname": "host-a",
  "last_error": "renew failed",
  "last_attempt_at": 1723800000
}
```

`200` even when `hostname` has never reported a renewal attempt — `last_error` and
`last_attempt_at` are simply omitted (proto3 zero values: empty string, unset timestamp), not a
`404`. This mirrors `GetNodeCertStatus`'s own "absence isn't an error" contract; a `404` is
reserved for genuinely malformed requests, not "nothing to report yet."

## `GET /api/v1/catalog`

Query parameters (all optional):

| Param | Type | Description |
|-------|------|--------------|
| `source_host` | string | Exact match on the real originating (backed-up) host |
| `store_host` | string | Exact match on the `bwfs` node that replicated the entry |
| `pattern` | string | Substring match against the entry's underlying object ID (which embeds the original file path) |
| `received_after` | int, unix seconds | Only entries received at or after this time |
| `received_before` | int, unix seconds | Only entries received at or before this time |
| `source_hosts` | comma-separated strings | OR-matched, additive to `source_host` |
| `job_names` | comma-separated strings | OR-matched against the policy name embedded in the entry's `job_id` |
| `parent_directories` | comma-separated strings | OR-matched against the entry's exact immediate containing directory (not a subtree/prefix match) |
| `limit` | int, 1–500 | Page size, default 100 |
| `starting_after` | int | Continue from this entry `id` (from a previous page's last entry) |

```json
{
  "data": [
    {
      "id": 42,
      "source_host": "database",
      "store_host": "bwfs-east",
      "job_id": "backup:daily-db-backup:...",
      "object_id": "fs://database:f:/var/lib/dbdata/data.db:1752400000",
      "ctime": 1752400000,
      "store_created_at": 1752400000,
      "received_at": 1752400010,
      "path": "/var/lib/dbdata/data.db",
      "size": 8192,
      "mode": "-rw-r--r--",
      "owner": 999,
      "group": 999,
      "mod_time": 1752400000,
      "parent_directory": "/var/lib/dbdata",
      "short_filename": "data.db"
    }
  ],
  "has_more": false
}
```

`400` if `limit` isn't an integer in `[1, 500]`, or `starting_after` isn't a non-negative integer.

## `GET /api/v1/catalog/clients`

Returns the distinct client (source host) facets matching the given filters, each with a count and
last-seen timestamp. Not paginated — a fleet's distinct client count is expected to stay in the
dozens. Query parameters: `received_after`, `received_before`, `pattern`, `job_names`,
`parent_directories` (comma-separated) — note there is no `source_hosts` parameter here, since a
client facet list is never narrowed by its own dimension.

```json
{
  "data": [
    {"name": "database", "count": 42, "last_seen": 1752400010}
  ]
}
```

## `GET /api/v1/catalog/jobs`

Same shape as `/catalog/clients`, grouped by policy name instead of client host. Query parameters:
`received_after`, `received_before`, `pattern`, `source_hosts`, `parent_directories`
(comma-separated) — no `job_names` parameter, for the same own-dimension reason.

```json
{
  "data": [
    {"name": "nightly-db", "count": 7, "last_seen": 1752400010}
  ]
}
```

## `GET /api/v1/catalog/directories`

Same shape as `/catalog/clients`/`/catalog/jobs`, grouped by the entry's exact immediate parent
directory. Query parameters: `received_after`, `received_before`, `pattern`, `source_hosts`,
`job_names` (comma-separated) — no `parent_directories` parameter, for the same own-dimension
reason.

```json
{
  "data": [
    {"name": "/var/lib/dbdata", "count": 12, "last_seen": 1752400010}
  ]
}
```

## `GET /api/v1/catalog/stores`

Same shape as `/catalog/clients`/`/catalog/jobs`, grouped by the store's `store_host` (the `bwfs`
node that replicated the entry). Query parameters: `received_after`, `received_before`, `pattern`,
`source_hosts`, `job_names` (comma-separated) — no other filter dimensions for stores. The `name`
field is the store's `store_host`.

```json
{
  "data": [
    {"name": "bwfs-east", "count": 42, "last_seen": 1752400010}
  ]
}
```

## `GET /api/v1/catalog/directories/children`

Backs the web catalog view's directory browsing — one level of a directory tree at a time, not the
flat facet list `/catalog/directories` returns. Query parameters: `parent_path` (empty/omitted =
the true roots: `/` and each distinct Windows drive/UNC root present in the catalog),
`received_after`, `received_before`, `source_hosts`, `job_names` (comma-separated) — no `pattern`
parameter: directory browsing and the free-text path search are mutually exclusive UI modes.

Every directory that has ever been an ancestor of a synced file always appears here, regardless of
the date/host/job filters — only `file_count`/`last_seen` per child respect them (0/absent if
nothing currently matches). This lets the UI navigate through a folder that currently has no
matching files of its own but does have matching descendants further down.

```json
{
  "data": [
    {"path": "/var/lib/dbdata", "name": "dbdata", "file_count": 12, "last_seen": 1752400010, "has_children": false}
  ]
}
```

## `GET /api/v1/policies`

Returns every policy, unfiltered by any client identity (unlike `policy-server`'s own `GetPolicies`
RPC, which every mesh node calls and which is scoped to its own matching policies). Not paginated.
Accepts an optional `?type=backup`, `?type=storage`, or `?type=restore` query parameter to restrict
the response to one policy type; omitted returns every type.

```json
{
  "data": [
    {
      "id": "b1f2c3d4-...",
      "name": "nightly-web-backup",
      "created_at": 1752400000,
      "updated_at": 1752400010,
      "client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
      "object_filters": [
        {"id": "a9e8d7c6-...", "path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}
      ],
      "rpo": "24h",
      "backup_window": ["0 2 * * *", "0 20 * * *"],
      "destinations": ["bwfs-east.internal:8080"],
      "storage_policy_id": "b2c3d4e5-...",
      "type": "backup",
      "port": 0,
      "config": "",
      "checkins": [
        {"hostname": "web-01", "last_seen_at": 1752400500}
      ]
    }
  ]
}
```

`created_at`/`updated_at` are Unix seconds, matching every other timestamp field in this API.
`checkins` lists every host that has received this policy from `GetPolicies`, each with its most
recent check-in time (Unix seconds) -- not a full history, one entry per host. Empty for a policy no
host has polled yet.

## `GET /api/v1/policies/{id}`

Returns one policy (same shape as one entry above). `404` if `id` doesn't match any policy.

## `POST /api/v1/policies`

Creates a new policy. Body:

```json
{
  "name": "nightly-web-backup",
  "client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
  "object_filters": [{"path": "/var/www", "include": ["*.html"], "exclude": ["*.tmp"]}],
  "rpo": "24h",
  "backup_window": ["0 2 * * *"],
  "storage_policy_id": "b2c3d4e5-..."
}
```

`201` with the created policy (including its server-assigned `id` and each object filter's `id`) on
success. `400` if `name` is empty or slugifies to nothing (no alphanumeric characters), any
`include`/`exclude`/hostname entry isn't a syntactically valid glob pattern, `storage_policy_id` is
empty, or `storage_policy_id` doesn't name an existing storage policy — no file is written when
validation fails. The response's `destinations` is always derived live from `storage_policy_id`'s
checkin records, never something this body sets directly.

An optional integer `disabled_at` (Unix seconds) may also be included; once that time passes,
`GetPolicies` stops serving the policy. Omit it (or send `0`) for a policy that's never disabled.

## `PUT /api/v1/policies/{id}`

Replaces an existing policy's editable fields — same body shape as `POST`, full replacement rather
than a partial patch. `200` with the updated policy; the `id` and `created_at` never change.
Reordering or inserting `object_filters` entries changes the affected filters' `id`s. `400` on the
same validation failures as `POST` (the existing file is left untouched). `404` if `id` doesn't
match any policy. `disabled_at` round-trips like every other field: since this is a full replacement, an existing
`disabled_at` survives an edit only if the request echoes it back explicitly (the same way
`client_filters` already must be) — omitting it (or sending `0`) clears it. Known limitation: the
web UI's policy edit form does not currently read or send `disabled_at`, so saving an edit through
the UI always clears it — including for an adhoc policy edited before it expires, which becomes a
permanent recurring policy as a result.

## `DELETE /api/v1/policies/{id}`

Deletes a policy. `204` on success, `404` if `id` doesn't match any policy, `400` if `id` names a
storage policy still referenced by at least one backup policy (error message shape:
`storage policy in use by: <names>`).

## `POST /api/v1/policies/adhoc`

Creates a one-time backup policy without composing `backup_window`/`rpo`/`disabled_at` by hand.
Body — same shape as `POST /api/v1/policies`; `rpo`/`backup_window`/`disabled_at` are accepted for
compatibility but always ignored:

```json
{
  "name": "web-emergency",
  "client_filters": {"hostnames": ["web-*"]},
  "object_filters": [{"path": "/var/www"}],
  "storage_policy_id": "b2c3d4e5-..."
}
```

`api-server` prefixes the name with `adhoc_`, sets `backup_window` to `["* * * * *"]` (open every
minute), and sets `rpo` and `disabled_at` from the configured `AdhocPolicyTimeoutSec` (default
`3600`/1h) — `rpo` as a duration string equal to the timeout, `disabled_at` as `now + timeout`. Every
matched node runs the backup exactly once, the next time it polls within that window, and the policy
disables itself (pruning matched nodes' state for it) once the timeout passes, though it still
appears in `GET /api/v1/policies`/`ListPolicies` until explicitly deleted — `disabled_at` only stops
execution, not listing. `201` with the created policy (including `disabled_at`) on success; same
`400`/malformed-JSON handling as `POST /api/v1/policies`.

## `POST /api/v1/storage-policies`

Creates a new `"storage"`-typed policy. Body:

```json
{
  "name": "east-1-storage",
  "client_filters": {"hostnames": ["storage-east-1.internal"], "labels": {}},
  "port": 9400,
  "config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
}
```

`config` is a JSON string, not a nested object — `policy-server` treats it as opaque, pass-through
text; the web UI is the one that gives it the `backend`/`root` shape shown above. There is no
`hostname` field — targeting a node is `client_filters.hostnames`, identical to a backup policy.
`201` with the created policy on success. `400` if `name` is empty, `port` isn't in `[1,
65535]`, or `config` isn't well-formed JSON — no file is written when validation fails.

## `PUT /api/v1/storage-policies/{id}`

Replaces an existing storage policy's editable fields — same body shape as `POST`, full replacement
rather than a partial patch. `200` with the updated policy; `id`, `created_at`, and `type` never
change. `400` on the same validation failures as `POST`. `404` if `id` doesn't match any policy.
`disabled_at` round-trips like every other field: since this is a full replacement, an existing
`disabled_at` survives an edit only if the request echoes it back explicitly (the same way
`client_filters` already must be) — omitting it (or sending `0`) clears it. Same known limitation as
`PUT /api/v1/policies/{id}` above: the web UI's storage policy edit form doesn't read or send
`disabled_at`, so an edit made through the UI always clears it.

## `POST /api/v1/restore`

Creates a new `"restore"`-typed policy -- the only way to create one; there is no
`POST /api/v1/restore-policies` and no update endpoint. Body:

```json
{
  "name": "web01-emergency",
  "client_filters": {"hostnames": ["web-01"], "labels": {}},
  "storage_policy_id": "<id of an existing \"storage\" policy>",
  "rules": [{"host": "web-01", "path": "/var/www/index.html", "include": true}],
  "mode": "verify",
  "overwrite": false
}
```

`storage_policy_id` must reference an existing `"storage"`-typed policy -- its dial address is
resolved live from that policy's check-ins, exactly like a `"backup"` policy's `destinations`.
`rules` must contain at least one entry; an entry with `"host": null` (or omitted) is
host-agnostic, applying across every source host under `path`. `client_filters` targets the node
that will execute the restore, the same mechanism every other policy type uses.

`mode` is `"verify"` or `"restore"`, defaulting to `"verify"` when omitted (so existing callers that
never set it are unaffected). `mode: "verify"` creates the policy exactly as before: `agent` picks
it up and runs `rwfs verify` against it, writing nothing. `mode: "restore"` creates the policy
exactly like `mode: "verify"` (`201` with the created policy), but `agent` runs `rwfs restore`
against it instead of `rwfs verify` -- `rwfs restore` resolves the policy's rules against the live
store, logs each file's source path and computed destination path, and actually creates the
resolved directory structure at each destination (parent-before-child, stopping at the first
failure); file content restore itself remains log-only, writing nothing. `overwrite` (bool, default
`false`) is accepted in both modes; under `mode: "verify"` it has no effect, since `rwfs verify`
doesn't read it. `201` with the created policy on success, for either mode.

`400` if `name` is empty, `storage_policy_id` doesn't reference an existing `"storage"` policy,
`rules` is empty or contains an entry with an empty `path`, or `mode` is neither `"verify"` nor
`"restore"`.

An optional integer `disabled_at` (Unix seconds) may also be included; once that time passes,
`GetPolicies` stops serving the policy. Omit it (or send `0`) for a policy that's never disabled.

Restore policies are never updatable: `PUT /api/v1/policies/{id}` against one returns `400`.
`GET /api/v1/policies/{id}` and `DELETE /api/v1/policies/{id}` work on them like any other type.

## `GET /api/v1/jobs`

Returns backup and agent-lifecycle jobs, reconstructed by pairing `event=start`/`event=finish` log
lines from Loki (via `log-gateway`'s read-proxy) on `job_id`. Not backed by a database — every
request re-queries Loki over the requested time window.

Query parameters (all optional):

| Param | Type | Description |
|-------|------|--------------|
| `kind` | string | One of `backup`, `bootstrap-refresh`, `operating-refresh`, `policy-update`, `restore` |
| `source_host` | string | Exact match on the job's start-line hostname. Must match `^[a-zA-Z0-9.-]+$` — `400` on invalid characters |
| `state` | string | Exact match on the job's terminal status (e.g. `success`, `failure`); jobs still running never match, since they have no finish line yet |
| `since` | int, unix seconds | Start of the query window, default `now - 24h` |
| `until` | int, unix seconds | End of the query window, default `now` |
| `limit` | int, 1–500 | Page size, default 100 |

```json
{
  "data": [
    {
      "job_id": "backup:nightly:var-www:abcd1234:1752400000",
      "kind": "backup",
      "source_host": "database",
      "store_host": "bwfs-east",
      "started_at": 1752400000,
      "finished_at": 1752400010,
      "state": "success"
    }
  ],
  "truncated": false
}
```

`kind` is derived from the `job_id` prefix (everything before the first `:`), not a separate
stored field. `store_host` is only ever populated for `kind=backup` (from the `bwfs` finish
line's hostname); every other kind leaves it `null`. A job with only a start line in the window is
`"state": "in_progress"` with `finished_at: null`. A job with only a finish line (its start fell
outside the window) gets `started_at: null` — never guessed. `truncated: true` means one of the
underlying Loki queries hit its own line cap and the result may be incomplete; narrow `since`/
`until` and retry.

`400` if `kind` isn't one of the five valid values, `since`/`until` aren't unix-second integers,
`until` is before `since`, the window exceeds 168h, or `limit` isn't an integer in `[1, 500]`.
`502` if the underlying Loki query fails.

## `GET /api/v1/jobs/{job_id}/logs`

| Param | Type | Description |
|-------|------|--------------|
| `since` | unix seconds | Only lines after this timestamp. Default: 24h before now |
| `source_host` / `store_host` | string | Optional — narrows the query to the hosts involved, if already known from a prior `/jobs` response. Each must match `^[a-zA-Z0-9.-]+$` — `400` on invalid characters |

`job_id` must match `^[a-zA-Z0-9:._-]+$` — `400` otherwise.

```json
{
  "data": [
    {"timestamp": 1752400000123456789, "hostname": "database", "binary": "brfs", "line": "{...raw json log line...}"}
  ]
}
```

A client polling with an advancing `since` cursor gets a near-real-time tail.

## See Also

- [Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)
- [Catalog Sync Protocol](../protocols/catalog-sync.md) — the internal gRPC protocol `ListEntries` (this API's `/catalog` backend) is part of
