# Design: parent directory and short filename fields for the catalog

**Date:** 2026-08-07
**Status:** Approved for planning

## Problem

`ListEntries` (`src/api/catalog.proto:29-42`) already supports a `pattern` substring filter against
`object_id` (which embeds the full path), but there's no way to browse "what's under this directory" —
a user has to know a path fragment up front. `EntryRecord` (`src/storage/catalog/models.go`) has no
column that isolates a file's containing directory or its bare filename either; both live only inside
the gob-encoded `Metadata` blob, decoded per-row at read time in `toProtoEntry`
(`src/cmd/catalog/server.go:100-118`) into `Entry.Path`, with no directory/filename split.

This mirrors `source_host`'s history: it also used to require decoding `Metadata` per row until
`decodeSourceHost` (`cmd/catalog/server.go:63-69`) started computing it once at sync time into an
indexed column. This design applies the same pattern to two new derived fields — `parent_directory`
(filterable) and `short_filename` (display-only) — and adds a third facet dimension,
`ListDirectoryFacets`, alongside the existing `ListClientFacets`/`ListJobFacets`
(`2026-08-05-catalog-filter-panels-design.md`).

## Approach

### Path splitting: correct per-platform, not per-runtime-OS

`catalog` always runs on Linux, but `Metadata` is recorded verbatim by whichever OS backed the file up
— `fileinfo_windows.go`/`fileinfo_linux.go` both store the raw `os.Lstat` path unmodified, so a
Windows-origin path is fully backslash-separated (`C:\Users\alice\file.txt`) and a Unix-origin path is
fully forward-slash-separated (`/var/lib/dbdata/data.db`). `path/filepath` can't be used directly: it
splits on the *build* platform's separator, which is always `/` here regardless of which OS a given
row's path came from.

Naively splitting on "whichever of `/` or `\` appears last" is also wrong: `\` is a legal Unix filename
character (rare, but valid), so a Unix path like `/var/log/weird\file.txt` would be mis-split into
parent `/var/log/weird`, filename `file.txt` instead of keeping `weird\file.txt` intact.

The fix is to decide separator style from the path's own shape, not from either the runtime OS or a
blind "last separator wins" scan:

```go
// splitPath derives (parentDir, shortFilename) from a stored path, choosing
// separator style from the path's own shape (leading "/" vs a drive-letter
// or UNC prefix) rather than the build platform's os.PathSeparator or a
// naive "last of either / or \" scan -- the latter mis-splits a Unix path
// containing a literal backslash in its filename (legal on Unix, illegal on
// Windows). Root paths keep a trailing separator ("/" , "C:\") rather than
// collapsing to "" -- "" is reserved elsewhere in this package to mean
// "unknown" (a decode failure), and a real root-level file must not read as
// unknown and get silently dropped by ListDirectoryFacets.
func splitPath(p string) (dir, base string) {
	if p == "" {
		return "", ""
	}
	sep := byte('/')
	if isWindowsStyle(p) {
		sep = '\\'
	}

	idx := strings.LastIndexByte(p, sep)
	if idx < 0 {
		return "", p // no separator: whole string is the name, no known parent
	}
	dir, base = p[:idx], p[idx+1:]

	if base == "" {
		return splitPath(p[:idx]) // tolerate a trailing separator, strip and retry
	}
	if dir == "" {
		dir = string(sep) // unix root: "/file.txt" -> "/"
	} else if isDriveRoot(dir) {
		dir += string(sep) // "C:" -> "C:\"
	}
	return dir, base
}

// isWindowsStyle reports whether p is UNC ("\\server\share\...") or
// drive-letter-rooted ("C:\..." / "C:/..."). Anything else -- including a
// bare relative path -- is treated as Unix-style, matching this system's
// object_filters convention that backup source paths are always absolute
// (see docs/api/rest-v1.md's "/var/www" vs "C:\..." examples).
func isWindowsStyle(p string) bool
func isDriveRoot(s string) bool // len(s)==2 && s[1]==':' && ascii letter
```

Verified against representative cases (Unix nested/root, a Unix filename containing a literal
backslash, Windows nested/drive-root, UNC nested/minimal, no-separator input, and malformed trailing
separator) — see splitting behavior table below.

| Input | `parent_directory` | `short_filename` |
|---|---|---|
| `/var/lib/dbdata/data.db` | `/var/lib/dbdata` | `data.db` |
| `/data.db` | `/` | `data.db` |
| `/var/log/weird\file.txt` | `/var/log` | `weird\file.txt` |
| `C:\Users\alice\Documents\file.txt` | `C:\Users\alice\Documents` | `file.txt` |
| `C:\file.txt` | `C:\` | `file.txt` |
| `\\server\share\folder\file.txt` | `\\server\share\folder` | `file.txt` |
| `data.db` (no separator) | `""` | `data.db` |

A bare no-separator input is not expected in practice (backup source paths are always absolute), and
falls into the same `""`-means-unknown bucket as a genuine `Metadata` decode failure — acceptable,
never user-visible today.

### Data model (`src/storage/catalog/models.go`)

```go
type EntryRecord struct {
	// ...existing fields...
	ParentDirectory string `gorm:"index"` // computed at sync time; filterable
	ShortFilename   string                // computed at sync time; display-only, not indexed
}
```

`db.AutoMigrate` (`db.go:41`) adds both columns on next startup, same as every prior schema change to
this table. **No backfill**: rows already synced before this change ships keep both columns blank until
superseded by a fresh sync of the same `(store_node, job_id, object_id)` — consistent with how
`source_host` was introduced with no retroactive decode pass.

### Sync-time computation (`src/cmd/catalog/server.go`)

A new helper alongside `decodeSourceHost`, same "never fail the batch" tolerance for a bad row:

```go
// decodePathParts extracts parent_directory/short_filename from an entry's
// Metadata blob, decoded once at sync time so ListEntries/ListDirectoryFacets
// can filter/group on plain indexed columns instead of decoding Metadata
// per row (see decodeSourceHost, same rationale). A decode failure yields
// ("", "") rather than failing the whole batch.
func decodePathParts(metadata []byte) (parentDir, shortName string) {
	fi, err := filesystem.DecodeFileInfo(metadata)
	if err != nil {
		return "", ""
	}
	return splitPath(fi.Path())
}
```

`SyncFileVersions` calls this alongside the existing `decodeSourceHost(e.GetMetadata())` when building
each `catalogstore.Entry`.

### Proto (`src/api/catalog.proto`)

```proto
message Entry {
  // ...existing fields 1-14...
  string parent_directory = 15; // computed server-side from Metadata at sync time
  string short_filename   = 16; // computed server-side from Metadata at sync time; display only
}

message ListEntriesRequest {
  // ...existing fields 1-9...
  repeated string parent_directories = 10; // OR-matched; empty = no filter
}

message ListFacetsRequest {
  // ...existing fields 1-5...
  repeated string parent_directories = 6; // ignored by ListDirectoryFacets (own dimension)
}

service CatalogService {
  // ...existing rpcs...
  rpc ListDirectoryFacets(ListFacetsRequest) returns (ListFacetsResponse);
}
```

`parent_directories` on `ListEntriesRequest` follows the `source_hosts`/`job_names` precedent: OR-matched,
AND-combined with every other active filter. `ListDirectoryFacets` reuses the existing
`ListFacetsRequest`/`ListFacetsResponse`/`Facet` messages — same shape as the other two facet RPCs, only
the grouping column differs.

### Store layer (`src/storage/catalog/store.go`)

`ListEntriesFilter` and `FacetFilter` each gain `ParentDirectories []string`. `ListEntries` adds a
`parent_directory IN (?)` clause when non-empty, additive to its existing conditions.

Extending the three-facet-RPC "apply every other dimension, ignore your own" rule
(`2026-08-05-catalog-filter-panels-design.md`'s cross-filter contract) from two dimensions to three:

- `ListClientFacets` (groups by `source_host`): now also applies `ParentDirectories`, in addition to
  the existing `JobNames`.
- `ListJobFacets` (groups by policy name): now also applies `ParentDirectories`, in addition to the
  existing `SourceHosts`.
- `ListDirectoryFacets` (new, groups by `parent_directory`): applies `SourceHosts` + `JobNames`, ignores
  `ParentDirectories`. Follows the same Go-side aggregation as `ListClientFacets`
  (`store.go:209-247`) — scan `(parent_directory, received_at)` rows, aggregate in a map — and drops
  rows where `parent_directory == ""`, mirroring `ListClientFacets`'s drop of an empty `source_host`
  (both represent a sync-time decode failure, never a real value worth surfacing as a facet).

### gRPC server (`src/cmd/catalog/server.go`)

`ListEntries` passes `req.GetParentDirectories()` into `ListEntriesFilter` alongside the existing
fields. A new `ListDirectoryFacets` handler, same `ListFacetsRequest` → `FacetFilter` → store call →
`ListFacetsResponse` shape as `ListClientFacets`/`ListJobFacets`.

### api-server (`src/cmd/api-server/catalog.go`, `server.go`)

`entryDTO` gains `parent_directory`/`short_filename` JSON fields, populated in `toEntryDTO` from
`e.GetParentDirectory()`/`e.GetShortFilename()`.

`handleListCatalog` gains a `parent_directories` comma-separated query param, parsed with the existing
`splitCommaParam` and passed into `pb.ListEntriesRequest`.

`handleListCatalogClients` and `handleListCatalogJobs` each gain `parent_directories` as an additional
narrowing param (symmetric with how `/clients` already takes `job_names` and `/jobs` already takes
`source_hosts`).

A new handler, `handleListCatalogDirectories` → `GET /api/v1/catalog/directories`, following
`handleListCatalogClients`/`handleListCatalogJobs`'s shape exactly: unpaginated `{"data": [...]}`,
accepting `received_after`, `received_before`, `pattern`, `source_hosts`, and `job_names` as narrowing
params (no `parent_directories` param — own dimension).

`catalogQueryClient` (`server.go:37`) gains `ListDirectoryFacets` to its method set.

## Out of scope

- Web frontend UI — no directory filter control is added to `CatalogView.vue` in this pass; the
  backend filter/facet endpoint is designed to support one later without further API changes.
- Making `short_filename` a filter dimension of its own — it's stored and returned for display only;
  the existing `pattern` substring filter already covers filename search.
- Prefix/subtree directory matching (e.g. `/var/lib` also matching files under `/var/lib/dbdata`) —
  `parent_directory` is an exact match against a file's *immediate* containing directory only, a
  file-manager-style "browse this folder" filter, not a recursive one.
- Backfilling `parent_directory`/`short_filename` for entries already synced before this change ships
  — left blank until superseded by a fresh sync, matching how `source_host` was introduced.
- Any change to `object_id`'s existing `pattern` substring filter — untouched, still the only
  filename-*search* (as opposed to filename-*filter*) mechanism.

## Testing plan

- **`splitPath` unit test** (table-driven): every row in the splitting-behavior table above, plus an
  empty-string input.
- **`storage/catalog/store_test.go`**: `ParentDirectories` filtering on `ListEntries`; new
  `ListDirectoryFacets` — empty-`parent_directory` rows dropped, narrowing by `SourceHosts`/`JobNames`,
  and that it ignores a `ParentDirectories` value passed on its own request. `ListClientFacets`/
  `ListJobFacets` extended to also cover `ParentDirectories` narrowing.
- **`cmd/catalog/server_test.go`**: `SyncFileVersions` populates `parent_directory`/`short_filename`
  from valid metadata; malformed metadata yields both empty without failing the batch (mirrors the
  existing `decodeSourceHost` failure-tolerance test). `ListEntries`/`ListDirectoryFacets` handlers
  translate request fields correctly.
- **`cmd/api-server/catalog_test.go`**: `parent_directories` parsed and passed through on `/catalog`,
  `/catalog/clients`, `/catalog/jobs`; new `/catalog/directories` handler wired, DTO-mapped, and
  rejects the same malformed `received_after`/`received_before` inputs the existing facet handlers do.

## Documentation

- `docs/protocols/catalog-sync.md` — document `Entry.parent_directory`/`short_filename`,
  `ListEntriesRequest.parent_directories`, the new `ListDirectoryFacets` RPC, and the extended
  three-way "applies every other dimension, ignores your own" cross-filter contract.
- `docs/components/catalog.md` — describe `parent_directory`/`short_filename` as sync-time-computed
  fields (same section as `source_host`'s existing writeup), and the new directory filter/facet
  capability.
- `docs/api/rest-v1.md` — new `GET /api/v1/catalog/directories` endpoint; updated `/catalog`,
  `/catalog/clients`, `/catalog/jobs` param tables with `parent_directories`; `parent_directory`/
  `short_filename` added to the `/catalog` response example.
- `docs/components/api-server.md` — no structural change to the component description; the endpoint
  list already defers to `rest-v1.md` for details.
- `README.md` — no change expected; only touch if the catalog-sync protocol doc's summary line changes.
- `docs/ARCHITECTURE.md` — no changes; no new component or topology/data-flow change, only new
  fields/filters on the existing `CatalogService`.
- `CHANGELOG.md` — one dated entry: the catalog gains `parent_directory`/`short_filename` fields,
  computed once at sync time, plus a `parent_directories` filter and a new `ListDirectoryFacets`
  aggregate endpoint mirroring the existing client/job facets — additive to the existing `/catalog`
  contract, no breaking changes.
