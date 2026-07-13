# Design: `include`/`exclude` patterns on object filters

**Date:** 2026-07-13
**Status:** Approved for planning

## Problem

`brfs` backs up an entire source folder recursively with no way to narrow what gets sent. Policies
served by `policy-server` (`ObjectFilter`) carry only a `path` — the same all-or-nothing limitation
propagates through `policyclient`'s cache and into the backup tasks `agent` derives and execs.

This adds two new attributes alongside `path` on every `ObjectFilter`, at every layer it appears:
`include` (glob patterns; a file must match at least one to be backed up) and `exclude` (glob
patterns; a matching file or directory is skipped). Both are optional and empty by default —
`brfs` itself supplies the effective default of `include=["*"]` when nothing else does.

## Matching semantics

Patterns are plain shell globs (`filepath.Match` syntax — `*`, `?`, `[...]`), not regex, applied
per walked entry (relative to the object filter's own root — the folder `brfs` was pointed at):

- A pattern with **no `/`** matches the entry's **basename**, at any depth. `*.tmp` matches
  `cache/x.tmp` and `a/b/c/x.tmp` alike.
- A pattern **containing `/`** matches the entry's **path relative to the root**, exactly (globs
  still don't cross `/`, so this only matches at the specific depth spelled out).

Algorithm, applied while walking:

1. **Exclude first.** If any exclude pattern matches:
   - a **directory** → skip descending into it (`filepath.SkipDir`); the directory itself and
     everything beneath it are omitted from the result, and the walk never visits them (so a
     huge excluded subtree is never even traversed).
   - a **file** → omit it; the walk continues normally with siblings.
2. **Include second, files only.** A surviving file is emitted only if it matches at least one
   include pattern. Directories are never filtered by `include` — the walk always descends into
   non-excluded directories (so nested files still get evaluated), and a surviving directory entry
   is always emitted regardless of whether its own name happens to match an include pattern
   (directory entries represent structure, not content, and restore needs them regardless).

With `include=["*"]` and `exclude=[]` (the effective default when neither is specified), every file
matches include and nothing is excluded — identical to today's unfiltered recursive backup.

## Data model changes

`ObjectFilter` gains `Include []string` and `Exclude []string` next to `Path`, at every layer:

**policy-server on-disk JSON** (`src/cmd/policy-server/policy.go`):
```go
type ObjectFilter struct {
    Path    string   `json:"path"`
    Include []string `json:"include,omitempty"`
    Exclude []string `json:"exclude,omitempty"`
}
```
`parsePolicyFile` does **no defaulting** — a policy file that omits `include`/`exclude` keeps them
nil/empty, unchanged, all the way through to the `brfs` exec line. It does validate every pattern
present as a syntactically valid `path.Match` glob, exactly like the existing
`ClientFilters.Hostnames` validation: an invalid pattern is a load error for that file (skip it,
keep serving the rest).

**gRPC proto** (`src/api/policyserver.proto`):
```proto
message ObjectFilter {
  string path = 1;
  repeated string include = 2;
  repeated string exclude = 3;
}
```
Regenerate `policyserver.pb.go` / `policyserver_grpc.pb.go`.

**policyclient's cache** (`src/cmd/policyclient/fetch.go`): `CachedPolicy.ObjectFilters` is today
`[]string`, a flattening of `ObjectFilter.Path` that was lossless only because `Path` was the only
field. It becomes:
```go
type ObjectFilter struct {
    Path    string   `json:"path"`
    Include []string `json:"include,omitempty"`
    Exclude []string `json:"exclude,omitempty"`
}
type CachedPolicy struct {
    // ...
    ObjectFilters []ObjectFilter `json:"object_filters"`
    // ...
}
```
This is a breaking change to the on-disk `policies-cache.json` format. No migration is needed: the
cache is agent-internal, fully repopulated every `policy-update` tick, and the project is
pre-release.

**agent's mirror struct** (`src/cmd/agent/backup.go`): `cachedPolicy.ObjectFilters` changes the same
way. agent duplicates this schema rather than importing policyclient's package (Go forbids
importing another command's `main` package) — same convention already in place.

## agent: backup task construction

`backupTasks` loops over `[]ObjectFilter` (was `[]string` paths) and builds each task's `Args`
with `--include`/`--exclude` appended only when non-empty — symmetric, no defaulting on agent's
side either:
```go
args := []string{filter.Path, "--destination", destination, "--job-id", jobID}
if len(filter.Include) > 0 {
    args = append(args, "--include", strings.Join(filter.Include, ","))
}
if len(filter.Exclude) > 0 {
    args = append(args, "--exclude", strings.Join(filter.Exclude, ","))
}
```
`backupTaskID`/`backupJobID`/`slug` stay keyed on `filter.Path` only — include/exclude affect which
files a run walks, not task identity or backoff tracking.

## brfs: CLI and internals

**CLI** (`src/cmd/brfs/arguments.go`): two new flags, each a comma-separated pattern list:
```go
cmd.Flags().StringVar(&includeFlag, "include", "*", "Comma-separated glob patterns; only matching files are backed up")
cmd.Flags().StringVar(&excludeFlag, "exclude", "", "Comma-separated glob patterns; matching files/directories are skipped")
```
Parsed by splitting on `,` into `Arguments.Include []string` / `Arguments.Exclude []string` (empty
string parses to an empty slice, not `[""]`). `--include`'s flag default of `"*"` is the *only*
place the project-wide default lives — it is not materialized anywhere upstream.

**Discover** (`src/workload/filesystem/fileslist.go`): signature changes to
`Discover(path string, include, exclude []string) (FilesList, error)`. The matching algorithm above
is implemented directly inside the `filepath.WalkDir` callback (basename match for slash-less
patterns, root-relative-path match otherwise, `filepath.SkipDir` for excluded directories).

**Dead code removal**: `FilesList.WithIncludes`/`WithExcludes` and the two methods they implement
from the `workload.BackupObjectsList` interface are unused anywhere today, and their shape (a
post-hoc filter over an already-fully-walked flat list) can't express subtree pruning — which this
design requires for directory excludes. Rather than adapt them, they're deleted; filtering happens
entirely inside `Discover`. `FileInfo.match` is replaced by the new walk-time matcher. If removing
both methods leaves `BackupObjectsList` an empty interface with no other purpose, remove the
interface too.

## Documentation

- **New: `docs/process/filesystem-backup.md`** — a short, high-level walk-through of the end-to-end
  flow: policy authored on `policy-server` (optional `include`/`exclude` per object filter) →
  `policyclient` caches it → `agent` derives a backup task per object filter and execs `brfs` with
  the resolved flags → `brfs` walks the source folder applying include/exclude → surviving files
  stream to `bwfs`. This is the one place explaining *why* the feature exists and how the pieces
  connect; it does not repeat wire-level detail already covered elsewhere.
- `docs/protocols/policy-server.md` — update the `ObjectFilter` message and `object_filters` prose.
- `docs/components/policy-server.md` — update the on-disk JSON schema description and note pattern
  validation at load time.
- `docs/components/policyclient.md` — update the cache JSON example to the new object shape.
- `docs/components/agent.md` — update the description of the `brfs` exec line to mention the
  conditional `--include`/`--exclude` flags.
- `docs/components/brfs.md` — add `--include`/`--exclude` to the Arguments/Flags table with the
  matching-semantics explanation and an example.
- `docs/ARCHITECTURE.md` — a one-line mention alongside the existing object-path description if
  needed; no new components or hops, so the diagram itself shouldn't need to change.
- `README.md` — add `docs/process/filesystem-backup.md` to the Documentation index.
- `CHANGELOG.md` — one dated entry before merging to `main`.
- `demo/policy-server/policies/webserver-backup.json` — add an `exclude` example so the feature is
  visible when running the demo.

## Testing plan

- **`filesystem` package**: basename-pattern matching at depth; relative-path pattern matching;
  directory pruning via `SkipDir` (nothing beneath an excluded directory appears in the result, and
  the walk doesn't descend into it — e.g. a subdirectory that would error if visited); include
  as a files-only whitelist; the no-patterns-given case identical to today's unfiltered walk.
- **policy-server** (`policy_test.go`): loading a policy JSON with `include`/`exclude` present vs.
  omitted; rejecting a file with an invalid glob in either field (mirrors the existing
  hostname-pattern test).
- **policyclient** and **agent** (`policy_test.go`): round-tripping the new `ObjectFilter` shape
  through cache JSON; `backupTasks` building the correct conditional `--include`/`--exclude` args.
- **brfs** (`arguments_test.go` if present): comma-split flag parsing, including the
  empty-string-means-empty-slice edge case.

## Out of scope

- No recursive `**` glob support — deliberately staying within `filepath.Match`'s syntax.
- No migration path for old `policies-cache.json` files.
- No new e2e/demo scenario beyond the one static `exclude` example added to an existing demo
  policy file.
