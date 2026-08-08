# Catalog Directory Browsing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the catalog's flat file table into a file-manager-style directory browser — root (or a list of Windows drives), click-to-drill-down, and a breadcrumb path bar — backed by a new sync-time-computed directory hierarchy.

**Architecture:** A new `catalog_directories` table records every directory that has ever been an ancestor of a synced file's `parent_directory`, computed once at sync time (mirroring how `parent_directory`/`short_filename` were pulled out of `Metadata`). A new `ListDirectoryChildren` RPC/REST endpoint answers "what's directly under this path" from that table — existence is filter-independent (every directory ever seen), but each child's file count/last-seen respects the current date/host/job filters. The frontend adds a browse mode (folder rows above file rows, breadcrumb path bar) that coexists with, but is mutually exclusive with, today's flat pattern-search mode.

**Tech Stack:** Go (gorm/SQLite) for `catalog`/`api-server`; Vue 3 + Pinia + vue-good-table-next for the web frontend; `protoc`/`protoc-gen-go`/`protoc-gen-go-grpc` for RPC codegen (already installed at `~/.local/bin`).

## Global Constraints

- No backward-compatibility handling for directories/files synced before this ships — the demo catalog will be fully resynced (per design's "Out of scope").
- `catalog_directories` existence is filter-independent; only `file_count`/`last_seen` per child respect date/host/job filters — never make existence recursive/subtree-aware (out of scope, same as `ListEntries`'s `parent_directories` filter).
- Directory browsing and the free-text pattern search are mutually exclusive UI modes: entering a pattern exits browse mode to today's flat cross-directory list; clearing it restores whatever folder was last browsed (or root).
- Every gRPC protocol change (this plan edits `src/api/catalog.proto`) requires updating `docs/protocols/catalog-sync.md` before commit, per `.claude/CLAUDE.md`.
- Every feature change requires updating `docs/components/<component>.md` and `README.md`/`docs/ARCHITECTURE.md` if applicable, per `.claude/CLAUDE.md`.
- A `CHANGELOG.md` entry (dated, prose summary) is required before merging to `main`, per `.claude/CLAUDE.md`.
- Spec: `docs/superpowers/specs/2026-08-08-catalog-directory-browsing-design.md`.

---

## Task 1: Directory hierarchy storage layer

**Files:**
- Modify: `src/storage/catalog/models.go`
- Modify: `src/storage/catalog/db.go`
- Modify: `src/storage/catalog/store.go`
- Test: `src/storage/catalog/store_test.go`

**Interfaces:**
- Produces: `DirectoryRecord{Path, ParentPath, Name, Depth}` (gorm model), `DirectoryAncestor{Path, ParentPath, Name, Depth}` (decoupled input type, mirrors `Entry`/`EntryRecord`), `(*Store).EnsureDirectories(batch []DirectoryAncestor) error`, `DirectoryChild{Path, Name, FileCount, LastSeen, HasChildren}`, `(*Store).ListDirectoryChildren(parentPath string, filter FacetFilter) ([]DirectoryChild, error)`. Task 2 calls `EnsureDirectories`; Task 3 calls `ListDirectoryChildren`.

- [ ] **Step 1: Write the failing store tests**

Append to `src/storage/catalog/store_test.go`:

```go
func TestEnsureDirectories_PersistsBatch(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}))

	children, err := store.ListDirectoryChildren("", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "/", children[0].Path)
}

func TestEnsureDirectories_DuplicatePathIsNoOp(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []DirectoryAncestor{{Path: "/var", ParentPath: "/", Name: "var", Depth: 1}}
	require.NoError(t, store.EnsureDirectories(batch))
	require.NoError(t, store.EnsureDirectories(batch)) // resend, e.g. after a retried sync

	children, err := store.ListDirectoryChildren("/", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
}

func TestEnsureDirectories_EmptyBatchSucceeds(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories(nil))
}

func TestListDirectoryChildren_ReturnsChildrenOfGivenParentPath(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
		{Path: "/var/www", ParentPath: "/var", Name: "www", Depth: 2},
	}))

	children, err := store.ListDirectoryChildren("/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 2)
	names := []string{children[0].Name, children[1].Name}
	assert.ElementsMatch(t, []string{"lib", "www"}, names)
}

func TestListDirectoryChildren_EmptyParentPathReturnsTrueRoots(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
		{Path: `C:\`, ParentPath: "", Name: `C:\`, Depth: 0},
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}))

	children, err := store.ListDirectoryChildren("", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 2)
	names := []string{children[0].Name, children[1].Name}
	assert.ElementsMatch(t, []string{"/", `C:\`}, names)
}

func TestListDirectoryChildren_UnknownParentPathReturnsEmpty(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	children, err := store.ListDirectoryChildren("/nope", FacetFilter{})
	require.NoError(t, err)
	assert.Empty(t, children)
}

func TestListDirectoryChildren_FileCountAndLastSeenRespectFilters(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: older},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: newer},
	}))
	// EnsureEntries stamps ReceivedAt = time.Now(); simulate a range that
	// excludes nothing so both count, then a range that excludes both.
	children, err := store.ListDirectoryChildren("/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(2), children[0].FileCount)

	future := time.Now().Add(24 * time.Hour)
	children, err = store.ListDirectoryChildren("/var", FacetFilter{ReceivedAfter: future})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(0), children[0].FileCount)
	assert.True(t, children[0].LastSeen.IsZero())
}

func TestListDirectoryChildren_FileCountRespectsSourceHostsAndJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))
	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-lib:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", ParentDirectory: "/var/lib", StoreCreatedAt: time.Now()},
	}))

	children, err := store.ListDirectoryChildren("/var", FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(1), children[0].FileCount)

	children, err = store.ListDirectoryChildren("/var", FacetFilter{JobNames: []string{"hourly-web"}})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, int64(1), children[0].FileCount)
}

func TestListDirectoryChildren_ChildWithNoMatchingFilesStillAppears(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	// /var/lib has no direct files (only a subfolder, /var/lib/dbdata,
	// does) -- existence must still surface it so the UI can navigate
	// through it, per the design's filter-independent-existence rule.
	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
		{Path: "/var/lib/dbdata", ParentPath: "/var/lib", Name: "dbdata", Depth: 3},
	}))

	children, err := store.ListDirectoryChildren("/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "/var/lib", children[0].Path)
	assert.Equal(t, int64(0), children[0].FileCount)
	assert.True(t, children[0].HasChildren)
}

func TestListDirectoryChildren_HasChildrenFalseForLeafDirectory(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureDirectories([]DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))

	children, err := store.ListDirectoryChildren("/var", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.False(t, children[0].HasChildren)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./storage/catalog/... -run 'TestEnsureDirectories|TestListDirectoryChildren' -v`
Expected: FAIL — `DirectoryAncestor`, `EnsureDirectories`, `ListDirectoryChildren` undefined.

- [ ] **Step 3: Add the `DirectoryRecord` model and migration**

In `src/storage/catalog/models.go`, add after `EntryRecord`:

```go
// DirectoryRecord is one directory known to exist because some synced
// file's ParentDirectory chain passes through it -- not just directories
// that directly contain a file. Computed once at sync time by walking
// each file's ParentDirectory with splitPath (see
// cmd/catalog/server.go's decodeDirectoryAncestors), the same helper that
// produced ParentDirectory itself. Existence here is intentionally
// filter-independent: see ListDirectoryChildren's comment in store.go for
// why.
type DirectoryRecord struct {
	Path       string `gorm:"uniqueIndex"`
	ParentPath string `gorm:"index"` // "" for a true root: "/", "C:\", "\\server\share\"
	Name       string // short display label, e.g. "lib", or the root itself ("/", "C:\") when ParentPath == ""
	Depth      int    // 0 at a true root, increasing toward the leaf
}
```

In `src/storage/catalog/db.go`, change:

```go
	if err := db.AutoMigrate(&EntryRecord{}); err != nil {
```

to:

```go
	if err := db.AutoMigrate(&EntryRecord{}, &DirectoryRecord{}); err != nil {
```

- [ ] **Step 4: Implement `DirectoryAncestor`/`EnsureDirectories`/`DirectoryChild`/`ListDirectoryChildren`**

In `src/storage/catalog/store.go`, add after `EnsureEntries`:

```go
// DirectoryAncestor mirrors DirectoryRecord's replicated fields, decoupled
// from the gorm model the same way Entry is decoupled from EntryRecord.
type DirectoryAncestor struct {
	Path       string
	ParentPath string
	Name       string
	Depth      int
}

// EnsureDirectories idempotently persists batch: a row already present for
// a given Path is left untouched (ON CONFLICT DO NOTHING) -- directory
// structure never changes once known, and many files sync-after-sync
// share the same ancestor directories.
func (s *Store) EnsureDirectories(batch []DirectoryAncestor) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]DirectoryRecord, len(batch))
	for i, a := range batch {
		records[i] = DirectoryRecord{Path: a.Path, ParentPath: a.ParentPath, Name: a.Name, Depth: a.Depth}
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path"}},
		DoNothing: true,
	}).Create(&records).Error
}

// DirectoryChild is one directory returned by ListDirectoryChildren: a
// known subdirectory of the requested parent, plus how many files sit
// directly in it and when the most recent one arrived, both computed
// under filter's date/host/job narrowing.
type DirectoryChild struct {
	Path        string
	Name        string
	FileCount   int64
	LastSeen    time.Time
	HasChildren bool
}

// ListDirectoryChildren returns every directory whose ParentPath equals
// parentPath (parentPath == "" for the true roots: "/" and each distinct
// drive/UNC root). Existence is filter-independent -- it reflects every
// directory ever synced, not just ones with entries matching filter's
// date/host/job narrowing -- because making existence filter-aware would
// require knowing whether *any* descendant anywhere in a subtree
// matches, the same recursive-subtree question ListEntries'
// ParentDirectories filter and ListDirectoryFacets both deliberately
// avoid (exact-match only, see their comments). FileCount/LastSeen, by
// contrast, only need a direct (non-recursive) parent_directory match
// against entries, so those do respect filter -- computed as one grouped
// scan across every child, not N+1 per-child queries. HasChildren is
// true when a child itself has any row in catalog_directories (a
// DISTINCT parent_path scan), letting the UI show an expand affordance
// without a second round trip.
func (s *Store) ListDirectoryChildren(parentPath string, filter FacetFilter) ([]DirectoryChild, error) {
	var dirRows []DirectoryRecord
	if err := s.db.Where("parent_path = ?", parentPath).Order("path").Find(&dirRows).Error; err != nil {
		return nil, err
	}
	if len(dirRows) == 0 {
		return []DirectoryChild{}, nil
	}

	paths := make([]string, len(dirRows))
	for i, d := range dirRows {
		paths[i] = d.Path
	}

	q := s.db.Model(&EntryRecord{}).
		Select("parent_directory, received_at").
		Where("parent_directory IN ?", paths)
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}
	var entryRows []struct {
		ParentDirectory string
		ReceivedAt      time.Time
	}
	if err := q.Scan(&entryRows).Error; err != nil {
		return nil, err
	}
	fileCount := make(map[string]int64, len(paths))
	lastSeen := make(map[string]time.Time, len(paths))
	for _, r := range entryRows {
		fileCount[r.ParentDirectory]++
		if r.ReceivedAt.After(lastSeen[r.ParentDirectory]) {
			lastSeen[r.ParentDirectory] = r.ReceivedAt
		}
	}

	var grandchildRows []struct{ ParentPath string }
	if err := s.db.Model(&DirectoryRecord{}).
		Distinct("parent_path").
		Where("parent_path IN ?", paths).
		Scan(&grandchildRows).Error; err != nil {
		return nil, err
	}
	hasChildren := make(map[string]bool, len(grandchildRows))
	for _, g := range grandchildRows {
		hasChildren[g.ParentPath] = true
	}

	children := make([]DirectoryChild, len(dirRows))
	for i, d := range dirRows {
		children[i] = DirectoryChild{
			Path:        d.Path,
			Name:        d.Name,
			FileCount:   fileCount[d.Path],
			LastSeen:    lastSeen[d.Path],
			HasChildren: hasChildren[d.Path],
		}
	}
	return children, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS (all tests in the package, including the pre-existing ones)

- [ ] **Step 6: Commit**

```bash
git add src/storage/catalog/models.go src/storage/catalog/db.go src/storage/catalog/store.go src/storage/catalog/store_test.go
git commit -m "feat(catalog): add catalog_directories table and ListDirectoryChildren"
```

---

## Task 2: Sync-time directory ancestor computation

**Files:**
- Modify: `src/cmd/catalog/server.go`
- Test: `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: `splitPath(p string) (dir, base string)` (`src/cmd/catalog/pathsplit.go`, existing); `catalogstore.DirectoryAncestor{Path, ParentPath, Name, Depth}`, `(*catalogstore.Store).EnsureDirectories` (Task 1).
- Produces: `decodeDirectoryAncestors(parentDir string) []catalogstore.DirectoryAncestor`, wired into `SyncFileVersions`.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/catalog/server_test.go`:

```go
func TestSyncFileVersions_PersistsDirectoryAncestorsForSyncedFile(t *testing.T) {
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

	roots, err := store.ListDirectoryChildren("", catalogstore.FacetFilter{})
	require.NoError(t, err)
	require.Len(t, roots, 1)
	assert.Equal(t, "/", roots[0].Path)

	varChildren, err := store.ListDirectoryChildren("/", catalogstore.FacetFilter{})
	require.NoError(t, err)
	require.Len(t, varChildren, 1)
	assert.Equal(t, "/var", varChildren[0].Path)

	dbdataChildren, err := store.ListDirectoryChildren("/var/lib", catalogstore.FacetFilter{})
	require.NoError(t, err)
	require.Len(t, dbdataChildren, 1)
	assert.Equal(t, "/var/lib/dbdata", dbdataChildren[0].Path)
	assert.Equal(t, int64(1), dbdataChildren[0].FileCount)
}

func TestSyncFileVersions_MalformedMetadataPersistsNoDirectoryAncestors(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: "obj-1", Metadata: []byte("not-gob-encoded"), CreatedAt: time.Now().Unix()},
	}}
	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err) // a bad row's metadata doesn't fail the batch

	roots, err := store.ListDirectoryChildren("", catalogstore.FacetFilter{})
	require.NoError(t, err)
	assert.Empty(t, roots)
}

func TestSyncFileVersions_DirectoryAncestorsDedupedAcrossBatch(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	fi1 := filesystem.NewFileInfoForTest("origin-host", "/var/lib/dbdata/data.db", 8192, 0o644, 999, 999, time.Now())
	m1, err := fi1.Encode()
	require.NoError(t, err)
	fi2 := filesystem.NewFileInfoForTest("origin-host", "/var/lib/dbdata/wal.log", 4096, 0o644, 999, 999, time.Now())
	m2, err := fi2.Encode()
	require.NoError(t, err)

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: fi1.ID(), Metadata: m1, CreatedAt: time.Now().Unix()},
		{JobId: "job-1", ObjectId: fi2.ID(), Metadata: m2, CreatedAt: time.Now().Unix()},
	}}
	_, err = srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	libChildren, err := store.ListDirectoryChildren("/var/lib", catalogstore.FacetFilter{})
	require.NoError(t, err)
	require.Len(t, libChildren, 1) // "dbdata" persisted once, not twice
	assert.Equal(t, int64(2), libChildren[0].FileCount)
}

func TestSyncFileVersions_DirectoryAncestorsIdempotentAcrossRepeatedSyncs(t *testing.T) {
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
	_, err = srv.SyncFileVersions(ctx, req) // resend, e.g. after a retried RPC
	require.NoError(t, err)

	libChildren, err := store.ListDirectoryChildren("/var/lib", catalogstore.FacetFilter{})
	require.NoError(t, err)
	require.Len(t, libChildren, 1)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/catalog/... -run TestSyncFileVersions_.*Directory -v`
Expected: FAIL — no directory ancestors persisted (root/`/var` lookups return empty).

- [ ] **Step 3: Implement `decodeDirectoryAncestors` and wire it into `SyncFileVersions`**

In `src/cmd/catalog/server.go`, add near `decodeSourceHost`:

```go
// decodeDirectoryAncestors walks parentDir's ancestor chain via splitPath
// -- the same shape-detecting split that produced parentDir itself in
// decodePathParts -- collecting one DirectoryAncestor per level from
// parentDir up to its root, root-first Depth (0 at the root). A blank
// parentDir (a decodePathParts failure) yields no ancestors: an unknown
// location can't be placed in the tree.
func decodeDirectoryAncestors(parentDir string) []catalogstore.DirectoryAncestor {
	if parentDir == "" {
		return nil
	}
	var ancestors []catalogstore.DirectoryAncestor
	current := parentDir
	for current != "" {
		parent, base := splitPath(current)
		name := base
		if parent == "" {
			name = current // true root: display itself, e.g. "/" or "C:\"
		}
		ancestors = append(ancestors, catalogstore.DirectoryAncestor{Path: current, ParentPath: parent, Name: name})
		current = parent
	}
	for i := range ancestors {
		ancestors[i].Depth = len(ancestors) - 1 - i // built leaf-to-root; index from the end for root-first depth
	}
	return ancestors
}
```

Replace `SyncFileVersions`'s body with:

```go
func (s *catalogServer) SyncFileVersions(ctx context.Context, req *pb.SyncRequest) (*pb.SyncResponse, error) {
	storeNode, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("SyncFileVersions: could not determine peer identity", "error", err)
		return nil, err
	}

	entries := req.GetEntries()
	batch := make([]catalogstore.Entry, len(entries))
	directoriesByPath := make(map[string]catalogstore.DirectoryAncestor)
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
		for _, a := range decodeDirectoryAncestors(parentDir) {
			directoriesByPath[a.Path] = a
		}
	}

	if err := s.store.EnsureEntries(batch); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}

	if len(directoriesByPath) > 0 {
		directories := make([]catalogstore.DirectoryAncestor, 0, len(directoriesByPath))
		for _, a := range directoriesByPath {
			directories = append(directories, a)
		}
		if err := s.store.EnsureDirectories(directories); err != nil {
			s.logger.Error("SyncFileVersions: persisting directory ancestors failed", "error", err, "count", len(directories))
			return nil, err
		}
	}

	s.logger.Info("SyncFileVersions: batch persisted", "store_node", storeNode, "count", len(batch))
	return &pb.SyncResponse{}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS (whole package, including pre-existing tests)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalog/server.go src/cmd/catalog/server_test.go
git commit -m "feat(catalog): populate catalog_directories at sync time"
```

---

## Task 3: Proto and gRPC `ListDirectoryChildren`

**Files:**
- Modify: `src/api/catalog.proto`
- Regenerate: `src/api/catalog.pb.go`, `src/api/catalog_grpc.pb.go`
- Modify: `src/cmd/catalog/server.go`
- Test: `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: `(*catalogstore.Store).ListDirectoryChildren` (Task 1).
- Produces: `pb.ListDirectoryChildrenRequest{ParentPath, ReceivedAfter, ReceivedBefore, SourceHosts, JobNames}`, `pb.DirectoryChild{Path, Name, FileCount, LastSeen, HasChildren}`, `pb.ListDirectoryChildrenResponse{Children}`, `(*catalogServer).ListDirectoryChildren(ctx, req) (*pb.ListDirectoryChildrenResponse, error)`. Task 4 calls this RPC.

- [ ] **Step 1: Edit the proto**

In `src/api/catalog.proto`, add to the `CatalogService` block (after `rpc ListDirectoryFacets`):

```protobuf
  rpc ListDirectoryChildren(ListDirectoryChildrenRequest) returns (ListDirectoryChildrenResponse);
```

Add these messages after `ListFacetsResponse`:

```protobuf
message ListDirectoryChildrenRequest {
  string parent_path     = 1; // "" = true roots ("/", each distinct drive/UNC root)
  int64  received_after  = 2;
  int64  received_before = 3;
  repeated string source_hosts = 4;
  repeated string job_names    = 5;
  // No pattern field: directory browsing and pattern search are mutually
  // exclusive UI modes, never combined.
}

message DirectoryChild {
  string path         = 1; // full path, e.g. "/var/lib"
  string name         = 2; // short display label, e.g. "lib"
  int64  file_count   = 3; // direct files under path matching the current date/host/job filters
  int64  last_seen    = 4; // unix seconds, max(received_at) among those files; 0 if file_count == 0
  bool   has_children = 5; // true if catalog_directories has any row with parent_path == path
}

message ListDirectoryChildrenResponse {
  repeated DirectoryChild children = 1;
}
```

- [ ] **Step 2: Regenerate the protobuf/gRPC code**

Run: `cd src && protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative api/*.proto`
Expected: no output on success; `git diff --stat src/api/catalog.pb.go src/api/catalog_grpc.pb.go` shows new `ListDirectoryChildrenRequest`/`DirectoryChild`/`ListDirectoryChildrenResponse` types and a new `ListDirectoryChildren` method on `CatalogServiceClient`/`CatalogServiceServer`/`UnimplementedCatalogServiceServer`.

- [ ] **Step 3: Write the failing gRPC handler tests**

Append to `src/cmd/catalog/server_test.go`:

```go
func TestListDirectoryChildren_ReturnsTrueRootsForEmptyParentPath(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureDirectories([]catalogstore.DirectoryAncestor{
		{Path: "/", ParentPath: "", Name: "/", Depth: 0},
	}))

	resp, err := srv.ListDirectoryChildren(context.Background(), &pb.ListDirectoryChildrenRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetChildren(), 1)
	assert.Equal(t, "/", resp.GetChildren()[0].GetPath())
	assert.Equal(t, "/", resp.GetChildren()[0].GetName())
}

func TestListDirectoryChildren_ReturnsChildrenForGivenParentPath(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureDirectories([]catalogstore.DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))

	resp, err := srv.ListDirectoryChildren(context.Background(), &pb.ListDirectoryChildrenRequest{ParentPath: "/var"})
	require.NoError(t, err)
	require.Len(t, resp.GetChildren(), 1)
	assert.Equal(t, "/var/lib", resp.GetChildren()[0].GetPath())
	assert.Equal(t, "lib", resp.GetChildren()[0].GetName())
}

func TestListDirectoryChildren_AppliesDateAndHostAndJobFilters(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureDirectories([]catalogstore.DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
		{Path: "/var/lib", ParentPath: "/var", Name: "lib", Depth: 2},
	}))
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", ParentDirectory: "/var/lib", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListDirectoryChildren(context.Background(), &pb.ListDirectoryChildrenRequest{
		ParentPath:  "/var",
		SourceHosts: []string{"database"},
		JobNames:    []string{"nightly-db"},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetChildren(), 1)
	assert.Equal(t, int64(1), resp.GetChildren()[0].GetFileCount())

	resp, err = srv.ListDirectoryChildren(context.Background(), &pb.ListDirectoryChildrenRequest{
		ParentPath:  "/var",
		SourceHosts: []string{"webserver"},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetChildren(), 1)
	assert.Equal(t, int64(0), resp.GetChildren()[0].GetFileCount())
}
```

- [ ] **Step 4: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/catalog/... -run TestListDirectoryChildren -v`
Expected: FAIL — `srv.ListDirectoryChildren` undefined.

- [ ] **Step 5: Implement the handler**

In `src/cmd/catalog/server.go`, add after `ListDirectoryFacets`:

```go
func (s *catalogServer) ListDirectoryChildren(ctx context.Context, req *pb.ListDirectoryChildrenRequest) (*pb.ListDirectoryChildrenResponse, error) {
	children, err := s.store.ListDirectoryChildren(req.GetParentPath(), catalogstore.FacetFilter{
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		SourceHosts:    req.GetSourceHosts(),
		JobNames:       req.GetJobNames(),
	})
	if err != nil {
		s.logger.Error("ListDirectoryChildren: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list directory children: %v", err)
	}
	return &pb.ListDirectoryChildrenResponse{Children: toProtoDirectoryChildren(children)}, nil
}

func toProtoDirectoryChildren(children []catalogstore.DirectoryChild) []*pb.DirectoryChild {
	out := make([]*pb.DirectoryChild, len(children))
	for i, c := range children {
		var lastSeen int64
		if !c.LastSeen.IsZero() {
			lastSeen = c.LastSeen.Unix()
		}
		out[i] = &pb.DirectoryChild{
			Path:        c.Path,
			Name:        c.Name,
			FileCount:   c.FileCount,
			LastSeen:    lastSeen,
			HasChildren: c.HasChildren,
		}
	}
	return out
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS (whole package)

- [ ] **Step 7: Commit**

```bash
git add src/api/catalog.proto src/api/catalog.pb.go src/api/catalog_grpc.pb.go src/cmd/catalog/server.go src/cmd/catalog/server_test.go
git commit -m "feat(api): add ListDirectoryChildren RPC"
```

---

## Task 4: api-server REST endpoint

**Files:**
- Modify: `src/cmd/api-server/catalog.go`
- Modify: `src/cmd/api-server/server.go`
- Test: `src/cmd/api-server/catalog_test.go`

**Interfaces:**
- Consumes: `pb.ListDirectoryChildrenRequest`/`pb.ListDirectoryChildrenResponse`/`pb.DirectoryChild` (Task 3).
- Produces: `GET /api/v1/catalog/directories/children`, `directoryChildDTO{Path, Name, FileCount, LastSeen, HasChildren}` (JSON: `path`, `name`, `file_count`, `last_seen`, `has_children`). Task 8 (frontend) calls this endpoint.

- [ ] **Step 1: Write the failing handler tests**

In `src/cmd/api-server/catalog_test.go`, add to the `fakeCatalogQueryClient` struct:

```go
	childrenResp    *pb.ListDirectoryChildrenResponse
	childrenErr     error
	lastChildrenReq *pb.ListDirectoryChildrenRequest
```

and a new method alongside the other fake methods:

```go
func (f *fakeCatalogQueryClient) ListDirectoryChildren(ctx context.Context, in *pb.ListDirectoryChildrenRequest, opts ...grpc.CallOption) (*pb.ListDirectoryChildrenResponse, error) {
	f.lastChildrenReq = in
	return f.childrenResp, f.childrenErr
}
```

Append these tests to the file:

```go
func TestHandleListCatalogDirectoryChildren_ReturnsData(t *testing.T) {
	fake := &fakeCatalogQueryClient{childrenResp: &pb.ListDirectoryChildrenResponse{
		Children: []*pb.DirectoryChild{{Path: "/var/lib", Name: "lib", FileCount: 2, LastSeen: 1752400000, HasChildren: true}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories/children?parent_path=/var", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	child := data[0].(map[string]any)
	assert.Equal(t, "/var/lib", child["path"])
	assert.Equal(t, "lib", child["name"])
	assert.Equal(t, float64(2), child["file_count"])
	assert.Equal(t, true, child["has_children"])
}

func TestHandleListCatalogDirectoryChildren_PassesParentPathAndFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{childrenResp: &pb.ListDirectoryChildrenResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories/children?parent_path=/var/lib&received_after=1000&source_hosts=database&job_names=nightly-db", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastChildrenReq)
	assert.Equal(t, "/var/lib", fake.lastChildrenReq.GetParentPath())
	assert.Equal(t, int64(1000), fake.lastChildrenReq.GetReceivedAfter())
	assert.Equal(t, []string{"database"}, fake.lastChildrenReq.GetSourceHosts())
	assert.Equal(t, []string{"nightly-db"}, fake.lastChildrenReq.GetJobNames())
}

func TestHandleListCatalogDirectoryChildren_OmittedParentPathMeansRoot(t *testing.T) {
	fake := &fakeCatalogQueryClient{childrenResp: &pb.ListDirectoryChildrenResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories/children", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastChildrenReq)
	assert.Equal(t, "", fake.lastChildrenReq.GetParentPath())
}

func TestHandleListCatalogDirectoryChildren_InvalidReceivedAfterReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories/children?received_after=not-a-number", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleListCatalogDirectoryChildren -v`
Expected: FAIL — compile error (`fakeCatalogQueryClient` doesn't implement `catalogQueryClient`'s not-yet-added method, `handleListCatalogDirectoryChildren` undefined, route not registered).

- [ ] **Step 3: Implement the DTO, handler, route, and interface method**

In `src/cmd/api-server/catalog.go`, add after `handleListCatalogDirectories`:

```go
type directoryChildDTO struct {
	Path        string `json:"path"`
	Name        string `json:"name"`
	FileCount   int64  `json:"file_count"`
	LastSeen    int64  `json:"last_seen"`
	HasChildren bool   `json:"has_children"`
}

func toDirectoryChildDTO(c *pb.DirectoryChild) directoryChildDTO {
	return directoryChildDTO{
		Path:        c.GetPath(),
		Name:        c.GetName(),
		FileCount:   c.GetFileCount(),
		LastSeen:    c.GetLastSeen(),
		HasChildren: c.GetHasChildren(),
	}
}

func (s *server) handleListCatalogDirectoryChildren(w http.ResponseWriter, r *http.Request) {
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

	resp, err := s.catalog.ListDirectoryChildren(r.Context(), &pb.ListDirectoryChildrenRequest{
		ParentPath:     q.Get("parent_path"),
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		SourceHosts:    splitCommaParam(q.Get("source_hosts")),
		JobNames:       splitCommaParam(q.Get("job_names")),
	})
	if err != nil {
		s.logger.Error("handleListCatalogDirectoryChildren: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	children := make([]directoryChildDTO, len(resp.GetChildren()))
	for i, c := range resp.GetChildren() {
		children[i] = toDirectoryChildDTO(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": children})
}
```

In `src/cmd/api-server/server.go`, add to the `catalogQueryClient` interface:

```go
	ListDirectoryChildren(ctx context.Context, in *pb.ListDirectoryChildrenRequest, opts ...grpc.CallOption) (*pb.ListDirectoryChildrenResponse, error)
```

and register the route (after `GET /api/v1/catalog/directories`):

```go
	mux.HandleFunc("GET /api/v1/catalog/directories/children", s.handleListCatalogDirectoryChildren)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS (whole package)

- [ ] **Step 5: Run the full Go test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: PASS everywhere (confirms nothing else implements `catalogQueryClient` and needs the new method, and the module still builds)

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/catalog.go src/cmd/api-server/server.go src/cmd/api-server/catalog_test.go
git commit -m "feat(api-server): add GET /catalog/directories/children"
```

---

## Task 5: Frontend path-splitting utility

**Files:**
- Create: `web/src/utils/pathSplit.js`
- Test: `web/src/utils/pathSplit.spec.js`

**Interfaces:**
- Produces: `splitPath(path: string) -> { parentPath: string, name: string }`, `pathCrumbs(path: string) -> Array<{ path: string, name: string }>`. Task 7 (`DirectoryPathBar.vue`) consumes `pathCrumbs`.

This is a display-only utility (mirrors `src/cmd/catalog/pathsplit.go`'s shape-detection) for rendering breadcrumb segments of an already-known `currentPath` — it never re-derives directory structure, which always comes from the backend.

- [ ] **Step 1: Write the failing tests**

Create `web/src/utils/pathSplit.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { splitPath, pathCrumbs } from './pathSplit'

describe('splitPath', () => {
  it('splits a nested unix path', () => {
    expect(splitPath('/var/lib/dbdata')).toEqual({ parentPath: '/var/lib', name: 'dbdata' })
  })

  it('splits a unix root-level path', () => {
    expect(splitPath('/data.db')).toEqual({ parentPath: '/', name: 'data.db' })
  })

  it('splits a windows nested path', () => {
    expect(splitPath('C:\\Users\\alice\\Documents')).toEqual({ parentPath: 'C:\\Users\\alice', name: 'Documents' })
  })

  it('splits a windows drive-root path', () => {
    expect(splitPath('C:\\file.txt')).toEqual({ parentPath: 'C:\\', name: 'file.txt' })
  })

  it('returns empty parent and name for empty input', () => {
    expect(splitPath('')).toEqual({ parentPath: '', name: '' })
  })
})

describe('pathCrumbs', () => {
  it('returns root-first crumbs for a nested unix path', () => {
    expect(pathCrumbs('/var/lib/dbdata')).toEqual([
      { path: '/', name: '/' },
      { path: '/var', name: 'var' },
      { path: '/var/lib', name: 'lib' },
      { path: '/var/lib/dbdata', name: 'dbdata' },
    ])
  })

  it('returns a single crumb for the unix root itself', () => {
    expect(pathCrumbs('/')).toEqual([{ path: '/', name: '/' }])
  })

  it('returns root-first crumbs for a windows drive path', () => {
    expect(pathCrumbs('C:\\Users\\alice')).toEqual([
      { path: 'C:\\', name: 'C:\\' },
      { path: 'C:\\Users', name: 'Users' },
      { path: 'C:\\Users\\alice', name: 'alice' },
    ])
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/utils/pathSplit.spec.js`
Expected: FAIL — `./pathSplit` module not found.

- [ ] **Step 3: Implement `pathSplit.js`**

Create `web/src/utils/pathSplit.js`:

```js
// splitPath mirrors src/cmd/catalog/pathsplit.go's splitPath: derive
// (parentPath, name) for a directory path, choosing separator style from
// the path's own shape (leading "/" vs a drive-letter or UNC prefix)
// rather than assuming Unix, since a catalog mixes entries from both
// platforms. Used client-side only to render breadcrumb segments for a
// known currentPath -- never to re-derive directory structure, which
// always comes from the backend's catalog_directories table.
export function splitPath(path) {
  if (!path) return { parentPath: '', name: '' }
  let sep = '/'
  if (isWindowsStyle(path)) {
    sep = path.includes('\\') ? '\\' : '/'
  }
  const idx = path.lastIndexOf(sep)
  if (idx < 0) return { parentPath: '', name: path }
  let parentPath = path.slice(0, idx)
  const name = path.slice(idx + 1)
  if (name === '') return splitPath(parentPath) // tolerate a trailing separator, strip and retry
  if (parentPath === '') parentPath = sep
  else if (isDriveRoot(parentPath)) parentPath += sep
  return { parentPath, name }
}

function isWindowsStyle(path) {
  if (path.startsWith('\\\\')) return true
  return isDriveRoot(path.slice(0, 2))
}

function isDriveRoot(s) {
  return s.length === 2 && s[1] === ':' && /^[a-zA-Z]$/.test(s[0])
}

// pathCrumbs returns [{path, name}] from the true root down to path
// itself, root first -- the reverse walk of
// cmd/catalog/server.go's decodeDirectoryAncestors.
export function pathCrumbs(path) {
  const crumbs = []
  let current = path
  while (current) {
    const { parentPath, name } = splitPath(current)
    crumbs.unshift({ path: current, name: parentPath === '' ? current : name })
    current = parentPath
  }
  return crumbs
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/utils/pathSplit.spec.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src/utils/pathSplit.js web/src/utils/pathSplit.spec.js
git commit -m "feat(web): add client-side path-splitting utility for breadcrumbs"
```

---

## Task 6: Catalog store browse-mode state and actions

**Files:**
- Modify: `web/src/stores/catalog.js`
- Test: `web/src/stores/catalog.spec.js`

**Interfaces:**
- Produces: state `currentPath` (default `null`), `directoryChildren`, `directoryChildrenLoading`, `directoryChildrenError`; actions `fetchDirectoryChildren()`, `refresh()`, `navigateTo(path)`, `navigateHome()`; `search()` now scopes by `parent_directories=[currentPath]` when browsing. Task 8 (`CatalogView.vue`) consumes all of these.

- [ ] **Step 1: Write the failing tests**

Append to `web/src/stores/catalog.spec.js`, before the final closing `})` of the outer `describe('catalog store', ...)`:

```js
  describe('search with currentPath', () => {
    it('scopes the query to currentPath via parent_directories when browsing a folder', async () => {
      apiFetch.mockResolvedValue({ data: [], has_more: false })
      const catalog = useCatalogStore()
      catalog.currentPath = '/var/lib/dbdata'

      await catalog.search()

      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('parent_directories=%2Fvar%2Flib%2Fdbdata'))
    })

    it('ignores currentPath and omits parent_directories when a pattern is set', async () => {
      apiFetch.mockResolvedValue({ data: [], has_more: false })
      const catalog = useCatalogStore()
      catalog.currentPath = '/var/lib/dbdata'
      catalog.filters.pattern = 'dbdata'

      await catalog.search()

      expect(apiFetch).toHaveBeenCalledWith(expect.not.stringContaining('parent_directories'))
    })
  })

  describe('fetchDirectoryChildren', () => {
    it('queries /catalog/directories/children for the root when currentPath is null', async () => {
      apiFetch.mockResolvedValue({ data: [{ path: '/', name: '/', file_count: 0, last_seen: 0, has_children: true }] })
      const catalog = useCatalogStore()

      await catalog.fetchDirectoryChildren()

      const now = Math.floor(Date.now() / 1000)
      expect(apiFetch).toHaveBeenCalledWith(
        `/catalog/directories/children?parent_path=&received_after=${now - 7 * DAY}&received_before=${now}`
      )
      expect(catalog.directoryChildren).toEqual([
        { path: '/', name: '/', file_count: 0, last_seen: 0, has_children: true },
      ])
    })

    it('queries the current path when browsing a folder', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const catalog = useCatalogStore()
      catalog.currentPath = '/var/lib'

      await catalog.fetchDirectoryChildren()

      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('parent_path=%2Fvar%2Flib'))
    })

    it('sets directoryChildrenError without touching the results error on failure', async () => {
      apiFetch.mockRejectedValue(new Error('boom'))
      const catalog = useCatalogStore()

      await catalog.fetchDirectoryChildren()

      expect(catalog.directoryChildrenError).toBe('boom')
      expect(catalog.error).toBe(null)
    })
  })

  describe('refresh', () => {
    it('fetches directory children only, and clears entries, when at the root with no pattern', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const catalog = useCatalogStore()
      catalog.entries = [{ id: 1 }]

      await catalog.refresh()

      expect(apiFetch).toHaveBeenCalledTimes(1)
      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('/catalog/directories/children'))
      expect(catalog.entries).toEqual([])
    })

    it('fetches both directory children and entries when browsing a folder', async () => {
      apiFetch.mockResolvedValue({ data: [], has_more: false })
      const catalog = useCatalogStore()
      catalog.currentPath = '/var/lib'

      await catalog.refresh()

      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('/catalog/directories/children'))
      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('/catalog?'))
    })

    it('fetches only entries, and clears directoryChildren, when a pattern is set', async () => {
      apiFetch.mockResolvedValue({ data: [], has_more: false })
      const catalog = useCatalogStore()
      catalog.filters.pattern = 'dbdata'
      catalog.directoryChildren = [{ path: '/var', name: 'var', file_count: 1, last_seen: 1, has_children: false }]

      await catalog.refresh()

      expect(apiFetch).toHaveBeenCalledTimes(1)
      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('/catalog?'))
      expect(catalog.directoryChildren).toEqual([])
    })
  })

  describe('navigateTo / navigateHome', () => {
    it('navigateTo sets currentPath and refreshes', async () => {
      apiFetch.mockResolvedValue({ data: [], has_more: false })
      const catalog = useCatalogStore()

      await catalog.navigateTo('/var/lib')

      expect(catalog.currentPath).toBe('/var/lib')
      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('parent_path=%2Fvar%2Flib'))
    })

    it('navigateHome clears currentPath and refreshes', async () => {
      apiFetch.mockResolvedValue({ data: [] })
      const catalog = useCatalogStore()
      catalog.currentPath = '/var/lib'

      await catalog.navigateHome()

      expect(catalog.currentPath).toBe(null)
      expect(apiFetch).toHaveBeenCalledWith(expect.stringContaining('parent_path=&'))
    })
  })
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/stores/catalog.spec.js`
Expected: FAIL — `catalog.fetchDirectoryChildren`/`refresh`/`navigateTo`/`navigateHome` undefined, `currentPath` undefined.

- [ ] **Step 3: Implement the store changes**

Replace the full contents of `web/src/stores/catalog.js` with:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

const MAX_PAGE_LIMIT = 500
const DEFAULT_RANGE_SECONDS = 7 * 24 * 60 * 60

function buildQuery(filters, parentDirectories, startingAfter, limit) {
  const params = new URLSearchParams()
  if (filters.receivedAfter) params.set('received_after', String(filters.receivedAfter))
  if (filters.receivedBefore) params.set('received_before', String(filters.receivedBefore))
  if (filters.sourceHosts?.length) params.set('source_hosts', filters.sourceHosts.join(','))
  if (filters.jobNames?.length) params.set('job_names', filters.jobNames.join(','))
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (parentDirectories?.length) params.set('parent_directories', parentDirectories.join(','))
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  params.set('limit', String(limit))
  return params.toString()
}

// buildFacetQuery mirrors buildQuery but excludes `exclude` (the facet's
// own dimension -- 'sourceHosts' for the clients facet, 'jobNames' for the
// jobs facet) so a facet list is never narrowed by its own current
// selection.
function buildFacetQuery(filters, exclude) {
  const params = new URLSearchParams()
  if (filters.receivedAfter) params.set('received_after', String(filters.receivedAfter))
  if (filters.receivedBefore) params.set('received_before', String(filters.receivedBefore))
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (exclude !== 'sourceHosts' && filters.sourceHosts?.length) {
    params.set('source_hosts', filters.sourceHosts.join(','))
  }
  if (exclude !== 'jobNames' && filters.jobNames?.length) {
    params.set('job_names', filters.jobNames.join(','))
  }
  return params.toString()
}

// buildChildrenQuery narrows ListDirectoryChildren by date/host/job, same
// as buildFacetQuery, plus the parent_path being browsed. No pattern
// param: directory browsing and pattern search are mutually exclusive
// modes (see refresh()).
function buildChildrenQuery(filters, parentPath) {
  const params = new URLSearchParams()
  params.set('parent_path', parentPath ?? '')
  if (filters.receivedAfter) params.set('received_after', String(filters.receivedAfter))
  if (filters.receivedBefore) params.set('received_before', String(filters.receivedBefore))
  if (filters.sourceHosts?.length) params.set('source_hosts', filters.sourceHosts.join(','))
  if (filters.jobNames?.length) params.set('job_names', filters.jobNames.join(','))
  return params.toString()
}

export const useCatalogStore = defineStore('catalog', {
  state: () => {
    const now = Math.floor(Date.now() / 1000)
    return {
      filters: {
        pattern: '',
        receivedAfter: now - DEFAULT_RANGE_SECONDS,
        receivedBefore: now,
        sourceHosts: [],
        jobNames: [],
      },
      currentPath: null,
      entries: [],
      loading: false,
      error: null,
      clientFacets: [],
      clientFacetsLoading: false,
      clientFacetsError: null,
      jobFacets: [],
      jobFacetsLoading: false,
      jobFacetsError: null,
      directoryChildren: [],
      directoryChildrenLoading: false,
      directoryChildrenError: null,
      _searchToken: 0,
      _clientFacetsToken: 0,
      _jobFacetsToken: 0,
      _directoryChildrenToken: 0,
    }
  },
  actions: {
    async search() {
      const token = ++this._searchToken
      const parentDirectories = this.filters.pattern ? [] : this.currentPath ? [this.currentPath] : []
      try {
        await withRequest(this, async () => {
          const collected = []
          let startingAfter
          for (;;) {
            const qs = buildQuery(this.filters, parentDirectories, startingAfter, MAX_PAGE_LIMIT)
            const body = await apiFetch(`/catalog?${qs}`)
            if (token !== this._searchToken) return // superseded by a newer search
            collected.push(...body.data)
            if (!body.has_more || body.data.length === 0) break
            startingAfter = body.data[body.data.length - 1].id
          }
          if (token === this._searchToken) this.entries = collected
        })
      } catch {
        // withRequest already recorded this.error; discard any partial or
        // stale results rather than leaving a previous search's rows on screen.
        if (token === this._searchToken) this.entries = []
      }
    },
    async fetchClientFacets() {
      const token = ++this._clientFacetsToken
      await withRequest(
        this,
        async () => {
          const qs = buildFacetQuery(this.filters, 'sourceHosts')
          const body = await apiFetch(`/catalog/clients?${qs}`)
          if (token === this._clientFacetsToken) this.clientFacets = body.data
        },
        { rethrow: false, loadingKey: 'clientFacetsLoading', errorKey: 'clientFacetsError' }
      )
    },
    async fetchJobFacets() {
      const token = ++this._jobFacetsToken
      await withRequest(
        this,
        async () => {
          const qs = buildFacetQuery(this.filters, 'jobNames')
          const body = await apiFetch(`/catalog/jobs?${qs}`)
          if (token === this._jobFacetsToken) this.jobFacets = body.data
        },
        { rethrow: false, loadingKey: 'jobFacetsLoading', errorKey: 'jobFacetsError' }
      )
    },
    async fetchDirectoryChildren() {
      const token = ++this._directoryChildrenToken
      await withRequest(
        this,
        async () => {
          const qs = buildChildrenQuery(this.filters, this.currentPath)
          const body = await apiFetch(`/catalog/directories/children?${qs}`)
          if (token === this._directoryChildrenToken) this.directoryChildren = body.data
        },
        { rethrow: false, loadingKey: 'directoryChildrenLoading', errorKey: 'directoryChildrenError' }
      )
    },
    // refresh re-fetches whatever the current view needs: a pattern
    // search is a flat, cross-directory mode (no folder rows, entries
    // unscoped by currentPath); otherwise it's browse mode, which always
    // re-fetches the current folder's children, plus that folder's
    // direct files if currentPath isn't the synthetic root/Home screen.
    async refresh() {
      if (this.filters.pattern) {
        this.directoryChildren = []
        await this.search()
        return
      }
      await this.fetchDirectoryChildren()
      if (this.currentPath !== null) {
        await this.search()
      } else {
        this.entries = []
      }
    },
    navigateTo(path) {
      this.currentPath = path
      return this.refresh()
    },
    navigateHome() {
      this.currentPath = null
      return this.refresh()
    },
  },
})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/stores/catalog.spec.js`
Expected: PASS (whole file, including pre-existing tests)

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/catalog.js web/src/stores/catalog.spec.js
git commit -m "feat(web): add directory browse-mode state to the catalog store"
```

---

## Task 7: `DirectoryPathBar` component

**Files:**
- Create: `web/src/components/catalog/DirectoryPathBar.vue`
- Test: `web/src/components/catalog/DirectoryPathBar.spec.js`

**Interfaces:**
- Consumes: `pathCrumbs` (Task 5).
- Produces: `<DirectoryPathBar :current-path="String|null" @navigate="(path: string|null) => void" />`. Task 8 (`CatalogView.vue`) consumes this component.

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/catalog/DirectoryPathBar.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DirectoryPathBar from './DirectoryPathBar.vue'

describe('DirectoryPathBar', () => {
  it('shows only Home when currentPath is null', () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: null } })
    expect(wrapper.text()).toBe('Home')
  })

  it('renders root-to-leaf crumbs for a nested unix path, only the last one non-clickable', () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: '/var/lib/dbdata' } })
    const clickable = wrapper.findAll('[data-test="crumb"]').map((c) => c.text())
    expect(clickable).toEqual(['/', 'var', 'lib'])
    expect(wrapper.find('[data-test="crumb-current"]').text()).toBe('dbdata')
  })

  it('renders a windows drive-rooted path correctly', () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: 'C:\\Users\\alice\\Documents' } })
    const clickable = wrapper.findAll('[data-test="crumb"]').map((c) => c.text())
    expect(clickable).toEqual(['C:\\', 'Users', 'alice'])
    expect(wrapper.find('[data-test="crumb-current"]').text()).toBe('Documents')
  })

  it('emits navigate with null when Home is clicked', async () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: '/var/lib' } })
    await wrapper.find('[data-test="crumb-home"]').trigger('click')
    expect(wrapper.emitted('navigate')).toEqual([[null]])
  })

  it('emits navigate with the crumb path when an intermediate crumb is clicked', async () => {
    const wrapper = mount(DirectoryPathBar, { props: { currentPath: '/var/lib/dbdata' } })
    const crumbs = wrapper.findAll('[data-test="crumb"]')
    await crumbs[1].trigger('click') // "var"
    expect(wrapper.emitted('navigate')).toEqual([['/var']])
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/components/catalog/DirectoryPathBar.spec.js`
Expected: FAIL — `./DirectoryPathBar.vue` not found.

- [ ] **Step 3: Implement the component**

Create `web/src/components/catalog/DirectoryPathBar.vue`:

```vue
<script setup>
import { computed } from 'vue'
import { pathCrumbs } from '../../utils/pathSplit'

const props = defineProps({
  currentPath: { type: String, default: null },
})
const emit = defineEmits(['navigate'])

const crumbs = computed(() => (props.currentPath ? pathCrumbs(props.currentPath) : []))
</script>

<template>
  <nav
    data-test="directory-path-bar"
    aria-label="Directory path"
    class="flex items-center gap-1 text-sm text-gray-600 mb-2"
  >
    <button type="button" data-test="crumb-home" class="hover:underline" @click="emit('navigate', null)">
      Home
    </button>
    <template v-for="(crumb, index) in crumbs" :key="crumb.path">
      <span class="text-gray-400">&rsaquo;</span>
      <button
        v-if="index < crumbs.length - 1"
        type="button"
        data-test="crumb"
        class="hover:underline"
        @click="emit('navigate', crumb.path)"
      >
        {{ crumb.name }}
      </button>
      <span v-else data-test="crumb-current" class="font-semibold">{{ crumb.name }}</span>
    </template>
  </nav>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/components/catalog/DirectoryPathBar.spec.js`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add web/src/components/catalog/DirectoryPathBar.vue web/src/components/catalog/DirectoryPathBar.spec.js
git commit -m "feat(web): add DirectoryPathBar breadcrumb component"
```

---

## Task 8: `CatalogView.vue` browse-mode integration

**Files:**
- Modify: `web/src/views/CatalogView.vue`
- Test: `web/src/views/CatalogView.spec.js`

**Interfaces:**
- Consumes: `catalog.currentPath`, `catalog.directoryChildren`, `catalog.directoryChildrenLoading`, `catalog.directoryChildrenError`, `catalog.refresh()`, `catalog.navigateTo(path)`, `catalog.navigateHome()` (Task 6); `<DirectoryPathBar>` (Task 7).

This task replaces the flat-only table with folder rows above file rows while browsing, wires the path bar, and switches `onMounted`/the filter watchers from calling `catalog.search()` directly to calling the new `catalog.refresh()` orchestrator.

- [ ] **Step 1: Update `CatalogView.spec.js`**

Replace the full contents of `web/src/views/CatalogView.spec.js` with:

```js
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'
import DateRangePanel from '../components/catalog/DateRangePanel.vue'
import FacetPanel from '../components/catalog/FacetPanel.vue'
import DirectoryPathBar from '../components/catalog/DirectoryPathBar.vue'

function entry(overrides) {
  return {
    id: 1,
    source_host: 'database',
    store_host: 'bwfs-east',
    job_id: 'backup:daily-db-backup:1',
    object_id: 'fs://database:f:/var/lib/dbdata/data.db:1752400000',
    ctime: 1752400000,
    store_created_at: 1752400000,
    received_at: 1752400010,
    path: '/var/lib/dbdata/data.db',
    parent_directory: '/var/lib/dbdata',
    short_filename: 'data.db',
    size: 8192,
    mode: '-rw-r--r--',
    owner: 999,
    group: 999,
    mod_time: 1752400000,
    ...overrides,
  }
}

function mountView(state) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: {
      catalog: {
        currentPath: null,
        entries: [],
        loading: false,
        error: null,
        filters: { pattern: '', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
        clientFacets: [],
        clientFacetsError: null,
        jobFacets: [],
        jobFacetsError: null,
        directoryChildren: [],
        directoryChildrenLoading: false,
        directoryChildrenError: null,
        ...state,
      },
    },
  })
  const wrapper = mount(CatalogView, {
    global: { plugins: [pinia], stubs: { DateRangePanel: true, FacetPanel: true } },
  })
  return { wrapper, catalog: useCatalogStore() }
}

describe('CatalogView', () => {
  beforeEach(() => {
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('refreshes and fetches both facet lists on mount', () => {
    const { catalog } = mountView({})
    expect(catalog.refresh).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
  })

  it('opens the date panel by default', () => {
    const { wrapper } = mountView({})
    expect(wrapper.findComponent(DateRangePanel).exists()).toBe(true)
    expect(wrapper.findComponent(FacetPanel).exists()).toBe(false)
  })

  it('switches to the clients panel when its chip is clicked', async () => {
    const { wrapper } = mountView({})
    await wrapper.find('[data-test="chip-clients"]').trigger('click')
    expect(wrapper.findComponent(DateRangePanel).exists()).toBe(false)
    const panel = wrapper.findComponent(FacetPanel)
    expect(panel.exists()).toBe(true)
    expect(panel.props('nameLabel')).toBe('Client')
  })

  it('switches to the jobs panel when its chip is clicked', async () => {
    const { wrapper } = mountView({})
    await wrapper.find('[data-test="chip-jobs"]').trigger('click')
    const panel = wrapper.findComponent(FacetPanel)
    expect(panel.exists()).toBe(true)
    expect(panel.props('nameLabel')).toBe('Policy')
  })

  it('closes the open panel when its own chip is clicked again', async () => {
    const { wrapper } = mountView({})
    expect(wrapper.findComponent(DateRangePanel).exists()).toBe(true)
    await wrapper.find('[data-test="chip-date"]').trigger('click')
    expect(wrapper.findComponent(DateRangePanel).exists()).toBe(false)
  })

  it('re-refreshes and re-fetches both facet lists when the date range changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.refresh.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.receivedAfter = 500
    await wrapper.vm.$nextTick()

    expect(catalog.refresh).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
  })

  it('re-refreshes and only re-fetches job facets when the client selection changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.refresh.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.sourceHosts.push('database')
    await wrapper.vm.$nextTick()

    expect(catalog.refresh).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).not.toHaveBeenCalled()
  })

  it('re-refreshes and only re-fetches client facets when the job selection changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.refresh.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.jobNames.push('nightly-db')
    await wrapper.vm.$nextTick()

    expect(catalog.refresh).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).not.toHaveBeenCalled()
  })

  it('debounces path input before refreshing', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.refresh.mockClear()

    await wrapper.find('[data-test="path-input"]').setValue('dbdata')
    expect(catalog.refresh).not.toHaveBeenCalled()

    vi.advanceTimersByTime(300)
    await flushPromises()
    expect(catalog.refresh).toHaveBeenCalledTimes(1)
  })

  it('shows a no-results message when there are no entries or folders', () => {
    const { wrapper } = mountView({})
    expect(wrapper.text()).toContain('No entries match this filter.')
  })

  it('renders folder rows above file rows when browsing', () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      directoryChildren: [{ path: '/var/lib/dbdata/backups', name: 'backups', file_count: 3, last_seen: 1752400010, has_children: false }],
      entries: [entry({ id: 1 })],
    })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('backups/')
    expect(rows[1].text()).toContain('data.db')
  })

  it('navigates into a folder when its row is clicked', async () => {
    const { wrapper, catalog } = mountView({
      directoryChildren: [{ path: '/var', name: 'var', file_count: 0, last_seen: 0, has_children: true }],
    })
    await wrapper.find('tbody tr').trigger('click')
    expect(catalog.navigateTo).toHaveBeenCalledWith('/var')
  })

  it('shows the directory path bar while browsing', () => {
    const { wrapper } = mountView({ currentPath: '/var/lib' })
    expect(wrapper.find('[data-test="directory-path-bar"]').exists()).toBe(true)
  })

  it('hides the directory path bar during pattern search', () => {
    const { wrapper } = mountView({
      filters: { pattern: 'dbdata', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
    })
    expect(wrapper.find('[data-test="directory-path-bar"]').exists()).toBe(false)
  })

  it('navigates home when the path bar emits a null path', async () => {
    const { wrapper, catalog } = mountView({ currentPath: '/var/lib' })
    await wrapper.findComponent(DirectoryPathBar).vm.$emit('navigate', null)
    expect(catalog.navigateHome).toHaveBeenCalled()
  })

  it('navigates to a crumb path when the path bar emits it', async () => {
    const { wrapper, catalog } = mountView({ currentPath: '/var/lib' })
    await wrapper.findComponent(DirectoryPathBar).vm.$emit('navigate', '/var')
    expect(catalog.navigateTo).toHaveBeenCalledWith('/var')
  })

  it('groups entries sharing source_host and path into a single row with a version count', () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [
        entry({ id: 1, store_created_at: 1752300000, size: 8004 }),
        entry({ id: 2, store_created_at: 1752400000, size: 8192 }),
      ],
    })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('data.db')
    expect(rows[0].text()).toContain('8.0 KB')
    expect(rows[0].text()).toContain('2')
  })

  it('renders a single-version file without a version count', () => {
    const { wrapper } = mountView({ currentPath: '/var/lib/dbdata', entries: [entry({ id: 1 })] })
    const rows = wrapper.findAll('tbody tr')
    const cells = rows[0].findAll('td')
    expect(cells[cells.length - 1].text()).toBe('')
  })

  it('opens the versions modal for the row actually clicked, even after sorting reorders the table', async () => {
    const { wrapper } = mountView({
      filters: { pattern: 'x', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
      entries: [
        entry({ id: 3, source_host: 'webserver', path: '/var/www/index.html', store_created_at: 1752350000 }),
        entry({ id: 1, source_host: 'database', path: '/var/lib/dbdata/data.db', store_created_at: 1752300000 }),
        entry({ id: 2, source_host: 'database', path: '/var/lib/dbdata/data.db', store_created_at: 1752400000 }),
      ],
    })

    await wrapper.find('thead th button').trigger('click')
    await flushPromises()
    const sortedRows = wrapper.findAll('tbody tr')
    expect(sortedRows[0].text()).toContain('/var/lib/dbdata/data.db')

    await sortedRows[0].trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.text()).toContain('Versions of /var/lib/dbdata/data.db on database')
  })

  it('does not open the versions modal when a single-version row is clicked', async () => {
    const { wrapper } = mountView({ currentPath: '/var/lib/dbdata', entries: [entry({ id: 1 })] })
    await wrapper.find('tbody tr').trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('closes the versions modal via its Close button', async () => {
    const { wrapper } = mountView({
      currentPath: '/var/lib/dbdata',
      entries: [
        entry({ id: 1, store_created_at: 1752300000 }),
        entry({ id: 2, store_created_at: 1752400000 }),
      ],
    })
    await wrapper.find('tbody tr').trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(true)

    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Close')
      .trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('shows the store error message when present', () => {
    const { wrapper } = mountView({ error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })

  it('renders a single-segment breadcrumb', () => {
    const { wrapper } = mountView({})
    expect(wrapper.find('[data-test="breadcrumb"]').text()).toBe('Catalog')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: FAIL — `catalog.refresh` never called (view still calls `search` directly), no folder rows, no path bar, `Path` column shows full path instead of `short_filename` while browsing.

- [ ] **Step 3: Rewrite `CatalogView.vue`**

Replace the full contents of `web/src/views/CatalogView.vue` with:

```vue
<script setup>
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import DateRangePanel from '../components/catalog/DateRangePanel.vue'
import FacetPanel from '../components/catalog/FacetPanel.vue'
import DirectoryPathBar from '../components/catalog/DirectoryPathBar.vue'
import VersionsModal from '../components/VersionsModal.vue'

const catalog = useCatalogStore()
const activePanel = ref('date')
const selectedGroup = ref(null)

// browsing is true whenever we're not in the flat, cross-directory
// pattern-search mode -- the two are mutually exclusive (see the
// catalog store's refresh()).
const browsing = computed(() => !catalog.filters.pattern)

const folderRows = computed(() =>
  catalog.directoryChildren.map((d) => ({
    isFolder: true,
    path: d.path,
    name: d.name,
    file_count: d.file_count,
    last_seen: d.last_seen,
  }))
)
const fileRows = computed(() => groupEntriesByFile(catalog.entries).map((g) => ({ isFolder: false, ...g })))
// Folders always precede files -- directoryChildren is empty during
// pattern search (refresh() clears it), so this needs no extra branching.
const rows = computed(() => [...folderRows.value, ...fileRows.value])

function summaryLabel(names, allLabel) {
  if (names.length === 0) return allLabel
  if (names.length <= 2) return names.join(', ')
  return `${names.length} selected`
}
const clientsSummary = computed(() => summaryLabel(catalog.filters.sourceHosts, 'All hosts'))
const jobsSummary = computed(() => summaryLabel(catalog.filters.jobNames, 'All policies'))
const dateSummary = computed(() => {
  const days = Math.round((catalog.filters.receivedBefore - catalog.filters.receivedAfter) / 86400)
  return `Last ${days} day${days === 1 ? '' : 's'}`
})

function togglePanel(name) {
  activePanel.value = activePanel.value === name ? null : name
}

function onRowClick(row) {
  if (row.isFolder) {
    catalog.navigateTo(row.path)
    return
  }
  if (row.versions.length > 1) selectedGroup.value = row
}

function onPathBarNavigate(path) {
  if (path === null) catalog.navigateHome()
  else catalog.navigateTo(path)
}

onMounted(() => {
  catalog.refresh()
  catalog.fetchClientFacets()
  catalog.fetchJobFacets()
})

let pathDebounce
watch(
  () => catalog.filters.pattern,
  () => {
    clearTimeout(pathDebounce)
    pathDebounce = setTimeout(() => {
      catalog.refresh()
      catalog.fetchClientFacets()
      catalog.fetchJobFacets()
    }, 300)
  }
)
onUnmounted(() => clearTimeout(pathDebounce))
watch(
  () => [catalog.filters.receivedAfter, catalog.filters.receivedBefore],
  () => {
    catalog.refresh()
    catalog.fetchClientFacets()
    catalog.fetchJobFacets()
  }
)
watch(
  () => catalog.filters.jobNames,
  () => {
    catalog.refresh()
    catalog.fetchClientFacets()
  },
  { deep: true }
)
watch(
  () => catalog.filters.sourceHosts,
  () => {
    catalog.refresh()
    catalog.fetchJobFacets()
  },
  { deep: true }
)

const baseColumns = [
  { label: 'Path', field: 'path', sortable: true },
  { label: 'Source Host', field: 'sourceHost', sortable: true },
  { label: 'Store Host', field: 'representative.store_host', sortable: true },
  { label: 'Size', field: 'representative.size', sortable: true, type: 'number' },
  { label: 'Mode', field: 'representative.mode', sortable: true },
  { label: 'Modified', field: 'representative.mod_time', sortable: true, type: 'number' },
  { label: 'Versions', field: 'versions', sortable: false },
]
// Sorting is disabled while browsing so folder rows stay pinned above
// file rows -- vue-good-table's per-column sort has no notion of
// "folders first," only a single ordering. Pattern-search mode is a
// flat file-only list, so sorting there is unaffected.
const columns = computed(() => (browsing.value ? baseColumns.map((c) => ({ ...c, sortable: false })) : baseColumns))
</script>

<template>
  <div>
    <PageHeader title="Catalog" :crumbs="[{ label: 'Catalog' }]" />

    <div class="mb-4">
      <div class="flex gap-2 mb-2">
        <button
          type="button"
          data-test="chip-date"
          class="flex-1 border rounded px-3 py-2 text-left"
          :class="{ 'border-blue-500': activePanel === 'date' }"
          @click="togglePanel('date')"
        >
          <div class="text-xs uppercase text-gray-500">Date range</div>
          <div>{{ dateSummary }}</div>
        </button>
      </div>
      <div class="flex gap-2 mb-2">
        <button
          type="button"
          data-test="chip-clients"
          class="flex-1 border rounded px-3 py-2 text-left"
          :class="{ 'border-blue-500': activePanel === 'clients' }"
          @click="togglePanel('clients')"
        >
          <div class="text-xs uppercase text-gray-500">Clients</div>
          <div>{{ clientsSummary }}</div>
        </button>
        <button
          type="button"
          data-test="chip-jobs"
          class="flex-1 border rounded px-3 py-2 text-left"
          :class="{ 'border-blue-500': activePanel === 'jobs' }"
          @click="togglePanel('jobs')"
        >
          <div class="text-xs uppercase text-gray-500">Job / Policy</div>
          <div>{{ jobsSummary }}</div>
        </button>
      </div>
      <div class="mb-2">
        <input
          data-test="path-input"
          :value="catalog.filters.pattern"
          @input="catalog.filters.pattern = $event.target.value"
          placeholder="Path contains…"
          class="border rounded px-2 py-1 w-full"
        />
      </div>

      <DateRangePanel
        v-if="activePanel === 'date'"
        v-model:received-after="catalog.filters.receivedAfter"
        v-model:received-before="catalog.filters.receivedBefore"
      />
      <FacetPanel
        v-if="activePanel === 'clients'"
        :facets="catalog.clientFacets"
        :error="catalog.clientFacetsError"
        name-label="Client"
        count-label="Entries in range"
        v-model:selected="catalog.filters.sourceHosts"
      />
      <FacetPanel
        v-if="activePanel === 'jobs'"
        :facets="catalog.jobFacets"
        :error="catalog.jobFacetsError"
        name-label="Policy"
        count-label="Runs in range"
        v-model:selected="catalog.filters.jobNames"
      />
    </div>

    <DirectoryPathBar v-if="browsing" :current-path="catalog.currentPath" @navigate="onPathBarNavigate" />

    <StatusMessage
      :loading="catalog.loading || catalog.directoryChildrenLoading"
      :error="catalog.error || catalog.directoryChildrenError"
      :empty="rows.length === 0"
      empty-text="No entries match this filter."
    >
      <DataTable :columns="columns" :rows="rows" :search-enabled="false" @row-click="onRowClick">
        <template #table-row="{ column, row }">
          <template v-if="row.isFolder">
            <span v-if="column.field === 'path'" class="font-semibold">{{ row.name }}/</span>
            <span v-else-if="column.field === 'representative.mod_time'">{{ formatTimestamp(row.last_seen) || '—' }}</span>
            <span v-else-if="column.field === 'versions'">{{ row.file_count || '' }}</span>
            <span v-else></span>
          </template>
          <template v-else>
            <span v-if="column.field === 'path'">{{ browsing ? row.representative.short_filename : row.path }}</span>
            <span v-else-if="column.field === 'sourceHost'">{{ row.sourceHost }}</span>
            <span v-else-if="column.field === 'representative.store_host'">{{ row.representative.store_host }}</span>
            <span v-else-if="column.field === 'representative.size'">{{ formatBytes(row.representative.size) }}</span>
            <span v-else-if="column.field === 'representative.mode'">{{ row.representative.mode }}</span>
            <span v-else-if="column.field === 'representative.mod_time'">{{ formatTimestamp(row.representative.mod_time) || '—' }}</span>
            <span v-else-if="column.field === 'versions'">{{ row.versions.length > 1 ? row.versions.length : '' }}</span>
          </template>
        </template>
      </DataTable>
    </StatusMessage>
    <VersionsModal v-if="selectedGroup" :group="selectedGroup" @close="selectedGroup = null" />
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: PASS

- [ ] **Step 5: Run the full frontend test suite and build**

Run: `cd web && npx vitest run && npm run build`
Expected: PASS / build succeeds (confirms no other file imports the old `CatalogView.vue` shape or `catalog.js` exports in a now-broken way)

- [ ] **Step 6: Manually verify in the browser**

Run: `cd web && npm run dev` (or use the project's existing dev-server workflow), open the Catalog page, and confirm: it loads showing root-level folders (or nothing, if the demo catalog has no synced data yet); clicking a folder drills in and shows its subfolders/files with the path bar updating; clicking a breadcrumb crumb jumps back; typing in "Path contains…" switches to the flat cross-directory list and hides the path bar; clearing it restores the folder you were browsing.

- [ ] **Step 7: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js
git commit -m "feat(web): browse the catalog as a directory tree"
```

---

## Task 9: Documentation and changelog

**Files:**
- Modify: `docs/protocols/catalog-sync.md`
- Modify: `docs/components/catalog.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `CHANGELOG.md`

**Interfaces:** None (documentation only).

- [ ] **Step 1: Update `docs/protocols/catalog-sync.md`**

In the `## Service` code block, add after `rpc ListDirectoryFacets(...)`:

```protobuf
  rpc ListDirectoryChildren(ListDirectoryChildrenRequest) returns (ListDirectoryChildrenResponse);
```

Add a new section after `## ListClientFacets / ListJobFacets / ListDirectoryFacets` (before `## See Also`):

```markdown
## ListDirectoryChildren

Backs [api-server](../components/api-server.md)'s `GET /api/v1/catalog/directories/children`, the
query behind the web catalog view's directory browsing (root, or a list of Windows drives, with
click-to-drill-down and a breadcrumb path bar).

```protobuf
message ListDirectoryChildrenRequest {
  string parent_path     = 1; // "" = true roots ("/", each distinct drive/UNC root)
  int64  received_after  = 2;
  int64  received_before = 3;
  repeated string source_hosts = 4;
  repeated string job_names    = 5;
}

message DirectoryChild {
  string path         = 1; // full path, e.g. "/var/lib"
  string name         = 2; // short display label, e.g. "lib"
  int64  file_count   = 3; // direct files under path matching the current date/host/job filters
  int64  last_seen    = 4; // unix seconds, max(received_at) among those files; 0 if file_count == 0
  bool   has_children = 5; // true if a subdirectory of path is itself known
}

message ListDirectoryChildrenResponse {
  repeated DirectoryChild children = 1;
}
```

Unlike `ListEntries`/`ListDirectoryFacets`, this RPC is backed by a second table,
`catalog_directories` — one row per directory that has ever been an ancestor of some synced file's
`parent_directory`, computed once at sync time (`decodeDirectoryAncestors`,
`cmd/catalog/server.go`) by walking `splitPath` up from `parent_directory` to its root, alongside
the existing `parent_directory`/`short_filename` computation. This is necessary because
`parent_directory` only names a file's *immediate* containing directory: `/var/lib/dbdata` (with a
file in it) doesn't imply `/var` or `/var/lib` exist as rows anywhere else, so browsing "what's
under `/var`" can't be answered from `EntryRecord` alone.

`ListDirectoryChildren` returns every `catalog_directories` row whose parent is `parent_path`.
**Existence is intentionally filter-independent** — it reflects every directory ever synced, not
just ones with entries currently matching `received_after`/`received_before`/`source_hosts`/
`job_names`. Making existence filter-aware would require knowing whether *any* descendant anywhere
in a subtree matches, the same recursive-subtree question `ListEntries`'s `parent_directories`
filter and `ListDirectoryFacets` both deliberately avoid (exact-match only). `file_count`/
`last_seen` per child, by contrast, only need a direct (non-recursive) `parent_directory` match
against `EntryRecord`, so those *do* respect the request's date/host/job filters. There is no
`pattern` field: directory browsing and the web UI's free-text pattern search are mutually
exclusive modes, never combined.
```

Update `## See Also`'s REST API bullet to:

```markdown
- [REST API v1](../api/rest-v1.md) — `GET /api/v1/catalog` (`ListEntries`), `GET /api/v1/catalog/clients`
  (`ListClientFacets`), `GET /api/v1/catalog/jobs` (`ListJobFacets`), `GET /api/v1/catalog/directories`
  (`ListDirectoryFacets`), and `GET /api/v1/catalog/directories/children` (`ListDirectoryChildren`)
```

- [ ] **Step 2: Update `docs/components/catalog.md`**

Replace the intro paragraph's RPC-count sentence:

```markdown
Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Also serves five read-only query RPCs: `ListEntries`
(filter by store host, real source host, a date range, an exact parent directory, and a substring
match against the underlying object ID, keyset-paginated), the aggregate
`ListClientFacets`/`ListJobFacets`/`ListDirectoryFacets` (grouped counts by client host, policy
name, or parent directory, backing the web catalog view's filter panels), and
`ListDirectoryChildren` (the web catalog view's directory browsing: what's directly under a given
path) — see [api-server](./api-server.md), the only intended caller today.
```

In the `## How It Works` section, after the existing `source_host` bullet, add a new bullet:

```markdown
- `parent_directory`'s full ancestor chain — every directory between it and its root — is also
  recorded, in a second table (`catalog_directories`, one row per distinct directory ever seen)
  populated at the same sync time via `decodeDirectoryAncestors`. This is what backs
  `ListDirectoryChildren` (see [Catalog Sync Protocol](../protocols/catalog-sync.md)): answering
  "what's directly under this path" from `EntryRecord`'s `parent_directory` column alone isn't
  possible, since it only names a file's *immediate* directory, not every ancestor of it.
```

- [ ] **Step 3: Update `docs/api/rest-v1.md`**

Add a new section after `## `GET /api/v1/catalog/directories`` (before `## `GET /api/v1/policies``):

```markdown
## `GET /api/v1/catalog/directories/children`

Backs the web catalog view's directory browsing — one level of a directory tree at a time, not the
flat facet list `/catalog/directories` returns. Query parameters: `parent_path` (empty/omitted =
the true roots: `/` and each distinct Windows drive/UNC root present in the catalog),
`received_after`, `received_before`, `source_hosts`, `job_names` (comma-separated) — no `pattern`
parameter: directory browsing and the free-text path search are mutually exclusive UI modes.

Every directory that has ever been an ancestor of a synced file always appears here, regardless of
the date/host/job filters — only `file_count`/`last_seen` per child respect them (0/absent if
nothing currently matches). This lets the UI navigate through a folder that currently has no
matching files of its own but does have matching descendants further down.

```json
{
  "data": [
    {"path": "/var/lib/dbdata", "name": "dbdata", "file_count": 12, "last_seen": 1752400010, "has_children": false}
  ]
}
```
```

- [ ] **Step 4: Verify `docs/ARCHITECTURE.md` and `README.md` need no change**

Run: `grep -n "ListDirectoryFacets\|parent_directory\|EntryRecord" docs/ARCHITECTURE.md`
Expected: no output — `ARCHITECTURE.md` documents component/network topology, not per-table schema (confirmed by the 2026-08-07 design's own precedent of "no changes" for the same reason). No edit needed. `README.md` already links both `docs/components/catalog.md` and `docs/protocols/catalog-sync.md`; neither link text changes, so no edit needed there either.

- [ ] **Step 5: Add the changelog entry**

At the top of `CHANGELOG.md`, before the existing `## 2026-08-07 — catalog: parent directory and filename fields, directory filtering` entry, add:

```markdown
## 2026-08-08 — catalog: directory browsing UI

The web catalog view is now a file-manager-style directory browser instead of a flat file list:
starting at root (or a list of Windows drives), clicking a folder drills into it, and a breadcrumb
path bar at the top of the table jumps back to any ancestor. This is backed by a new
`catalog_directories` table — one row per directory that has ever been an ancestor of a synced
file's `parent_directory`, computed once at sync time the same way `parent_directory`/
`short_filename` already are — and a new `ListDirectoryChildren` RPC backing
`GET /api/v1/catalog/directories/children`. A folder's existence in the tree is filter-independent
(it reflects everything ever backed up); only its file count and last-seen timestamp respect the
current date/host/job filters, avoiding the recursive subtree matching this system has
consistently avoided elsewhere. Typing in the existing free-text path search still switches to
today's flat, cross-directory result list — the two modes are mutually exclusive. See
`docs/superpowers/specs/2026-08-08-catalog-directory-browsing-design.md`.
```

- [ ] **Step 6: Commit**

```bash
git add docs/protocols/catalog-sync.md docs/components/catalog.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "docs: document catalog directory browsing"
```
