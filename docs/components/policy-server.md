# policy-server

Serves backup policies — JSON files under `$MP_CONFIG_PATH/policies/backup/`, one per policy —
filtered to exactly the policies whose `client_filters` match a requesting client's verified
hostname and certificate-embedded attribute labels. Also exposes an admin write API
(`ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy`) that `api-server` proxies as REST, so
policies no longer have to be hand-edited on this host. See
[Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md) and
[Design: Policy Management API](../superpowers/specs/2026-07-18-policy-management-api-design.md).

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

### Policy types and directory layout

A policy's type is derived from the name of the immediate subfolder its file lives in under
`$MP_CONFIG_PATH/policies/` — `policies/backup/*.json` are type `"backup"`, the only type that
exists today. Type is never read from or written to the on-disk policy JSON itself; it's purely a
function of file location, computed at load time the same way `policy-server` already computes each
policy's `id` and never reads it from the file. A `*.json` sitting directly under `policies/`,
outside any type subfolder, is skipped and logged — the same "loud skip, don't block the rest"
treatment already applied to a malformed file (see below). An unrecognized subfolder name is still
loaded and tagged with its literal name; `policy-server` does not validate against a whitelist of
known types — that's left to whichever downstream consumer (`agent` today) knows what to do with a
given type. `CreatePolicy` always writes into `policies/backup/`, creating that subdirectory if
missing — there is currently no way to create a policy of any other type through this RPC. See
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md).

### Policy files and hot reload

Each policy type subfolder's `*.json` file is one policy: `metadata` (`name` plus operator-set
`created_at`/`updated_at`), `client_filters` (`hostnames` glob list, `labels` map), `object_filters`
(a list of `{"path": "...", "include": [...], "exclude": [...]}` entries — `include`/`exclude` are
optional glob-pattern lists, validated as syntactically-valid patterns at load time but otherwise
opaque to `policy-server`; see [Filesystem Backup Flow](../process/filesystem-backup.md) for how
`brfs` applies them), `rpo` (a duration string, e.g. `"24h"`), `backup_window`
(a list of cron expressions, e.g. `["0 2 * * *", "0 20 * * *"]`), and `destination` (a `host:port`
string, the target `bwfs` for this policy's backups). `policy-server` also computes (never reads)
a deterministic ID for the policy itself and for each object filter, derived from the file's name
and each filter's position — stable across reloads, and changes only if the file is renamed or its
`object_filters` are reordered/have entries inserted before an existing one.

All policies are loaded into memory at startup. To pick up edits, touch
`$MP_CONFIG_PATH/policies/.changed` after finishing your edit(s) — `policy-server` watches that one
top-level sentinel file via `fsnotify` and reloads every type subfolder as a single atomic swap on
each write. This lets you edit several policy files (in one or more type subfolders) as a batch and
trigger exactly one reload, rather than reloading (potentially mid-edit) on every individual file
write.

A single malformed policy file is skipped, logged loudly, and does not block the rest of the
directory from loading. If every file in a reload attempt fails to parse, the previous good
in-memory cache is kept rather than replaced with an empty list.

Writes made through `CreatePolicy`/`UpdatePolicy`/`DeletePolicy` bypass this sentinel-and-fsnotify
path entirely: each validates its input, atomically writes (or removes) the affected file, then
calls the same `Reload` directly, in-process, before the RPC responds. An operator hand-editing
files on disk and the write RPCs can coexist — both funnel through the same `Reload`/validation
logic — but there's no locking between them beyond the atomic-rename write itself.

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
