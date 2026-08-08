# Design: directory-browsing UI for the catalog

**Date:** 2026-08-08
**Status:** Approved for planning

## Problem

`CatalogView.vue` shows every matching file version as one flat, ungrouped table — a user has to
already know a path fragment and type it into "Path contains…" to narrow the list. The
`2026-08-07-catalog-parent-directory-design.md` work (already merged into this branch) added
`parent_directory`/`short_filename` columns and a `ListDirectoryFacets`/`GET /catalog/directories`
facet endpoint, but explicitly deferred the frontend: *"no directory filter control is added to
`CatalogView.vue` in this pass; the backend... is designed to support one later without further API
changes."* This is that later pass.

The goal: browse the catalog like a file manager — start at a root (or, for Windows-sourced entries,
a list of drives), click a folder to drill into it, see a breadcrumb path bar above the table for fast
navigation back up. A folder can contain both files and subfolders at once — both must show, folders
first — since a real directory tree frequently has files living alongside subdirectories (e.g.
`/var/log/app.log` next to `/var/log/nginx/`).

## Why the existing `ListDirectoryFacets` endpoint isn't enough

`ListDirectoryFacets` groups by `parent_directory`, which is a file's *exact immediate* containing
directory — not a recursive/prefix relationship. Its response is a flat, unpaginated list of every
distinct immediate-parent directory that currently has matching files, e.g. `/var/lib/dbdata`,
`/var/log`, `/home/alice/Documents/Projects/2024/reports` all side by side, with no notion of
`/var/lib/dbdata` living under `/var`.

Building a true multi-level tree by decomposing those strings client-side runs into a second problem:
backups are rarely a full-disk snapshot. If `/var/lib/dbdata` is the only thing ever backed up under
`/var`, a naive one-segment-per-level tree makes a user click through empty pass-through folders
(`/` → `var` → `lib` → `dbdata`) to reach the one real directory.

The fix used here is the same one this codebase already used for `source_host` and
`parent_directory` itself: stop deriving the structure per-request and persist it at sync time.

## Approach

### New table: `catalog_directories`

`src/storage/catalog/models.go` gains a second model:

```go
// DirectoryRecord is one directory that has ever appeared as an ancestor of
// a synced file's ParentDirectory -- not just directories that directly
// contain a file. Computed once at sync time (see decodeDirectoryAncestors
// in cmd/catalog/server.go) by walking each file's ParentDirectory chain
// with splitPath, the same helper that produced ParentDirectory itself.
// Existence here is intentionally filter-independent: it reflects
// everything ever backed up, not what matches the current date/host/job
// filters (see ListDirectoryChildren below for why).
type DirectoryRecord struct {
	Path       string `gorm:"uniqueIndex"`
	ParentPath string `gorm:"index"` // "" for a true root: "/", "C:\", "\\server\share\"
	Depth      int
}
```

`db.AutoMigrate` (`db.go:41`) adds `&DirectoryRecord{}` alongside `&EntryRecord{}`.

Row count is bounded by the number of *distinct directories ever seen* across the whole catalog, not
by file count — stays small and stable as the file table grows, unlike a per-request scan of
`parent_directory` values.

### Sync-time computation (`src/cmd/catalog/server.go`)

Alongside the existing `decodePathParts` call in `SyncFileVersions` (server.go:36), a new helper walks
a file's `ParentDirectory` up to its root, one `splitPath` call per level, collecting every ancestor:

```go
// decodeDirectoryAncestors walks parentDir's ancestor chain (via splitPath,
// the same shape-detecting split ParentDirectory itself was built from) and
// returns one (path, parentPath, depth) tuple per level, root-first. A
// blank parentDir (sync-time decode failure -- see decodePathParts) yields
// no rows: an unknown location can't be placed in the tree.
func decodeDirectoryAncestors(parentDir string) []catalogstore.DirectoryAncestor
```

`SyncFileVersions` collects ancestors for every entry in the batch into a per-batch
`map[string]catalogstore.DirectoryAncestor` (deduping repeat directories within the same batch — many
files usually share a folder) before calling a new `EnsureDirectories` store method, which upserts with
`ON CONFLICT (path) DO NOTHING` (pure structure, nothing to update on conflict; idempotent across
repeated syncs of files under an already-known folder).

### Proto (`src/api/catalog.proto`)

```proto
service CatalogService {
  // ...existing rpcs...
  rpc ListDirectoryChildren(ListDirectoryChildrenRequest) returns (ListDirectoryChildrenResponse);
}

message ListDirectoryChildrenRequest {
  string parent_path     = 1; // "" = true roots ("/", each distinct drive/UNC root)
  int64  received_after  = 2;
  int64  received_before = 3;
  repeated string source_hosts = 4;
  repeated string job_names    = 5;
}

message DirectoryChild {
  string path       = 1; // full path, e.g. "/var/lib"
  string name       = 2; // short display label, e.g. "lib"
  int64  file_count  = 3; // direct files under path matching the current date/host/job filters
  int64  last_seen   = 4; // unix seconds, max(received_at) among those files; 0 if file_count == 0
  bool   has_children = 5; // true if catalog_directories has any row with parent_path == path
}

message ListDirectoryChildrenResponse {
  repeated DirectoryChild children = 1;
}
```

No `pattern` field: pattern search is a separate flat mode (see Frontend below), never combined with
directory browsing.

### Store layer (`src/storage/catalog/store.go`)

```go
// ListDirectoryChildren returns every directory whose ParentPath equals
// parentPath -- filter-independent, deliberately: existence answers "was
// this ever backed up," not "does it currently match." Making existence
// filter-aware would require knowing whether *any* descendant anywhere in
// a folder's subtree matches, which is the same recursive-subtree
// question 2026-08-07's design explicitly ruled out for ParentDirectories
// filtering. FileCount/LastSeen on each child, by contrast, only need a
// direct (non-recursive) parent_directory match, so those stay
// filter-aware -- one grouped query, no per-child N+1.
func (s *Store) ListDirectoryChildren(parentPath string, filter FacetFilter) ([]DirectoryChild, error)
```

Implementation: (1) `SELECT path, depth FROM catalog_directories WHERE parent_path = ?` for the
child list itself; (2) one `SELECT parent_directory, received_at FROM entries WHERE parent_directory IN
(?) AND <filter.applyCommon minus Pattern> GROUP BY ...`-shaped scan (same Go-side aggregation as
`ListClientFacets`/`ListDirectoryFacets`) to get `file_count`/`last_seen` per child, keyed by path; (3)
`SELECT DISTINCT parent_path FROM catalog_directories WHERE parent_path IN (?)` to compute
`has_children` per child in one pass. `FacetFilter.Pattern` is unused here (browsing and pattern search
never combine).

### gRPC server (`src/cmd/catalog/server.go`)

New `ListDirectoryChildren` handler, same `Request → filter → store call → Response` shape as the
existing three facet handlers.

### api-server (`src/cmd/api-server/catalog.go`, `server.go`)

`GET /api/v1/catalog/directories/children?parent_path=&received_after=&received_before=&source_hosts=&job_names=`
→ `{"data": [{"path", "name", "file_count", "last_seen", "has_children"}]}`, following the existing
facet-handler shape (`splitCommaParam`, DTO mapping). `catalogQueryClient` (server.go:37) gains
`ListDirectoryChildren` to its method set.

### Frontend (`web/src/`)

- **Store (`stores/catalog.js`)**: new state — `currentPath` (`null` = root listing), `directoryChildren`,
  plus the existing `entries`/`search()` now scoped to `parent_directories: [currentPath]` when
  `currentPath` is set. `navigateTo(path)` sets `currentPath` and re-fetches both the directory's
  children and its direct files. `navigateHome()` sets `currentPath = null`.
- **Table (`CatalogView.vue`)**: one `DataTable`. Rows = directory children (icon, `name`, `file_count`,
  `last_seen` — reusing `FacetPanel`'s column shape) sorted alphabetically, concatenated before file
  rows (existing columns from `groupEntriesByFile`, but the "Path" column renders `short_filename`
  instead of the full `path` — the path bar already shows location, so the full path is redundant
  noise here). Per-column sort is disabled in this view so folder rows stay pinned above file rows;
  within each group, order is fixed — folders alphabetical by `name`, files in the order `ListEntries`
  already returns them (newest first by ID, same as today's flat view).
- **Path bar**: new `DirectoryPathBar.vue`, rendered above the table when browsing (hidden in pattern-
  search mode). "Home" (always → root listing) followed by one clickable crumb per ancestor directory
  from the chosen root down to `currentPath`, using each directory's short `name`. Every crumb except
  the last is clickable, jumping straight to that `path` via `navigateTo`.
- **Pattern search**: typing in "Path contains…" (unchanged control) clears `currentPath` and switches
  to today's flat, ungrouped, cross-directory `entries` list — the path bar and folder rows disappear.
  Clearing the pattern restores whichever folder was last being browsed (or root, if none).
- **Existing facet chips** (date range, clients, jobs): unchanged, apply in both modes the same way
  `ListDirectoryChildren`'s filter params do.

## Out of scope

- Making folder *existence* filter-aware (hiding a folder with zero currently-matching descendants) —
  would require recursive subtree awareness, ruled out for the same reason 2026-08-07's design ruled
  out recursive `parent_directory` matching.
- Backfilling `catalog_directories` for files synced before this change ships — same precedent as
  `parent_directory`/`short_filename`: populated only on a fresh sync of the same
  `(store_node, job_id, object_id)`. No backward-compatibility handling needed since the demo catalog
  will be fully resynced.
- Pagination of `ListDirectoryChildren` — a single directory's immediate child count is expected to be
  small (real subfolders, not files); revisit if that proves wrong in practice.
- Any change to `ListDirectoryFacets`/`GET /catalog/directories` — stays exactly as shipped, unused by
  this UI, available for a future flat directory-filter panel if ever needed.
- Recursive rollup counts (e.g. "1,204 files somewhere under this folder" shown before expanding) —
  each child only shows its own direct `file_count`; `has_children` is a plain boolean, not a count.

## Testing plan

- **`storage/catalog/store_test.go`**: `EnsureDirectories` upserts ancestors idempotently across
  repeated calls; `ListDirectoryChildren` returns correct children for a given `parent_path`, including
  the true-root case (`parent_path == ""`) with mixed Unix/Windows data; `file_count`/`last_seen`
  respect `received_after`/`received_before`/`SourceHosts`/`JobNames`; `has_children` is correct for
  both leaf and branch children; a child with `file_count == 0` under current filters but real
  subfolders still appears.
- **`cmd/catalog/server_test.go`**: `SyncFileVersions` populates `catalog_directories` with every
  ancestor of a synced file's `ParentDirectory`, deduped within a batch; a blank `ParentDirectory`
  (decode failure) produces no directory rows; `ListDirectoryChildren` handler translates request
  fields correctly.
- **`cmd/api-server/catalog_test.go`**: new `/catalog/directories/children` handler wired, DTO-mapped,
  `parent_path` passed through (including omitted = root), rejects malformed
  `received_after`/`received_before` like the existing facet handlers.
- **`web/src/stores/catalog.spec.js`**: `navigateTo`/`navigateHome` update `currentPath` and trigger the
  right fetches; entering a pattern clears `currentPath` and restores it on clear.
- **`web/src/views/CatalogView.spec.js`**: folder rows render above file rows and are click-navigable;
  path bar renders the right crumbs and each is clickable except the last; pattern search hides the
  path bar and folder rows.
- **`web/src/components/catalog/DirectoryPathBar.spec.js`** (new): crumb generation from a path, root
  case, click-to-navigate emits.

## Documentation

- `docs/protocols/catalog-sync.md` — document `catalog_directories`, `DirectoryRecord`, the new
  `ListDirectoryChildren` RPC, and why its existence check is filter-independent while its counts are
  filter-aware.
- `docs/components/catalog.md` — describe the directory-browsing capability and the new table, same
  section style as `parent_directory`'s existing writeup.
- `docs/api/rest-v1.md` — new `GET /api/v1/catalog/directories/children` endpoint, request/response
  shape, and a note on how it differs from the existing `/catalog/directories` facet endpoint.
- `docs/ARCHITECTURE.md` — no topology change (still the same `CatalogService`/store), but note the new
  table if the doc enumerates catalog storage schema.
- `README.md` — no change expected unless the catalog-sync protocol doc's summary line changes.
- `CHANGELOG.md` — one dated entry: the catalog UI gains file-manager-style directory browsing (root
  or Windows-drive listing, click-to-drill, breadcrumb path bar), backed by a new sync-time-computed
  directory hierarchy table and `ListDirectoryChildren` RPC/endpoint.
