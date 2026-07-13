# Design: deterministic IDs for policies and object filters

**Date:** 2026-07-13
**Status:** Approved for planning

## Problem

`policy-server` has no concept of identity beyond operator-typed text: a policy's `metadata.name`
is never checked for uniqueness across policy files, and an object filter's identity is just its
`path` string. The final review of the include/exclude feature flagged the concrete consequence:
two object filters in the same policy sharing a `path` (e.g. one with `include`, one with
`exclude`, applied to the same root) collide in `agent`'s task-ID scheme
(`backup:<policy-name>:<path>`), silently sharing one `agent-state.json` entry and one in-flight
slot instead of being tracked independently.

This adds a `policy-server`-computed, deterministic ID to every policy and every object filter,
used to guarantee uniqueness end-to-end without relying on operator-supplied text.

## ID generation

Computed in `parsePolicyFile` (`src/cmd/policy-server/policy.go`), using UUID v5
(`github.com/google/uuid`'s `uuid.NewSHA1`, RFC 4122/SHA1-based — deterministic given the same
inputs, no state written anywhere):

- **Policy ID:** `uuid.NewSHA1(policyNamespace, []byte(filename))`, where `filename` is the policy
  file's basename (already available as `filePath`'s base) and `policyNamespace` is a fixed
  constant UUID defined once in the codebase, existing solely to separate this ID-space from
  unrelated UUID uses.
- **Object filter ID:** `uuid.NewSHA1(policy.ID, []byte(strconv.Itoa(index)))` — derived from the
  policy's own ID plus the filter's position in its `object_filters` array, so filter IDs are
  naturally scoped under their policy and can never collide across policies even by coincidence.

Both are recomputed fresh on every reload — `policy-server` stays stateless, nothing is written
back to the JSON policy files. Consequence: renaming a policy file, or reordering/inserting object
filters within one, changes the resulting ID(s). This is an accepted trade-off (see "Consequences"
below) in exchange for not needing any persistent ID store.

## Schema and wire changes

Purely additive — `Metadata.Name` and `ObjectFilter.Path` are unchanged, remain the human-facing
labels. The on-disk policy JSON schema itself does not change; IDs are compute-only and never
read from or written to a policy file.

**`policy-server` in-memory types** (`src/cmd/policy-server/policy.go`):
```go
type Metadata struct {
    ID        string    `json:"-"`
    Name      string    `json:"name"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}

type ObjectFilter struct {
    ID      string   `json:"-"`
    Path    string   `json:"path"`
    Include []string `json:"include,omitempty"`
    Exclude []string `json:"exclude,omitempty"`
}
```
`parsePolicyFile` fills both `ID` fields after unmarshaling, before validation.

**gRPC proto** (`src/api/policyserver.proto`): `Policy` gains `string id = 8;`; `ObjectFilter`
gains `string id = 4;` (both appended, existing field numbers untouched).

**`policyclient`'s cache** (`policies-cache.json`): `CachedPolicy` gains `ID string`; its
`ObjectFilter` struct gains `ID string` — carried through from the proto response exactly as
`path`/`include`/`exclude` already are, no defaulting.

**`agent`'s mirrored structs**: the same two `ID` fields added to `agent`'s own duplicate
`cachedPolicy`/`ObjectFilter` types (agent can't import `policyclient`'s `main` package, per the
existing convention).

**`policy-server`'s `cache.go` deep copy**: `Policy.ID` needs no code change — it lives on
`Metadata`, already copied by whole-struct value (`Metadata: p.Metadata`). `ObjectFilter.ID` does
need one line added to the existing per-element reconstruction loop
(`ObjectFilter{ID: f.ID, Path: f.Path, Include: ..., Exclude: ...}`) since that loop already
rebuilds the struct field-by-field.

## `agent`'s task/job ID construction

`backupTasks` (`src/cmd/agent/backup.go`) currently derives identity purely from
`(policyName, filter.Path)`. It now also folds in an 8-hex-character suffix derived from
`filter.ID` (the filter's UUID with dashes stripped, first 8 characters) — keeping the existing
human-readable segments for grep-ability while making every ID unique by construction regardless
of name/path collisions:

```go
func shortID(id string) string {
    return strings.ReplaceAll(id, "-", "")[:8]
}

func backupTaskID(policyName, path, filterID string) string {
    return fmt.Sprintf("backup:%s:%s:%s", policyName, path, shortID(filterID))
}

func backupJobID(policyName, path, filterID string, now time.Time) string {
    return fmt.Sprintf("backup:%s:%s:%s:%d", policyName, slug(path), shortID(filterID), now.Unix())
}
```

`agent-state.json` entries and `bwfs` job-ids go from `backup:daily-db-backup:/var/lib/postgres` to
`backup:daily-db-backup:/var/lib/postgres:3f2a1b9c` — still immediately human-scannable, but now
two filters that would otherwise look identical get visibly distinct, stable suffixes.
`list-policies`'s existing "POLICY" column (which prints the task ID verbatim) picks this up with
no code change. `filter.ID` itself (the full UUID) is not otherwise used by `agent` — it's opaque,
purely a uniqueness input.

## Consequences

- **One-time history reset.** Every existing `agent-state.json` entry's key changes format on
  upgrade, so all backup task history (last success, consecutive failures, RPO tracking) is
  orphaned once. Worst case: a task that legitimately ran recently looks like "never run" and may
  fire immediately if its `backup_window` is open. No migration or fallback lookup is implemented —
  accepted as a one-time, low-cost cost of fixing the underlying collision, consistent with this
  project's existing precedent of not migrating internal/pre-release state formats.
- **ID churn on file rename or filter reorder.** Renaming a policy file, or inserting/reordering
  entries in one policy's `object_filters` array, changes the affected ID(s) and thus that task's
  history the same way. Treated as rare, deliberate operator actions rather than a case worth
  additional engineering.

## Testing plan

- **`policy-server`** (`policy_test.go`): same filename parsed twice yields the same policy ID
  (determinism); two different filenames yield different policy IDs; two object filters at
  different indices in one file yield different filter IDs; two object filters with the *identical*
  `path` in one file yield distinct IDs (the direct regression test for the collision this design
  fixes).
- **`policy-server`** (`cache_test.go`): extend the existing snapshot-copy test to assert
  `ObjectFilter.ID` survives a `Policies()` round-trip.
- **`policyclient`** (`fetch_test.go`): `toCachedPolicies` carries `Policy.ID` and each
  `ObjectFilter.ID` through from the proto response.
- **`agent`** (`backup_test.go`): a direct unit test for `shortID` (dash-stripping + 8-char
  truncation); a `backupTasks` test proving two object filters sharing a `path` (different
  include/exclude, mirroring the real collision scenario) now produce two distinct task IDs.

## Documentation

- `docs/protocols/policy-server.md` — add `id` to both `Policy` and `ObjectFilter` proto blocks;
  note both are `policy-server`-computed and deterministic, not part of the on-disk policy schema.
- `docs/components/policy-server.md` — one sentence: IDs derive from filename (+ position, for
  object filters), stable across reloads, change only on rename/reorder/insert.
- `docs/components/policyclient.md` — update the cache JSON example to show the new `id` fields.
- `docs/components/agent.md` — update the task-ID format description and explain why the suffix
  exists (uniqueness even when two object filters share a path).
- `CHANGELOG.md` — one dated entry.
- No `docs/ARCHITECTURE.md` change (no new topology/data flow) and no new narrative doc.

## Out of scope

- No ID persisted back into policy files, no sidecar ID store.
- No migration or fallback lookup for `agent-state.json`'s task-ID format change.
- No new `list-policies` column or CLI surface for the raw IDs — the existing task-ID string
  (now suffixed) is the only place they're user-visible.
