# Design: policy type subfolders

**Date:** 2026-07-20
**Status:** Approved for planning

## Problem

`policy-server` currently loads every `*.json` file directly under `$MP_CONFIG_PATH/policies/` and
treats each one identically: a backup policy, full stop. Downstream, `agent` derives a backup task
for every `(cached policy, object_filters path)` pair unconditionally — it has no way to tell a
backup policy apart from any other kind, because no other kind exists yet.

A second, non-backup policy type is planned for the future. Introducing it later must not require
retrofitting a type distinction across every layer at once; that distinction needs to already exist
by the time it's needed. This design adds a `type` to policies now — with exactly one type,
`"backup"`, in use — and updates `agent` to filter on it, so the plumbing is in place before a
second type shows up.

## Approach

**Type is derived from the policy's immediate parent subfolder, not stored in the JSON file
itself.** `policies/backup/*.json` are type `"backup"`; a future `policies/<other>/*.json` would be
type `"<other>"` with no code change to the loading/reload logic. This was chosen over an explicit
`"type"` field inside each JSON file because it removes an entire class of drift (a file's folder
and its declared type disagreeing) and needs no schema migration for files that already exist — the
folder they're moved into *is* the migration.

`policy-server`'s directory scan changes from a flat glob to walking one level of subdirectories,
tagging every policy loaded from a given subfolder with that subfolder's name. This is a one-time
change to `Cache.Reload`: a future type needs a new subfolder on disk, not a new code path here.

Write-side (`CreatePolicy`/`UpdatePolicy`) is intentionally *not* extended with a `type` parameter
in this change — with only one type in existence, there is nothing for an operator to choose
between yet. `CreatePolicy` hardcodes `policies/backup/` as its target directory. A `type` selector
on the write path is deferred to whenever the second type is actually introduced.

Read-side (`GetPolicies`/`ListPolicies`, and everything downstream of them) does gain a `type`
field now, because `agent`'s filtering need is real today, not hypothetical.

## On-disk layout

```
$MP_CONFIG_PATH/policies/
└── backup/
    ├── audit-logs.json
    ├── database-backup.json
    └── webserver-backup.json
```

`demo/policy-server/policies/*.json` (the three existing demo files) move into
`demo/policy-server/policies/backup/` as part of this change. No back-compat and no migration
code: this project has no live deployments yet, so an operator with existing flat files simply
moves them into `policies/backup/` before upgrading. Noted as a breaking change in the changelog.

## `policy-server` internals

- `Policy` (`src/cmd/policy-server/policy.go`) gains `Type string` (`json:"-"`, computed at load
  time, never read from or written to the JSON file — the same treatment `ID` and `SourcePath`
  already get).
- `parsePolicyFile` takes the type its caller already knows (the subfolder name) and sets it on the
  returned `Policy`, rather than deriving it from the path itself — keeps the function's existing
  single-file-in, single-`Policy`-out shape.
- `Cache.Reload` (`cache.go`): instead of `filepath.Glob(filepath.Join(dir, "*.json"))`, it lists
  `dir`'s immediate subdirectories (`os.ReadDir`, filtered to `IsDir()`), globs `*.json` inside each,
  and calls `parsePolicyFile` with that subdirectory's base name as the type. A `*.json` sitting
  directly under `dir` (not inside any subfolder) is logged and skipped — same "loud skip, don't
  block the rest" handling already applied to a malformed file — it never fails the whole reload.
  `policy-server` does not validate the subfolder name against a whitelist of known types; an
  unrecognized subfolder is loaded and tagged with its literal name regardless. Deciding what an
  unrecognized type means is left entirely to downstream consumers (`agent` today).
- `Cache.Policies()`'s deep copy (used by every RPC handler) copies `Type` along with the other
  plain-value fields.
- `CreatePolicy` (`write.go`) writes into `filepath.Join(s.policiesDir, "backup")`, creating that
  subdirectory if it doesn't exist yet (mirrors the `os.MkdirAll` `main.go` already does for
  `policiesDir` itself). `UpdatePolicy` is unchanged beyond carrying `Type` through — it keeps
  writing to the existing file's `SourcePath`, so a policy's type is immutable via `Update`; moot
  today since only one type exists, and not a decision this design needs to make for a
  not-yet-existing second type.
- `watchForReload`'s sentinel stays a single file, `policiesDir/.changed` — one touch still triggers
  one full recursive reload across every type subfolder, unchanged from today's single-directory
  behavior.

## Proto (`src/api/policyserver.proto`)

`Policy` gains one field:

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string id = 8;
  ClientFilters client_filters = 9;
  string type = 10;
}
```

Populated by both `GetPolicies` (node-facing — `agent` needs it via the cache to filter) and
`ListPolicies` (admin-facing). `CreatePolicyRequest`/`UpdatePolicyRequest` are unchanged — no
`type` field, per the write-side decision above.

## `policyclient`

`CachedPolicy` (`src/cmd/policyclient/fetch.go`) gains `Type string \`json:"type"\``, populated
straight from `pb.Policy.GetType()` in `toCachedPolicies` — pure passthrough, identical treatment
to every other field already there. `policies-cache.json`'s example in
`docs/components/policyclient.md` gains `"type": "backup"`.

## `agent`

`backup.go`'s `cachedPolicy` struct gains `Type string`. `backupTasks()` gets one new skip, checked
first in its per-policy loop — before the existing unparseable-`rpo` and no-valid-`backup_window`
skips:

```go
for _, p := range cachedPolicies {
    if p.Type != "backup" {
        continue
    }
    ...
}
```

A cached policy of any other (or empty/missing) type contributes zero backup tasks — the same
fail-safe direction already used for the rpo/backup_window skips: no sound task can be built for a
policy this code doesn't understand, so it's skipped rather than guessed at.

## `api-server`

`policyDTO` (`src/cmd/api-server/policies.go`) gains `Type string \`json:"type"\`` in
`toPolicyDTO`, passthrough for API/UI consistency. `policyInput` (the `POST`/`PUT` request body) is
unchanged — no `type` field, matching the proto decision.

## Web UI

No changes. `PolicyFormView`/`PoliciesListView`/`PolicyDetailView` and `stores/policies.js` don't
need a type selector or filter while there's exactly one type and `Create` is hardcoded to it.

## Testing plan

- **`policy-server`**: `cache_test.go` — reload picks up `policies/backup/*.json` and tags each
  with `Type: "backup"`; a `*.json` directly under `policies/` (no subfolder) is skipped and logged,
  not fatal to the reload; multiple subfolders each tag their own policies with their own subfolder
  name. `write_test.go` — `CreatePolicy` writes into `policies/backup/`, creating the subdirectory
  on a fresh config path.
- **`policyclient`**: `fetch_test.go` — `Type` round-trips from a fake `GetPolicies` response into
  `CachedPolicy`.
- **`agent`**: `backup_test.go` — a cached policy with `Type: "backup"` still produces tasks exactly
  as before; a cached policy with a different (or empty) `Type` produces zero tasks, without
  affecting task derivation for other, correctly-typed policies in the same cache.

## Documentation

- `docs/components/policy-server.md` — directory layout section: policies now live under a
  per-type subfolder, `backup/` today; type is derived from the subfolder, not stored in the file;
  a stray flat file is skipped and logged.
- `docs/components/policyclient.md` — `CachedPolicy`/`policies-cache.json` example gains `"type"`.
- `docs/components/agent.md` — note that backup-task derivation now skips any cached policy whose
  type isn't `"backup"`.
- `docs/protocols/policy-server.md` — document `Policy.type`.
- `CHANGELOG.md` — one dated entry, noting the on-disk layout change is breaking (no migration
  path) for anyone with existing flat policy files.

## Out of scope

- A `type` parameter on `CreatePolicy`/`UpdatePolicy` or the web UI's policy form — deferred until
  a second type actually exists to choose between.
- Auto-migrating flat `policies/*.json` files into `policies/backup/` on `policy-server` startup —
  explicitly rejected; no back-compat is being kept.
- Validating subfolder names against a whitelist of known types in `policy-server` — left to
  downstream consumers.
- Anything about what a future second policy type actually does (its schema, its consumer, its
  execution model) — this design only establishes that types are distinguishable end-to-end.
