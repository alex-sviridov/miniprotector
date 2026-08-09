# Catalog Write-Path Atomicity and Readability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `catalog`'s sync write path atomic, surface previously-silent metadata decode failures (while decoding once instead of twice), and remove duplicated aggregation/parsing logic from `storage/catalog` and `cmd/api-server`.

**Architecture:** `storage/catalog.Store` gains one new transactional method (`SyncBatch`) that both `EnsureEntries`/`EnsureDirectories` factor into via shared unexported functions. `cmd/catalog/server.go`'s `SyncFileVersions` switches to the new method and collapses its two metadata-decode calls into one, with the failure now logged. The three `List*Facets` methods move into a new `storage/catalog/facets.go` behind a shared aggregation helper. `cmd/api-server/catalog.go` gets one shared date-range-parsing helper used by all 5 catalog handlers.

**Tech Stack:** Go 1.26, GORM (`gorm.io/gorm`), `testify` (`require`/`assert`), standard library `net/http`/`net/url`.

## Global Constraints

- No behavior change to what any RPC or REST endpoint returns — this is write-path correctness and internal DRY-up only.
- Not implementing the SQL-side facet aggregation rewrite the code's own comments flag as a future option — explicitly out of scope per the design spec.
- Not touching `catalogsync`, `ListEntries`, `ListDirectoryChildren`, or anything from the separately-shipped storage-connection-foundation work (connection pooling, context propagation) — those are already done.
- Every task ends with `go build ./... && go test ./...` passing from `src/`.

---

### Task 1: `Store.SyncBatch` — atomic write path

**Files:**
- Modify: `src/storage/catalog/store.go`
- Modify: `src/cmd/catalog/server.go`
- Modify: `src/storage/catalog/store_test.go`

**Interfaces:**
- Produces: `Store.SyncBatch(ctx context.Context, entries []Entry, directories []DirectoryAncestor) error` — Task 2 does not call this, but its presence and behavior (single call replacing the old two-call ending of `SyncFileVersions`) is a precondition Task 2's diff assumes when it edits a different part of the same function.

- [ ] **Step 1: Add `"fmt"` back to `store.go`'s imports**

Replace:

```go
import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)
```

with:

```go
import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)
```

- [ ] **Step 2: Factor `EnsureEntries`'s body into an unexported function**

Replace:

```go
// EnsureEntries idempotently persists batch: a row already present for a
// given (StoreNode, JobID, ObjectID) is left untouched rather than
// erroring — catalogsync retries a batch it isn't sure was received, so a
// resend after a partial success must be a safe no-op.
func (s *Store) EnsureEntries(ctx context.Context, batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]EntryRecord, len(batch))
	now := time.Now()
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
	return s.writeDB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "store_node"}, {Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&records).Error
}
```

with:

```go
// EnsureEntries idempotently persists batch: a row already present for a
// given (StoreNode, JobID, ObjectID) is left untouched rather than
// erroring — catalogsync retries a batch it isn't sure was received, so a
// resend after a partial success must be a safe no-op.
func (s *Store) EnsureEntries(ctx context.Context, batch []Entry) error {
	return ensureEntries(s.writeDB.WithContext(ctx), batch)
}

func ensureEntries(db *gorm.DB, batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]EntryRecord, len(batch))
	now := time.Now()
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
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "store_node"}, {Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&records).Error
}
```

- [ ] **Step 3: Factor `EnsureDirectories`'s body into an unexported function, the same way**

Replace:

```go
// EnsureDirectories idempotently persists batch: a row already present for
// a given Path is left untouched (ON CONFLICT DO NOTHING) -- directory
// structure never changes once known, and many files sync-after-sync
// share the same ancestor directories.
func (s *Store) EnsureDirectories(ctx context.Context, batch []DirectoryAncestor) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]DirectoryRecord, len(batch))
	for i, a := range batch {
		records[i] = DirectoryRecord{Path: a.Path, ParentPath: a.ParentPath, Name: a.Name, Depth: a.Depth}
	}
	return s.writeDB.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path"}},
		DoNothing: true,
	}).Create(&records).Error
}
```

with:

```go
// EnsureDirectories idempotently persists batch: a row already present for
// a given Path is left untouched (ON CONFLICT DO NOTHING) -- directory
// structure never changes once known, and many files sync-after-sync
// share the same ancestor directories.
func (s *Store) EnsureDirectories(ctx context.Context, batch []DirectoryAncestor) error {
	return ensureDirectories(s.writeDB.WithContext(ctx), batch)
}

func ensureDirectories(db *gorm.DB, batch []DirectoryAncestor) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]DirectoryRecord, len(batch))
	for i, a := range batch {
		records[i] = DirectoryRecord{Path: a.Path, ParentPath: a.ParentPath, Name: a.Name, Depth: a.Depth}
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "path"}},
		DoNothing: true,
	}).Create(&records).Error
}
```

- [ ] **Step 4: Add `SyncBatch` immediately after `ensureDirectories`**

```go
// SyncBatch persists entries and their directory ancestors atomically:
// both commit, or neither does. Used by SyncFileVersions instead of
// separate EnsureEntries/EnsureDirectories calls, closing the window
// where a batch's entries could be durable with no corresponding
// directory tree.
func (s *Store) SyncBatch(ctx context.Context, entries []Entry, directories []DirectoryAncestor) error {
	return s.writeDB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := ensureEntries(tx, entries); err != nil {
			return fmt.Errorf("ensure entries: %w", err)
		}
		if err := ensureDirectories(tx, directories); err != nil {
			return fmt.Errorf("ensure directories: %w", err)
		}
		return nil
	})
}
```

- [ ] **Step 5: Write the failing tests**

Append to `src/storage/catalog/store_test.go`:

```go
func TestSyncBatch_PersistsEntriesAndDirectories(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	entries := []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}
	directories := []DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}
	require.NoError(t, store.SyncBatch(t.Context(), entries, directories))

	count, err := store.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	children, err := store.ListDirectoryChildren(t.Context(), "/", FacetFilter{})
	require.NoError(t, err)
	require.Len(t, children, 1)
	assert.Equal(t, "/var", children[0].Path)
}

func TestSyncBatch_RollsBackEntriesIfDirectoriesInsertFails(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	// Force the directories phase to fail with a genuine SQL error --
	// EnsureDirectories/EnsureEntries both use ON CONFLICT DO NOTHING, so no
	// ordinary bad input produces a real constraint violation.
	require.NoError(t, store.writeDB.Exec("DROP TABLE catalog_directories").Error)

	entries := []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}
	directories := []DirectoryAncestor{
		{Path: "/var", ParentPath: "/", Name: "var", Depth: 1},
	}
	err = store.SyncBatch(t.Context(), entries, directories)
	require.Error(t, err)
	assert.ErrorContains(t, err, "ensure directories")

	count, err := store.Count(t.Context())
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "entries insert must roll back when the directories insert fails")
}
```

- [ ] **Step 6: Run the tests to verify they fail**

Run: `cd src && go test ./storage/catalog/... -run TestSyncBatch -v`
Expected: FAIL — `store.SyncBatch undefined` (method doesn't exist until Steps 1-4 are applied). If you're doing Steps 1-4 before writing the test (reasonable for this size of change, since `SyncBatch` is a small, fully-specified addition), run this step right after Step 5 regardless, to confirm the test file itself is valid Go before moving on — it should already pass at that point, which is fine; the point of this step is catching a typo in the test, not strict step ordering.

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -run TestSyncBatch -v`
Expected: PASS — both new tests green.

- [ ] **Step 8: Run the full `storage/catalog` suite to confirm no regressions**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS — including every existing `TestEnsureEntries_*`/`TestEnsureDirectories_*` test, unchanged (same public signatures, same behavior).

- [ ] **Step 9: Update `cmd/catalog/server.go`'s `SyncFileVersions` to use `SyncBatch`**

Replace:

```go
	if err := s.store.EnsureEntries(ctx, batch); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}

	if len(directoriesByPath) > 0 {
		directories := make([]catalogstore.DirectoryAncestor, 0, len(directoriesByPath))
		for _, a := range directoriesByPath {
			directories = append(directories, a)
		}
		if err := s.store.EnsureDirectories(ctx, directories); err != nil {
			s.logger.Error("SyncFileVersions: persisting directory ancestors failed", "error", err, "count", len(directories))
			return nil, err
		}
	}
```

with:

```go
	directories := make([]catalogstore.DirectoryAncestor, 0, len(directoriesByPath))
	for _, a := range directoriesByPath {
		directories = append(directories, a)
	}
	if err := s.store.SyncBatch(ctx, batch, directories); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}
```

(The `if len(directoriesByPath) > 0` guard is no longer needed: `ensureDirectories` already treats an empty slice as a no-op, so it's safe to always build and pass `directories` regardless of whether the batch had any.)

- [ ] **Step 10: Run `cmd/catalog`'s tests to confirm the integration still works**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS — every existing `TestSyncFileVersions_*` test, unchanged (same RPC behavior; `SyncBatch`'s two wrapped errors, `"ensure entries: ..."` / `"ensure directories: ..."`, still surface through `SyncFileVersions`'s one log line via `%v`/`%w`, so no existing assertion on the outer error changes).

- [ ] **Step 11: Run the full build and test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: succeeds.

- [ ] **Step 12: Commit**

```bash
cd src && git add storage/catalog/store.go storage/catalog/store_test.go cmd/catalog/server.go
git commit -m "$(cat <<'EOF'
feat: make catalog's sync write path atomic

SyncFileVersions now persists a batch's entries and directory ancestors
in one transaction (Store.SyncBatch) instead of two independent writes,
closing the window where entries could be durable with no corresponding
directory tree if the second write failed.
EOF
)"
```

---

### Task 2: Decode visibility and dedup

**Files:**
- Modify: `src/cmd/catalog/server.go`
- Modify: `src/cmd/catalog/server_test.go`
- Modify: `src/storage/catalog/store.go`
- Modify: `src/storage/catalog/models.go`

**Interfaces:**
- Consumes: nothing from Task 1 — this task edits a different part of `SyncFileVersions` (the per-entry decode loop, before the write-path call Task 1 already changed).
- Produces: nothing new later tasks depend on. `decodeSourceHost`/`decodePathParts` (removed here) are not referenced by any other file in the repo (verified: `grep -rn "decodeSourceHost\|decodePathParts" src/` before this task shows only `cmd/catalog/server.go`, `storage/catalog/store.go`, and `storage/catalog/models.go` — all three are in this task's file list).

- [ ] **Step 1: Replace the per-entry decode loop in `SyncFileVersions`**

Replace:

```go
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
```

with:

```go
	entries := req.GetEntries()
	batch := make([]catalogstore.Entry, len(entries))
	directoriesByPath := make(map[string]catalogstore.DirectoryAncestor)
	for i, e := range entries {
		var sourceHost, parentDir, shortName string
		if fi, err := filesystem.DecodeFileInfo(e.GetMetadata()); err != nil {
			s.logger.Error("SyncFileVersions: metadata decode failed, entry stored without derived fields",
				"job_id", e.GetJobId(), "object_id", e.GetObjectId(), "error", err)
		} else {
			sourceHost = fi.Source()
			parentDir, shortName = splitPath(fi.Path())
		}
		batch[i] = catalogstore.Entry{
			StoreNode:       storeNode,
			JobID:           e.GetJobId(),
			ObjectID:        e.GetObjectId(),
			Metadata:        e.GetMetadata(),
			Ctime:           e.GetCtime(),
			StoreSeq:        e.GetStoreSeq(),
			StoreCreatedAt:  time.Unix(e.GetCreatedAt(), 0).UTC(),
			SourceHost:      sourceHost,
			ParentDirectory: parentDir,
			ShortFilename:   shortName,
		}
		for _, a := range decodeDirectoryAncestors(parentDir) {
			directoriesByPath[a.Path] = a
		}
	}
```

`filesystem.DecodeFileInfo` and `splitPath` are already used elsewhere in this file (`toProtoEntry`, `decodeDirectoryAncestors`), so no import changes are needed.

- [ ] **Step 2: Remove `decodeSourceHost` and `decodePathParts`**

Delete these two functions (and their doc comments) entirely from `server.go`:

```go
// decodeSourceHost extracts the real originating (backed-up) host from a
// FileVersionEntry's opaque Metadata blob, decoded once at sync time so
// ListEntries can filter on a plain indexed column instead of re-decoding
// Metadata on every read. A decode failure (malformed or non-filesystem
// metadata) yields "" rather than failing the whole batch — one bad entry
// shouldn't block every other entry in it.
func decodeSourceHost(metadata []byte) string {
	fi, err := filesystem.DecodeFileInfo(metadata)
	if err != nil {
		return ""
	}
	return fi.Source()
}

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

- [ ] **Step 3: Fix the two `decodeDirectoryAncestors` comment lines that name `decodePathParts`**

Replace:

```go
// decodeDirectoryAncestors walks parentDir's ancestor chain via splitPath
// -- the same shape-detecting split that produced parentDir itself in
// decodePathParts -- collecting one DirectoryAncestor per level from
// parentDir up to its root, root-first Depth (0 at the root). A blank
// parentDir (a decodePathParts failure) yields no ancestors: an unknown
// location can't be placed in the tree.
```

with:

```go
// decodeDirectoryAncestors walks parentDir's ancestor chain via splitPath
// -- the same shape-detecting split SyncFileVersions uses to derive
// parentDir itself -- collecting one DirectoryAncestor per level from
// parentDir up to its root, root-first Depth (0 at the root). A blank
// parentDir (a sync-time metadata decode failure) yields no ancestors: an
// unknown location can't be placed in the tree.
```

- [ ] **Step 4: Fix `toProtoEntry`'s comment referencing the removed functions**

Replace:

```go
// toProtoEntry decodes rec.Metadata (a gob-encoded filesystem.FileInfo)
// into Entry's path/size/mode/owner/group/mod_time fields. A decode
// failure (malformed or non-filesystem metadata) leaves those fields at
// their zero values rather than failing the whole ListEntries call --
// one bad row shouldn't hide every other entry in the response. SourceHost
// is NOT decoded here — it's read directly from rec.SourceHost, persisted
// once at sync time (see decodeSourceHost above). ParentDirectory and
// ShortFilename are the same: persisted columns computed once at sync time
// (see decodePathParts), not decoded here.
```

with:

```go
// toProtoEntry decodes rec.Metadata (a gob-encoded filesystem.FileInfo)
// into Entry's path/size/mode/owner/group/mod_time fields. A decode
// failure (malformed or non-filesystem metadata) leaves those fields at
// their zero values rather than failing the whole ListEntries call --
// one bad row shouldn't hide every other entry in the response. SourceHost
// is NOT decoded here — it's read directly from rec.SourceHost, persisted
// once at sync time in SyncFileVersions. ParentDirectory and ShortFilename
// are the same: persisted columns computed once at sync time, not decoded
// here.
```

- [ ] **Step 5: Fix `storage/catalog/store.go`'s two doc comments naming the removed functions**

Replace:

```go
// ListClientFacets groups entries matching filter by source_host, dropping
// rows where source_host is empty (a decodeSourceHost failure at sync time
// -- see cmd/catalog/server.go) rather than surfacing a blank-named facet.
```

with:

```go
// ListClientFacets groups entries matching filter by source_host, dropping
// rows where source_host is empty (a sync-time metadata decode failure in
// SyncFileVersions, see cmd/catalog/server.go) rather than surfacing a
// blank-named facet.
```

Replace:

```go
// ListDirectoryFacets groups entries matching filter by parent_directory,
// dropping rows where parent_directory is empty (a sync-time Metadata
// decode failure -- see decodePathParts in cmd/catalog/server.go) rather
// than surfacing a blank-named facet, mirroring ListClientFacets's drop of
// an empty source_host. filter.ParentDirectories is ignored: a directory
```

with:

```go
// ListDirectoryFacets groups entries matching filter by parent_directory,
// dropping rows where parent_directory is empty (a sync-time metadata
// decode failure in SyncFileVersions, see cmd/catalog/server.go) rather
// than surfacing a blank-named facet, mirroring ListClientFacets's drop of
// an empty source_host. filter.ParentDirectories is ignored: a directory
```

(These two comments are still in `store.go` at this point in the plan — Task 3 moves them, unchanged, into `facets.go`.)

- [ ] **Step 6: Fix `storage/catalog/models.go`'s comment naming `decodePathParts`**

Replace:

```go
	// ParentDirectory is the file's immediate containing directory, and
	// ShortFilename its bare name, both derived from Metadata at sync time
	// the same way SourceHost is (see cmd/catalog/server.go's
	// decodePathParts). ParentDirectory is indexed for filtering;
	// ShortFilename is display-only, not a filter dimension.
```

with:

```go
	// ParentDirectory is the file's immediate containing directory, and
	// ShortFilename its bare name, both derived from Metadata at sync time
	// the same way SourceHost is, in cmd/catalog/server.go's
	// SyncFileVersions. ParentDirectory is indexed for filtering;
	// ShortFilename is display-only, not a filter dimension.
```

- [ ] **Step 7: Write the failing test for decode-failure logging**

Append to `src/cmd/catalog/server_test.go`:

```go
func TestSyncFileVersions_MalformedMetadataLogsError(t *testing.T) {
	store, err := catalogstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError}))
	srv := NewCatalogServer(store, logger)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: "obj-1", Metadata: []byte("not-gob-encoded"), CreatedAt: time.Now().Unix()},
	}}
	_, err = srv.SyncFileVersions(ctx, req)
	require.NoError(t, err) // a bad row's metadata still doesn't fail the batch

	logOutput := logBuf.String()
	assert.Contains(t, logOutput, "metadata decode failed")
	assert.Contains(t, logOutput, "job-1")
	assert.Contains(t, logOutput, "obj-1")
}
```

This needs `"bytes"` added to `server_test.go`'s import block (`slog`, `os`, `testing`, etc. are already imported by `newTestCatalogServer`'s neighbors in this file).

- [ ] **Step 8: Run the test to verify it fails**

Run: `cd src && go test ./cmd/catalog/... -run TestSyncFileVersions_MalformedMetadataLogsError -v`
Expected: FAIL — before Step 1's change, the decode failure is silent, so `logOutput` never contains `"metadata decode failed"`.

If you're applying Steps 1-6 before writing this test (reasonable, since the decode-loop replacement is one self-contained edit), this step instead confirms the test is valid and already passing — that's fine; the important check is Step 9.

- [ ] **Step 9: Run the test to verify it passes**

Run: `cd src && go test ./cmd/catalog/... -run TestSyncFileVersions_MalformedMetadataLogsError -v`
Expected: PASS.

- [ ] **Step 10: Run the full `cmd/catalog` suite to confirm no regressions**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS — including the three existing malformed-metadata tests (`TestSyncFileVersions_MalformedMetadataLeavesSourceHostEmpty`, `TestSyncFileVersions_MalformedMetadataLeavesPathPartsEmpty`, `TestSyncFileVersions_MalformedMetadataPersistsNoDirectoryAncestors`), unchanged — they only assert on persisted data, not logs, and that behavior is identical.

- [ ] **Step 11: Run the full build and test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: succeeds.

- [ ] **Step 12: Commit**

```bash
cd src && git add cmd/catalog/server.go cmd/catalog/server_test.go storage/catalog/store.go storage/catalog/models.go
git commit -m "$(cat <<'EOF'
fix: log sync-time metadata decode failures, decode once not twice

decodeSourceHost/decodePathParts each independently decoded the same
metadata blob and silently dropped failures. SyncFileVersions now
decodes once per entry and logs at Error (matching this codebase's
existing convention for tolerated-but-visible per-item failures) with
the entry's job_id/object_id.
EOF
)"
```

---

### Task 3: Facet aggregation DRY (`facets.go` split)

**Files:**
- Create: `src/storage/catalog/facets.go`
- Create: `src/storage/catalog/facets_test.go`
- Modify: `src/storage/catalog/store.go`
- Modify: `src/storage/catalog/store_test.go`

**Interfaces:**
- Consumes: `store.go`'s two decode-failure doc comments in their Task-2-updated wording (this task moves them verbatim, unchanged, into `facets.go`).
- Produces: nothing later tasks depend on.

- [ ] **Step 1: Create `facets.go`**

```go
// src/storage/catalog/facets.go
package catalog

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// jobNamesWhere adds an OR of job_id LIKE 'backup:<name>:%' conditions, one
// per name -- job_id has no column for the policy name, so this is the
// only way to filter on it (see policyNameFromJobID below for the matching
// Go-side parse used by ListJobFacets).
func jobNamesWhere(q *gorm.DB, names []string) *gorm.DB {
	conds := make([]string, len(names))
	args := make([]interface{}, len(names))
	for i, name := range names {
		conds[i] = "job_id LIKE ?"
		args[i] = "backup:" + name + ":%"
	}
	return q.Where(strings.Join(conds, " OR "), args...)
}

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

func (f FacetFilter) applyCommon(q *gorm.DB) *gorm.DB {
	if !f.ReceivedAfter.IsZero() {
		q = q.Where("received_at >= ?", f.ReceivedAfter)
	}
	if !f.ReceivedBefore.IsZero() {
		q = q.Where("received_at <= ?", f.ReceivedBefore)
	}
	if f.Pattern != "" {
		q = q.Where("object_id LIKE ?", "%"+f.Pattern+"%")
	}
	return q
}

// Facet is one aggregated row: a distinct client hostname, policy name, or
// parent directory, how many matching entries it has, and the most recent
// one.
type Facet struct {
	Name     string    `gorm:"column:name"`
	Count    int64     `gorm:"column:count"`
	LastSeen time.Time `gorm:"column:last_seen"`
}

// facetRow is one (name, receivedAt) pair scanned from a facet query,
// before grouping.
type facetRow struct {
	Name       string
	ReceivedAt time.Time
}

// aggregateFacets groups rows by Name -- counting occurrences and tracking
// the max ReceivedAt per name, in first-seen order -- dropping rows with an
// empty Name. Shared by ListClientFacets/ListJobFacets/ListDirectoryFacets,
// which derive Name differently (raw source_host, policyNameFromJobID(job_id),
// raw parent_directory) but aggregate identically once Name is known.
//
// Aggregation happens in Go, not SQL: an earlier version used SQL-side
// MAX(received_at) and parsed the result via Go's non-portable time.Time
// string format, which crashed on any host with a negative UTC timezone
// offset (time.Parse couldn't handle the locale-dependent zone suffix).
// Scanning the raw, non-aggregated rows and aggregating here avoids that
// string-parsing entirely, at the accepted cost of loading every matching
// row into memory per call rather than letting SQLite do the GROUP BY --
// acceptable at this catalog's expected scale, and consistent with this
// package's stated preference for simple, portable code over premature
// optimization (see storage/CLAUDE.md). Revisit with a SQL-side
// strftime()-based approach if this ever becomes a measured hot path.
func aggregateFacets(rows []facetRow) []Facet {
	byName := make(map[string]*Facet)
	var order []string
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		f, ok := byName[r.Name]
		if !ok {
			f = &Facet{Name: r.Name}
			byName[r.Name] = f
			order = append(order, r.Name)
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
	return facets
}

// ListClientFacets groups entries matching filter by source_host, dropping
// rows where source_host is empty (a sync-time metadata decode failure in
// SyncFileVersions, see cmd/catalog/server.go) rather than surfacing a
// blank-named facet. filter.SourceHosts is ignored: a client facet list is
// never narrowed by its own dimension's current selection.
func (s *Store) ListClientFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).
		Select("source_host, received_at").
		Where("source_host != ''")
	q = filter.applyCommon(q)
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}
	if len(filter.ParentDirectories) > 0 {
		q = q.Where("parent_directory IN ?", filter.ParentDirectories)
	}

	var rows []struct {
		SourceHost string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: r.SourceHost, ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
}

// ListJobFacets groups entries matching filter by the policy name embedded
// in job_id. Grouping happens in Go, not SQL -- job_id's colon-delimited
// format isn't fixed-width, matching this codebase's existing preference
// for decoding a similar composite ID in Go (cmd/bwfs/list.go's
// parseFileID) over a SQL substr/instr split. filter.SourceHosts is
// applied (it narrows which entries are considered); filter.JobNames is
// ignored: a job facet list is never narrowed by its own dimension's
// current selection.
func (s *Store) ListJobFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).Select("job_id, received_at")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.ParentDirectories) > 0 {
		q = q.Where("parent_directory IN ?", filter.ParentDirectories)
	}

	var rows []struct {
		JobID      string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: policyNameFromJobID(r.JobID), ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
}

// ListDirectoryFacets groups entries matching filter by parent_directory,
// dropping rows where parent_directory is empty (a sync-time metadata
// decode failure in SyncFileVersions, see cmd/catalog/server.go) rather
// than surfacing a blank-named facet, mirroring ListClientFacets's drop of
// an empty source_host. filter.ParentDirectories is ignored: a directory
// facet list is never narrowed by its own dimension's current selection.
// Both SourceHosts and JobNames narrow it, extending the same
// "apply every other dimension, ignore your own" rule ListClientFacets/
// ListJobFacets already follow to this third dimension.
func (s *Store) ListDirectoryFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
	q := s.readDB.WithContext(ctx).Model(&EntryRecord{}).
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

	facetRows := make([]facetRow, len(rows))
	for i, r := range rows {
		facetRows[i] = facetRow{Name: r.ParentDirectory, ReceivedAt: r.ReceivedAt}
	}
	return aggregateFacets(facetRows), nil
}

// policyNameFromJobID extracts the policy-name segment of a backup job_id
// (e.g. "nightly-db" from "backup:nightly-db:var-lib:abcd1234:..." -- see
// cmd/agent/backup.go's backupJobID). Returns "" for anything that isn't a
// "backup:"-prefixed job_id, or has fewer than two segments -- never
// errors, mirroring cmd/bwfs/list.go's parseFileID tolerance for
// malformed/foreign IDs.
func policyNameFromJobID(jobID string) string {
	parts := strings.SplitN(jobID, ":", 3)
	if len(parts) < 2 || parts[0] != "backup" {
		return ""
	}
	return parts[1]
}
```

- [ ] **Step 2: Remove the now-duplicated block from `store.go`**

Delete everything from the `jobNamesWhere` doc comment through the end of `policyNameFromJobID` — i.e. everything between the end of `ListEntries` and the start of `Close()`. Identify the exact range with:

```bash
cd src/storage/catalog && grep -n '^// jobNamesWhere adds an OR\|^func (s \*Store) Close() error {' store.go
```

Then delete from the `jobNamesWhere` comment line through (but not including) the `Close()` line — the cleanest way is a pattern-range `sed` delete, which needs no hardcoded line numbers:

```bash
sed -i '/^\/\/ jobNamesWhere adds an OR of job_id LIKE/,/^func (s \*Store) Close() error {/{/^func (s \*Store) Close() error {/!d}' store.go
```

This deletes every line from the `jobNamesWhere` comment up to (not including) the `Close()` function line, which also removes the blank line that used to separate `policyNameFromJobID` from `Close()` — leaving exactly one blank line between `ListEntries`'s closing brace and `Close()` (the blank line that used to separate `ListEntries` from `jobNamesWhere`). Run `gofmt -w store.go` afterward regardless, as a safety net.

- [ ] **Step 3: Remove `"strings"` from `store.go`'s imports**

After Step 2, `strings.Join`/`strings.SplitN` (the only uses of `"strings"` in `store.go`) no longer exist in this file — they moved to `facets.go`. Replace:

```go
import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)
```

with:

```go
import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)
```

- [ ] **Step 4: Confirm `store.go` builds on its own**

Run: `cd src && go build ./storage/catalog/...`
Expected: succeeds. (If it doesn't — e.g. a leftover blank-line/brace issue from the `sed` in Step 2 — fix `store.go` by hand; the file should now contain, in order: `Store` struct, `New`, `Entry`, `EnsureEntries`/`ensureEntries`, `DirectoryAncestor`, `EnsureDirectories`/`ensureDirectories`, `SyncBatch`, `DirectoryChild`, `ListDirectoryChildren`, `Count`, `ListEntriesFilter`, consts, `ListEntries`, `Close`.)

- [ ] **Step 5: Move the facet-related tests out of `store_test.go` into `facets_test.go`**

The block from `func TestPolicyNameFromJobID` through the end of `TestListDirectoryFacets_IgnoresOwnDimension` needs to move, unchanged, into a new `facets_test.go`. Find its exact boundaries and extract it:

```bash
cd src/storage/catalog
grep -n '^func TestPolicyNameFromJobID\|^func TestListDirectoryFacets_IgnoresOwnDimension\|^func TestEnsureDirectories_PersistsBatch' store_test.go
```

Using the line number `TestPolicyNameFromJobID` starts at (call it `START`) and the line number immediately before `TestEnsureDirectories_PersistsBatch` starts at (call it `END`, i.e. `TestEnsureDirectories_PersistsBatch`'s own start line minus 1 — this includes the blank line separating the two blocks, so it's removed cleanly from `store_test.go`):

```bash
{
  echo 'package catalog'
  echo ''
  echo 'import ('
  echo '	"testing"'
  echo '	"time"'
  echo ''
  echo '	"github.com/stretchr/testify/assert"'
  echo '	"github.com/stretchr/testify/require"'
  echo ')'
  echo ''
  sed -n "${START},$((END - 1))p" store_test.go
} > facets_test.go
sed -i "${START},${END}d" store_test.go
gofmt -w facets_test.go store_test.go
```

(`END - 1` when extracting excludes the trailing blank line from the new file's content; deleting through `END` — the blank line itself — from `store_test.go` leaves exactly one blank line before `TestEnsureDirectories_PersistsBatch`, matching what was there before.)

- [ ] **Step 6: Confirm both test files build and the moved tests still pass**

Run: `cd src && go build ./storage/catalog/... && go vet ./storage/catalog/... && go test ./storage/catalog/... -v`
Expected: succeeds; every `TestListClientFacets_*`/`TestListJobFacets_*`/`TestListDirectoryFacets_*`/`TestPolicyNameFromJobID` test passes, unchanged. If `go vet` flags an unused import in `store_test.go`, remove it — the only import that could become unused by this move is one exclusively referenced within the moved block (verified during planning: `"fmt"`, `"strings"`, `"database/sql"` are each still used by tests outside the moved range, so none should need removing — this check is a safety net, not an expected fix).

- [ ] **Step 7: Write the new `aggregateFacets` unit tests**

Append to `facets_test.go`:

```go
func TestAggregateFacets_EmptyInputReturnsEmptySlice(t *testing.T) {
	facets := aggregateFacets(nil)
	assert.Empty(t, facets)
}

func TestAggregateFacets_SingleName(t *testing.T) {
	now := time.Now()
	facets := aggregateFacets([]facetRow{
		{Name: "host-a", ReceivedAt: now},
	})
	require.Len(t, facets, 1)
	assert.Equal(t, "host-a", facets[0].Name)
	assert.Equal(t, int64(1), facets[0].Count)
	assert.Equal(t, now, facets[0].LastSeen)
}

func TestAggregateFacets_MultipleNamesCountAndTrackLatestReceivedAt(t *testing.T) {
	earlier := time.Now().Add(-time.Hour)
	later := time.Now()
	facets := aggregateFacets([]facetRow{
		{Name: "host-a", ReceivedAt: earlier},
		{Name: "host-b", ReceivedAt: later},
		{Name: "host-a", ReceivedAt: later}, // later than host-a's first row -- LastSeen must advance
	})
	require.Len(t, facets, 2)
	assert.Equal(t, "host-a", facets[0].Name) // first-seen order preserved
	assert.Equal(t, int64(2), facets[0].Count)
	assert.Equal(t, later, facets[0].LastSeen)
	assert.Equal(t, "host-b", facets[1].Name)
	assert.Equal(t, int64(1), facets[1].Count)
}

func TestAggregateFacets_DropsEmptyNameRows(t *testing.T) {
	facets := aggregateFacets([]facetRow{
		{Name: "", ReceivedAt: time.Now()},
		{Name: "host-a", ReceivedAt: time.Now()},
	})
	require.Len(t, facets, 1)
	assert.Equal(t, "host-a", facets[0].Name)
}
```

- [ ] **Step 8: Run the new tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -run TestAggregateFacets -v`
Expected: PASS — all 4 new tests green.

- [ ] **Step 9: Run the full build and test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: succeeds.

- [ ] **Step 10: Commit**

```bash
cd src && git add storage/catalog/facets.go storage/catalog/facets_test.go storage/catalog/store.go storage/catalog/store_test.go
git commit -m "$(cat <<'EOF'
refactor: extract storage/catalog/facets.go, DRY up facet aggregation

Moves FacetFilter/Facet/the three List*Facets methods out of store.go
into their own file (fixing a stale comment that already claimed
policyNameFromJobID lived there), and replaces each method's duplicated
~30-line aggregation block with a single shared aggregateFacets helper.
EOF
)"
```

---

### Task 4: api-server date-range parsing DRY, plus CHANGELOG

**Files:**
- Modify: `src/cmd/api-server/catalog.go`
- Modify: `src/cmd/api-server/catalog_test.go`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing from Tasks 1-3 — this task is fully independent (different package).
- Produces: `parseDateRange(w http.ResponseWriter, q url.Values) (after, before int64, ok bool)`, used only within `catalog.go`.

- [ ] **Step 1: Add `"net/url"` to `catalog.go`'s imports**

Replace:

```go
import (
	"net/http"
	"strconv"
	"strings"

	pb "github.com/alex-sviridov/miniprotector/api"
)
```

with:

```go
import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	pb "github.com/alex-sviridov/miniprotector/api"
)
```

- [ ] **Step 2: Add `parseDateRange` immediately after `parseUnixParam`**

Replace:

```go
// parseUnixParam parses an optional unix-seconds query param. An empty
// string is "unset" (returns 0, true); anything else must be a
// non-negative integer.
func parseUnixParam(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}
```

with:

```go
// parseUnixParam parses an optional unix-seconds query param. An empty
// string is "unset" (returns 0, true); anything else must be a
// non-negative integer.
func parseUnixParam(raw string) (int64, bool) {
	if raw == "" {
		return 0, true
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, false
	}
	return parsed, true
}

// parseDateRange parses received_after/received_before from q, writing a
// 400 response and returning ok=false if either is malformed. Callers must
// return immediately when ok is false -- the response is already written.
func parseDateRange(w http.ResponseWriter, q url.Values) (after, before int64, ok bool) {
	after, ok = parseUnixParam(q.Get("received_after"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_after must be a non-negative integer")
		return 0, 0, false
	}
	before, ok = parseUnixParam(q.Get("received_before"))
	if !ok {
		writeJSONError(w, http.StatusBadRequest, "received_before must be a non-negative integer")
		return 0, 0, false
	}
	return after, before, true
}
```

- [ ] **Step 3: Replace `handleListCatalog`'s inline block**

Replace:

```go
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

	resp, err := s.catalog.ListEntries(r.Context(), &pb.ListEntriesRequest{
```

with:

```go
	receivedAfter, receivedBefore, ok := parseDateRange(w, q)
	if !ok {
		return
	}

	resp, err := s.catalog.ListEntries(r.Context(), &pb.ListEntriesRequest{
```

- [ ] **Step 4: Replace `handleListCatalogClients`'s inline block**

Replace:

```go
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

	resp, err := s.catalog.ListClientFacets(r.Context(), &pb.ListFacetsRequest{
```

with:

```go
	receivedAfter, receivedBefore, ok := parseDateRange(w, q)
	if !ok {
		return
	}

	resp, err := s.catalog.ListClientFacets(r.Context(), &pb.ListFacetsRequest{
```

- [ ] **Step 5: Replace `handleListCatalogJobs`'s inline block**

Replace:

```go
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

	resp, err := s.catalog.ListJobFacets(r.Context(), &pb.ListFacetsRequest{
```

with:

```go
	receivedAfter, receivedBefore, ok := parseDateRange(w, q)
	if !ok {
		return
	}

	resp, err := s.catalog.ListJobFacets(r.Context(), &pb.ListFacetsRequest{
```

- [ ] **Step 6: Replace `handleListCatalogDirectories`'s inline block**

Replace:

```go
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
```

with:

```go
	receivedAfter, receivedBefore, ok := parseDateRange(w, q)
	if !ok {
		return
	}

	resp, err := s.catalog.ListDirectoryFacets(r.Context(), &pb.ListFacetsRequest{
```

- [ ] **Step 7: Replace `handleListCatalogDirectoryChildren`'s inline block**

Replace:

```go
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
```

with:

```go
	receivedAfter, receivedBefore, ok := parseDateRange(w, q)
	if !ok {
		return
	}

	resp, err := s.catalog.ListDirectoryChildren(r.Context(), &pb.ListDirectoryChildrenRequest{
```

- [ ] **Step 8: Run the existing handler tests to confirm no regressions**

Run: `cd src && go build ./cmd/api-server/... && go test ./cmd/api-server/... -v`
Expected: PASS — every existing `TestHandleListCatalog*_Invalid*Returns400` / `TestHandleListCatalog*_Negative*Returns400` test, unchanged (same request/response contract, same two error messages, same `400` status).

- [ ] **Step 9: Add `"net/url"` to `catalog_test.go`'s imports**

Replace:

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
)
```

with:

```go
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
)
```

- [ ] **Step 10: Write direct unit tests for `parseDateRange`**

Append to `catalog_test.go`:

```go
func TestParseDateRange_BothValid(t *testing.T) {
	w := httptest.NewRecorder()
	q := url.Values{"received_after": {"1000"}, "received_before": {"2000"}}

	after, before, ok := parseDateRange(w, q)

	require.True(t, ok)
	assert.Equal(t, int64(1000), after)
	assert.Equal(t, int64(2000), before)
	assert.Equal(t, http.StatusOK, w.Code) // nothing written on success
}

func TestParseDateRange_BothOmittedReturnsZeroBounds(t *testing.T) {
	w := httptest.NewRecorder()

	after, before, ok := parseDateRange(w, url.Values{})

	require.True(t, ok)
	assert.Equal(t, int64(0), after)
	assert.Equal(t, int64(0), before)
}

func TestParseDateRange_InvalidReceivedAfterWrites400(t *testing.T) {
	w := httptest.NewRecorder()
	q := url.Values{"received_after": {"not-a-number"}}

	_, _, ok := parseDateRange(w, q)

	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestParseDateRange_InvalidReceivedBeforeWrites400(t *testing.T) {
	w := httptest.NewRecorder()
	q := url.Values{"received_before": {"-5"}}

	_, _, ok := parseDateRange(w, q)

	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestParseDateRange_BothMalformedReturns400OnReceivedAfterFirst(t *testing.T) {
	w := httptest.NewRecorder()
	q := url.Values{"received_after": {"not-a-number"}, "received_before": {"also-not-a-number"}}

	_, _, ok := parseDateRange(w, q)

	// received_after is checked first; its error is the one written when both are malformed.
	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "received_after")
}
```

- [ ] **Step 11: Run the new tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -run TestParseDateRange -v`
Expected: PASS — all 5 new tests green.

- [ ] **Step 12: Add the CHANGELOG.md entry**

Read `CHANGELOG.md` first to confirm today's date and match its exact style (a dated `## YYYY-MM-DD — <topic>` heading followed by a prose paragraph, most-recent-first — the most recent existing entry is dated `2026-08-08`). Add a new entry at the top, dated with today's actual date, covering:

- `SyncFileVersions`'s write path is now atomic (`Store.SyncBatch`) — entries and their directory ancestors commit together or not at all.
- Sync-time metadata decode failures are now logged (previously silent), and decoded once per entry instead of twice.
- `storage/catalog/facets.go` is a new file holding the three `List*Facets` methods behind a shared aggregation helper, replacing ~90 duplicated lines.
- `cmd/api-server/catalog.go`'s 5 handlers share one date-range-parsing helper instead of repeating it.
- No behavior change to any RPC or REST endpoint's contract.

- [ ] **Step 13: Run the full build and test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: succeeds.

- [ ] **Step 14: Commit**

```bash
cd src && git add cmd/api-server/catalog.go cmd/api-server/catalog_test.go
git add ../CHANGELOG.md
git commit -m "$(cat <<'EOF'
refactor: share date-range parsing across api-server's catalog handlers

All 5 catalog REST handlers now call one parseDateRange helper instead
of repeating the same 8-line received_after/received_before parse-or-400
block. Also adds the CHANGELOG.md entry for this spec's whole set of
changes (write-path atomicity, decode visibility, facets.go split).
EOF
)"
```
