# policy-server

Serves backup policies — static, operator-authored JSON files under `$MP_CONFIG_PATH/policies/` —
filtered to exactly the policies whose `client_filters` match a requesting client's verified
hostname and certificate-embedded attribute labels. See
[Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md).

`policy-server` is bootstrapped and certificate-managed exactly like any other node in the mesh
(`client-manager add`, `agent` + `issuer` refresh) — it holds no database and calls no other
service. A client's attribute labels are read directly off its presented mTLS certificate: `issuer`
already embeds a hostname's current `attribute` key/value pairs as a custom X.509 extension on
every operating certificate it mints.

## Usage

```bash
policy-server --port 9300
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `9300` (or `policy_server_port` from `local.conf`) | Port to listen on |
| `--debug` | false | Enable debug logging |

## Behavior

`GetPolicies` (see [protocol](../protocols/policy-server.md)) is `policy-server`'s only RPC. The
caller's hostname is always the verified mTLS peer identity; the caller's attribute labels are
parsed from the same peer certificate's embedded extension (`mtls.PeerAttributes`) — neither is
ever a field on the request.

A policy matches a client when: (`client_filters.hostnames` is empty, or the client's hostname
matches at least one glob pattern in it) **and** (every key/value pair in `client_filters.labels`
is present among the client's attribute labels). Both conditions must hold. The response never
includes `client_filters` — a returned policy has already matched, so the filter that selected it
carries no further meaning to the caller.

`policy-server` never parses, validates, or evaluates a policy's `rpo` or `backup_window` — both
are opaque strings, stored and returned verbatim for a future consumer to interpret.

### Policy files and hot reload

Each `$MP_CONFIG_PATH/policies/*.json` file is one policy: `metadata` (`name` plus operator-set
`created_at`/`updated_at`), `client_filters` (`hostnames` glob list, `labels` map), `object_filters`
(a list of `{"path": "..."}` entries), `rpo` (a duration string, e.g. `"24h"`), and `backup_window`
(a list of cron expressions, e.g. `["0 2 * * *", "0 20 * * *"]`).

All policies are loaded into memory at startup. To pick up edits, touch
`$MP_CONFIG_PATH/policies/.changed` after finishing your edit(s) — `policy-server` watches that one
sentinel file via `fsnotify` and reloads the entire directory as a single atomic swap on each
write. This lets you edit several policy files as a batch and trigger exactly one reload, rather
than reloading (potentially mid-edit) on every individual file write.

A single malformed policy file is skipped, logged loudly, and does not block the rest of the
directory from loading. If every file in a reload attempt fails to parse, the previous good
in-memory cache is kept rather than replaced with an empty list.

## Configuration Keys

- `policy_server_host` / `policy_server_port` — where `policy-server` listens *(default port:
  9300)*

## Building

```bash
make policy-server
```

## See Also

- [issuer](./issuer.md) — mints the operating certificates whose embedded attribute extension
  `policy-server` reads
- [policyclient](./policyclient.md) — fetches `GetPolicies` on `agent`'s `policy-update` schedule
- [Policy Server Protocol](../protocols/policy-server.md)
- [Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md)
- [Architecture](../ARCHITECTURE.md)
