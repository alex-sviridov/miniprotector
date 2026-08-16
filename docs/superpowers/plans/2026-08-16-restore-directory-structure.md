# Restore Directory Structure Phase — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make directories real, queryable, restorable objects in `bwfs`'s storage, and give `rwfs
restore` a real phase 1 that recreates a selection's directory structure on the destination
filesystem — parent before child, stopping at the first failure, before any file content restore
(still unbuilt, future work).

**Architecture:** `file_version_records` (the only table a directory is ever recorded in — it never
gets a `file_data_records` row) gains real `source_host`/`path`/`type` columns, decomposed the same
way the 2026-08-15 design decomposed `file_data_records`. `bwfs`'s `ResolveRestoreFiles` gains a
second query, alongside the existing file query, that streams matching directory rows (`type = "d"`)
for folder-rule filters. `rwfs`'s `restoreResolver.Feed` widens its dispatch gate to also keep
directory rows. `rwfs restore` buckets resolved rows by type as they stream in — files logged exactly
as before, directories collected — then, once resolution succeeds with zero not-found failures, runs
phase 1: dedupe, sort parent-first, and call one small function per directory that checks existence,
creates if missing, and stubs a future permissions step.

**Tech Stack:** Go (`gorm.io/gorm`, `google.golang.org/grpc`) across `storage/filesystem`, `bwfs`,
`rwfs`.

## Global Constraints

- `EnsureFileVersion`'s new `sourceHost`/`path`/`objType` parameters are populated directly from
  `filesystem.FileInfo`'s existing `Source()`/`Path()`/`GetType()` accessors at both call sites —
  never re-parsed from the object ID string. Only the one-time backfill of pre-existing rows parses
  the ID string, mirroring `backfillFileDataColumns`'s exact precedent.
- A directory can only ever be selected by a folder rule (`path_is_prefix = true`); a host-specific,
  exact-path filter never matches a directory. (spec Architecture §2)
- `restoreResolver.Feed`'s precedence tie-break, `dest_path` rule attribution, and `filterFoundAny`
  tracking are unchanged — only the final dispatch gate widens. (spec Architecture §3)
- Phase 1 stops at the first directory-creation failure — no further directories are attempted, and
  the created/reused summary line is never logged on that path. (spec Architecture §4, confirmed in
  brainstorm)
- A pre-existing directory is always reused, regardless of `--overwrite`. A pre-existing
  non-directory at the target path is always a hard error. (spec Architecture §4)
- Not-found failures (existing behavior) abort before phase 1 ever starts. (spec Architecture §4)
- No metadata/permissions wiring this round — `createRestoreDirectory`'s permissions step is a
  literal no-op with a `TODO` comment; no new field is added to `FileRow` for it. (spec Non-Goals)
- Go tests: run via `cd src && go test ./<pkg>/... -run <TestName> -v`.

---

### Task 1: `storage/filesystem` — decompose `FileVersionRecord`

**Files:**
- Modify: `src/storage/filesystem/models.go` (`FileVersionRecord`)
- Modify: `src/storage/filesystem/fileversion.go` (`EnsureFileVersion`)
- Modify: `src/storage/filesystem/filedata.go` (`parseFileID`, its one caller)
- Modify: `src/storage/filesystem/db.go` (new `backfillFileVersionColumns`, wired into `openDB`)
- Modify: `src/storage/interface.go` (`BackupStore.EnsureFileVersion` signature)
- Test: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Consumes: nothing new — this is the foundation task.
- Produces: `EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata []byte,
  ctime int64) error` (both on `storage.BackupStore` and `*filesystem.Store`); `FileVersionRecord`
  rows carry real `SourceHost`/`Path`/`Type` columns going forward, backfilled for pre-existing rows
  at `openDB` time. Task 2 (`bwfs/handler.go`) updates both call sites to the new signature. Task 3
  (`bwfs`'s new directory query) depends on these columns existing and being populated.

- [ ] **Step 1: Write the failing tests**

In `src/storage/filesystem/store_test.go`, update the existing `TestEnsureFileVersion_CreatesRow`
and `TestEnsureFileVersion_DuplicateWithinJobIsNoOp` (they currently call the 4-arg form) to the new
5-string-plus-2 signature:

```go
func TestEnsureFileVersion_CreatesRow(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", "hosta", "/etc/a.conf", "f", []byte("meta"), 12345))

	v, err := store.LatestFileVersion("obj-1")
	require.NoError(t, err)
	assert.Equal(t, []byte("meta"), v.Metadata)
	assert.Equal(t, int64(12345), v.Ctime)

	var got FileVersionRecord
	require.NoError(t, store.RawDB().Where("object_id = ?", "obj-1").First(&got).Error)
	assert.Equal(t, "hosta", got.SourceHost)
	assert.Equal(t, "/etc/a.conf", got.Path)
	assert.Equal(t, "f", got.Type)
}

func TestEnsureFileVersion_DuplicateWithinJobIsNoOp(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", "hosta", "/etc/a.conf", "f", []byte("first"), 100))
	require.NoError(t, store.EnsureFileVersion("job-1", "obj-1", "hosta", "/etc/a.conf", "f", []byte("second"), 200))

	var count int64
```

(The rest of `TestEnsureFileVersion_DuplicateWithinJobIsNoOp`'s body, after that `var count int64`
line, is unchanged — only the two `EnsureFileVersion` call sites above it change.)

Grep the whole file for every other `store.EnsureFileVersion(` call and update each the same way —
there are exactly these two in the file (confirm with `grep -n "EnsureFileVersion(" src/storage/filesystem/store_test.go`
before editing, to catch any you might have missed).

Add three new tests, mirroring `TestOpenDB_BackfillsPreMigrationFileDataColumns`,
`TestOpenDB_BackfillTerminatesOnUnparseableFileID`, and `TestOpenDB_BackfillProbeUsesPathIndex`
exactly (same file, near them):

```go
// insertPreMigrationFileVersion writes a file_versions row the way rows
// written before source_host/path/type existed look once AutoMigrate has
// added those columns: a real object_id, all three new columns at their
// zero values. Goes through raw SQL to bypass EnsureFileVersion, which is
// what populates them going forward.
func insertPreMigrationFileVersion(t *testing.T, db *gorm.DB, objectID, jobID string) {
	t.Helper()
	require.NoError(t, db.Exec(
		`INSERT INTO file_version_records (object_id, job_id, source_host, path, type, metadata, ctime, created_at)
		 VALUES (?, ?, '', '', '', ?, ?, ?)`,
		objectID, jobID, []byte{1, 2, 3}, int64(1000), time.Now(),
	).Error)
}

func TestOpenDB_BackfillsPreMigrationFileVersionColumns(t *testing.T) {
	dir := t.TempDir()

	store, err := New(dir)
	require.NoError(t, err)
	insertPreMigrationFileVersion(t, store.RawDB(), "fs://hosta:d:/tmp/nested:1782605538", "job1")
	insertPreMigrationFileVersion(t, store.RawDB(), `fs://winhost:d:C:\Users\foo:1700000000`, "job2")
	// A malformed object_id must still be backfilled, not skipped -- same
	// fallback contract parseFileID already has for FileDataRecord.
	insertPreMigrationFileVersion(t, store.RawDB(), "not-a-valid-id", "job3")
	// A row already carrying its columns must be left exactly as-is.
	require.NoError(t, store.EnsureFileVersion("job4", "obj-4", "hostb", "/var/log/syslog", "f", nil, 999))
	require.NoError(t, store.Close())

	// Re-opening runs openDB again, which is where the backfill lives.
	store, err = New(dir)
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	for _, want := range []struct {
		objectID   string
		sourceHost string
		path       string
		objType    string
	}{
		{"fs://hosta:d:/tmp/nested:1782605538", "hosta", "/tmp/nested", "d"},
		{`fs://winhost:d:C:\Users\foo:1700000000`, "winhost", `C:\Users\foo`, "d"},
		{"not-a-valid-id", "", "not-a-valid-id", ""},
		{"obj-4", "hostb", "/var/log/syslog", "f"},
	} {
		var got FileVersionRecord
		require.NoError(t, store.RawDB().Where("object_id = ?", want.objectID).First(&got).Error, want.objectID)
		assert.Equal(t, want.sourceHost, got.SourceHost, want.objectID)
		assert.Equal(t, want.path, got.Path, want.objectID)
		assert.Equal(t, want.objType, got.Type, want.objectID)
	}

	var remaining int64
	require.NoError(t, store.RawDB().Model(&FileVersionRecord{}).Where("path = ?", "").Count(&remaining).Error)
	assert.Zero(t, remaining)
}

func TestOpenDB_BackfillFileVersionTerminatesOnUnparseableObjectID(t *testing.T) {
	dir := t.TempDir()

	store, err := New(dir)
	require.NoError(t, err)
	// tokens[2:len-1] is empty here, so parseFileID yields an empty path.
	insertPreMigrationFileVersion(t, store.RawDB(), "fs://host:f::1000", "job1")
	insertPreMigrationFileVersion(t, store.RawDB(), "fs://hosta:d:/etc/ok:1000", "job2")
	require.NoError(t, store.Close())

	done := make(chan error, 1)
	go func() {
		s, err := New(dir)
		if err == nil {
			defer s.Close()
			var got FileVersionRecord
			err = s.RawDB().Where("object_id = ?", "fs://hosta:d:/etc/ok:1000").First(&got).Error
			if err == nil && got.Path != "/etc/ok" {
				err = fmt.Errorf("the parseable row was not backfilled, path = %q", got.Path)
			}
		}
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("openDB's file_version backfill did not terminate -- the batch loop is spinning on a row it can never update")
	}
}

func TestOpenDB_BackfillFileVersionProbeUsesPathIndex(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.EnsureFileVersion("job1", "obj-1", "hosta", "/etc/a.conf", "f", nil, 1000))

	var plan []struct {
		Detail string
	}
	require.NoError(t, store.RawDB().Raw(
		`EXPLAIN QUERY PLAN SELECT seq, object_id FROM file_version_records WHERE path = ''`).Scan(&plan).Error)
	require.NotEmpty(t, plan)

	var detail string
	for _, p := range plan {
		detail += p.Detail + "\n"
	}
	assert.Contains(t, detail, "idx_file_version_path_host",
		"the backfill probe must use the path index; plan was:\n"+detail)
	assert.NotContains(t, detail, "SCAN file_version_records\n",
		"the backfill probe must not full-scan; plan was:\n"+detail)
}
```

Add the missing `"fmt"` import to `store_test.go` if it isn't already imported (check first).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./storage/filesystem/... -run "TestEnsureFileVersion|TestOpenDB_Backfill" -v`
Expected: FAIL — compile errors (`EnsureFileVersion` still takes 4 args; `FileVersionRecord` has no
`SourceHost`/`Path`/`Type` fields).

- [ ] **Step 3: Decompose `FileVersionRecord`**

In `src/storage/filesystem/models.go`, change `FileVersionRecord`:

```go
type FileVersionRecord struct {
	Seq        int64  `gorm:"primaryKey;autoIncrement"`
	ObjectID   string `gorm:"uniqueIndex:idx_job_object;index:idx_file_version_object_created,priority:1"`
	JobID      string `gorm:"uniqueIndex:idx_job_object"`
	SourceHost string `gorm:"index:idx_file_version_path_host,priority:2"`
	Path       string `gorm:"index:idx_file_version_path_host,priority:1"`
	Type       string // single char, from FileInfo.GetType() -- 'f', 'd', 'l', ...
	Metadata   []byte
	Ctime      int64
	CreatedAt  time.Time `gorm:"index:idx_file_version_object_created,priority:2"`
}
```

- [ ] **Step 4: Extend `parseFileID` to also return the type character**

In `src/storage/filesystem/filedata.go`, change `parseFileID`'s signature and body:

```go
// parseFileID splits "fs://host:type:path:mtime" into its components, so
// SourceHost/Type/Path/Mtime can be derived once, at write time, instead of
// re-parsed on every query -- mirrors cmd/bwfs/list.go's parseFileID
// exactly (duplicated, not imported: this is package "filesystem", that is
// package "main").
func parseFileID(fileID string) (source, objType, path string, mtime int64) {
	const prefix = "fs://"
	if !strings.HasPrefix(fileID, prefix) {
		return "", "", fileID, 0
	}
	rest := fileID[len(prefix):]
	tokens := strings.Split(rest, ":")
	if len(tokens) < 4 {
		return "", "", fileID, 0
	}
	source = tokens[0]
	objType = tokens[1]
	mt, err := strconv.ParseInt(tokens[len(tokens)-1], 10, 64)
	if err != nil {
		return "", "", fileID, 0
	}
	path = strings.Join(tokens[2:len(tokens)-1], ":")
	return source, objType, path, mt
}
```

Update its one existing caller in the same file, inside `CreateFileData` (currently `source, path,
mtime := parseFileID(fileID)`, the first line of that function's body) to `source, _, path, mtime :=
parseFileID(fileID)` — `FileDataRecord` has no `Type` column, so the new return value is discarded
there.

- [ ] **Step 5: Update `EnsureFileVersion` and the `BackupStore` interface**

In `src/storage/interface.go`, change the interface method:

```go
	// FileVersion operations - create metadata version for each backup
	EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata []byte, ctime int64) error
	RemoveFileVersion(jobID, objectID string) error
```

In `src/storage/filesystem/fileversion.go`, change `EnsureFileVersion`:

```go
// EnsureFileVersion idempotently records that objectID was observed during
// jobID's backup run. The first observation of a given (jobID, objectID)
// pair wins — a duplicate send of the same object within the same job (e.g.
// a future retry) is a safe no-op rather than a second catalog row.
// sourceHost/path/objType are the caller's already-known values (from
// filesystem.FileInfo's Source()/Path()/GetType() accessors at both
// cmd/bwfs/handler.go call sites) -- never re-derived from objectID here.
func (s *Store) EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata []byte, ctime int64) error {
	record := FileVersionRecord{
		JobID:      jobID,
		ObjectID:   objectID,
		SourceHost: sourceHost,
		Path:       path,
		Type:       objType,
		Metadata:   metadata,
		Ctime:      ctime,
		CreatedAt:  time.Now(),
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&record).Error
}
```

- [ ] **Step 6: Add the `FileVersionRecord` backfill**

In `src/storage/filesystem/db.go`, add `&FileVersionRecord{}` stays in the existing `AutoMigrate`
call (it's already there — no change needed to that list itself, GORM's `AutoMigrate` on an existing
struct with new fields adds the new columns automatically). After the existing
`backfillFileDataColumns(db)` call in `openDB`, add:

```go
	if err := backfillFileVersionColumns(db); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("backfill file version columns: %w", err)
	}
```

Add the function itself, right after `backfillFileDataColumns` (same file), mirroring it exactly:

```go
// backfillFileVersionColumns populates source_host/path/type on
// file_version_records rows written before those columns existed --
// otherwise every such row (including every directory ever backed up
// before this migration) stays permanently invisible to
// ResolveRestoreFiles's directory query. Same batched, idempotent,
// empty-path-marker approach as backfillFileDataColumns above -- see that
// function's comment for the full rationale (bounded batches, index-
// assisted probe, safe-to-interrupt).
func backfillFileVersionColumns(db *gorm.DB) error {
	const batchSize = 1000
	for {
		var stale []FileVersionRecord
		if err := db.Select("seq", "object_id").
			Where("path = ?", "").
			Limit(batchSize).
			Find(&stale).Error; err != nil {
			return fmt.Errorf("select stale rows: %w", err)
		}
		if len(stale) == 0 {
			return nil
		}

		progressed := 0
		err := db.Transaction(func(tx *gorm.DB) error {
			for _, r := range stale {
				source, objType, path, _ := parseFileID(r.ObjectID)
				if path == "" {
					continue
				}
				if err := tx.Model(&FileVersionRecord{}).
					Where("seq = ?", r.Seq).
					Updates(map[string]any{
						"source_host": source,
						"path":        path,
						"type":        objType,
					}).Error; err != nil {
					return fmt.Errorf("update row %d: %w", r.Seq, err)
				}
				progressed++
			}
			return nil
		})
		if err != nil {
			return err
		}
		if progressed == 0 {
			return nil
		}
	}
}
```

- [ ] **Step 7: Run the tests to verify they pass**

Run: `cd src && go test ./storage/filesystem/... -run "TestEnsureFileVersion|TestOpenDB_Backfill" -v`
Expected: PASS, all listed tests including the three new ones.

- [ ] **Step 8: Run the full storage/filesystem package suite**

Run: `cd src && go test ./storage/filesystem/...`
Expected: PASS. This will FAIL to compile at this point because `src/cmd/bwfs/handler.go` (Task 2)
and `src/cmd/bwfs/resolverestorefiles_test.go`'s `seedFile` helper (unaffected — it constructs
`FileVersionRecord{}` via a raw `Create`, not the method) still reference the old signature —
confirm the failure is specifically in `cmd/bwfs`, not in `storage/filesystem` itself:

Run: `cd src && go build ./storage/...`
Expected: PASS (this package alone compiles cleanly; `cmd/bwfs` is a separate package and is Task
2's job).

- [ ] **Step 9: Commit**

```bash
git add src/storage/filesystem/models.go src/storage/filesystem/fileversion.go src/storage/filesystem/filedata.go src/storage/filesystem/db.go src/storage/interface.go src/storage/filesystem/store_test.go
git commit -m "feat(storage): decompose file_version_records with real source_host/path/type

Mirrors the 2026-08-15 design's file_data_records decomposition,
applied to the one table a directory is ever recorded in -- a
directory never gets a file_data_records row, so this is the only
place its (host, path) can become a real, indexed, queryable column
instead of an opaque embedded string. Backfills pre-existing rows the
same batched, idempotent way backfillFileDataColumns already does.

cmd/bwfs's two EnsureFileVersion call sites are updated in the next
commit -- this package alone builds and tests green."
```

---

### Task 2: `bwfs` — wire `handler.go`'s two `EnsureFileVersion` call sites

**Files:**
- Modify: `src/cmd/bwfs/handler.go`

**Interfaces:**
- Consumes: Task 1's `EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata
  []byte, ctime int64) error`.
- Produces: every `file_version_records` row `bwfs` writes going forward (both the skip-path branch
  and the post-finalize branch) carries real `SourceHost`/`Path`/`Type`. Task 3's directory query
  depends on this for any *newly* backed-up directory (pre-existing ones rely on Task 1's backfill
  instead).

- [ ] **Step 1: Update both call sites**

In `src/cmd/bwfs/handler.go`'s `handleFileInfoRequest` (the skip-path/non-transferable branch,
~line 101), change:

```go
			if err := h.store.EnsureFileVersion(
				h.jobID,
				h.currentFile.ID(),
				h.currentFile.MetadataBlob(),
				h.currentFile.Ctime(),
			); err != nil {
```

to:

```go
			if err := h.store.EnsureFileVersion(
				h.jobID,
				h.currentFile.ID(),
				h.currentFile.Source(),
				h.currentFile.Path(),
				fmt.Sprintf("%c", h.currentFile.GetType()),
				h.currentFile.MetadataBlob(),
				h.currentFile.Ctime(),
			); err != nil {
```

In `fileWritten` (the post-finalize branch, ~line 236), the identical change:

```go
	if err := h.store.EnsureFileVersion(
		h.jobID,
		h.currentFile.ID(),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
	); err != nil {
```

becomes:

```go
	if err := h.store.EnsureFileVersion(
		h.jobID,
		h.currentFile.ID(),
		h.currentFile.Source(),
		h.currentFile.Path(),
		fmt.Sprintf("%c", h.currentFile.GetType()),
		h.currentFile.MetadataBlob(),
		h.currentFile.Ctime(),
	); err != nil {
```

`"fmt"` is already imported in this file (used elsewhere for `fmt.Sprintf`/`fmt.Errorf`) — no new
import needed.

- [ ] **Step 2: Confirm the package builds**

Run: `cd src && go build ./cmd/bwfs/...`
Expected: PASS.

- [ ] **Step 3: Run the full bwfs package test suite**

Run: `cd src && go test ./cmd/bwfs/...`
Expected: PASS — nothing in the existing suite asserts `EnsureFileVersion`'s exact call arguments
(confirmed: no `cmd/bwfs/*_test.go` calls the Go method directly; existing tests construct
`FileVersionRecord{}` rows via raw GORM `Create`, bypassing the method entirely), so this should be
a clean, non-behavior-changing-to-tests pass.

- [ ] **Step 4: Commit**

```bash
git add src/cmd/bwfs/handler.go
git commit -m "feat(bwfs): populate source_host/path/type on every file_version_records write

Both EnsureFileVersion call sites in handler.go now pass the values
straight from filesystem.FileInfo's existing accessors -- no string
parsing needed for a freshly-written row, only for Task 1's backfill
of rows that predate this change."
```

---

### Task 3: `bwfs` — `ResolveRestoreFiles` also yields directory rows

**Files:**
- Modify: `src/cmd/bwfs/resolverestorefiles.go`
- Test: `src/cmd/bwfs/resolverestorefiles_test.go`

**Interfaces:**
- Consumes: Task 1's `file_version_records.source_host`/`path`/`type` columns; the existing
  `restoreChildRanges` helper (unchanged, reused verbatim).
- Produces: `resolveRestoreDirectoryFilter(store *wfs.Store, filter *pb.RestoreFileFilter, yield
  func(source, path string) bool) error`. `ResolveRestoreFiles` streams `Type: "d"` rows (empty
  `FileUuid`, zero `Size`/`Chunks`) for every `path_is_prefix` filter, alongside its existing file
  rows. Task 4 (`rwfs`'s `Feed` gate) and Task 6 (`rwfs restore`'s phase 1) both depend on directory
  rows actually arriving over the wire.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/bwfs/resolverestorefiles_test.go`:

```go
// seedDirectory writes a file_version_records row shaped like a directory
// bwfs actually backed up -- no file_data_records row (directories never
// get one), real source_host/path/type columns (Task 1), no checksum
// concept. Mirrors seedFile's shape for the parts that apply.
func seedDirectory(t *testing.T, store *wfs.Store, source, path, jobID string, createdAtUnix int64) {
	t.Helper()
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{
		ObjectID:   fmt.Sprintf("fs://%s:d:%s:%d", source, path, createdAtUnix),
		JobID:      jobID,
		SourceHost: source,
		Path:       path,
		Type:       "d",
		CreatedAt:  unixTime(createdAtUnix),
	}).Error)
}

func collectResolvedDirectories(t *testing.T, store *wfs.Store, filter *pb.RestoreFileFilter) [][2]string {
	t.Helper()
	var got [][2]string
	err := resolveRestoreDirectoryFilter(store, filter, func(source, path string) bool {
		got = append(got, [2]string{source, path})
		return true
	})
	require.NoError(t, err)
	return got
}

func TestResolveRestoreDirectoryFilter_HostSpecificMatch(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)
	seedDirectory(t, store, "hostb", "/tmp/nested", "job1", 5000)

	got := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/tmp", PathIsPrefix: true})
	require.Len(t, got, 1)
	assert.Equal(t, [2]string{"hosta", "/tmp/nested"}, got[0])
}

func TestResolveRestoreDirectoryFilter_HostAgnosticMatchesEveryHost(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)
	seedDirectory(t, store, "hostb", "/tmp/nested/sub", "job1", 5000)
	seedDirectory(t, store, "hosta", "/tmp2/other", "job1", 5000)

	got := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Path: "/tmp", PathIsPrefix: true})
	require.Len(t, got, 2)
	assert.ElementsMatch(t, [][2]string{{"hosta", "/tmp/nested"}, {"hostb", "/tmp/nested/sub"}}, got)
}

func TestResolveRestoreDirectoryFilter_ExactPathFilterNeverMatchesDirectories(t *testing.T) {
	// A non-prefix filter is what a host-specific FILE rule builds
	// (buildRestoreFilters: PathIsPrefix = rule.Host == ""). This test
	// pins that resolveRestoreDirectoryFilter itself doesn't need the
	// caller to gate it -- an exact-path filter naturally matches nothing,
	// since restoreChildRanges/the "path = ?" branch below only admits an
	// exact-path filter's own literal path, never a directory row that
	// merely starts with it.
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	got := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/tmp/nested", PathIsPrefix: false})
	assert.Empty(t, got)
}

func TestResolveRestoreDirectoryFilter_TimeframeWindowing(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 1000)

	inWindow := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Path: "/tmp", PathIsPrefix: true, NotBefore: 500, NotAfter: 1500})
	assert.Len(t, inWindow, 1)

	outOfWindow := collectResolvedDirectories(t, store, &pb.RestoreFileFilter{Path: "/tmp", PathIsPrefix: true, NotBefore: 5000, NotAfter: 6000})
	assert.Empty(t, outOfWindow)
}

func TestResolveRestoreFiles_GRPCRoundTrip_IncludesDirectoryRows(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedFile(t, store, "fs://hosta:f:/tmp/nested/a.txt:1000", 10, []byte{1}, "job1", 5000)
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := NewListServer(store, logger)

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewListServiceClient(conn)
	stream, err := client.ResolveRestoreFiles(context.Background(), &pb.ResolveRestoreFilesRequest{
		Filters: []*pb.RestoreFileFilter{{Path: "/tmp/nested", PathIsPrefix: true}},
	})
	require.NoError(t, err)

	var gotFile, gotDir bool
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		switch resp.GetRow().GetType() {
		case "f":
			gotFile = true
			assert.Equal(t, "/tmp/nested/a.txt", resp.GetRow().GetPath())
		case "d":
			gotDir = true
			assert.Equal(t, "/tmp/nested", resp.GetRow().GetPath())
			assert.Empty(t, resp.GetRow().GetFileUuid(), "a directory row must never carry a file_uuid")
		}
	}
	assert.True(t, gotFile, "the file row must still be streamed")
	assert.True(t, gotDir, "the directory row must now also be streamed")
}
```

Add the missing `"fmt"` import to `resolverestorefiles_test.go` if it isn't already imported (check
first with `grep -n '"fmt"' src/cmd/bwfs/resolverestorefiles_test.go`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/bwfs/... -run "TestResolveRestoreDirectoryFilter|TestResolveRestoreFiles_GRPCRoundTrip_IncludesDirectoryRows" -v`
Expected: FAIL — `resolveRestoreDirectoryFilter` doesn't exist yet (compile error); the round-trip
test never observes a `"d"` row.

- [ ] **Step 3: Implement `resolveRestoreDirectoryFilter` and wire it into `ResolveRestoreFiles`**

In `src/cmd/bwfs/resolverestorefiles.go`, add after `resolveRestoreFilter`:

```go
// resolveRestoreDirectoryFilter mirrors resolveRestoreFilter's shape, but
// queries file_version_records directly (WHERE type = 'd') since a
// directory never has a file_data_records row to join through. Reuses
// restoreChildRanges verbatim -- same separator-aware subtree matching. A
// directory has no content-version concept to disambiguate the way a
// file's checksum does, so GROUP BY (source_host, path) alone fully
// collapses to one row per directory; MAX(created_at) only needs to prove
// at least one version exists inside [filter.NotBefore, filter.NotAfter],
// not identify which one, since this round only checks existence -- see
// docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md.
//
// Only ever called for a path_is_prefix filter -- an exact-path filter
// naturally matches nothing here (its own literal path is a leaf a
// directory could share, but "d" rows this query returns are folder
// containers a caller is about to recreate, and an exact-file rule has no
// reason to ask for that), so callers don't need to gate this themselves,
// though ResolveRestoreFiles does anyway for clarity (see below).
func resolveRestoreDirectoryFilter(store *wfs.Store, filter *pb.RestoreFileFilter, yield func(source, path string) bool) error {
	query := store.RawDB().
		Table("file_version_records").
		Select("source_host, path, MAX(created_at) AS best_version_at").
		Where("type = ?", "d").
		Group("source_host, path").
		Order("source_host ASC, path ASC")

	if filter.GetHost() != "" {
		query = query.Where("source_host = ?", filter.GetHost())
	}
	if filter.GetPathIsPrefix() {
		r := restoreChildRanges(filter.GetPath())
		query = query.Where("path = ? OR (path >= ? AND path < ?) OR (path >= ? AND path < ?)",
			filter.GetPath(), r.Unix.Lower, r.Unix.Upper, r.Windows.Lower, r.Windows.Upper)
	} else {
		query = query.Where("path = ?", filter.GetPath())
	}
	if filter.GetNotBefore() != 0 {
		query = query.Where("created_at >= ?", time.Unix(filter.GetNotBefore(), 0))
	}
	if filter.GetNotAfter() != 0 {
		query = query.Where("created_at <= ?", time.Unix(filter.GetNotAfter(), 0))
	}

	rows, err := query.Rows()
	if err != nil {
		return fmt.Errorf("resolve restore directory filter query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var source, path string
		var bestVersionAt any
		if err := rows.Scan(&source, &path, &bestVersionAt); err != nil {
			return fmt.Errorf("scan resolved directory: %w", err)
		}
		if !yield(source, path) {
			return rows.Err()
		}
	}
	return rows.Err()
}
```

Replace `ResolveRestoreFiles` with a version that also calls the new function for prefix filters:

```go
func (s *listServer) ResolveRestoreFiles(req *pb.ResolveRestoreFilesRequest, stream pb.ListService_ResolveRestoreFilesServer) error {
	for filterIndex, filter := range req.GetFilters() {
		var sendErr error
		err := resolveRestoreFilter(s.store, filter, func(c resolvedCandidate) bool {
			err := stream.Send(&pb.ResolveRestoreFilesResponse{
				Row: &pb.FileRow{
					FileUuid: c.FileUUID,
					Source:   c.Source,
					Type:     "f",
					Path:     c.Path,
					Size:     c.Size,
					Chunks:   int32(c.ChunkCount),
				},
				FilterIndex: int32(filterIndex),
			})
			if err != nil {
				sendErr = err
				return false
			}
			return true
		})
		if sendErr != nil {
			return sendErr
		}
		if err != nil {
			s.logger.Error("ResolveRestoreFiles query failed", "filter_index", filterIndex, "error", err)
			return err
		}

		if !filter.GetPathIsPrefix() {
			continue
		}
		err = resolveRestoreDirectoryFilter(s.store, filter, func(source, path string) bool {
			err := stream.Send(&pb.ResolveRestoreFilesResponse{
				Row:         &pb.FileRow{Source: source, Type: "d", Path: path},
				FilterIndex: int32(filterIndex),
			})
			if err != nil {
				sendErr = err
				return false
			}
			return true
		})
		if sendErr != nil {
			return sendErr
		}
		if err != nil {
			s.logger.Error("ResolveRestoreFiles directory query failed", "filter_index", filterIndex, "error", err)
			return err
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/bwfs/... -run "TestResolveRestoreDirectoryFilter|TestResolveRestoreFiles" -v`
Expected: PASS, all matching tests including the pre-existing `TestResolveRestoreFiles_GRPCRoundTrip`
and `TestResolveRestoreFiles_SendErrorIsReturned` (confirm neither regressed — the send-error test
seeds two *files* under `/etc`, a `path_is_prefix` filter, so `ResolveRestoreFiles` now also
attempts the directory sub-query for that filter; since no directory rows are seeded, that
sub-query returns nothing and the test's assertion on the file-send failure is unaffected).

- [ ] **Step 5: Run the full bwfs package test suite**

Run: `cd src && go test ./cmd/bwfs/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/bwfs/resolverestorefiles.go src/cmd/bwfs/resolverestorefiles_test.go
git commit -m "feat(bwfs): ResolveRestoreFiles streams directory rows for folder filters

resolveRestoreDirectoryFilter queries file_version_records directly
(WHERE type = 'd') since a directory never has a file_data_records row
-- reuses restoreChildRanges verbatim for the same separator-aware
subtree matching the file query already has. Only path_is_prefix
filters trigger it; an exact-path (file-level) filter never matches a
directory."
```

---

### Task 4: `rwfs` — widen `restoreResolver.Feed`'s dispatch gate

**Files:**
- Modify: `src/cmd/rwfs/resolve.go`
- Test: `src/cmd/rwfs/resolve_test.go`

**Interfaces:**
- Consumes: Task 3's `Type: "d"` rows now genuinely arriving over `ResolveRestoreFiles`.
- Produces: `restoreResolver.Feed` now returns `dispatch == true` for a `Type: "d"` row (any size),
  in addition to its existing `Type: "f" && Size > 0` case. Precedence tie-break and
  `filterFoundAny` tracking are unchanged. Task 6 (`rwfs restore`'s phase 1) depends on directory
  rows actually being dispatched instead of silently dropped.

- [ ] **Step 1: Update the existing test and add a new one**

In `src/cmd/rwfs/resolve_test.go`, `TestRestoreResolver_ZeroByteOrNonFileRowIsFoundButNotKept`
currently asserts a directory row is dropped — that's the exact behavior this task changes. Split it
into two tests. Replace the whole function with:

```go
func TestRestoreResolver_ZeroByteFileRowIsFoundButNotKept(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)

	resolver := newRestoreResolver(rules, filterToRuleIndex)
	zeroByte := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 0}
	if dispatch, _ := resolver.Feed(zeroByte, 0); dispatch {
		t.Fatal("a zero-byte file row must be found but not selected")
	}
	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a found-but-unselected row must not be reported as not-found, got %v", notFound)
	}
}

func TestRestoreResolver_DirectoryRowIsDispatched(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)

	resolver := newRestoreResolver(rules, filterToRuleIndex)
	dir := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "d"}
	dispatch, ruleIndex := resolver.Feed(dir, 0)
	if !dispatch {
		t.Fatal("a directory row must now be dispatched, not dropped")
	}
	if ruleIndex != 0 {
		t.Fatalf("expected the winning rule index to be 0, got %d", ruleIndex)
	}

	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a dispatched directory must not be reported as not-found, got %v", notFound)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run "TestRestoreResolver_ZeroByteFileRowIsFoundButNotKept|TestRestoreResolver_DirectoryRowIsDispatched" -v`
Expected: FAIL — `TestRestoreResolver_DirectoryRowIsDispatched` finds `dispatch == false` (today's
gate drops every non-`"f"` row regardless of size).

- [ ] **Step 3: Widen `Feed`'s gate**

In `src/cmd/rwfs/resolve.go`, change `Feed`'s final block:

```go
	r.filterFoundAny[filterIndex] = true
	if row.GetType() != "f" || row.GetSize() <= 0 {
		return false, winningRuleIndex
	}
	return true, winningRuleIndex
```

to:

```go
	r.filterFoundAny[filterIndex] = true
	isRestorableFile := row.GetType() == "f" && row.GetSize() > 0
	isDirectory := row.GetType() == "d"
	if !isRestorableFile && !isDirectory {
		return false, winningRuleIndex
	}
	return true, winningRuleIndex
```

Update the doc comment's last sentence (currently "Only a real, non-empty file is ever dispatched
(type/size defense-in-depth -- bwfs's file_data_records only ever holds such rows today, but this
guards against that invariant ever changing silently)") to:

```go
// A real, non-empty file or a directory is dispatched; anything else
// (zero-byte file, symlink, or other non-regular/non-directory type) is
// found but not dispatched -- rwfs restore's phase 1 (directory creation)
// and file resolution are the only two things bwfs's data can currently be
// turned into. See
// docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run "TestRestoreResolver_ZeroByteFileRowIsFoundButNotKept|TestRestoreResolver_DirectoryRowIsDispatched" -v`
Expected: PASS.

- [ ] **Step 5: Run the full rwfs package test suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/rwfs/resolve.go src/cmd/rwfs/resolve_test.go
git commit -m "feat(rwfs): dispatch directory rows through restoreResolver.Feed

Widens the type/size gate to keep Type: \"d\" rows (any size)
alongside the existing Type: \"f\" && Size > 0 case. Precedence
tie-break and NotFound tracking are unchanged -- only the final
dispatch decision widens, now that bwfs (Task 3) actually sends
directory rows."
```

---

### Task 5: `rwfs` — `restoreDirectory` type and `createRestoreDirectory`

**Files:**
- Create: `src/cmd/rwfs/restoredirectory.go`
- Test: `src/cmd/rwfs/restoredirectory_test.go`

**Interfaces:**
- Consumes: nothing — pure `os`-package filesystem logic, no gRPC/store involved.
- Produces: `type restoreDirectory struct { DestPath string }`; `func createRestoreDirectory(dir
  restoreDirectory) (created bool, err error)`. Task 6 (`rwfs restore`'s phase 1 driver) is the only
  caller.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/rwfs/restoredirectory_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRestoreDirectory_CreatesMissingDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "newdir")

	created, err := createRestoreDirectory(restoreDirectory{DestPath: target})
	require.NoError(t, err)
	assert.True(t, created)

	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
}

func TestCreateRestoreDirectory_ReusesExistingDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "existing")
	require.NoError(t, os.Mkdir(target, 0o755))

	created, err := createRestoreDirectory(restoreDirectory{DestPath: target})
	require.NoError(t, err)
	assert.False(t, created)
}

func TestCreateRestoreDirectory_NonDirectoryAtPathIsHardError(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "actually-a-file")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o644))

	_, err := createRestoreDirectory(restoreDirectory{DestPath: target})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestCreateRestoreDirectory_MissingParentReturnsError(t *testing.T) {
	base := t.TempDir()
	// "missing-parent" is never created, so "child" under it can't be
	// created either -- os.Mkdir is not recursive, and this pins that the
	// resulting error surfaces rather than being silently swallowed.
	target := filepath.Join(base, "missing-parent", "child")

	_, err := createRestoreDirectory(restoreDirectory{DestPath: target})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestCreateRestoreDirectory -v`
Expected: FAIL — `restoreDirectory`/`createRestoreDirectory` don't exist yet (compile error).

- [ ] **Step 3: Implement**

Create `src/cmd/rwfs/restoredirectory.go`:

```go
// restoredirectory.go implements phase 1 of `rwfs restore`: recreating a
// resolved selection's directory structure on the destination filesystem,
// before any file content restore (still unbuilt -- see
// docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md).
package main

import (
	"fmt"
	"os"
)

// restoreDirectory is one directory phase 1 must ensure exists at its
// (dest_path-renamed) destination.
type restoreDirectory struct {
	DestPath string
}

// createRestoreDirectory checks whether dir.DestPath exists, creates it if
// not (its parent must already exist -- callers are responsible for
// creating in parent-before-child order), and would apply captured
// permissions/ownership once that metadata is threaded through from bwfs.
//
// TODO: apply dir's captured permissions/ownership once FileRow carries
// the metadata blob -- deferred until that step is actually built (see
// this design's Non-Goals).
func createRestoreDirectory(dir restoreDirectory) (created bool, err error) {
	info, statErr := os.Stat(dir.DestPath)
	switch {
	case statErr == nil && info.IsDir():
		return false, nil
	case statErr == nil:
		return false, fmt.Errorf("path exists and is not a directory: %s", dir.DestPath)
	case !os.IsNotExist(statErr):
		return false, statErr
	}
	if err := os.Mkdir(dir.DestPath, 0o755); err != nil {
		return false, err
	}
	return true, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestCreateRestoreDirectory -v`
Expected: PASS.

- [ ] **Step 5: Run the full rwfs package test suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/rwfs/restoredirectory.go src/cmd/rwfs/restoredirectory_test.go
git commit -m "feat(rwfs): add createRestoreDirectory, the per-directory phase 1 primitive

check-exists / create-if-missing / (stubbed) update-permissions, one
directory at a time. A non-directory occupying the target path is a
hard error. No caller yet -- Task 6 wires this into rwfs restore's
phase 1 driver."
```

---

### Task 6: `rwfs restore` — phase 1 driver

**Files:**
- Modify: `src/cmd/rwfs/restore.go`
- Modify: `src/cmd/rwfs/verify_test.go` (`testResolveServer.ResolveRestoreFiles` — extend the shared
  test fixture; `verify.go`'s own tests are unaffected, this only adds a new capability the fixture
  didn't have)
- Test: `src/cmd/rwfs/restore_test.go`

**Interfaces:**
- Consumes: Task 4's widened `Feed` (directory rows now dispatch), Task 5's `restoreDirectory`/
  `createRestoreDirectory`, the existing `ancestorsOrSelfRestorePath` (`rules.go`, unchanged) for
  depth-based sort ordering.
- Produces: `runRestoreWithConn` now actually creates directories on the destination filesystem
  before returning success. No other task depends on this — it's the plan's terminal behavior
  change (Task 7 is documentation only).

- [ ] **Step 1: Extend `testResolveServer` to also emit directory rows**

In `src/cmd/rwfs/verify_test.go`, inside `testResolveServer.ResolveRestoreFiles`'s `for filterIndex,
filter := range req.GetFilters()` loop, immediately after the existing file query's `if sendErr !=
nil { return sendErr }` block (and before the loop's closing `}`), add:

```go
		if !filter.GetPathIsPrefix() {
			continue
		}
		dirQuery := s.store.RawDB().
			Table("file_version_records").
			Select("source_host, path").
			Where("type = ?", "d").
			Group("source_host, path")
		if filter.GetHost() != "" {
			dirQuery = dirQuery.Where("source_host = ?", filter.GetHost())
		}
		dirQuery = dirQuery.Where("path = ? OR path LIKE ?", filter.GetPath(), filter.GetPath()+"/%")
		if filter.GetNotBefore() != 0 {
			dirQuery = dirQuery.Where("created_at >= ?", time.Unix(filter.GetNotBefore(), 0))
		}
		if filter.GetNotAfter() != 0 {
			dirQuery = dirQuery.Where("created_at <= ?", time.Unix(filter.GetNotAfter(), 0))
		}

		dirRows, err := dirQuery.Rows()
		if err != nil {
			return err
		}
		dirSendErr := func() error {
			defer dirRows.Close()
			for dirRows.Next() {
				var source, path string
				if err := dirRows.Scan(&source, &path); err != nil {
					return err
				}
				if err := stream.Send(&pb.ResolveRestoreFilesResponse{
					Row:         &pb.FileRow{Source: source, Type: "d", Path: path},
					FilterIndex: int32(filterIndex),
				}); err != nil {
					return err
				}
			}
			return dirRows.Err()
		}()
		if dirSendErr != nil {
			return dirSendErr
		}
```

Update the type's doc comment (immediately above `type testResolveServer struct`) to add one
sentence noting it now also serves directory rows for `path_is_prefix` filters, LIKE-based (not
`restoreChildRanges`-based) same as its existing file-query simplification.

- [ ] **Step 2: Write the failing integration tests**

Add to `src/cmd/rwfs/restore_test.go`:

```go
// seedDirectory writes a file_version_records row shaped like a directory
// bwfs actually backed up, for driving testResolveServer's new directory
// query (Step 1 above) -- no file_data_records row, since directories
// never get one.
func seedDirectory(t *testing.T, store *wfs.Store, source, path, jobID string, createdAtUnix int64) {
	t.Helper()
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{
		ObjectID:   fmt.Sprintf("fs://%s:d:%s:%d", source, path, createdAtUnix),
		JobID:      jobID,
		SourceHost: source,
		Path:       path,
		Type:       "d",
		CreatedAt:  time.Unix(createdAtUnix, 0),
	}).Error)
}

func TestRunRestore_CreatesDirectoryStructureForFolderSelection(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	destDir := destBase + "/nested_recovered"
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/tmp/nested","include":true,"dest_path":%q}]}`, destDir)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
	require.NoError(t, err)

	info, statErr := os.Stat(destDir)
	require.NoError(t, statErr, "the directory must actually exist on disk now")
	assert.True(t, info.IsDir())

	out := logBuf.String()
	assert.Contains(t, out, "creating restored directory structure")
	assert.Contains(t, out, "restored directory structure created")
	assert.Contains(t, out, "created=1")
	assert.Contains(t, out, "reused=0")
}

func TestRunRestore_ReusesExistingDirectory(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	destDir := destBase + "/already-here"
	require.NoError(t, os.Mkdir(destDir, 0o755))
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/tmp/nested","include":true,"dest_path":%q}]}`, destDir)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
	require.NoError(t, err)

	out := logBuf.String()
	assert.Contains(t, out, "created=0")
	assert.Contains(t, out, "reused=1")
}

func TestRunRestore_AbortsOnDirectoryCreationFailureBeforeSummary(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	// A plain file sits where the directory needs to go.
	destDir := destBase + "/blocked"
	require.NoError(t, os.WriteFile(destDir, []byte("data"), 0o644))
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/tmp/nested","include":true,"dest_path":%q}]}`, destDir)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "blocked")

	out := logBuf.String()
	assert.Contains(t, out, "failed to create restored directory")
	assert.NotContains(t, out, "restored directory structure created",
		"the summary line must never be logged when phase 1 aborts")
}

func TestRunRestore_ParentBeforeChildOrdering(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	// Three levels deep, seeded in a deliberately non-hierarchical order --
	// if phase 1 didn't sort parent-first, os.Mkdir would fail on whichever
	// child streams in before its parent exists.
	seedDirectory(t, store, "hosta", "/tmp/a/b/c", "job1", 5000)
	seedDirectory(t, store, "hosta", "/tmp/a", "job1", 5000)
	seedDirectory(t, store, "hosta", "/tmp/a/b", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	destRoot := destBase + "/a"
	rulesJSON := fmt.Sprintf(`{"rules":[{"host":"","path":"/tmp/a","include":true,"dest_path":%q}]}`, destRoot)

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
	require.NoError(t, err)

	for _, p := range []string{destRoot, destRoot + "/b", destRoot + "/b/c"} {
		info, statErr := os.Stat(p)
		require.NoError(t, statErr, p)
		assert.True(t, info.IsDir(), p)
	}
}

func TestRunRestore_NotFoundAbortsBeforePhase1(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	// A directory that WOULD be creatable, but a file-level rule elsewhere
	// in the same rule set matches nothing -- phase 1 must never run.
	seedDirectory(t, store, "hosta", "/tmp/nested", "job1", 5000)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	listSrv := &testResolveServer{store: store}
	restoreSrv := &recordingRestoreServer{}

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	destBase := t.TempDir()
	rulesJSON := fmt.Sprintf(`{"rules":[
		{"host":"","path":"/tmp/nested","include":true,"dest_path":%q},
		{"host":"hosta","path":"/etc/never-backed-up.conf","include":true}
	]}`, destBase+"/nested")

	err = runRestoreWithDialer(t, logger, lis, rulesJSON, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 file(s) failed resolution")

	out := logBuf.String()
	assert.NotContains(t, out, "creating restored directory structure",
		"phase 1 must never start when resolution already has a not-found failure")
	_, statErr := os.Stat(destBase + "/nested")
	assert.True(t, os.IsNotExist(statErr), "the directory must not have been created")
}
```

Add `"os"` to this file's import block if not already present (check first — `restore_test.go`
currently has no `"os"` import).

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/rwfs/... -run TestRunRestore -v`
Expected: FAIL — `runRestoreWithConn` doesn't yet bucket directory rows or run phase 1, so no
directory is ever created on disk, and none of the new log lines are emitted.

- [ ] **Step 4: Implement phase 1 in `restore.go`**

In `src/cmd/rwfs/restore.go`, add `"os"` and `"sort"` to the import block. Replace
`runRestoreWithConn`'s body from the `total := 0` line through its final `return nil` with:

```go
	total := 0
	warnings := 0
	var dirs []restoreDirectory
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("resolve restore files: %w", err)
		}

		row := resp.GetRow()
		dispatch, ruleIndex := resolver.Feed(row, resp.GetFilterIndex())
		if !dispatch {
			continue
		}
		destPath := restoreDestPath(rules[ruleIndex], row.GetPath())

		if row.GetType() == "d" {
			dirs = append(dirs, restoreDirectory{DestPath: destPath})
			continue
		}

		total++
		if !quiet {
			logger.Info("resolved",
				"source", row.GetSource(),
				"path", row.GetPath(),
				"dest_path", destPath,
			)
		}
	}

	for _, nf := range resolver.NotFound() {
		warnings++
		logger.Warn("resolution failed", "source", nf.Host, "path", nf.Path, "reason", nf.Reason)
	}

	logger.Info("summary", "resolved", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed resolution", warnings)
	}

	return createRestoreDirectoryStructure(logger, dirs)
}

// createRestoreDirectoryStructure is restore's phase 1: recreate every
// resolved directory, parent before child, stopping at the first failure
// (per docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md).
// dirs may contain duplicate DestPaths -- two different rules resolving to
// the same destination -- which this collapses to one create-or-reuse
// rather than flagging as a conflict (see the design's Non-Goals).
func createRestoreDirectoryStructure(logger *slog.Logger, dirs []restoreDirectory) error {
	if len(dirs) == 0 {
		return nil
	}

	seen := make(map[string]bool, len(dirs))
	var unique []restoreDirectory
	for _, d := range dirs {
		if seen[d.DestPath] {
			continue
		}
		seen[d.DestPath] = true
		unique = append(unique, d)
	}
	sort.Slice(unique, func(i, j int) bool {
		return len(ancestorsOrSelfRestorePath(unique[i].DestPath)) < len(ancestorsOrSelfRestorePath(unique[j].DestPath))
	})

	logger.Info("creating restored directory structure")
	created, reused := 0, 0
	for _, dir := range unique {
		wasCreated, err := createRestoreDirectory(dir)
		if err != nil {
			logger.Error("failed to create restored directory", "path", dir.DestPath, "reason", err)
			return fmt.Errorf("create restored directory %s: %w", dir.DestPath, err)
		}
		if wasCreated {
			created++
		} else {
			reused++
		}
	}
	logger.Info("restored directory structure created", "created", created, "reused", reused)
	return nil
}
```

Update the file's top-of-file package doc comment (currently describing `restore.go` as purely
log-only) to note that phase 1 now actually creates directories — one added sentence is enough,
matching this file's existing terse style; don't rewrite the whole comment.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/rwfs/... -run TestRunRestore -v`
Expected: PASS, all `TestRunRestore_*` tests including the five new ones and the three pre-existing
ones (`TestRunRestore_LogsResolvedFileWithRenamedDestPath`,
`TestRunRestore_FileLevelRuleMatchingNothingFails`,
`TestRunRestore_FolderLevelRuleMatchingNothingSucceeds` — confirm none regressed; the first seeds no
directories, so phase 1 runs with an empty `dirs` slice and returns immediately without logging
anything new, per Step 4's `len(dirs) == 0` early return).

- [ ] **Step 6: Run the full rwfs package test suite**

Run: `cd src && go test ./cmd/rwfs/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/rwfs/restore.go src/cmd/rwfs/verify_test.go src/cmd/rwfs/restore_test.go
git commit -m "feat(rwfs): rwfs restore actually creates the resolved directory structure

Phase 1: resolved directory rows are collected during stream
consumption (files still just logged, unchanged), then -- once
resolution has zero not-found failures -- deduped, sorted
parent-before-child, and created one at a time via
createRestoreDirectory. Stops at the first failure; the
created/reused summary line is only ever reached on full success.
This is the first round of the restore-execute line that actually
writes to the destination filesystem -- file content restore (phase
2) remains unbuilt."
```

---

### Task 7: Documentation and changelog

**Files:**
- Modify: `docs/protocols/list.md`
- Modify: `docs/components/rwfs.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: the final, shipped behavior of Tasks 1-6.
- Produces: nothing consumed by other tasks — terminal documentation task, per
  `.claude/CLAUDE.md`'s protocol-change and feature-change rules (this plan touches no `.proto`
  file, but does change `ResolveRestoreFiles`'s server-side behavior and `rwfs restore`'s CLI
  behavior, both documented).

- [ ] **Step 1: `docs/protocols/list.md`**

In the `## ResolveRestoreFiles` section (currently line 126), the intro paragraph (line 128) reads:

```markdown
The `ResolveRestoreFiles` RPC resolves a batch of restore-rule-shaped filters directly, scoped by host, path, and timeframe, instead of the unbounded dump `ListFiles` would require for the same job. This is used by `rwfs verify --rules-stdin` and its `restore` sibling (log-only so far -- it resolves and logs the would-be restore, but does not yet write files) to efficiently find the exact backed-up versions matching each restore rule.
```

Replace with:

```markdown
The `ResolveRestoreFiles` RPC resolves a batch of restore-rule-shaped filters directly, scoped by host, path, and timeframe, instead of the unbounded dump `ListFiles` would require for the same job. This is used by `rwfs verify --rules-stdin` and its `restore` sibling to efficiently find the exact backed-up versions matching each restore rule -- `restore`'s file-content resolution is still log-only (it resolves and logs the would-be file restore, but does not yet write file content), but as of this round it also actually creates the resolved directory structure on disk (see [rwfs](../components/rwfs.md#restore)).
```

Add a new subsection after `### Filter Semantics` (currently ending at line 163, before `###
Filter Index`):

```markdown
### Directory Rows

For a `path_is_prefix` filter (a folder rule), the response also includes a row per real,
backed-up directory under that path -- `type = "d"`, empty `file_uuid` (a directory has no
content, and nothing ever calls `RestoreFile` against one), zero `size`/`chunks`. These come from
`file_version_records` directly rather than `file_data_records` -- a directory never gets a
`file_data_records` row (see [Design: Restore Directory Structure
Phase](../superpowers/specs/2026-08-16-restore-directory-structure-design.md)'s Problem section for
why), so this is the only place its existence is queryable at all. An exact-path (non-prefix,
file-level) filter never returns a directory row.
```

Update the `### Usage` section's final sentence (currently line 171):

```markdown
This RPC is used only by `rwfs verify --rules-stdin` and `rwfs restore --rules-stdin` (the latter still log-only -- see [rwfs](../components/rwfs.md#restore)). Plain `bwfs list` and `rwfs list` continue to use `ListFiles` unchanged.
```

to:

```markdown
This RPC is used only by `rwfs verify --rules-stdin` and `rwfs restore --rules-stdin` (the latter's
directory rows are acted on -- see [rwfs](../components/rwfs.md#restore) -- its file rows are still
log-only). Plain `bwfs list` and `rwfs list` continue to use `ListFiles` unchanged.
```

- [ ] **Step 2: `docs/components/rwfs.md`**

In the `## restore` section (currently lines 135-162), replace the intro paragraph:

```markdown
Resolves a restore policy's rules against a remote `bwfs` server's file listing and logs what a
real restore of that policy would do -- **this round writes nothing to disk**. Requires
`--rules-stdin` (the only way to select files; there is no plain-listing restore mode).
```

with:

```markdown
Resolves a restore policy's rules against a remote `bwfs` server's file listing. **File content
restore is still log-only** -- it resolves and logs what a real file restore would do, writing
nothing. **Directory structure restore is real**: for every resolved directory (see [list
protocol](../protocols/list.md#directory-rows)), `rwfs restore` actually creates it on the
destination filesystem -- phase 1 of two, in parent-before-child order, before any file content
would be written (phase 2, still unbuilt). Requires `--rules-stdin` (the only way to select
anything; there is no plain-listing restore mode). See [Design: Restore Directory Structure
Phase](../superpowers/specs/2026-08-16-restore-directory-structure-design.md).
```

Replace the paragraph after the flags table (currently lines 161-162):

```markdown
Exit code follows the same not-found rule `verify --rules-stdin` uses: a file-level rule matching no
row is a failure (non-zero exit); a folder-level rule matching nothing is not.
```

with:

```markdown
Exit code follows the same not-found rule `verify --rules-stdin` uses: a file-level rule matching no
row is a failure (non-zero exit); a folder-level rule matching nothing is not. A not-found failure
aborts before directory creation (phase 1) ever starts.

Phase 1 logs `creating restored directory structure` once at start, then either a `restored
directory structure created` summary (with `created`/`reused` counts) on full success, or a
`failed to create restored directory` error and an immediate abort on the first failure -- no
further directories are attempted, and the summary line is never reached. A pre-existing directory
is always reused, regardless of `--overwrite`; a pre-existing non-directory at the destination path
is always a hard error.
```

- [ ] **Step 3: `docs/ARCHITECTURE.md`**

Change the `rwfs` row of the component status table (currently line 10):

```markdown
| rwfs | Restore Writer for File System — queries bwfs (list, verify, restore) | list + verify implemented; `restore` resolves rules and logs the would-be restore, does not yet write files |
```

to:

```markdown
| rwfs | Restore Writer for File System — queries bwfs (list, verify, restore) | list + verify implemented; `restore` resolves rules, creates the resolved directory structure on disk, and logs the would-be file restore (file content not yet written) |
```

Change the `agent` row (currently line 13) — replace `"log-only for now"` with a note that
directory creation is real:

```markdown
| agent | Node Agent — reconciles local state against embedded policies | Implemented (bootstrap credential renewal, operating-certificate refresh via `issuer`, policy fetch via `policyclient`, policy-driven backup execution via `brfs`, one-shot restore-policy verification via `rwfs verify`, and one-shot restore execution via `rwfs restore` for `mode: "restore"` policies, log-only for now) |
```

to:

```markdown
| agent | Node Agent — reconciles local state against embedded policies | Implemented (bootstrap credential renewal, operating-certificate refresh via `issuer`, policy fetch via `policyclient`, policy-driven backup execution via `brfs`, one-shot restore-policy verification via `rwfs verify`, and one-shot restore execution via `rwfs restore` for `mode: "restore"` policies -- directory structure creation is real, file content restore is still log-only) |
```

In the prose paragraph (currently lines 103-106), replace:

```markdown
cached `"restore"`-typed policy, executing `rwfs verify` against the resolved source `bwfs` (or,
when that policy's `mode` is `"restore"`, the new log-only `rwfs restore`, which this round only
resolves and logs the file list) — see
```

with:

```markdown
cached `"restore"`-typed policy, executing `rwfs verify` against the resolved source `bwfs` (or,
when that policy's `mode` is `"restore"`, `rwfs restore`, which creates the resolved directory
structure on disk and logs the would-be file restore -- file content restore is still unbuilt) —
see
```

In the `## Restore/Verify Process` section (currently lines 126-131), replace the last bullet:

```markdown
- **rwfs** (future restore) reconstructs files on the destination filesystem
```

with:

```markdown
- **rwfs restore** creates the resolved directory structure on the destination filesystem (real,
  as of this round); file content restore remains future work
```

- [ ] **Step 4: Add a CHANGELOG entry**

In `CHANGELOG.md`, insert a new entry immediately after line 3 (the `All notable changes...` line),
before the current top entry (`## 2026-08-16 — restore execution: first slice (log-only)`):

```markdown
## 2026-08-16 — restore execution: directory structure phase

`rwfs restore` now actually creates the resolved directory structure on the destination filesystem
-- the first write this restore-execution line has ever performed; file content restore remains
unbuilt, still log-only. Directories are now real, queryable, restorable objects: `bwfs`'s
`file_version_records` table (the only place a directory is ever recorded -- it never gets the
content-bearing `file_data_records` row a file does) gained real `source_host`/`path`/`type`
columns, and `ResolveRestoreFiles` streams matching directory rows for folder-rule selections
alongside its existing file rows. `rwfs restore` creates them one at a time, parent before child,
stopping at the first failure with a detailed error; a pre-existing directory is always safely
reused, a pre-existing non-directory at the target path is always a hard error.

```

- [ ] **Step 5: Verify the doc edits render sensibly**

Run: `git diff docs/ CHANGELOG.md`
Expected: a clean, readable diff -- no broken markdown, no stray blank lines inside the CHANGELOG
entry, every added cross-link path resolves to a file that actually exists (spot-check with `ls
docs/superpowers/specs/2026-08-16-restore-directory-structure-design.md`).

- [ ] **Step 6: Commit**

```bash
git add docs/protocols/list.md docs/components/rwfs.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document the restore directory-structure phase

Per .claude/CLAUDE.md's protocol-change, feature-change, and
changelog rules."
```
