# Catalog Parent Directory & Short Filename Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add sync-time-computed `parent_directory` (filterable) and `short_filename` (display-only) fields to the catalog, plus a new `ListDirectoryFacets` aggregate RPC mirroring the existing client/job facets, so a caller can filter/browse "what's in this directory" without decoding `Metadata` per row.

**Architecture:** `catalog` already derives `source_host` once at sync time from each entry's gob-encoded `Metadata` blob into a plain indexed column (`decodeSourceHost`, `cmd/catalog/server.go`). This design applies the identical pattern to two new derived fields, using a new `splitPath` helper that picks separator style (`/` vs `\`) from a path's own shape (leading `/`, drive-letter, or UNC prefix) rather than the build platform's separator or a naive "last of either character" scan — the latter mis-splits a Unix path containing a literal backslash in its filename. `parent_directory` becomes a third facet/filter dimension alongside `source_host`/`job_name`, extending the existing three-RPC (`ListClientFacets`/`ListJobFacets`) "apply every other dimension, ignore your own" cross-filter contract to a fourth RPC, `ListDirectoryFacets`.

**Tech Stack:** Go (GORM + `modernc.org/sqlite`), gRPC/protobuf, `testify`.

## Global Constraints

- Design source of truth: `docs/superpowers/specs/2026-08-07-catalog-parent-directory-design.md`.
- TDD throughout: write the failing test before the implementation for every behavior change, per `superpowers:test-driven-development`.
- Proto changes require running `make proto` from the repo root and committing the regenerated `src/api/catalog.pb.go`/`catalog_grpc.pb.go` alongside the `.proto` edit (per `.claude/CLAUDE.md`'s gRPC Protocol Changes rule).
- Per `.claude/CLAUDE.md`: update `docs/protocols/catalog-sync.md`, `docs/components/catalog.md`, and `docs/api/rest-v1.md` before this feature is considered complete (Task 8), and add a `CHANGELOG.md` entry before merging to `main`.
- No backfill for entries already synced before this change ships — `parent_directory`/`short_filename` stay blank on old rows until superseded by a fresh sync (per the design's explicit scope decision). No task in this plan writes a backfill/migration script.
- Web frontend is explicitly out of scope (per the design) — no task in this plan touches `web/`.
- Every task ends with a passing test run and a commit.

---

## Task 1: `splitPath` — cross-platform path-splitting helper

**Files:**
- Create: `src/cmd/catalog/pathsplit.go`
- Test: Create `src/cmd/catalog/pathsplit_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func splitPath(p string) (dir, base string)`, package `main` (`src/cmd/catalog`). Consumed by Task 6.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/catalog/pathsplit_test.go`:

```go
package main

import "testing"

func TestSplitPath(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		wantDir  string
		wantBase string
	}{
		{"unix nested", "/var/lib/dbdata/data.db", "/var/lib/dbdata", "data.db"},
		{"unix root", "/data.db", "/", "data.db"},
		{"unix filename containing a literal backslash", `/var/log/weird\file.txt`, "/var/log", `weird\file.txt`},
		{"windows nested", `C:\Users\alice\Documents\file.txt`, `C:\Users\alice\Documents`, "file.txt"},
		{"windows drive root", `C:\file.txt`, `C:\`, "file.txt"},
		{"unc nested", `\\server\share\folder\file.txt`, `\\server\share\folder`, "file.txt"},
		{"unc minimal", `\\server\share\file.txt`, `\\server\share`, "file.txt"},
		{"no separator", "data.db", "", "data.db"},
		{"empty string", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, base := splitPath(c.path)
			if dir != c.wantDir || base != c.wantBase {
				t.Errorf("splitPath(%q) = (%q, %q), want (%q, %q)", c.path, dir, base, c.wantDir, c.wantBase)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/catalog/... -run TestSplitPath -v`
Expected: FAIL — `splitPath` undefined, compile error.

- [ ] **Step 3: Implement**

Create `src/cmd/catalog/pathsplit.go`:

```go
package main

import "strings"

// splitPath derives (parentDir, shortFilename) from a stored path, choosing
// separator style from the path's own shape (leading "/" vs a drive-letter
// or UNC prefix) rather than the build platform's os.PathSeparator or a
// naive "last of either / or \" scan -- the latter mis-splits a Unix path
// containing a literal backslash in its filename (legal on Unix, illegal on
// Windows). catalog always runs on Linux, but Metadata is recorded verbatim
// by whichever OS backed the file up (fileinfo_windows.go/fileinfo_linux.go
// both store the raw os.Lstat path unmodified), so a Windows-origin path is
// fully backslash-separated and a Unix-origin path is fully
// forward-slash-separated -- path/filepath can't be used directly since it
// always splits on the build platform's separator.
//
// Root paths keep a trailing separator ("/", "C:\") rather than collapsing
// to "" -- "" is reserved elsewhere in this package to mean "unknown" (a
// Metadata decode failure), and a real root-level file must not read as
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
func isWindowsStyle(p string) bool {
	if strings.HasPrefix(p, `\\`) {
		return true
	}
	return len(p) >= 2 && isDriveRoot(p[:2])
}

// isDriveRoot reports whether s is exactly a two-character drive letter
// prefix, e.g. "C:".
func isDriveRoot(s string) bool {
	return len(s) == 2 && s[1] == ':' && isASCIILetter(s[0])
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd src && go test ./cmd/catalog/... -run TestSplitPath -v`
Expected: PASS, all 9 subtests.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalog/pathsplit.go src/cmd/catalog/pathsplit_test.go
git commit -m "feat(catalog): add splitPath, a cross-platform-aware path-splitting helper"
```

---

## Task 2: Extend `catalog.proto` with `parent_directory`/`short_filename` fields and `ListDirectoryFacets`

**Files:**
- Modify: `src/api/catalog.proto:14-84`
- Generated (via `make proto`, do not hand-edit): `src/api/catalog.pb.go`, `src/api/catalog_grpc.pb.go`

**Interfaces:**
- Consumes: nothing new from other tasks.
- Produces: `pb.Entry` gains `ParentDirectory`, `ShortFilename string` (fields 15-16); `pb.ListEntriesRequest` gains `ParentDirectories []string` (field 10); `pb.ListFacetsRequest` gains `ParentDirectories []string` (field 6); new `pb.CatalogServiceClient`/`CatalogServiceServer` method `ListDirectoryFacets`. Consumed by Tasks 3-7.

- [ ] **Step 1: Edit the proto file**

In `src/api/catalog.proto`, replace the `Entry` message with:

```proto
message Entry {
  int64  id                = 1;
  string store_host        = 2;
  string job_id            = 3;
  string object_id         = 4;
  int64  ctime             = 5;
  int64  store_created_at  = 6;
  int64  received_at       = 7;
  // decoded server-side from the stored Metadata blob:
  string path      = 8;
  int64  size       = 9;
  string mode      = 10; // e.g. "-rw-r--r--", from fs.FileMode.String()
  uint32 owner     = 11; // Unix UID (or Windows SID hash) — numeric, no name resolution
  uint32 group     = 12; // Unix GID (or Windows SID hash) — numeric, no name resolution
  int64  mod_time   = 13;
  string source_host = 14; // the real originating (backed-up) host, derived from Metadata at sync time
  string parent_directory = 15; // the file's immediate containing directory, derived from Metadata at sync time
  string short_filename   = 16; // the file's bare name (no directory), derived from Metadata at sync time; display only
}
```

Replace the `ListEntriesRequest` message with:

```proto
message ListEntriesRequest {
  string store_host     = 1; // exact match against the sending bwfs node's identity; empty = all
  string pattern        = 2; // substring match against object_id; empty = no filter
  int32  limit           = 3; // 1..500, default 100
  int64  starting_after  = 4; // last-seen entry ID from a previous page; 0 = first page
  string source_host    = 5; // exact match against the real originating (backed-up) host; empty = all
  // New, additive -- old singular fields (1-5) keep their current exact-match
  // behavior; the new repeated fields are OR-matched, combined with
  // everything else via AND, same as the old fields.
  int64  received_after  = 6; // unix seconds; 0 = no lower bound
  int64  received_before = 7; // unix seconds; 0 = no upper bound
  repeated string source_hosts = 8; // OR-matched; empty = no filter
  repeated string job_names    = 9; // OR-matched against the policy name embedded in job_id
  repeated string parent_directories = 10; // OR-matched against the exact immediate containing directory; empty = no filter
}
```

Replace the `ListFacetsRequest` message with:

```proto
message ListFacetsRequest {
  int64  received_after  = 1;
  int64  received_before = 2;
  string pattern         = 3;
  repeated string source_hosts = 4; // ignored by ListClientFacets (own dimension)
  repeated string job_names    = 5; // ignored by ListJobFacets (own dimension)
  repeated string parent_directories = 6; // ignored by ListDirectoryFacets (own dimension)
}
```

Replace the `service CatalogService` block with:

```proto
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
  rpc ListClientFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListJobFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListDirectoryFacets(ListFacetsRequest) returns (ListFacetsResponse);
}
```

- [ ] **Step 2: Regenerate the Go protobuf code**

Run: `make proto`
Expected: `Protobuf code generated in src/api/` with no errors; `git status` shows `src/api/catalog.pb.go` and `src/api/catalog_grpc.pb.go` modified.

- [ ] **Step 3: Verify the whole module still builds**

Run: `cd src && go build ./...`
Expected: exits 0. Nothing references the new fields/RPC yet, so this only proves the regenerated code itself is well-formed and nothing existing broke.

- [ ] **Step 4: Commit**

```bash
git add src/api/catalog.proto src/api/catalog.pb.go src/api/catalog_grpc.pb.go
git commit -m "feat(api): add parent_directory/short_filename fields and ListDirectoryFacets RPC to catalog.proto"
```

---

## Task 3: Store layer — schema and persistence for `ParentDirectory`/`ShortFilename`

**Files:**
- Modify: `src/storage/catalog/models.go:11-25`
- Modify: `src/storage/catalog/store.go:26-64`
- Test: `src/storage/catalog/store_test.go`

**Interfaces:**
- Consumes: nothing new from other tasks.
- Produces: `EntryRecord` gains `ParentDirectory string` (indexed), `ShortFilename string`. `catalog.Entry` (the store-layer DTO) gains the same two fields, mapped through by `EnsureEntries`. Consumed by Tasks 4-6.

- [ ] **Step 1: Write the failing test**

Append to `src/storage/catalog/store_test.go`:

```go
func TestEnsureEntries_PersistsParentDirectoryAndShortFilename(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "/var/lib/dbdata", entries[0].ParentDirectory)
	assert.Equal(t, "data.db", entries[0].ShortFilename)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./storage/catalog/... -run TestEnsureEntries_PersistsParentDirectoryAndShortFilename -v`
Expected: FAIL — `Entry`/`EntryRecord` has no field `ParentDirectory`/`ShortFilename`, compile error.

- [ ] **Step 3: Implement**

In `src/storage/catalog/models.go`, add two fields to `EntryRecord` (after the existing `SourceHost` field, before `ReceivedAt`):

```go
	// SourceHost is the real originating (backed-up) host, decoded from
	// Metadata at sync time -- distinct from StoreNode, the bwfs node that
	// sent the batch. Indexed so ListEntries can filter on it directly.
	SourceHost string `gorm:"index"`
	// ParentDirectory is the file's immediate containing directory, and
	// ShortFilename its bare name, both derived from Metadata at sync time
	// the same way SourceHost is (see cmd/catalog/server.go's
	// decodePathParts). ParentDirectory is indexed for filtering;
	// ShortFilename is display-only, not a filter dimension.
	ParentDirectory string `gorm:"index"`
	ShortFilename   string
	ReceivedAt time.Time `gorm:"index"`
```

In `src/storage/catalog/store.go`, add the same two fields to the `Entry` DTO struct:

```go
// Entry mirrors EntryRecord's replicated fields, decoupled from the gorm
// model so callers (the gRPC server) don't need to import gorm tags.
type Entry struct {
	StoreNode       string
	JobID           string
	ObjectID        string
	Metadata        []byte
	Ctime           int64
	StoreSeq        int64
	StoreCreatedAt  time.Time
	SourceHost      string
	ParentDirectory string
	ShortFilename   string
}
```

And map them through in `EnsureEntries`:

```go
	for i, e := range batch {
		records[i] = EntryRecord{
			StoreNode:       e.StoreNode,
			JobID:           e.JobID,
			ObjectID:        e.ObjectID,
			Metadata:        e.Metadata,
			Ctime:           e.Ctime,
			StoreSeq:        e.StoreSeq,
			StoreCreatedAt:  e.StoreCreatedAt,
			SourceHost:      e.SourceHost,
			ParentDirectory: e.ParentDirectory,
			ShortFilename:   e.ShortFilename,
			ReceivedAt:      now,
		}
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS, all tests including the pre-existing ones in this file.

- [ ] **Step 5: Commit**

```bash
git add src/storage/catalog/store.go src/storage/catalog/models.go src/storage/catalog/store_test.go
git commit -m "feat(catalog): add parent_directory/short_filename columns and persistence"
```

---

## Task 4: Store layer — `ParentDirectories` filter on `ListEntries`

**Files:**
- Modify: `src/storage/catalog/store.go:75-143` (as renumbered after Task 3; the `ListEntriesFilter` struct and `ListEntries` method)
- Test: `src/storage/catalog/store_test.go`

**Interfaces:**
- Consumes: `Entry.ParentDirectory` (Task 3).
- Produces: `ListEntriesFilter` gains `ParentDirectories []string`. Consumed by Task 6.

- [ ] **Step 1: Write the failing test**

Append to `src/storage/catalog/store_test.go`:

```go
func TestListEntries_FiltersByParentDirectoriesMultiValue(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", ShortFilename: "index.html", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", ParentDirectory: "/etc", ShortFilename: "passwd", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{ParentDirectories: []string{"/var/lib/dbdata", "/etc"}})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var objIDs []string
	for _, e := range entries {
		objIDs = append(objIDs, e.ObjectID)
	}
	assert.ElementsMatch(t, []string{"obj-1", "obj-3"}, objIDs)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./storage/catalog/... -run TestListEntries_FiltersByParentDirectoriesMultiValue -v`
Expected: FAIL — `ListEntriesFilter` has no field `ParentDirectories`, compile error.

- [ ] **Step 3: Implement**

In `src/storage/catalog/store.go`, add `ParentDirectories []string` to `ListEntriesFilter`:

```go
// ListEntriesFilter narrows and paginates a ListEntries query. A
// zero-valued filter matches every entry, newest first, first page.
type ListEntriesFilter struct {
	StoreNode         string    // exact match against the sending bwfs node; "" = all store nodes
	SourceHost        string    // exact match against the real originating host; "" = all source hosts
	Pattern           string    // substring match against object_id; "" = no filter
	Limit             int       // clamped to [1, 500]; 0 or negative defaults to 100
	StartingAfter     int64     // last-seen entry ID from a previous page; 0 = first page
	ReceivedAfter     time.Time // zero value = no lower bound
	ReceivedBefore    time.Time // zero value = no upper bound
	SourceHosts       []string  // OR-matched; empty = no filter, additive to SourceHost
	JobNames          []string  // OR-matched against the policy name embedded in job_id
	ParentDirectories []string  // OR-matched against the exact immediate containing directory; empty = no filter
}
```

And add a clause to `ListEntries`, immediately after the existing `JobNames` clause:

```go
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}
	if len(filter.ParentDirectories) > 0 {
		q = q.Where("parent_directory IN ?", filter.ParentDirectories)
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS, all tests including the pre-existing ones in this file.

- [ ] **Step 5: Commit**

```bash
git add src/storage/catalog/store.go src/storage/catalog/store_test.go
git commit -m "feat(catalog): filter ListEntries by parent_directories"
```

---

## Task 5: Store layer — `ListDirectoryFacets`, and `ParentDirectories` narrowing on the existing facets

**Files:**
- Modify: `src/storage/catalog/store.go:161-296` (as renumbered after Tasks 3-4; `FacetFilter`, `ListClientFacets`, `ListJobFacets`)
- Test: `src/storage/catalog/store_test.go`

**Interfaces:**
- Consumes: `Entry.ParentDirectory` (Task 3), `Facet{Name, Count, LastSeen}` (existing, unchanged shape).
- Produces: `FacetFilter` gains `ParentDirectories []string`. New `func (s *Store) ListDirectoryFacets(filter FacetFilter) ([]Facet, error)`. Consumed by Task 6.

- [ ] **Step 1: Write the failing tests**

Append to `src/storage/catalog/store_test.go`:

```go
func TestListClientFacets_NarrowedByParentDirectories(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListJobFacets_NarrowedByParentDirectories(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}

func TestListDirectoryFacets_GroupsByParentDirectoryWithCountAndLastSeen(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 2)

	byName := map[string]Facet{}
	for _, f := range facets {
		byName[f.Name] = f
	}
	assert.Equal(t, int64(2), byName["/var/lib/dbdata"].Count)
	assert.Equal(t, int64(1), byName["/var/www"].Count)
}

func TestListDirectoryFacets_ExcludesEmptyParentDirectory(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/www", facets[0].Name)
}

func TestListDirectoryFacets_NarrowedBySourceHostsAndJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListDirectoryFacets(FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/lib/dbdata", facets[0].Name)

	facets, err = store.ListDirectoryFacets(FacetFilter{JobNames: []string{"hourly-web"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "/var/www", facets[0].Name)
}

func TestListDirectoryFacets_IgnoresOwnDimension(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	// A ParentDirectories value on the request itself must not narrow
	// ListDirectoryFacets -- it's this facet's own dimension.
	facets, err := store.ListDirectoryFacets(FacetFilter{ParentDirectories: []string{"/var/lib/dbdata"}})
	require.NoError(t, err)
	assert.Len(t, facets, 2)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./storage/catalog/... -run 'TestListClientFacets_NarrowedByParentDirectories|TestListJobFacets_NarrowedByParentDirectories|TestListDirectoryFacets' -v`
Expected: FAIL — `FacetFilter` has no field `ParentDirectories`, and `store.ListDirectoryFacets` undefined, compile error.

- [ ] **Step 3: Implement**

In `src/storage/catalog/store.go`, add `ParentDirectories []string` to `FacetFilter`:

```go
// FacetFilter narrows a ListClientFacets/ListJobFacets/ListDirectoryFacets
// aggregate query. A zero-valued filter matches every entry, no date bound.
type FacetFilter struct {
	ReceivedAfter     time.Time
	ReceivedBefore    time.Time
	Pattern           string
	SourceHosts       []string // ignored by ListClientFacets -- its own dimension
	JobNames          []string // ignored by ListJobFacets -- its own dimension
	ParentDirectories []string // ignored by ListDirectoryFacets -- its own dimension
}
```

Add a `ParentDirectories` clause to `ListClientFacets`, immediately after its existing `JobNames` clause:

```go
	q := s.db.Model(&EntryRecord{}).
		Select("source_host, received_at").
		Where("source_host != ''")
	q = filter.applyCommon(q)
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}
	if len(filter.ParentDirectories) > 0 {
		q = q.Where("parent_directory IN ?", filter.ParentDirectories)
	}
```

Add a `ParentDirectories` clause to `ListJobFacets`, immediately after its existing `SourceHosts` clause:

```go
	q := s.db.Model(&EntryRecord{}).Select("job_id, received_at")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.ParentDirectories) > 0 {
		q = q.Where("parent_directory IN ?", filter.ParentDirectories)
	}
```

Add a new method, `ListDirectoryFacets`, immediately after `ListJobFacets` (before `policyNameFromJobID`):

```go
// ListDirectoryFacets groups entries matching filter by parent_directory,
// dropping rows where parent_directory is empty (a sync-time Metadata
// decode failure -- see decodePathParts in cmd/catalog/server.go) rather
// than surfacing a blank-named facet, mirroring ListClientFacets's drop of
// an empty source_host. filter.ParentDirectories is ignored: a directory
// facet list is never narrowed by its own dimension's current selection.
// Both SourceHosts and JobNames narrow it, extending the same
// "apply every other dimension, ignore your own" rule ListClientFacets/
// ListJobFacets already follow to this third dimension.
//
// Aggregation happens in Go, not SQL, for the same reason as
// ListClientFacets (see its comment): avoids non-portable SQL-side
// time-string parsing, consistent Go-side row-scan pattern across all
// three facet methods.
func (s *Store) ListDirectoryFacets(filter FacetFilter) ([]Facet, error) {
	q := s.db.Model(&EntryRecord{}).
		Select("parent_directory, received_at").
		Where("parent_directory != ''")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}

	var rows []struct {
		ParentDirectory string
		ReceivedAt      time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	byName := make(map[string]*Facet)
	var order []string
	for _, r := range rows {
		name := r.ParentDirectory
		f, ok := byName[name]
		if !ok {
			f = &Facet{Name: name}
			byName[name] = f
			order = append(order, name)
		}
		f.Count++
		if r.ReceivedAt.After(f.LastSeen) {
			f.LastSeen = r.ReceivedAt
		}
	}

	facets := make([]Facet, 0, len(order))
	for _, name := range order {
		facets = append(facets, *byName[name])
	}
	return facets, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS, all tests including the pre-existing ones in this file.

- [ ] **Step 5: Commit**

```bash
git add src/storage/catalog/store.go src/storage/catalog/store_test.go
git commit -m "feat(catalog): add ListDirectoryFacets and parent_directory narrowing on client/job facets"
```

---

## Task 6: gRPC server — sync-time computation and `ListEntries`/`ListDirectoryFacets` wiring

**Files:**
- Modify: `src/cmd/catalog/server.go`
- Test: `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: `splitPath` (Task 1), `pb.Entry.ParentDirectory`/`ShortFilename`, `pb.ListEntriesRequest.ParentDirectories`, `pb.ListFacetsRequest.ParentDirectories`, `pb.CatalogServiceServer.ListDirectoryFacets` (Task 2), `catalogstore.Entry.ParentDirectory`/`ShortFilename`, `ListEntriesFilter.ParentDirectories`, `FacetFilter.ParentDirectories`, `Store.ListDirectoryFacets` (Tasks 3-5).
- Produces: `catalogServer.ListDirectoryFacets` RPC handler. Consumed by Task 7 (indirectly, via the real server in integration; api-server itself only depends on the proto types).

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/catalog/server_test.go`:

```go
func TestSyncFileVersions_DerivesParentDirectoryAndShortFilenameFromMetadata(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	fi := filesystem.NewFileInfoForTest("origin-host", "/var/lib/dbdata/data.db", 8192, 0o644, 999, 999, time.Now())
	metadata, err := fi.Encode()
	require.NoError(t, err)

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: fi.ID(), Metadata: metadata, CreatedAt: time.Now().Unix()},
	}}
	_, err = srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	entries, _, err := store.ListEntries(catalogstore.ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "/var/lib/dbdata", entries[0].ParentDirectory)
	assert.Equal(t, "data.db", entries[0].ShortFilename)
}

func TestSyncFileVersions_MalformedMetadataLeavesPathPartsEmpty(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: "obj-1", Metadata: []byte("not-gob-encoded"), CreatedAt: time.Now().Unix()},
	}}
	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err) // a bad row's metadata doesn't fail the batch

	entries, _, err := store.ListEntries(catalogstore.ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "", entries[0].ParentDirectory)
	assert.Equal(t, "", entries[0].ShortFilename)
}

func TestListEntries_ReturnsParentDirectoryAndShortFilenameFields(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "/var/lib/dbdata", resp.GetEntries()[0].GetParentDirectory())
	assert.Equal(t, "data.db", resp.GetEntries()[0].GetShortFilename())
}

func TestListEntries_FiltersByParentDirectories(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{
		ParentDirectories: []string{"/var/lib/dbdata"},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "obj-1", resp.GetEntries()[0].GetObjectId())
}

func TestListDirectoryFacets_ReturnsGroupedCounts(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", ParentDirectory: "/var/lib/dbdata", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", ParentDirectory: "/var/www", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListDirectoryFacets(context.Background(), &pb.ListFacetsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetFacets(), 2)

	byName := map[string]int64{}
	for _, f := range resp.GetFacets() {
		byName[f.GetName()] = f.GetCount()
	}
	assert.Equal(t, int64(2), byName["/var/lib/dbdata"])
	assert.Equal(t, int64(1), byName["/var/www"])
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/catalog/... -run 'TestSyncFileVersions_DerivesParentDirectoryAndShortFilenameFromMetadata|TestSyncFileVersions_MalformedMetadataLeavesPathPartsEmpty|TestListEntries_ReturnsParentDirectoryAndShortFilenameFields|TestListEntries_FiltersByParentDirectories|TestListDirectoryFacets_ReturnsGroupedCounts' -v`
Expected: FAIL — `srv.ListDirectoryFacets` undefined and the persisted/returned fields are empty, compile or assertion errors.

- [ ] **Step 3: Implement**

In `src/cmd/catalog/server.go`, add `decodePathParts` immediately after `decodeSourceHost` (after line 69):

```go
// decodePathParts extracts parent_directory/short_filename from an entry's
// Metadata blob, decoded once at sync time so ListEntries/
// ListDirectoryFacets can filter/group on plain indexed columns instead of
// decoding Metadata per row (see decodeSourceHost above, same rationale). A
// decode failure yields ("", "") rather than failing the whole batch.
func decodePathParts(metadata []byte) (parentDir, shortName string) {
	fi, err := filesystem.DecodeFileInfo(metadata)
	if err != nil {
		return "", ""
	}
	return splitPath(fi.Path())
}
```

In `SyncFileVersions`'s batch-building loop, add the two new fields alongside `SourceHost`:

```go
	for i, e := range entries {
		parentDir, shortName := decodePathParts(e.GetMetadata())
		batch[i] = catalogstore.Entry{
			StoreNode:       storeNode,
			JobID:           e.GetJobId(),
			ObjectID:        e.GetObjectId(),
			Metadata:        e.GetMetadata(),
			Ctime:           e.GetCtime(),
			StoreSeq:        e.GetStoreSeq(),
			StoreCreatedAt:  time.Unix(e.GetCreatedAt(), 0).UTC(),
			SourceHost:      decodeSourceHost(e.GetMetadata()),
			ParentDirectory: parentDir,
			ShortFilename:   shortName,
		}
	}
```

In `ListEntries`, add `ParentDirectories` to the `ListEntriesFilter` construction:

```go
	records, hasMore, err := s.store.ListEntries(catalogstore.ListEntriesFilter{
		StoreNode:         req.GetStoreHost(),
		SourceHost:        req.GetSourceHost(),
		Pattern:           req.GetPattern(),
		Limit:             int(req.GetLimit()),
		StartingAfter:     req.GetStartingAfter(),
		ReceivedAfter:     unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore:    unixOrZero(req.GetReceivedBefore()),
		SourceHosts:       req.GetSourceHosts(),
		JobNames:          req.GetJobNames(),
		ParentDirectories: req.GetParentDirectories(),
	})
```

In `toProtoEntry`, add the two new fields to the initial `&pb.Entry{...}` literal, alongside `SourceHost` (both come directly from the persisted record, not from decoding `rec.Metadata`, same as `SourceHost`):

```go
	entry := &pb.Entry{
		Id:              rec.ID,
		StoreHost:       rec.StoreNode,
		SourceHost:      rec.SourceHost,
		JobId:           rec.JobID,
		ObjectId:        rec.ObjectID,
		Ctime:           rec.Ctime,
		StoreCreatedAt:  rec.StoreCreatedAt.Unix(),
		ReceivedAt:      rec.ReceivedAt.Unix(),
		ParentDirectory: rec.ParentDirectory,
		ShortFilename:   rec.ShortFilename,
	}
```

Add a new handler, `ListDirectoryFacets`, immediately after `ListJobFacets` (before `toProtoFacets`):

```go
func (s *catalogServer) ListDirectoryFacets(ctx context.Context, req *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error) {
	facets, err := s.store.ListDirectoryFacets(catalogstore.FacetFilter{
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		Pattern:        req.GetPattern(),
		SourceHosts:    req.GetSourceHosts(),
		JobNames:       req.GetJobNames(),
	})
	if err != nil {
		s.logger.Error("ListDirectoryFacets: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list directory facets: %v", err)
	}
	return &pb.ListFacetsResponse{Facets: toProtoFacets(facets)}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS, all tests including the pre-existing ones in this file.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalog/server.go src/cmd/catalog/server_test.go
git commit -m "feat(catalog): compute parent_directory/short_filename at sync time, wire ListDirectoryFacets"
```

---

## Task 7: api-server — REST wiring for the new field and endpoint

**Files:**
- Modify: `src/cmd/api-server/catalog.go`
- Modify: `src/cmd/api-server/server.go:35-41,81-83`
- Test: `src/cmd/api-server/catalog_test.go`

**Interfaces:**
- Consumes: `pb.Entry.ParentDirectory`/`ShortFilename`, `pb.ListEntriesRequest.ParentDirectories`, `pb.ListFacetsRequest.ParentDirectories`, `pb.CatalogServiceClient.ListDirectoryFacets` (Task 2).
- Produces: `GET /api/v1/catalog/directories` REST endpoint. Nothing consumed by later tasks in this plan (Task 8 is documentation only).

- [ ] **Step 1: Write the failing tests**

In `src/cmd/api-server/catalog_test.go`, add a `ListDirectoryFacets` method to `fakeCatalogQueryClient` (it must satisfy the `catalogQueryClient` interface once Task 2's RPC is added to it in this task's Step 3):

```go
func (f *fakeCatalogQueryClient) ListDirectoryFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error) {
	f.lastFacetsReq = in
	return f.facetsResp, f.facetsErr
}
```

Then append the new test functions:

```go
func TestHandleListCatalog_ReturnsParentDirectoryAndShortFilename(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{
		Entries: []*pb.Entry{{Id: 1, ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db"}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	entry := data[0].(map[string]any)
	assert.Equal(t, "/var/lib/dbdata", entry["parent_directory"])
	assert.Equal(t, "data.db", entry["short_filename"])
}

func TestHandleListCatalog_PassesParentDirectoriesQueryParamThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?parent_directories=/var/lib/dbdata,/var/www", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, []string{"/var/lib/dbdata", "/var/www"}, fake.lastReq.GetParentDirectories())
}

func TestHandleListCatalogClients_PassesParentDirectoriesQueryParamThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/clients?parent_directories=/var/lib/dbdata", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, []string{"/var/lib/dbdata"}, fake.lastFacetsReq.GetParentDirectories())
}

func TestHandleListCatalogJobs_PassesParentDirectoriesQueryParamThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs?parent_directories=/var/lib/dbdata", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, []string{"/var/lib/dbdata"}, fake.lastFacetsReq.GetParentDirectories())
}

func TestHandleListCatalogDirectories_ReturnsFacetData(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{
		Facets: []*pb.Facet{{Name: "/var/lib/dbdata", Count: 2, LastSeen: 1752400000}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	facet := data[0].(map[string]any)
	assert.Equal(t, "/var/lib/dbdata", facet["name"])
	assert.Equal(t, float64(2), facet["count"])
}

func TestHandleListCatalogDirectories_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories?received_after=1000&source_hosts=database&job_names=nightly-db", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, int64(1000), fake.lastFacetsReq.GetReceivedAfter())
	assert.Equal(t, []string{"database"}, fake.lastFacetsReq.GetSourceHosts())
	assert.Equal(t, []string{"nightly-db"}, fake.lastFacetsReq.GetJobNames())
}

func TestHandleListCatalogDirectories_InvalidReceivedAfterReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories?received_after=not-a-number", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestHandleListCatalog|TestHandleListCatalogClients|TestHandleListCatalogJobs|TestHandleListCatalogDirectories' -v`
Expected: FAIL — `fakeCatalogQueryClient` doesn't satisfy `catalogQueryClient` yet (missing `ListDirectoryFacets` on the real interface), `entryDTO` has no `parent_directory`/`short_filename` keys, `s.handleListCatalogDirectories` undefined, compile errors.

- [ ] **Step 3: Implement**

In `src/cmd/api-server/server.go`, add `ListDirectoryFacets` to the `catalogQueryClient` interface:

```go
// catalogQueryClient is the subset of pb.CatalogServiceClient the catalog
// handlers (Tasks 5-6) need.
type catalogQueryClient interface {
	ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error)
	ListClientFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
	ListJobFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
	ListDirectoryFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
}
```

And register the new route in `registerRoutes`, immediately after `/api/v1/catalog/jobs`:

```go
	mux.HandleFunc("GET /api/v1/catalog", s.handleListCatalog)
	mux.HandleFunc("GET /api/v1/catalog/clients", s.handleListCatalogClients)
	mux.HandleFunc("GET /api/v1/catalog/jobs", s.handleListCatalogJobs)
	mux.HandleFunc("GET /api/v1/catalog/directories", s.handleListCatalogDirectories)
```

In `src/cmd/api-server/catalog.go`, add the two fields to `entryDTO` and populate them in `toEntryDTO`:

```go
type entryDTO struct {
	ID              int64  `json:"id"`
	SourceHost      string `json:"source_host"`
	StoreHost       string `json:"store_host"`
	JobID           string `json:"job_id"`
	ObjectID        string `json:"object_id"`
	Ctime           int64  `json:"ctime"`
	StoreCreatedAt  int64  `json:"store_created_at"`
	ReceivedAt      int64  `json:"received_at"`
	Path            string `json:"path"`
	Size            int64  `json:"size"`
	Mode            string `json:"mode"`
	Owner           uint32 `json:"owner"`
	Group           uint32 `json:"group"`
	ModTime         int64  `json:"mod_time"`
	ParentDirectory string `json:"parent_directory"`
	ShortFilename   string `json:"short_filename"`
}

func toEntryDTO(e *pb.Entry) entryDTO {
	return entryDTO{
		ID:              e.GetId(),
		SourceHost:      e.GetSourceHost(),
		StoreHost:       e.GetStoreHost(),
		JobID:           e.GetJobId(),
		ObjectID:        e.GetObjectId(),
		Ctime:           e.GetCtime(),
		StoreCreatedAt:  e.GetStoreCreatedAt(),
		ReceivedAt:      e.GetReceivedAt(),
		Path:            e.GetPath(),
		Size:            e.GetSize(),
		Mode:            e.GetMode(),
		Owner:           e.GetOwner(),
		Group:           e.GetGroup(),
		ModTime:         e.GetModTime(),
		ParentDirectory: e.GetParentDirectory(),
		ShortFilename:   e.GetShortFilename(),
	}
}
```

In `handleListCatalog`, add `ParentDirectories` to the `pb.ListEntriesRequest` construction:

```go
	resp, err := s.catalog.ListEntries(r.Context(), &pb.ListEntriesRequest{
		SourceHost:        q.Get("source_host"),
		StoreHost:         q.Get("store_host"),
		Pattern:           q.Get("pattern"),
		Limit:             int32(limit),
		StartingAfter:     startingAfter,
		ReceivedAfter:     receivedAfter,
		ReceivedBefore:    receivedBefore,
		SourceHosts:       splitCommaParam(q.Get("source_hosts")),
		JobNames:          splitCommaParam(q.Get("job_names")),
		ParentDirectories: splitCommaParam(q.Get("parent_directories")),
	})
```

In `handleListCatalogClients`, add `ParentDirectories` to its `pb.ListFacetsRequest` construction:

```go
	resp, err := s.catalog.ListClientFacets(r.Context(), &pb.ListFacetsRequest{
		ReceivedAfter:     receivedAfter,
		ReceivedBefore:    receivedBefore,
		Pattern:           q.Get("pattern"),
		JobNames:          splitCommaParam(q.Get("job_names")),
		ParentDirectories: splitCommaParam(q.Get("parent_directories")),
	})
```

In `handleListCatalogJobs`, add `ParentDirectories` to its `pb.ListFacetsRequest` construction:

```go
	resp, err := s.catalog.ListJobFacets(r.Context(), &pb.ListFacetsRequest{
		ReceivedAfter:     receivedAfter,
		ReceivedBefore:    receivedBefore,
		Pattern:           q.Get("pattern"),
		SourceHosts:       splitCommaParam(q.Get("source_hosts")),
		ParentDirectories: splitCommaParam(q.Get("parent_directories")),
	})
```

Add a new handler, `handleListCatalogDirectories`, at the end of `src/cmd/api-server/catalog.go`, following `handleListCatalogJobs`'s shape exactly (no `parent_directories` param — own dimension):

```go
func (s *server) handleListCatalogDirectories(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	receivedAfter, ok := parseUnixParam(q.Get("received_after"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_after must be a non-negative integer")
		return
	}
	receivedBefore, ok := parseUnixParam(q.Get("received_before"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_before must be a non-negative integer")
		return
	}

	resp, err := s.catalog.ListDirectoryFacets(r.Context(), &pb.ListFacetsRequest{
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		Pattern:        q.Get("pattern"),
		SourceHosts:    splitCommaParam(q.Get("source_hosts")),
		JobNames:       splitCommaParam(q.Get("job_names")),
	})
	if err != nil {
		s.logger.Error("handleListCatalogDirectories: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	facets := make([]facetDTO, len(resp.GetFacets()))
	for i, f := range resp.GetFacets() {
		facets[i] = toFacetDTO(f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": facets})
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS, all tests including the pre-existing ones in this package.

- [ ] **Step 5: Run the full module test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: build succeeds, all tests across the module pass.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/catalog.go src/cmd/api-server/server.go src/cmd/api-server/catalog_test.go
git commit -m "feat(api-server): expose parent_directory/short_filename and GET /catalog/directories"
```

---

## Task 8: Documentation and changelog

**Files:**
- Modify: `docs/protocols/catalog-sync.md`
- Modify: `docs/components/catalog.md:1-9`
- Modify: `docs/api/rest-v1.md:114-180`
- Modify: `CHANGELOG.md` (prepend entry)
- Verify (no edit expected): `README.md`, `docs/components/api-server.md`, `docs/ARCHITECTURE.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: Update the protocol doc's service block**

In `docs/protocols/catalog-sync.md`, replace the `## Service` code block:

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
  rpc ListClientFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListJobFacets(ListFacetsRequest) returns (ListFacetsResponse);
}
```

with:

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
  rpc ListClientFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListJobFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListDirectoryFacets(ListFacetsRequest) returns (ListFacetsResponse);
}
```

- [ ] **Step 2: Update the Identity section**

In `docs/protocols/catalog-sync.md`'s `## Identity` section, immediately after the existing `source_host` paragraph (ending "...rather than failing the whole batch."), add:

```markdown
`catalog` also derives `parent_directory` and `short_filename` at sync time from the same
`metadata` blob, via `splitPath` (`cmd/catalog/pathsplit.go`). `parent_directory` is the file's
exact immediate containing directory (not a subtree/prefix match); `short_filename` is its bare
name. `splitPath` picks separator style (`/` vs `\`) from the path's own shape — a leading `/`, a
drive letter, or a UNC prefix — rather than the runtime OS, since `catalog` always runs on Linux
but a path may have been recorded by a Windows-origin `bwfs` node. A root-level file's
`parent_directory` is `/` (or `C:\` on Windows), never empty — empty is reserved to mean an
undecoded/failed entry, consistent with `source_host`'s existing convention. A metadata decode
failure leaves both fields empty for that entry rather than failing the whole batch.
```

- [ ] **Step 3: Update the `ListEntries` message block and field docs**

In `docs/protocols/catalog-sync.md`'s `## ListEntries` section, replace the `ListEntriesRequest`/`Entry` code block with:

```protobuf
message ListEntriesRequest {
  string store_host     = 1; // exact match against the sending bwfs node's identity; empty = all
  string pattern        = 2; // substring match against object_id; empty = no filter
  int32  limit           = 3; // 1..500, default 100
  int64  starting_after  = 4; // last-seen entry ID from a previous page; 0 = first page
  string source_host    = 5; // exact match against the real originating (backed-up) host; empty = all
  int64  received_after  = 6; // unix seconds; 0 = no lower bound, filters on received_at
  int64  received_before = 7; // unix seconds; 0 = no upper bound, filters on received_at
  repeated string source_hosts = 8; // OR-matched; empty = no filter, additive to source_host
  repeated string job_names    = 9; // OR-matched against the policy name embedded in job_id
  repeated string parent_directories = 10; // OR-matched against the exact immediate containing directory
}

message ListEntriesResponse {
  repeated Entry entries = 1;
  bool has_more = 2;
}

message Entry {
  int64  id                = 1;
  string store_host        = 2;
  string job_id            = 3;
  string object_id         = 4;
  int64  ctime             = 5;
  int64  store_created_at  = 6;
  int64  received_at       = 7;
  // decoded server-side from the stored Metadata blob:
  string path      = 8;
  int64  size       = 9;
  string mode      = 10; // e.g. "-rw-r--r--", from fs.FileMode.String()
  uint32 owner     = 11; // Unix UID (or Windows SID hash) — numeric, no name resolution
  uint32 group     = 12; // Unix GID (or Windows SID hash) — numeric, no name resolution
  int64  mod_time   = 13;
  string source_host = 14; // the real originating (backed-up) host, derived from Metadata at sync time
  string parent_directory = 15; // the file's exact immediate containing directory, derived from Metadata at sync time
  string short_filename   = 16; // the file's bare name, derived from Metadata at sync time; display only, not a filter
}
```

Then, immediately after that block's existing bullet list (ending "...not just persisting it..."), add:

```markdown
- `parent_directories` — OR-matched against `parent_directory`, an exact match against a file's
  *immediate* containing directory only (not a recursive subtree/prefix match); empty applies no
  filter, additive to every other active filter.
```

- [ ] **Step 4: Update the `ListClientFacets` / `ListJobFacets` section, renaming it and adding `ListDirectoryFacets`**

In `docs/protocols/catalog-sync.md`, replace the entire `## ListClientFacets / ListJobFacets` section (including its heading) with:

```markdown
## ListClientFacets / ListJobFacets / ListDirectoryFacets

Three read-only aggregate RPCs backing the web catalog view's faceted filter panels — for
[api-server](../components/api-server.md)'s `GET /api/v1/catalog/clients`,
`GET /api/v1/catalog/jobs`, and `GET /api/v1/catalog/directories`. All three share one
request/response shape:

\`\`\`protobuf
message Facet {
  string name       = 1; // hostname, policy name, or parent directory
  int64  count       = 2; // matching entries in the current scope
  int64  last_seen   = 3; // unix seconds, max(received_at) in scope
}

message ListFacetsRequest {
  int64  received_after  = 1;
  int64  received_before = 2;
  string pattern         = 3;
  repeated string source_hosts = 4; // ignored by ListClientFacets (own dimension)
  repeated string job_names    = 5; // ignored by ListJobFacets (own dimension)
  repeated string parent_directories = 6; // ignored by ListDirectoryFacets (own dimension)
}

message ListFacetsResponse {
  repeated Facet facets = 1;
}
\`\`\`

`ListClientFacets` groups by `source_host`; `ListJobFacets` groups by the policy name embedded in
`job_id` (see [Identity](#identity)'s `job_id` convention); `ListDirectoryFacets` groups by
`parent_directory`. Each RPC applies every *other* dimension's filter fields from the request but
ignores its own (e.g. `ListDirectoryFacets` applies `source_hosts`/`job_names` but ignores
`parent_directories`): a facet list is never narrowed by its own current selection, so a caller can
implement cross-filtering (selecting in one dimension narrows the other two) by passing every other
dimension's active selection. Rows with an empty grouping key (an undecoded `source_host`/
`parent_directory`, or a `job_id` that isn't `backup:`-prefixed) are dropped rather than surfaced as
a blank-named facet.
```

(Write plain triple-backtick fences in the actual file — the `\`\`\`` above is only escaped for nesting inside this plan's own fenced instructions.)

- [ ] **Step 5: Update the See Also cross-link**

In `docs/protocols/catalog-sync.md`'s `## See Also` section, replace:

```markdown
- [REST API v1](../api/rest-v1.md) — `GET /api/v1/catalog` (`ListEntries`), `GET /api/v1/catalog/clients`
  (`ListClientFacets`), and `GET /api/v1/catalog/jobs` (`ListJobFacets`)
```

with:

```markdown
- [REST API v1](../api/rest-v1.md) — `GET /api/v1/catalog` (`ListEntries`), `GET /api/v1/catalog/clients`
  (`ListClientFacets`), `GET /api/v1/catalog/jobs` (`ListJobFacets`), and `GET /api/v1/catalog/directories`
  (`ListDirectoryFacets`)
```

- [ ] **Step 6: Update the component doc's summary line**

In `docs/components/catalog.md`, replace:

```markdown
Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Also serves three read-only query RPCs: `ListEntries`
(filter by store host, real source host, a date range, and a substring match against the
underlying object ID, keyset-paginated) and the aggregate `ListClientFacets`/`ListJobFacets`
(grouped counts by client host or by policy name, backing the web catalog view's filter panels)
— see [api-server](./api-server.md), the only intended caller today.
```

with:

```markdown
Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Also serves four read-only query RPCs: `ListEntries`
(filter by store host, real source host, a date range, an exact parent directory, and a substring
match against the underlying object ID, keyset-paginated) and the aggregate
`ListClientFacets`/`ListJobFacets`/`ListDirectoryFacets` (grouped counts by client host, policy
name, or parent directory, backing the web catalog view's filter panels) — see
[api-server](./api-server.md), the only intended caller today.
```

- [ ] **Step 7: Update the REST API doc's `/catalog` param table and example**

In `docs/api/rest-v1.md`, replace the `## GET /api/v1/catalog` section's parameter table with:

```markdown
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
```

Then replace the response example immediately below it:

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

- [ ] **Step 8: Update the `/catalog/clients` and `/catalog/jobs` docs, and add `/catalog/directories`**

In `docs/api/rest-v1.md`, replace the `## GET /api/v1/catalog/clients` section's description line:

```markdown
Returns the distinct client (source host) facets matching the given filters, each with a count and
last-seen timestamp. Not paginated — a fleet's distinct client count is expected to stay in the
dozens. Query parameters: `received_after`, `received_before`, `pattern`, `job_names` (comma-separated)
— note there is no `source_hosts` parameter here, since a client facet list is never narrowed by its
own dimension.
```

with:

```markdown
Returns the distinct client (source host) facets matching the given filters, each with a count and
last-seen timestamp. Not paginated — a fleet's distinct client count is expected to stay in the
dozens. Query parameters: `received_after`, `received_before`, `pattern`, `job_names`,
`parent_directories` (comma-separated) — note there is no `source_hosts` parameter here, since a
client facet list is never narrowed by its own dimension.
```

Replace the `## GET /api/v1/catalog/jobs` section's description line:

```markdown
Same shape as `/catalog/clients`, grouped by policy name instead of client host. Query parameters:
`received_after`, `received_before`, `pattern`, `source_hosts` (comma-separated) — no `job_names`
parameter, for the same own-dimension reason.
```

with:

```markdown
Same shape as `/catalog/clients`, grouped by policy name instead of client host. Query parameters:
`received_after`, `received_before`, `pattern`, `source_hosts`, `parent_directories`
(comma-separated) — no `job_names` parameter, for the same own-dimension reason.
```

Then, immediately after the `/catalog/jobs` JSON example and before `## GET /api/v1/policies`, insert:

```markdown
## `GET /api/v1/catalog/directories`

Same shape as `/catalog/clients`/`/catalog/jobs`, grouped by the entry's exact immediate parent
directory. Query parameters: `received_after`, `received_before`, `pattern`, `source_hosts`,
`job_names` (comma-separated) — no `parent_directories` parameter, for the same own-dimension
reason.

\`\`\`json
{
  "data": [
    {"name": "/var/lib/dbdata", "count": 12, "last_seen": 1752400010}
  ]
}
\`\`\`
```

(As in Step 4, write plain triple-backtick fences — the `\`\`\`` above is only escaped for nesting inside this plan.)

- [ ] **Step 9: Verify README and other docs need no change**

Read `README.md`'s `catalog`/`api-server` component bullets and its documentation-index entry for
`Catalog Sync Protocol` (search for "catalog" in `README.md`). Confirm each is a one-line summary
that doesn't restate the RPC/endpoint list, so none needs editing.

Read `docs/components/api-server.md`'s `## Endpoints` section — it already defers to
`docs/api/rest-v1.md` for the full endpoint reference ("See REST API v1 for the full endpoint
reference") rather than listing endpoints itself, so it needs no edit.

Read `docs/ARCHITECTURE.md`'s catalog/api-server rows and mermaid diagram — this change adds no new
component and no new inter-component connection (only new fields/filters/RPC on the existing
`CatalogService`), so it needs no edit.

Confirm all of the above by re-reading each file after Steps 1-8, rather than editing on assumption.

- [ ] **Step 10: Add the changelog entry**

Prepend to `CHANGELOG.md`, immediately after the `# Changelog` header and its intro line (before the
existing `## 2026-08-07 — mtls: ...` entry):

```markdown
## 2026-08-07 — catalog: parent directory and filename fields, directory filtering

The catalog gains two fields computed once at sync time from each entry's `Metadata` blob —
`parent_directory` (the file's exact immediate containing directory) and `short_filename` (its bare
name) — following the same pattern `source_host` already uses. `parent_directory` becomes a new,
exact-match filter dimension on `ListEntries`/`GET /api/v1/catalog` (`parent_directories`, OR-matched),
and a new aggregate RPC, `ListDirectoryFacets`, backs `GET /api/v1/catalog/directories`, joining
`ListClientFacets`/`ListJobFacets` in the existing "apply every other dimension, ignore your own"
cross-filter contract. Path splitting (`splitPath`, `cmd/catalog/pathsplit.go`) picks Unix vs.
Windows separator style from a path's own shape rather than the server's runtime OS, so directories
compute correctly for a fleet backing up both platforms. All changes are additive to the existing
`/catalog` contract; entries synced before this change keep both new fields blank until a fresh sync
supersedes them (no backfill). See
`docs/superpowers/specs/2026-08-07-catalog-parent-directory-design.md`.
```

- [ ] **Step 11: Final full-stack verification**

Run: `cd src && go build ./... && go test ./...`
Expected: build succeeds, all tests pass.

- [ ] **Step 12: Commit**

```bash
git add docs/protocols/catalog-sync.md docs/components/catalog.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "docs: document parent_directory/short_filename, ListDirectoryFacets, and /catalog/directories"
```
