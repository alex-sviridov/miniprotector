# Filesystem Backup Flow

A short walk-through of how a file ends up backed up, end to end, and where `include`/`exclude`
fit in. For wire-level and per-component detail, see the linked docs below — this page is just the
narrative connecting them.

## The flow

1. An operator writes a policy JSON file under `policy-server`'s `$MP_CONFIG_PATH/policies/`. Each
   `object_filters` entry is a backup root (`path`) plus optional `include`/`exclude` glob-pattern
   lists. Neither is required — omit both to back up everything under `path`.
2. Every enrolled node's `agent` runs `policyclient fetch` on a schedule, which calls
   `policy-server`'s `GetPolicies` RPC and caches the matching policies (including each object
   filter's `include`/`exclude`) to `policies-cache.json`.
3. `agent` derives one backup task per cached `(policy, object filter)` pair. When a task is due
   (its `backup_window` is open and its `rpo` has elapsed), `agent` execs `brfs <path>
   --destination <destination> --job-id <id>`, adding `--include <patterns>` and/or `--exclude
   <patterns>` only when the object filter actually carries them.
4. `brfs` walks `path`, applying `--exclude` first (pruning a matched directory's entire subtree,
   omitting a matched file) and `--include` second (a files-only whitelist — directories are never
   filtered by it). Surviving files stream to `bwfs`.

## Matching semantics

Patterns are plain glob patterns (`*`, `?`, `[...]`) — no regex, no `**`. A pattern with no `/`
matches a file's basename at any depth (`*.tmp` matches `cache/x.tmp` and `a/b/x.tmp` alike); a
pattern containing `/` matches the path relative to the object filter's root exactly.

## See Also

- [policy-server](../components/policy-server.md) - policy authoring and on-disk schema
- [policyclient](../components/policyclient.md) - fetch/cache behavior
- [agent](../components/agent.md#policy-driven-backup-execution) - backup task derivation and scheduling
- [brfs](../components/brfs.md) - the actual directory walk and filtering
- [Policy Server Protocol](../protocols/policy-server.md) - wire-level `ObjectFilter` definition
