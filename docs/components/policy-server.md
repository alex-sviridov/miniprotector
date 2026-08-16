# policy-server

Serves backup policies — JSON files under `$MP_CONFIG_PATH/policies/backup/`, one per policy —
filtered to exactly the policies whose `client_filters` match a requesting client's verified
hostname and certificate-embedded attribute labels. Also exposes an admin write API
(`ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy`) that `api-server` proxies as REST, so
policies no longer have to be hand-edited on this host. See
[Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md) and
[Design: Policy Management API](../superpowers/specs/2026-07-18-policy-management-api-design.md).

`policy-server` is bootstrapped and certificate-managed exactly like any other node in the mesh
(`client-manager add`, `agent` + `issuer` refresh) — it calls no other service, but now owns one
piece of local state: a SQLite database recording check-ins (see
[Check-in tracking](#check-in-tracking) below). A client's attribute labels are read directly off its presented mTLS certificate: `issuer`
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

`GetPolicies` (see [protocol](../protocols/policy-server.md)) is `policy-server`'s main RPC, and the
only one every mesh node calls. The caller's hostname is always the verified mTLS peer identity;
the caller's attribute labels are parsed from the same peer certificate's embedded extension
(`mtls.PeerAttributes`) — neither is ever a field on the request.

A policy matches a client when: (`client_filters.hostnames` is empty, or the client's hostname
matches at least one glob pattern in it) **and** (every key/value pair in `client_filters.labels`
is present among the client's attribute labels). Both conditions must hold. The response never
includes `client_filters` — a returned policy has already matched, so the filter that selected it
carries no further meaning to the caller.

`policy-server` never parses, validates, or evaluates a policy's `rpo` or `backup_window` — both
are opaque strings, stored and returned verbatim for a future consumer to interpret.

### Policy types and directory layout

A policy's type is derived from the name of the immediate subfolder its file lives in under
`$MP_CONFIG_PATH/policies/` — `policies/backup/*.json` are type `"backup"`, `policies/storage/*.json`
are type `"storage"`, and `policies/restore/*.json` are type `"restore"`. Type is never read from or
written to the on-disk policy JSON itself; it's purely a function of file location, computed at load
time the same way `policy-server` already computes each policy's `id`. Each type is a distinct Go
type internally (`BackupPolicy`, `StoragePolicy`, `RestorePolicy`) implementing a shared `Policy`
interface, with its own on-disk schema, validation,
and wire conversion — adding a further type means writing one more such type and registering its
parser, not changing `policy-server`'s directory-walking or RPC-handling code. A `*.json` sitting
directly under `policies/`, outside any type subfolder, is skipped and logged — the same "loud skip,
don't block the rest" treatment applied to a malformed file. **A subfolder name that isn't a
registered type is also skipped and logged**, the same way — there's no schema to load an
unrecognized type's file into, so it can no longer be loaded generically the way an earlier design
allowed. `CreatePolicy` requires a `type` (`"backup"`, `"storage"`, or `"restore"`) and writes into the matching
`policies/<type>/`, creating that subdirectory if missing; a request that sets fields belonging to
the other type is rejected. `ListPolicies` additionally accepts an optional `type` filter — `"backup"`, `"storage"`, or `"restore"` restricts
the response to that type; empty returns every type, unchanged from before this filter existed. See
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md)
and [Design: Storage Policy Type](../superpowers/specs/2026-07-28-storage-policy-type-design.md).

A `"backup"` policy describes what to back up and, via `storage_policy_id`, where: `object_filters`,
`rpo`, `backup_window`, and a required reference to a `"storage"`-typed policy's `id`. Its
`destinations` (one `"host:port"` entry per storage server, freshest-checked-in-first) is never
itself stored or settable — it's computed live from the referenced storage policy's checkin records
every time `policy-server` returns the policy, so a storage node checking in under a new hostname (or
simply staying alive) keeps every backup policy linked to it current with no re-save needed. See
[Design: backup destination from checkin list](../superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md).

A `"storage"` policy describes how a future storage server should be configured: `port` and an opaque
`config` JSON blob `policy-server` validates is well-formed but never interprets. Targeting which node
runs it is `client_filters` — the same mechanism a backup policy already uses — not a field specific
to this type; see
[Design: agent storage-policy supervision](../superpowers/specs/2026-07-28-agent-storage-supervision-design.md),
which is the first actual consumer of `storage`-typed policies. See
[Design: link backup policies to storage policies by id](../superpowers/specs/2026-08-03-backup-policy-storage-link-design.md).

A `"restore"` policy is a one-shot directive: `client_filters` targets the node that will execute
the restore, `storage_policy_id` (required, references an existing `"storage"`-typed policy's `id`
— the same field, existence check, and live `destinations` resolution a `"backup"` policy already
uses) names the source `bwfs` to restore from, and `rules` (required, at least one entry —
`{host, path, include, dest_path, not_before, not_after}`, mirroring the web restore cart's own rule shape; an
empty/omitted `host` means the rule applies across every source host; `dest_path`, if set and
different from `path`, is the path to restore to instead — empty or equal to `path` means no
rename, and it's a validation error to set it on a rule with `include: false`; `not_before`/`not_after`
(both optional, Unix seconds; 0 = unbounded on that side) bound which backed-up version of the rule's
selection is used — the latest version whose backup date falls inside the window wins) says what to
restore. It has no `object_filters`,
`rpo`, `backup_window`, `port`, or `config`. Unlike every other type, a `"restore"` policy is never
updatable -- `UpdatePolicy` rejects any request targeting one with `INVALID_ARGUMENT`, regardless of
which fields the request sets, so `api-server`'s generic `PUT /api/v1/policies/{id}` rejects it too,
with no `api-server`-side special-casing needed. See
[Design: Restore Policy Verification Execution](../superpowers/specs/2026-08-10-restore-policy-verification-design.md)
and
[Design: Restore Destination Rename](../superpowers/specs/2026-08-13-restore-destination-rename-design.md).

### Policy files and hot reload

Each policy type subfolder's `*.json` file is one policy. Every type shares `metadata` (`name`,
operator-set `created_at`/`updated_at`, and an optional `disabled_at` -- unset by default, see
[Disabling a policy without deleting it](#disabling-a-policy-without-deleting-it) below) and
`client_filters` (`hostnames` glob list, `labels` map). A
`"backup"` policy additionally has `object_filters` (a list of `{"path": "...", "include": [...],
"exclude": [...]}` entries — `include`/`exclude` are optional glob-pattern lists, validated as
syntactically-valid patterns at load time but otherwise opaque to `policy-server`; see
[Filesystem Backup Flow](../process/filesystem-backup.md) for how `brfs` applies them), `rpo` (a
duration string, e.g. `"24h"`), `backup_window` (a list of cron expressions, e.g.
`["0 2 * * *", "0 20 * * *"]`), and `storage_policy_id` (the `id` of a `"storage"`-typed policy --
required). `destinations`, unlike every other backup- or restore-policy field, is never read from the on-disk JSON: it's
computed at read time from the checkin records against the storage policy `storage_policy_id` names,
ordered freshest-checked-in-first (`storage/policyserver`'s `CheckinsForPolicy` query, not re-sorted
downstream). A `"storage"` policy instead has `port` and `config` (an opaque JSON object,
validated as well-formed at load time but never interpreted). `policy-server` also computes
(never reads) a deterministic ID for the policy itself — and, for a `"backup"` policy, one for each
object filter — derived from the file's name (and each filter's position) — stable across reloads,
and changes only if the file is renamed or (for a backup policy) its `object_filters` are
reordered/have entries inserted before an existing one.

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
logic — but there's no locking between them beyond the atomic-rename write itself. Deleting a
`"storage"` policy is rejected if any `"backup"` policy's `storage_policy_id` still points at it —
an operator must repoint or delete those backup policies first.

### Disabling a policy without deleting it

Every policy, of any type, can carry a `disabled_at` timestamp -- unset by default, meaning "never
disabled." Once that time passes, `GetPolicies` stops returning the policy to any matching node
(checked live against the current time on every call); `ListPolicies` keeps showing it, disabled or
not, since it's the admin/`api-server` visibility surface. `policy-server` attaches no meaning to
*why* a policy is disabled -- it's a generic primitive, not an "adhoc" or "temporary" policy concept
of its own. A one-time backup, for instance, is planned to be nothing more than an ordinary `"backup"`
policy with an unusually permissive `backup_window` and a near-future `disabled_at`, composed by a
future `api-server` convenience endpoint -- neither `policy-server` nor `agent` need to know that
composition happened. See
[Design: generic disabled_at policy field](../superpowers/specs/2026-08-02-policy-disabled-at-design.md).

### Check-in tracking

Every time `GetPolicies` hands a policy to a host, `policy-server` upserts a row —
`(policy, hostname, last_seen_at)` — into a local SQLite database at
`<var-dir>/policy-server.sqlite`. One row exists per `(policy, hostname)` pair: a host re-polling
the same policy overwrites its own row's timestamp rather than adding a new one, so the table always
holds each host's *most recent* check-in per policy, not a full history. This covers every policy
type `GetPolicies` returns (`"backup"`, `"storage"`, and `"restore"` alike). If the check-in write fails,
`GetPolicies` fails the whole call — check-in tracking is not best-effort telemetry the caller's
policies can silently proceed without.

`ListPolicies` attaches each policy's current check-in rows (host + last-seen timestamp) to the
response; `GetPolicies` never does, the same way it never echoes back `client_filters`.

A background routine ticks every fixed 1 minute and deletes any check-in row whose `last_seen_at` is
older than `CheckinRetentionSec` (config key, default `86400` = 24h). A host that stops polling a
policy — decommissioned, or no longer matched — simply ages out of that policy's check-in list once
its one row passes the retention window. See
[Design: Policy Check-in Tracking](../superpowers/specs/2026-08-03-policy-checkin-tracking-design.md).

### Bootstrap-refresh cert status tracking

Every `GetPolicies` call also records the caller's `bootstrap_refresh_last_error` /
`bootstrap_refresh_last_attempt_at` request fields into a `(hostname, last_error, last_attempt_at)`
row in the same SQLite database, one row per hostname (a re-poll overwrites the prior row, so a
subsequent healthy report clears a previously recorded failure). Unlike check-in tracking above,
this happens exactly once per request, unconditionally, *before* the per-matched-policy loop —
not once per matched policy — so a host with zero matching policies (an unenrolled or
misconfigured node, exactly the case most likely to be having certificate trouble) still gets its
status recorded. It is also intentionally non-fatal: a recording failure is logged but does not
fail `GetPolicies`, unlike a check-in write failure.

The `GetNodeCertStatus` RPC (see [protocol](../protocols/policy-server.md)) serves this data back —
used by `api-server`/UI-facing tooling to surface renewal failures, not called by mesh nodes
themselves. A hostname that has never called `GetPolicies`, and one that has and reported healthy,
both correctly return a present result with an empty `last_error` — never an error. See
[Design: Bootstrap Certificate Renewal](../superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md).

## Configuration Keys

- `policy_server_host` / `policy_server_port` — where `policy-server` listens *(default port:
  9300)*
- `CheckinRetentionSec` — how long a check-in row survives with no re-poll before the cleanup
  routine removes it *(default: 86400)*

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
- [Design: Policy Check-in Tracking](../superpowers/specs/2026-08-03-policy-checkin-tracking-design.md)
- [Design: Bootstrap Certificate Renewal](../superpowers/specs/2026-08-16-bootstrap-cert-renewal-design.md)
- [Architecture](../ARCHITECTURE.md)
