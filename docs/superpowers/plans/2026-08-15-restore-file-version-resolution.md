# Restore File Version Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give restore rules a per-rule timeframe ("use the latest backup inside this window"), and make
resolving a restore policy's rules into an actual file list proportional to what the rules touch instead
of a full-store scan — the shared foundation both `rwfs verify` (today) and a future `rwfs restore`
(unbuilt) will consume.

**Architecture:** `RestoreRule` gains `not_before`/`not_after`, threaded through `policy-server`/
`api-server`/`agent` exactly like `dest_path` was. `bwfs` gets decomposed, indexed columns
(`source_host`, `path`, `mtime`) on `file_data_records` plus an index on `file_version_records`, and a new
server-streaming RPC, `ListService.ResolveRestoreFiles`, that resolves one winning `file_id` per
`(source_host, path)` per rule, scoped by that rule's timeframe, using a real DB cursor. `rwfs verify
--rules-stdin` consumes that stream incrementally instead of buffering a full-store `ListFiles` dump,
applying the existing longest-ancestor-wins rule precedence per arriving row and a new tie-break so the
most specific rule's resolved version wins when timeframes overlap.

**Tech Stack:** Go, gRPC/protobuf (`protoc` + `protoc-gen-go`/`protoc-gen-go-grpc`, already installed),
GORM + `modernc.org/sqlite` (bwfs's storage layer), `testify` (`assert`/`require`) for Go tests.

## Global Constraints

- No backward compatibility required for any proto/schema/signature change in this plan (explicit project
  decision — this is a from-scratch redesign of the resolution path, not an additive one).
- Every proto change must ship with its `docs/protocols/` update in the same commit, per
  `.claude/CLAUDE.md`'s documentation rule.
- Every feature change (new flag, new field, changed behavior) must update the relevant
  `docs/components/<component>.md` in the same commit as the code change, per `.claude/CLAUDE.md`.
- `CHANGELOG.md` gets one entry before this branch merges to `main` (last task in this plan).
- Follow this repo's existing duplication convention: a `main` package cannot import another `main`
  package, so shared shapes like `RestoreRule` are intentionally re-declared per-package (already true for
  `policy-server`, `api-server`'s `ruleDTO`, `agent`, and `rwfs` — this plan extends each copy the same
  way `dest_path` did, it does not consolidate them).

---

## File Structure

| File | Responsibility |
|---|---|
| `src/api/policyserver.proto` | `RestoreRule` gains `not_before`/`not_after` |
| `src/api/list.proto` | New `ResolveRestoreFiles` streaming RPC + `RestoreFileFilter` / request / response messages |
| `src/storage/filesystem/models.go` | `FileDataRecord` gains `SourceHost`/`Path`/`Mtime` (indexed); `FileVersionRecord` gains a `(ObjectID, CreatedAt)` index |
| `src/storage/filesystem/filedata.go` | `CreateFileData` parses the new columns out of `fileID` internally |
| `src/cmd/bwfs/resolverestorefiles.go` | New: the per-filter resolution query (streaming DB cursor) + the `ResolveRestoreFiles` RPC handler |
| `src/cmd/policy-server/restore_policy.go` | `RestoreRule`/`Validate`/`ToProto` gain the timeframe fields |
| `src/cmd/api-server/policies.go` | `ruleDTO`/`handleCreateRestore`/`toPolicyDTO` gain the timeframe fields |
| `src/cmd/agent/restore.go` | Duplicated `RestoreRule` struct gains the timeframe fields (pass-through only) |
| `src/cmd/rwfs/rules.go` | `RestoreRule` gains the timeframe fields; resolution extended to expose the winning rule's index |
| `src/cmd/rwfs/resolve.go` | New: `restoreResolver` — the streaming tie-break/not-found pipeline, replaces `applyRulesStdin` |
| `src/cmd/rwfs/verify.go` | `runVerify` wired to build filters, call the new RPC, consume it through `restoreResolver`, and dispatch to the worker pool without full buffering |

---

### Task 1: Proto — `RestoreRule` timeframe fields + `ResolveRestoreFiles` RPC

**Files:**
- Modify: `src/api/policyserver.proto`
- Modify: `src/api/list.proto`
- Modify (generated, do not hand-edit beyond running `make proto`): `src/api/policyserver.pb.go`,
  `src/api/list.pb.go`, `src/api/list_grpc.pb.go`
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/protocols/list.md`

**Interfaces:**
- Produces: `pb.RestoreRule.NotBefore`/`.NotAfter` (`int64`, getters `GetNotBefore()`/`GetNotAfter()`);
  `pb.RestoreFileFilter{Host, Path, PathIsPrefix, NotBefore, NotAfter}`;
  `pb.ResolveRestoreFilesRequest{Filters []*RestoreFileFilter}`;
  `pb.ResolveRestoreFilesResponse{Row *FileRow, FilterIndex int32}`;
  `pb.ListServiceClient.ResolveRestoreFiles(ctx, *ResolveRestoreFilesRequest) (ListService_ResolveRestoreFilesClient, error)`
  with `.Recv() (*ResolveRestoreFilesResponse, error)`; server-side
  `pb.ListService_ResolveRestoreFilesServer` with `.Send(*ResolveRestoreFilesResponse) error`.

- [ ] **Step 1: Add `not_before`/`not_after` to `RestoreRule` in `policyserver.proto`**

Edit `src/api/policyserver.proto`, replacing the existing `RestoreRule` message:

```proto
// One restore-cart selection rule: host-agnostic (Host == "") folder rules
// and host-specific file rules resolve by longest-matching-path-ancestor,
// exactly like web/src/utils/restoreRules.js's resolveFile. policy-server
// never interprets these beyond the load-time validation in
// RestorePolicy.Validate (non-empty Path, dest_path/not_before/not_after
// only on an included rule); resolution happens at verify time, in rwfs.
message RestoreRule {
  string host      = 1; // "" = host-agnostic, matches every source host
  string path      = 2;
  bool   include    = 3;
  // Destination path to restore to, if different from path. Empty (or
  // equal to path) means "no rename -- restore to the original path."
  // Only meaningful when include is true; see RestorePolicy.Validate.
  string dest_path = 4;
  // Timeframe bounding which backed-up version of this rule's selection to
  // use: the latest version whose backup date falls inside
  // [not_before, not_after] wins; a version outside the window is ignored
  // entirely, never used as a fallback. Unix seconds; 0 = unbounded on
  // that side. Only meaningful when include is true; see
  // RestorePolicy.Validate.
  int64  not_before = 5;
  int64  not_after  = 6;
}
```

- [ ] **Step 2: Add `ResolveRestoreFiles` RPC and its messages to `list.proto`**

Edit `src/api/list.proto`:

```proto
syntax = "proto3";

package listservice;

option go_package = "./proto";

service ListService {
  rpc ListFiles(ListRequest) returns (ListResponse);
  // ResolveRestoreFiles resolves a batch of restore-rule-shaped filters
  // directly, scoped by host/path/timeframe, instead of the unbounded dump
  // ListFiles would require for the same job. Server-streaming: one
  // response per resolved row, so a filter matching millions of rows never
  // has to be buffered whole on either side. See
  // docs/protocols/list.md#resolverestorefiles.
  rpc ResolveRestoreFiles(ResolveRestoreFilesRequest) returns (stream ResolveRestoreFilesResponse);
}

message ListRequest {
  string server_name = 1; // source hostname filter; empty = all sources
  string path        = 2; // prefix filter on file path; empty = no filter
  string filter      = 3; // free-text substring filter; empty = no filter
}

message ListResponse {
  repeated FileRow rows = 1;
}

message FileRow {
  string file_uuid     = 1;
  string source        = 2;
  string type          = 3;
  string path          = 4;
  int64  timestamp      = 5;
  int64  size           = 6;
  int32  chunks         = 7;
  int64  versions       = 8;
  string created_at     = 9; // RFC3339 UTC, matches listformat's JSON rendering
}

// RestoreFileFilter is one restore rule's selection criteria, sent as-is
// (only included rules become filters -- exclude rules never need file
// data, see docs/protocols/list.md#resolverestorefiles).
message RestoreFileFilter {
  string host           = 1; // "" = host-agnostic (folder rule)
  string path           = 2;
  bool   path_is_prefix = 3; // true = folder rule (recursive subtree), false = exact file rule
  int64  not_before     = 4; // 0 = unbounded
  int64  not_after      = 5; // 0 = unbounded
}

message ResolveRestoreFilesRequest {
  repeated RestoreFileFilter filters = 1;
}

message ResolveRestoreFilesResponse {
  FileRow row          = 1;
  int32   filter_index = 2; // index into the request's filters -- which filter resolved this row
}
```

- [ ] **Step 3: Regenerate protobuf code**

```bash
make proto
```

- [ ] **Step 4: Verify the generated code compiles**

```bash
cd src && go build ./... && cd ..
```

Expected: no errors. `src/api/policyserver.pb.go` now has `RestoreRule.NotBefore`/`.NotAfter`;
`src/api/list.pb.go`/`list_grpc.pb.go` now have `RestoreFileFilter`, `ResolveRestoreFilesRequest`,
`ResolveRestoreFilesResponse`, and the new client/server streaming interfaces.

- [ ] **Step 5: Update `docs/protocols/policy-server.md`**

Find the existing `RestoreRule` proto block (it currently ends with the `dest_path` field, per
`grep -n "dest_path" docs/protocols/policy-server.md`) and add the two new fields with the same
one-line-comment style already used there, e.g.:

```
  int64  not_before = 5; // restore this rule's latest backup dated on/after this unix time; 0 = unbounded
  int64  not_after  = 6; // ...and on/before this unix time; 0 = unbounded. Outside the window = ignored, not a fallback.
```

Add one sentence to the surrounding prose noting that a rule's timeframe selects which backed-up version
of its selection is used, resolved at verify time (mirroring how the `dest_path` sentence there already
reads).

- [ ] **Step 6: Update `docs/protocols/list.md`**

Add a new `## ResolveRestoreFiles` section (after the existing `## Output Format` / `## Key Design
Decisions` sections, or wherever the doc's existing structure best fits — follow its existing heading
style) covering:
- The request/response proto block from Step 2.
- Why streaming: a single folder-rule filter can match far more rows than a `bwfs` node's whole catalog
  did when `ListFiles` was written for ("thousands of entries per host, not millions") — see
  `docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md` for the full rationale.
- Filter semantics: `host`/`path_is_prefix` mirror `RestoreRule`'s host-agnostic-folder vs.
  host-specific-file convention; `not_before`/`not_after` (0 = unbounded) scope which backed-up version
  wins — the version with the latest `file_version_records.created_at` inside the window, per distinct
  `(source_host, path)`; zero versions in the window means that filter contributes no row for that path,
  never a stale fallback from outside the window.
- `filter_index`: which of the request's `filters` entries produced this row — only `ListService` clients
  that send more than one filter (i.e. `rwfs`) need this; a single-filter caller can ignore it.
- Note this RPC is used only by `rwfs verify --rules-stdin` (and its future `restore` sibling); `bwfs
  list`/`rwfs list` keep using plain `ListFiles`, unchanged.

- [ ] **Step 7: Commit**

```bash
git add src/api/policyserver.proto src/api/list.proto src/api/policyserver.pb.go src/api/list.pb.go \
  src/api/list_grpc.pb.go docs/protocols/policy-server.md docs/protocols/list.md
git commit -m "feat(api): add ResolveRestoreFiles RPC and RestoreRule timeframe fields"
```

---

### Task 2: `bwfs` storage — decomposed, indexed columns

**Files:**
- Modify: `src/storage/filesystem/models.go`
- Modify: `src/storage/filesystem/filedata.go`
- Test: `src/storage/filesystem/store_test.go`

**Interfaces:**
- Consumes: none (this task is self-contained storage-layer work).
- Produces: `FileDataRecord{UUID, FileID, SourceHost, Path, Mtime, Size, Checksum, ChunkCount, CreatedAt}`
  (columns `source_host`, `path` composite-indexed as `(path, source_host)`, `path`-leading);
  `FileVersionRecord` composite index on `(ObjectID, CreatedAt)`. `CreateFileData(fileID string, size
  int64) error`'s signature is unchanged — it now parses `fileID` internally to populate the three new
  columns, so every existing caller (`cmd/bwfs/handler.go`, and every existing test) needs no changes.

- [ ] **Step 1: Write the failing test**

Add to `src/storage/filesystem/store_test.go`:

```go
func TestCreateFileData_PopulatesDecomposedColumns(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.CreateFileData("fs://workstation:f:C:/Users/foo/bar.txt:1782605538", 42))

	var rec FileDataRecord
	require.NoError(t, store.RawDB().Where("file_id = ?", "fs://workstation:f:C:/Users/foo/bar.txt:1782605538").First(&rec).Error)
	assert.Equal(t, "workstation", rec.SourceHost)
	assert.Equal(t, "C:/Users/foo/bar.txt", rec.Path)
	assert.Equal(t, int64(1782605538), rec.Mtime)
}

func TestCreateFileData_MalformedFileIDLeavesColumnsEmpty(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.CreateFileData("not-a-valid-id", 1))

	var rec FileDataRecord
	require.NoError(t, store.RawDB().Where("file_id = ?", "not-a-valid-id").First(&rec).Error)
	assert.Equal(t, "", rec.SourceHost)
	assert.Equal(t, "not-a-valid-id", rec.Path)
	assert.Equal(t, int64(0), rec.Mtime)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./storage/filesystem/... -run 'TestCreateFileData_' -v && cd ..
```

Expected: FAIL — `FileDataRecord` has no field `SourceHost`/`Path`/`Mtime` yet.

- [ ] **Step 3: Add the columns and index to `FileDataRecord`, and index `FileVersionRecord`**

Edit `src/storage/filesystem/models.go`:

```go
type FileDataRecord struct {
	UUID       string `gorm:"primaryKey"`
	FileID     string `gorm:"index"` // retained for uniqueness/display; not parsed on the query path anymore
	SourceHost string `gorm:"index:idx_file_data_path_host,priority:2"`
	Path       string `gorm:"index:idx_file_data_path_host,priority:1"`
	Mtime      int64
	Size       int64
	Checksum   []byte
	ChunkCount int
	CreatedAt  time.Time
}
```

```go
type FileVersionRecord struct {
	Seq       int64  `gorm:"primaryKey;autoIncrement"`
	ObjectID  string `gorm:"uniqueIndex:idx_job_object;index:idx_file_version_object_created,priority:1"`
	JobID     string `gorm:"uniqueIndex:idx_job_object"`
	Metadata  []byte
	Ctime     int64
	CreatedAt time.Time `gorm:"index:idx_file_version_object_created,priority:2"`
}
```

`(path, source_host)` is `path`-leading: a host-agnostic folder rule's recursive-subtree query
(`path >= 'x/' AND path < 'x0'`) becomes a real B-tree range scan; a host-specific rule adds an equality
filter on the second column for free. `(object_id, created_at)` lets the version-window join (Task 3) do
an index range scan per candidate `file_id` instead of a full scan of `file_version_records` — the larger
of the two tables, since it grows once per file per backup job, not just once per content change.

- [ ] **Step 4: Parse `fileID` into the new columns in `CreateFileData`**

Edit `src/storage/filesystem/filedata.go`, adding a private parser and using it in `CreateFileData`:

```go
import (
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

// parseFileID splits "fs://host:type:path:mtime" into its components, so
// CreateFileData can persist them as indexed columns instead of leaving
// them locked inside an opaque string only parseable in Go, at read time,
// across the whole table. Mirrors cmd/bwfs/list.go's parseFileID exactly
// (duplicated here -- storage/filesystem can't import a cmd package, and
// that copy also returns a "type" this package has no column for, since
// file_data_records only ever holds type 'f' rows -- see
// cmd/bwfs/handler.go's handleFileInfoRequest). Malformed input never
// errors: it leaves path == the raw fileID and host/mtime at their zero
// values, the same permissive fallback the cmd/bwfs copy uses.
func parseFileID(fileID string) (source, path string, mtime int64) {
	const prefix = "fs://"
	if !strings.HasPrefix(fileID, prefix) {
		return "", fileID, 0
	}
	rest := fileID[len(prefix):]
	tokens := strings.Split(rest, ":")
	if len(tokens) < 4 {
		return "", fileID, 0
	}
	source = tokens[0]
	mt, err := strconv.ParseInt(tokens[len(tokens)-1], 10, 64)
	if err != nil {
		return "", fileID, 0
	}
	path = strings.Join(tokens[2:len(tokens)-1], ":")
	return source, path, mt
}

func (s *Store) CreateFileData(fileID string, size int64) error {
	source, path, mtime := parseFileID(fileID)
	record := FileDataRecord{
		UUID:       uuid.New().String(),
		FileID:     fileID,
		SourceHost: source,
		Path:       path,
		Mtime:      mtime,
		Size:       size,
		CreatedAt:  time.Now(),
	}
	return s.db.Create(&record).Error
}
```

Leave the rest of `filedata.go` (`FileDataExists`, `FinalizeFileData`, `FileData`, `FileDataChunks`)
unchanged.

- [ ] **Step 5: Run the tests to verify they pass**

```bash
cd src && go test ./storage/filesystem/... -run 'TestCreateFileData_' -v && cd ..
```

Expected: PASS.

- [ ] **Step 6: Run the full storage package test suite**

```bash
cd src && go test ./storage/... && cd ..
```

Expected: PASS — `AutoMigrate` (already called from `openDB`, unchanged) picks up the new columns/indexes
automatically; no existing test constructs a `FileDataRecord` literal that would need updating (confirm
via `grep -rn "FileDataRecord{" src/storage`).

- [ ] **Step 7: Commit**

```bash
git add src/storage/filesystem/models.go src/storage/filesystem/filedata.go src/storage/filesystem/store_test.go
git commit -m "feat(storage): decompose file_id into indexed columns for restore resolution"
```

---

### Task 3: `bwfs` — per-filter resolution query

**Files:**
- Create: `src/cmd/bwfs/resolverestorefiles.go`
- Test: `src/cmd/bwfs/resolverestorefiles_test.go`

**Interfaces:**
- Consumes: `FileDataRecord{SourceHost, Path}` / `FileVersionRecord{ObjectID, CreatedAt}` (Task 2);
  `store.RawDB() *gorm.DB` (existing, `src/storage/filesystem/store.go:70`); `pb.RestoreFileFilter` (Task
  1).
- Produces: `resolvedCandidate{FileUUID, Source, Path, Size, ChunkCount string/string/string/int64/int}`;
  `resolveRestoreFilter(store *wfs.Store, filter *pb.RestoreFileFilter, yield func(resolvedCandidate)
  bool) error` — streams the winning row per distinct `(source_host, path)` the filter matches, via a real
  DB cursor. Consumed by Task 4's RPC handler.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/bwfs/resolverestorefiles_test.go`:

```go
package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

func collectResolved(t *testing.T, store *wfs.Store, filter *pb.RestoreFileFilter) []resolvedCandidate {
	t.Helper()
	var got []resolvedCandidate
	err := resolveRestoreFilter(store, filter, func(c resolvedCandidate) bool {
		got = append(got, c)
		return true
	})
	require.NoError(t, err)
	return got
}

func seedFile(t *testing.T, store *wfs.Store, fileID string, size int64, checksum []byte, jobID string, versionCreatedAtUnix int64) {
	t.Helper()
	require.NoError(t, store.CreateFileData(fileID, size))
	require.NoError(t, store.FinalizeFileData(fileID, checksum))
	require.NoError(t, store.RawDB().Model(&wfs.FileVersionRecord{}).
		Create(&wfs.FileVersionRecord{
			ObjectID:  fileID,
			JobID:     jobID,
			CreatedAt: unixTime(versionCreatedAtUnix),
		}).Error)
}

func TestResolveRestoreFilter_ExactFileMatch(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/etc/nginx.conf:1000", 10, []byte{1}, "job1", 5000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/etc/nginx.conf"})
	require.Len(t, got, 1)
	assert.Equal(t, "hosta", got[0].Source)
	assert.Equal(t, "/etc/nginx.conf", got[0].Path)
}

func TestResolveRestoreFilter_HostAgnosticFolderMatchesEveryHost(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/etc/a.conf:1000", 10, []byte{1}, "job1", 5000)
	seedFile(t, store, "fs://hostb:f:/etc/sub/b.conf:1000", 10, []byte{1}, "job1", 5000)
	seedFile(t, store, "fs://hosta:f:/etc2/other.conf:1000", 10, []byte{1}, "job1", 5000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Path: "/etc", PathIsPrefix: true})
	require.Len(t, got, 2)
	paths := []string{got[0].Path, got[1].Path}
	assert.ElementsMatch(t, []string{"/etc/a.conf", "/etc/sub/b.conf"}, paths)
}

func TestResolveRestoreFilter_PicksLatestVersionInsideWindow(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	// Two distinct mtimes (content versions) of the same path.
	seedFile(t, store, "fs://hosta:f:/data/f.txt:1000", 10, []byte{1}, "job1", 1000)
	seedFile(t, store, "fs://hosta:f:/data/f.txt:2000", 20, []byte{2}, "job2", 2000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/data/f.txt", NotBefore: 1, NotAfter: 1500})
	require.Len(t, got, 1)
	assert.Equal(t, int64(10), got[0].Size) // the mtime=1000 version, whose version is inside the window
}

func TestResolveRestoreFilter_UnchangedFileStaysFoundAcrossManyReattestations(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	// Content created in January (created_at=1000), never changes, but is
	// re-attested (re-backed-up unchanged) through August.
	require.NoError(t, store.CreateFileData("fs://hosta:f:/data/stable.txt:1000", 10))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/data/stable.txt:1000", []byte{1}))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/data/stable.txt:1000", JobID: "jan", CreatedAt: unixTime(1000)}).Error)
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/data/stable.txt:1000", JobID: "jul", CreatedAt: unixTime(7000)}).Error)

	// A window around July, long after the content's original upload.
	got := collectResolved(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/data/stable.txt", NotBefore: 6000, NotAfter: 8000})
	require.Len(t, got, 1, "the July re-attestation must satisfy the window even though FileDataRecord.CreatedAt is January")
}

func TestResolveRestoreFilter_NoVersionInWindowReturnsNothing(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/data/f.txt:1000", 10, []byte{1}, "job1", 1000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Host: "hosta", Path: "/data/f.txt", NotBefore: 5000, NotAfter: 6000})
	assert.Empty(t, got)
}

func TestResolveRestoreFilter_FolderPrefixDoesNotOverMatchSiblingPath(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	seedFile(t, store, "fs://hosta:f:/etc/a.conf:1000", 10, []byte{1}, "job1", 5000)
	seedFile(t, store, "fs://hosta:f:/etc2/b.conf:1000", 10, []byte{1}, "job1", 5000)

	got := collectResolved(t, store, &pb.RestoreFileFilter{Path: "/etc", PathIsPrefix: true})
	require.Len(t, got, 1)
	assert.Equal(t, "/etc/a.conf", got[0].Path)
}
```

Add a tiny local helper at the bottom of the same file (`time.Unix` truncates monotonic reads
differently across call sites, so a single helper keeps the seeded timestamps exact):

```go
func unixTime(sec int64) time.Time { return time.Unix(sec, 0) }
```

Add `"time"` and `"github.com/stretchr/testify/assert"` to the test file's imports alongside `require`.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./cmd/bwfs/... -run 'TestResolveRestoreFilter_' -v && cd ..
```

Expected: FAIL — `resolveRestoreFilter`/`resolvedCandidate` don't exist yet.

- [ ] **Step 3: Implement `resolveRestoreFilter`**

Create `src/cmd/bwfs/resolverestorefiles.go`:

```go
package main

import (
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

// resolvedCandidate is the single winning row bwfs found for one
// (source_host, path) a RestoreFileFilter matched -- the version whose
// latest in-window file_version_records.created_at is greatest.
type resolvedCandidate struct {
	FileUUID   string
	Source     string
	Path       string
	Size       int64
	ChunkCount int
}

// pathPrefixUpperBound returns the exclusive upper bound of the
// lexicographic range covering every path "under" prefix (prefix followed
// by a path separator and anything else). '0' (0x30) is the next ASCII
// byte after '/' (0x2F), so [prefix+"/", prefix+"0") matches exactly
// "prefix/...", never a sibling like "prefix2/...".
func pathPrefixUpperBound(prefix string) string {
	return prefix + "0"
}

// resolveRestoreFilter streams the winning candidate row per distinct
// (source_host, path) that filter selects, using a real DB cursor rather
// than materializing the whole match set in memory -- see
// docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md's
// Performance Notes. yield is called once per winning row; a false return
// stops iteration early.
//
// For each candidate file_id, only its latest finalized FileDataRecord is
// considered (mirrors cmd/bwfs/list.go's queryFileRows), and only
// file_version_records whose created_at falls inside
// [filter.NotBefore, filter.NotAfter] (0 = unbounded on that side) count
// toward "backed up inside this timeframe" -- a file whose content hasn't
// changed can still have been re-attested by many later backup jobs, and
// any of those re-attestations satisfies the window (see the design doc's
// Problem section).
func resolveRestoreFilter(store *wfs.Store, filter *pb.RestoreFileFilter, yield func(resolvedCandidate) bool) error {
	query := store.RawDB().
		Table("file_data_records fd").
		Select("fd.uuid AS uuid, fd.source_host AS source_host, fd.path AS path, fd.size AS size, fd.chunk_count AS chunk_count, MAX(fv.created_at) AS best_version_at").
		Joins("JOIN file_version_records fv ON fv.object_id = fd.file_id").
		Where("fd.checksum IS NOT NULL").
		Where("fd.created_at = (SELECT MAX(fd2.created_at) FROM file_data_records fd2 WHERE fd2.file_id = fd.file_id AND fd2.checksum IS NOT NULL)").
		Group("fd.uuid, fd.source_host, fd.path, fd.size, fd.chunk_count").
		Order("fd.source_host ASC, fd.path ASC, best_version_at DESC")

	if filter.GetHost() != "" {
		query = query.Where("fd.source_host = ?", filter.GetHost())
	}
	if filter.GetPathIsPrefix() {
		query = query.Where("fd.path = ? OR (fd.path >= ? AND fd.path < ?)",
			filter.GetPath(), filter.GetPath()+"/", pathPrefixUpperBound(filter.GetPath()))
	} else {
		query = query.Where("fd.path = ?", filter.GetPath())
	}
	if filter.GetNotBefore() != 0 {
		query = query.Where("fv.created_at >= ?", time.Unix(filter.GetNotBefore(), 0))
	}
	if filter.GetNotAfter() != 0 {
		query = query.Where("fv.created_at <= ?", time.Unix(filter.GetNotAfter(), 0))
	}

	rows, err := query.Rows()
	if err != nil {
		return fmt.Errorf("resolve restore filter query: %w", err)
	}
	defer rows.Close()

	var lastSource, lastPath string
	haveLast := false
	for rows.Next() {
		var c resolvedCandidate
		var bestVersionAt time.Time
		if err := rows.Scan(&c.FileUUID, &c.Source, &c.Path, &c.Size, &c.ChunkCount, &bestVersionAt); err != nil {
			return fmt.Errorf("scan resolved candidate: %w", err)
		}
		if haveLast && c.Source == lastSource && c.Path == lastPath {
			continue // an older mtime for a path already emitted -- ORDER BY put the winner first
		}
		lastSource, lastPath, haveLast = c.Source, c.Path, true
		if !yield(c) {
			return rows.Err()
		}
	}
	return rows.Err()
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd src && go test ./cmd/bwfs/... -run 'TestResolveRestoreFilter_' -v && cd ..
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/bwfs/resolverestorefiles.go src/cmd/bwfs/resolverestorefiles_test.go
git commit -m "feat(bwfs): add scoped, timeframe-aware restore-file resolution query"
```

---

### Task 4: `bwfs` — wire the `ResolveRestoreFiles` RPC handler

**Files:**
- Modify: `src/cmd/bwfs/resolverestorefiles.go` (add the RPC handler alongside Task 3's query function)
- Modify: `src/cmd/bwfs/server.go` — confirm/extend `ListService` registration if needed (it already
  registers `listServer`, which will now also implement `ResolveRestoreFiles`; check whether registration
  is a single `pb.RegisterListServiceServer(grpcServer, listServer)` call — if so, no change needed there,
  since the same `*listServer` value now satisfies the extended interface once Step 1 below adds the
  method to it).
- Test: `src/cmd/bwfs/resolverestorefiles_test.go` (extend)

**Interfaces:**
- Consumes: `resolveRestoreFilter` (Task 3); `listServer{store, logger}` (existing,
  `src/cmd/bwfs/listserver.go`).
- Produces: `(*listServer).ResolveRestoreFiles(req *pb.ResolveRestoreFilesRequest, stream
  pb.ListService_ResolveRestoreFilesServer) error`, registered on `ListService`.

- [ ] **Step 1: Confirm `ListService` registration is a single call covering the whole server value**

```bash
grep -n "RegisterListServiceServer" src/cmd/bwfs/*.go
```

Expected: one call, e.g. `pb.RegisterListServiceServer(grpcServer, listSrv)` where `listSrv` is the same
`*listServer` `NewListServer` returns. If so, no registration-site change is needed — adding a method to
`*listServer` is enough, since it will now satisfy the (regenerated, Task 1) `pb.ListServiceServer`
interface's larger method set.

- [ ] **Step 2: Write the failing gRPC round-trip test**

Add to `src/cmd/bwfs/resolverestorefiles_test.go` (mirrors `integration_test.go`'s `TestListFiles_GRPCRoundTrip` bufconn pattern):

```go
func TestResolveRestoreFiles_GRPCRoundTrip(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	seedFile(t, store, "fs://hosta:f:/etc/a.conf:1000", 10, []byte{1}, "job1", 5000)

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
		Filters: []*pb.RestoreFileFilter{
			{Host: "hosta", Path: "/etc/a.conf"},
			{Path: "/nonexistent", PathIsPrefix: true},
		},
	})
	require.NoError(t, err)

	var got []*pb.ResolveRestoreFilesResponse
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		got = append(got, resp)
	}

	require.Len(t, got, 1)
	assert.Equal(t, "/etc/a.conf", got[0].GetRow().GetPath())
	assert.Equal(t, int32(0), got[0].GetFilterIndex())
}
```

Add `"context"`, `"io"`, `"log/slog"`, `"net"`, `"google.golang.org/grpc"`,
`"google.golang.org/grpc/credentials/insecure"`, `"google.golang.org/grpc/test/bufconn"` to the test
file's imports.

- [ ] **Step 3: Run the test to verify it fails**

```bash
cd src && go test ./cmd/bwfs/... -run 'TestResolveRestoreFiles_GRPCRoundTrip' -v && cd ..
```

Expected: FAIL — `*listServer` doesn't implement `ResolveRestoreFiles` yet, so `pb.RegisterListServiceServer` won't compile against it (or the test simply won't build).

- [ ] **Step 4: Implement the RPC handler**

Add to `src/cmd/bwfs/resolverestorefiles.go`:

```go
func (s *listServer) ResolveRestoreFiles(req *pb.ResolveRestoreFilesRequest, stream pb.ListService_ResolveRestoreFilesServer) error {
	for filterIndex, filter := range req.GetFilters() {
		err := resolveRestoreFilter(s.store, filter, func(c resolvedCandidate) bool {
			sendErr := stream.Send(&pb.ResolveRestoreFilesResponse{
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
			return sendErr == nil
		})
		if err != nil {
			s.logger.Error("ResolveRestoreFiles query failed", "filter_index", filterIndex, "error", err)
			return err
		}
	}
	return nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
cd src && go test ./cmd/bwfs/... -run 'TestResolveRestoreFiles_GRPCRoundTrip' -v && cd ..
```

Expected: PASS.

- [ ] **Step 6: Run the full `bwfs` package test suite**

```bash
cd src && go test ./cmd/bwfs/... && cd ..
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/bwfs/resolverestorefiles.go src/cmd/bwfs/resolverestorefiles_test.go
git commit -m "feat(bwfs): serve ResolveRestoreFiles as a streaming RPC"
```

---

### Task 5: `policy-server` — `RestoreRule` timeframe fields

**Files:**
- Modify: `src/cmd/policy-server/restore_policy.go`
- Test: `src/cmd/policy-server/restore_policy_test.go`
- Modify: `docs/components/policy-server.md`

**Interfaces:**
- Consumes: `pb.RestoreRule.NotBefore`/`.NotAfter` (Task 1).
- Produces: `RestoreRule{Host, Path, Include, DestPath, NotBefore, NotAfter}`; `Validate()` rejects
  `not_after < not_before` (both non-zero) and either field set on an excluded rule; `ToProto` carries
  both through.

- [ ] **Step 1: Write the failing tests**

Find the existing `dest_path` validation/round-trip tests in `src/cmd/policy-server/restore_policy_test.go`
(`grep -n "DestPath" src/cmd/policy-server/restore_policy_test.go` to locate them) and add alongside them:

```go
func TestRestorePolicy_Validate_RejectsNotBeforeAfterOnExcludedRule(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      validPolicyBase(),
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "h", Path: "/etc", Include: false, NotBefore: 100}},
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only valid on an included rule")
}

func TestRestorePolicy_Validate_RejectsNotAfterBeforeNotBefore(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      validPolicyBase(),
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "h", Path: "/etc", Include: true, NotBefore: 200, NotAfter: 100}},
	}
	err := p.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not_after")
}

func TestRestorePolicy_Validate_AcceptsUnboundedTimeframe(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      validPolicyBase(),
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "h", Path: "/etc", Include: true}},
	}
	assert.NoError(t, p.Validate())
}

func TestRestorePolicy_ToProto_IncludesTimeframe(t *testing.T) {
	p := &RestorePolicy{
		PolicyBase:      validPolicyBase(),
		StoragePolicyID: "sp-1",
		Rules:           []RestoreRule{{Host: "h", Path: "/etc", Include: true, NotBefore: 100, NotAfter: 200}},
	}
	proto := p.ToProto(false)
	require.Len(t, proto.Rules, 1)
	assert.Equal(t, int64(100), proto.Rules[0].GetNotBefore())
	assert.Equal(t, int64(200), proto.Rules[0].GetNotAfter())
}
```

Check whether a `validPolicyBase()` test helper already exists in that test file (used by the existing
`dest_path` tests) — reuse it as-is; if the existing tests instead inline a `PolicyBase` literal, match
that exact pattern instead of introducing a new helper.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./cmd/policy-server/... -run 'TestRestorePolicy_' -v && cd ..
```

Expected: FAIL — `RestoreRule` has no `NotBefore`/`NotAfter` fields yet.

- [ ] **Step 3: Add the fields, validation, and proto pass-through**

Edit `src/cmd/policy-server/restore_policy.go`:

```go
type RestoreRule struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	DestPath  string `json:"dest_path,omitempty"`
	// NotBefore/NotAfter bound which backed-up version of this rule's
	// selection to use: the latest version whose backup date falls inside
	// [NotBefore, NotAfter] wins; nothing in the window means no fallback
	// -- this rule selects nothing. Unix seconds; 0 = unbounded on that
	// side. Only meaningful when Include is true (see Validate). See
	// docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md.
	NotBefore int64 `json:"not_before,omitempty"`
	NotAfter  int64 `json:"not_after,omitempty"`
}
```

In `Validate()`, extend the per-rule loop (the existing `dest_path` check is the model to follow):

```go
	for i, r := range p.Rules {
		if r.Path == "" {
			return fmt.Errorf("rules[%d]: path is required", i)
		}
		if r.DestPath != "" && r.DestPath != r.Path && !r.Include {
			return fmt.Errorf("rules[%d]: dest_path is only valid on an included rule", i)
		}
		if (r.NotBefore != 0 || r.NotAfter != 0) && !r.Include {
			return fmt.Errorf("rules[%d]: not_before/not_after is only valid on an included rule", i)
		}
		if r.NotBefore != 0 && r.NotAfter != 0 && r.NotAfter < r.NotBefore {
			return fmt.Errorf("rules[%d]: not_after must not be before not_before", i)
		}
	}
```

In `ToProto`, extend the rule conversion:

```go
	for i, r := range p.Rules {
		rules[i] = &pb.RestoreRule{
			Host: r.Host, Path: r.Path, Include: r.Include, DestPath: r.DestPath,
			NotBefore: r.NotBefore, NotAfter: r.NotAfter,
		}
	}
```

`Clone` needs no change — it already deep-copies `Rules` element-wise via `copy(rules, p.Rules)`; two more
plain `int64` fields need no additional handling, exactly like `dest_path`'s note there already says.

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd src && go test ./cmd/policy-server/... -run 'TestRestorePolicy_' -v && cd ..
```

Expected: PASS.

- [ ] **Step 5: Run the full `policy-server` package test suite**

```bash
cd src && go test ./cmd/policy-server/... && cd ..
```

Expected: PASS.

- [ ] **Step 6: Update `docs/components/policy-server.md`**

Find the existing restore-rule prose (`grep -n "dest_path" docs/components/policy-server.md`) and add one
sentence noting a rule may also carry `not_before`/`not_after`, bounding which backed-up version of its
selection is used — same placement/tone as the existing `dest_path` sentence.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/policy-server/restore_policy.go src/cmd/policy-server/restore_policy_test.go docs/components/policy-server.md
git commit -m "feat(policy-server): thread not_before/not_after through RestoreRule"
```

---

### Task 6: `api-server` — DTO and handler

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Test: `src/cmd/api-server/policies_test.go`
- Modify: `docs/components/api-server.md`

**Interfaces:**
- Consumes: `pb.RestoreRule.NotBefore`/`.NotAfter` (Task 1).
- Produces: `ruleDTO{Host, Path, Include, DestPath, NotBefore, NotAfter}`; `handleCreateRestore` passes
  both through to `pb.RestoreRule`; `toPolicyDTO` round-trips both.

- [ ] **Step 1: Write the failing tests**

Find the existing `dest_path` round-trip tests in `src/cmd/api-server/policies_test.go`
(`grep -n "DestPath\|dest_path" src/cmd/api-server/policies_test.go`) and add alongside them, matching
that file's existing test style (check whether it uses a fake policy-client mock capturing the sent
`CreatePolicyRequest`, and mirror that exact pattern):

```go
func TestHandleCreateRestore_PassesThroughTimeframe(t *testing.T) {
	// Mirror the existing dest_path pass-through test's setup exactly --
	// same fake policy client, same request-capturing mechanism.
	body := `{"name":"r1","storage_policy_id":"sp-1","rules":[{"host":"h","path":"/etc","include":true,"not_before":100,"not_after":200}]}`
	// ... construct the request the same way the existing dest_path test does,
	// call handleCreateRestore, then assert on the captured CreatePolicyRequest:
	// assert.Equal(t, int64(100), captured.Rules[0].GetNotBefore())
	// assert.Equal(t, int64(200), captured.Rules[0].GetNotAfter())
}

func TestToPolicyDTO_RoundTripsTimeframe(t *testing.T) {
	p := &pb.Policy{
		Rules: []*pb.RestoreRule{{Host: "h", Path: "/etc", Include: true, NotBefore: 100, NotAfter: 200}},
	}
	dto := toPolicyDTO(p)
	require.Len(t, dto.Rules, 1)
	assert.Equal(t, int64(100), dto.Rules[0].NotBefore)
	assert.Equal(t, int64(200), dto.Rules[0].NotAfter)
}
```

Read the existing `dest_path`-pass-through test in full before writing
`TestHandleCreateRestore_PassesThroughTimeframe` for real (not the sketch above) — copy its exact
request-construction and assertion mechanism so the new test follows the same pattern the file already
established, rather than inventing a new one.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./cmd/api-server/... -run 'TestHandleCreateRestore_PassesThroughTimeframe|TestToPolicyDTO_RoundTripsTimeframe' -v && cd ..
```

Expected: FAIL — `ruleDTO` has no `NotBefore`/`NotAfter` fields yet.

- [ ] **Step 3: Add the fields and pass-through**

Edit `src/cmd/api-server/policies.go`:

```go
type ruleDTO struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	DestPath  string `json:"dest_path,omitempty"`
	NotBefore int64  `json:"not_before,omitempty"`
	NotAfter  int64  `json:"not_after,omitempty"`
}
```

In `toPolicyDTO`:

```go
	rules[i] = ruleDTO{Host: r.GetHost(), Path: r.GetPath(), Include: r.GetInclude(), DestPath: r.GetDestPath(), NotBefore: r.GetNotBefore(), NotAfter: r.GetNotAfter()}
```

In `handleCreateRestore`'s rule construction:

```go
	rules[i] = &pb.RestoreRule{Host: ru.Host, Path: ru.Path, Include: ru.Include, DestPath: ru.DestPath, NotBefore: ru.NotBefore, NotAfter: ru.NotAfter}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd src && go test ./cmd/api-server/... -run 'TestHandleCreateRestore_PassesThroughTimeframe|TestToPolicyDTO_RoundTripsTimeframe' -v && cd ..
```

Expected: PASS.

- [ ] **Step 5: Run the full `api-server` package test suite**

```bash
cd src && go test ./cmd/api-server/... && cd ..
```

Expected: PASS.

- [ ] **Step 6: Update `docs/components/api-server.md`**

Find the existing `dest_path` mention for `POST /api/v1/restore` (`grep -n "dest_path" docs/components/api-server.md`) and add `not_before`/`not_after` alongside it as optional per-rule fields.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go docs/components/api-server.md
git commit -m "feat(api-server): thread not_before/not_after through restore rule DTOs"
```

---

### Task 7: `agent` — thread the timeframe through to `rwfs`

**Files:**
- Modify: `src/cmd/agent/restore.go`
- Test: `src/cmd/agent/restore_test.go` (check it exists: `ls src/cmd/agent/restore_test.go`; if it
  doesn't, create it)

**Interfaces:**
- Consumes: nothing new from other tasks (this struct is JSON-decoded straight from `policies-cache.json`,
  independent of the proto layer).
- Produces: `RestoreRule{Host, Path, Include, NotBefore, NotAfter}` (agent's copy never needed `DestPath`
  — it only marshals rules back out as `rulesStdinPayload` for `rwfs` to interpret, so it only carries
  fields `rwfs` will read; check whether `DestPath` was added to this struct when the destination-rename
  design landed — `grep -n "DestPath" src/cmd/agent/restore.go` — and follow whatever precedent that
  established for whether unused-by-agent fields get added here too).

- [ ] **Step 1: Check the current struct and existing test coverage**

```bash
grep -n "DestPath" src/cmd/agent/restore.go
cat src/cmd/agent/restore_test.go 2>/dev/null || echo "NO EXISTING TEST FILE"
```

- [ ] **Step 2: Write the failing test**

If `src/cmd/agent/restore_test.go` exists, add to it; otherwise create it:

```go
package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestoreRule_TimeframeRoundTripsThroughJSON(t *testing.T) {
	rule := RestoreRule{Host: "h", Path: "/etc", Include: true, NotBefore: 100, NotAfter: 200}
	data, err := json.Marshal(rule)
	require.NoError(t, err)

	var decoded RestoreRule
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, int64(100), decoded.NotBefore)
	assert.Equal(t, int64(200), decoded.NotAfter)
}
```

- [ ] **Step 3: Run the test to verify it fails**

```bash
cd src && go test ./cmd/agent/... -run 'TestRestoreRule_TimeframeRoundTripsThroughJSON' -v && cd ..
```

Expected: FAIL — `RestoreRule` has no `NotBefore`/`NotAfter` fields yet.

- [ ] **Step 4: Add the fields**

Edit `src/cmd/agent/restore.go`'s `RestoreRule` struct:

```go
type RestoreRule struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	NotBefore int64  `json:"not_before,omitempty"`
	NotAfter  int64  `json:"not_after,omitempty"`
}
```

(If Step 1 found `DestPath` already present on this struct, keep it and add the two new fields alongside
it, matching that precedent. If `DestPath` is absent, follow the same reasoning it presumably established
— agent's copy only needs fields it must forward verbatim to `rwfs`, which is exactly what
`not_before`/`not_after` are.)

- [ ] **Step 5: Run the test to verify it passes**

```bash
cd src && go test ./cmd/agent/... -run 'TestRestoreRule_TimeframeRoundTripsThroughJSON' -v && cd ..
```

Expected: PASS.

- [ ] **Step 6: Run the full `agent` package test suite**

```bash
cd src && go test ./cmd/agent/... && cd ..
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/agent/restore.go src/cmd/agent/restore_test.go
git commit -m "feat(agent): thread not_before/not_after through restore rule payloads"
```

---

### Task 8: `rwfs` — `RestoreRule` timeframe fields + winning-rule-index resolution

**Files:**
- Modify: `src/cmd/rwfs/rules.go`
- Test: `src/cmd/rwfs/rules_test.go`

**Interfaces:**
- Consumes: none from other tasks (pure logic, only depends on the rule shape it already defines).
- Produces: `RestoreRule{Host, Path, Include, NotBefore, NotAfter}`; new
  `resolveRestoreFileRule(rules []RestoreRule, host, path string) (ruleIndex int, include bool, found
  bool)` — same longest-ancestor-wins precedence as the existing `resolveRestoreFile`, additionally
  exposing which rule (by index into `rules`) won, or `found=false` if none matched. `resolveRestoreFile`
  becomes a thin wrapper over it, so its existing behavior and existing tests are unchanged. Consumed by
  Task 9.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/rwfs/rules_test.go`:

```go
func TestResolveRestoreFileRule_ReturnsWinningRuleIndex(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/var", Include: true},
		{Host: "", Path: "/var/log", Include: false},
	}
	idx, include, found := resolveRestoreFileRule(rules, "any-host", "/var/log/x")
	assert.True(t, found)
	assert.False(t, include)
	assert.Equal(t, 1, idx, "the more specific /var/log exclude (index 1) must be reported as the winner")

	idx, include, found = resolveRestoreFileRule(rules, "any-host", "/var/lib/x")
	assert.True(t, found)
	assert.True(t, include)
	assert.Equal(t, 0, idx, "only the /var include (index 0) covers /var/lib")
}

func TestResolveRestoreFileRule_NoMatchReportsNotFound(t *testing.T) {
	_, _, found := resolveRestoreFileRule(nil, "host", "/x")
	assert.False(t, found)
}

func TestResolveRestoreFileRule_ExactRuleWinsOverFolderRuleRegardlessOfIndex(t *testing.T) {
	rules := []RestoreRule{
		{Host: "web-01", Path: "/var/log/app.log", Include: false}, // index 0
		{Host: "", Path: "/var/log", Include: true},                // index 1
	}
	idx, include, found := resolveRestoreFileRule(rules, "web-01", "/var/log/app.log")
	assert.True(t, found)
	assert.False(t, include)
	assert.Equal(t, 0, idx)
}
```

Add `"github.com/stretchr/testify/assert"` to the test file's imports if not already present (the
existing `rules_test.go` uses plain `t.Fatal` — check whether adding `testify` here is consistent with
the rest of the `rwfs` package's tests, e.g. `verify_test.go`; if `rwfs` tests never use `testify`, write
these three tests with `t.Fatal`/`if` checks instead, matching the file's existing style exactly).

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./cmd/rwfs/... -run 'TestResolveRestoreFileRule_' -v && cd ..
```

Expected: FAIL — `resolveRestoreFileRule` doesn't exist yet.

- [ ] **Step 3: Add the timeframe fields and the new resolution function**

Edit `src/cmd/rwfs/rules.go`:

```go
type RestoreRule struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	NotBefore int64  `json:"not_before"`
	NotAfter  int64  `json:"not_after"`
}
```

Replace `longestMatchingFolderRule` and `resolveRestoreFile` with:

```go
// longestMatchingFolderRuleIndex finds the most specific host-agnostic
// folder rule covering path (checking path itself before its ancestors),
// returning its index into rules.
func longestMatchingFolderRuleIndex(rules []RestoreRule, path string) (ruleIndex int, include bool, found bool) {
	chain := ancestorsOrSelfRestorePath(path)
	for i := len(chain) - 1; i >= 0; i-- {
		for ri, r := range rules {
			if r.Host == "" && r.Path == chain[i] {
				return ri, r.Include, true
			}
		}
	}
	return -1, false, false
}

// resolveRestoreFileRule reports which rule governs (host, path): an exact
// host-specific rule wins outright (first such rule in rules, by index);
// otherwise the longest matching host-agnostic ancestor folder rule
// applies; found=false means no rule matched at all. Exposes the winning
// rule's index (unlike resolveRestoreFile's plain bool) so a caller can
// attribute a resolved file back to the specific rule -- and therefore
// timeframe -- that should govern it. See
// docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md.
func resolveRestoreFileRule(rules []RestoreRule, host, path string) (ruleIndex int, include bool, found bool) {
	for i, r := range rules {
		if r.Host == host && r.Path == path {
			return i, r.Include, true
		}
	}
	return longestMatchingFolderRuleIndex(rules, path)
}

// resolveRestoreFile reports whether (host, path) is selected. Thin
// wrapper over resolveRestoreFileRule, kept for the pieces of this package
// that only need the bool -- see resolveRestoreFileRule for the precedence
// rules themselves.
func resolveRestoreFile(rules []RestoreRule, host, path string) bool {
	_, include, found := resolveRestoreFileRule(rules, host, path)
	return found && include
}
```

- [ ] **Step 4: Run all `rwfs` rule tests to verify they pass, including the pre-existing ones**

```bash
cd src && go test ./cmd/rwfs/... -run 'TestResolveRestoreFile' -v && cd ..
```

Expected: PASS — both the new `TestResolveRestoreFileRule_*` tests and the pre-existing
`TestResolveRestoreFile_*` tests (unchanged, now exercising `resolveRestoreFile` as a wrapper) pass.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/rwfs/rules.go src/cmd/rwfs/rules_test.go
git commit -m "feat(rwfs): add not_before/not_after and expose the winning rule's index"
```

---

### Task 9: `rwfs` — streaming resolution pipeline (`restoreResolver`)

**Files:**
- Create: `src/cmd/rwfs/resolve.go`
- Test: `src/cmd/rwfs/resolve_test.go`

**Interfaces:**
- Consumes: `resolveRestoreFileRule` (Task 8); `pb.FileRow`, `pb.RestoreFileFilter` (Task 1).
- Produces:
  - `buildRestoreFilters(rules []RestoreRule) (filters []*pb.RestoreFileFilter, filterToRuleIndex []int)`
    — one filter per included rule, in rule order; `filterToRuleIndex[i]` maps a filter's position back to
    its index in the original `rules` slice.
  - `newRestoreResolver(rules []RestoreRule, filterToRuleIndex []int) *restoreResolver`
  - `(*restoreResolver).Feed(row *pb.FileRow, filterIndex int32) bool` — true means "dispatch this row for
    verification/restore," false means "drop it" (shadowed by a more specific rule, or the winning rule is
    excluded).
  - `(*restoreResolver).NotFound() []notFoundRule` — call once after the stream ends; reports each
    file-level filter that kept zero rows, with `reason` distinguishing "no version in timeframe" from the
    pre-existing "not found on this store".
  - `notFoundRule` gains a `Reason string` field (was previously implied fixed text at the call site in
    `verify.go`; Task 10 will use it).

This task adds the new pipeline alongside the still-live `applyRulesStdin` — deleting `applyRulesStdin`
happens in Task 10, together with rewiring its only caller (`runVerify`), so that neither task leaves the
package unable to build. To avoid a duplicate-type collision in the meantime, this task extends
`verify.go`'s *existing* `notFoundRule` type (adding `Reason`) rather than declaring a new one in
`resolve.go`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/rwfs/resolve_test.go`:

```go
package main

import (
	pb "github.com/alex-sviridov/miniprotector/api"
	"testing"
)

func TestBuildRestoreFilters_OnlyIncludedRulesBecomeFilters(t *testing.T) {
	rules := []RestoreRule{
		{Host: "h", Path: "/etc/a", Include: true, NotBefore: 10, NotAfter: 20},
		{Host: "h", Path: "/etc/b", Include: false},
		{Host: "", Path: "/var", Include: true},
	}
	filters, filterToRuleIndex := buildRestoreFilters(rules)
	if len(filters) != 2 {
		t.Fatalf("expected 2 filters (excluded rule skipped), got %d", len(filters))
	}
	if filters[0].GetHost() != "h" || filters[0].GetPath() != "/etc/a" || filters[0].GetPathIsPrefix() {
		t.Fatalf("filter 0 mismatch: %+v", filters[0])
	}
	if filters[0].GetNotBefore() != 10 || filters[0].GetNotAfter() != 20 {
		t.Fatalf("filter 0 timeframe mismatch: %+v", filters[0])
	}
	if !filters[1].GetPathIsPrefix() {
		t.Fatal("host-agnostic rule must become a prefix filter")
	}
	if filterToRuleIndex[0] != 0 || filterToRuleIndex[1] != 2 {
		t.Fatalf("filterToRuleIndex mismatch: %v", filterToRuleIndex)
	}
}

func TestRestoreResolver_KeepsRowMatchingItsOwnRule(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	row := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 10}
	if !resolver.Feed(row, 0) {
		t.Fatal("expected the row to be kept")
	}
}

func TestRestoreResolver_DropsRowShadowedByMoreSpecificRule(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/etc", Include: true, NotBefore: 1, NotAfter: 100},     // filter 0 -- broad
		{Host: "h", Path: "/etc/a", Include: true, NotBefore: 200, NotAfter: 300}, // filter 1 -- specific
	}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	// bwfs resolved /etc/a under BOTH filters (it's under /etc, and it IS
	// /etc/a) -- each with a different version, since their windows differ.
	broadVersionRow := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 10}
	specificVersionRow := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 20}

	if resolver.Feed(broadVersionRow, 0) {
		t.Fatal("the broad rule's row for /etc/a must be dropped: the specific rule (index 1) governs this path")
	}
	if !resolver.Feed(specificVersionRow, 1) {
		t.Fatal("the specific rule's own row for its own path must be kept")
	}
}

func TestRestoreResolver_DropsRowWhoseWinningRuleIsExcluded(t *testing.T) {
	rules := []RestoreRule{
		{Host: "", Path: "/etc", Include: true},
		{Host: "h", Path: "/etc/secret", Include: false},
	}
	_, filterToRuleIndex := buildRestoreFilters(rules) // only the include rule (index 0) becomes a filter
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	// bwfs resolved /etc/secret under the broad folder filter (filter 0),
	// since the exclude rule never becomes a filter at all.
	row := &pb.FileRow{Source: "h", Path: "/etc/secret", Type: "f", Size: 10}
	if resolver.Feed(row, 0) {
		t.Fatal("the exclude rule governs /etc/secret, so this row must be dropped")
	}
}

func TestRestoreResolver_ZeroByteOrNonFileRowIsFoundButNotKept(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/a", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)

	resolver := newRestoreResolver(rules, filterToRuleIndex)
	zeroByte := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "f", Size: 0}
	if resolver.Feed(zeroByte, 0) {
		t.Fatal("a zero-byte row must be found but not selected")
	}
	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a found-but-unselected row must not be reported as not-found, got %v", notFound)
	}

	resolver = newRestoreResolver(rules, filterToRuleIndex)
	dir := &pb.FileRow{Source: "h", Path: "/etc/a", Type: "d", Size: 10}
	if resolver.Feed(dir, 0) {
		t.Fatal("a directory row must be found but not selected")
	}
}

func TestRestoreResolver_NotFound_FileLevelFilterWithNoKeptRowIsAFailure(t *testing.T) {
	rules := []RestoreRule{{Host: "h", Path: "/etc/missing", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)
	// No Feed calls at all -- bwfs never resolved anything for filter 0.

	notFound := resolver.NotFound()
	if len(notFound) != 1 {
		t.Fatalf("expected exactly one not-found entry, got %v", notFound)
	}
	if notFound[0].Host != "h" || notFound[0].Path != "/etc/missing" {
		t.Fatalf("not-found entry mismatch: %+v", notFound[0])
	}
	if notFound[0].Reason != "no version in timeframe" {
		t.Fatalf("expected the distinguished reason, got %q", notFound[0].Reason)
	}
}

func TestRestoreResolver_NotFound_FolderLevelFilterWithNoKeptRowIsNotAFailure(t *testing.T) {
	rules := []RestoreRule{{Host: "", Path: "/empty", Include: true}}
	_, filterToRuleIndex := buildRestoreFilters(rules)
	resolver := newRestoreResolver(rules, filterToRuleIndex)

	notFound := resolver.NotFound()
	if len(notFound) != 0 {
		t.Fatalf("a folder rule matching nothing is a legitimate empty result, got %v", notFound)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd src && go test ./cmd/rwfs/... -run 'TestBuildRestoreFilters_|TestRestoreResolver_' -v && cd ..
```

Expected: FAIL — none of `buildRestoreFilters`/`newRestoreResolver`/`restoreResolver` exist yet.

- [ ] **Step 3: Add `Reason` to `verify.go`'s existing `notFoundRule`, and implement `resolve.go`**

Edit `src/cmd/rwfs/verify.go`'s existing `notFoundRule` type (do not delete or replace it in this task —
`applyRulesStdin` still constructs it and remains live until Task 10):

```go
// notFoundRule records a file-level rule (non-empty Host) that matched no
// row in the ListFiles result -- reported as a verification failure,
// unlike a folder-level rule (empty Host) matching nothing, which is a
// legitimate outcome (an empty or already-fully-excluded folder), not an
// error. Reason distinguishes a version outside a requested timeframe
// from a path that plain doesn't exist on this store at all (populated by
// resolve.go's restoreResolver; applyRulesStdin's own construction sites
// keep using the pre-existing "not found on this store" text literally,
// unchanged, until Task 10 removes them).
type notFoundRule struct {
	Host   string
	Path   string
	Reason string
}
```

This is purely additive to the struct — `applyRulesStdin`'s two existing `notFoundRule{Host: ..., Path:
...}` construction sites in `verify.go` are unaffected (a missing `Reason` is just its zero value, `""`,
and nothing reads it yet).

Create `src/cmd/rwfs/resolve.go`:

```go
// resolve.go is the streaming counterpart to applyRulesStdin (verify.go):
// instead of resolving rules against one big, already-fetched slice of
// rows, restoreResolver.Feed is called once per row as bwfs's
// ResolveRestoreFiles response streams in, so memory stays bounded by rule
// count, never by store size. Task 10 rewires runVerify to use this
// instead of applyRulesStdin, and removes applyRulesStdin. See
// docs/superpowers/specs/2026-08-15-restore-file-version-resolution-design.md.
package main

import (
	pb "github.com/alex-sviridov/miniprotector/api"
)

// buildRestoreFilters derives one pb.RestoreFileFilter per included rule,
// in rule order -- excluded rules never need file data, they only carve
// exceptions out of a broader include for restoreResolver's precedence
// check, which needs no store data at all. filterToRuleIndex[i] is the
// index into rules that filters[i] came from, letting a caller translate a
// ResolveRestoreFilesResponse's FilterIndex back into a rule identity.
func buildRestoreFilters(rules []RestoreRule) (filters []*pb.RestoreFileFilter, filterToRuleIndex []int) {
	for i, r := range rules {
		if !r.Include {
			continue
		}
		filters = append(filters, &pb.RestoreFileFilter{
			Host:         r.Host,
			Path:         r.Path,
			PathIsPrefix: r.Host == "",
			NotBefore:    r.NotBefore,
			NotAfter:     r.NotAfter,
		})
		filterToRuleIndex = append(filterToRuleIndex, i)
	}
	return filters, filterToRuleIndex
}

// restoreResolver consumes a ResolveRestoreFiles response stream one row
// at a time and decides, per row, whether it should be dispatched for
// verification/restore -- see Feed. It also tracks, per file-level filter,
// whether any row was ever kept, to report NotFound once the stream ends.
type restoreResolver struct {
	rules             []RestoreRule
	filterToRuleIndex []int
	filterKeptAny     []bool
}

func newRestoreResolver(rules []RestoreRule, filterToRuleIndex []int) *restoreResolver {
	return &restoreResolver{
		rules:             rules,
		filterToRuleIndex: filterToRuleIndex,
		filterKeptAny:     make([]bool, len(filterToRuleIndex)),
	}
}

// Feed decides whether row (resolved by filters[filterIndex]) should be
// dispatched. It is dropped when: the rule that actually governs row's
// path (per resolveRestoreFileRule's longest-ancestor-wins precedence,
// evaluated fresh per row, over the whole rule list including excludes) is
// excluded or doesn't match filterIndex's own rule -- meaning a more
// specific rule shadows this one for this exact path, and its
// window-resolved version isn't the one that should be used. A kept row
// must also be a real, non-empty file (type/size defense-in-depth,
// mirroring the pre-existing applyRulesStdin behavior -- bwfs's
// file_data_records only ever holds such rows today, but this guards
// against that invariant ever changing silently).
func (r *restoreResolver) Feed(row *pb.FileRow, filterIndex int32) bool {
	winningRuleIndex, include, found := resolveRestoreFileRule(r.rules, row.GetSource(), row.GetPath())
	if !found || !include {
		return false
	}
	if winningRuleIndex != r.filterToRuleIndex[filterIndex] {
		return false
	}
	if row.GetType() != "f" || row.GetSize() <= 0 {
		return false
	}
	r.filterKeptAny[filterIndex] = true
	return true
}

// NotFound reports, for each file-level (non-host-agnostic) filter that
// never had a row kept via Feed, a failure -- distinguishing "the rule has
// a timeframe and nothing was backed up inside it" from the generic
// unbounded case. Call once, after the response stream ends.
func (r *restoreResolver) NotFound() []notFoundRule {
	var out []notFoundRule
	for i, ruleIndex := range r.filterToRuleIndex {
		if r.filterKeptAny[i] {
			continue
		}
		rule := r.rules[ruleIndex]
		if rule.Host == "" {
			continue // folder-level rule matching nothing is legitimate
		}
		reason := "not found on this store"
		if rule.NotBefore != 0 || rule.NotAfter != 0 {
			reason = "no version in timeframe"
		}
		out = append(out, notFoundRule{Host: rule.Host, Path: rule.Path, Reason: reason})
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd src && go test ./cmd/rwfs/... -run 'TestBuildRestoreFilters_|TestRestoreResolver_' -v && cd ..
```

Expected: PASS.

- [ ] **Step 5: Run the full `rwfs` package test suite**

```bash
cd src && go test ./cmd/rwfs/... && cd ..
```

Expected: PASS. `resolve.go`'s new pipeline coexists with the still-live `applyRulesStdin` — nothing calls
the new pipeline from `runVerify` yet (that wiring, and `applyRulesStdin`'s removal, is Task 10), so this
task's change is purely additive and the package builds and tests cleanly on its own.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/rwfs/resolve.go src/cmd/rwfs/resolve_test.go
git commit -m "feat(rwfs): add the streaming restore-resolution pipeline (restoreResolver)"
```

---

### Task 10: `rwfs` — wire `runVerify` to the streaming pipeline

**Files:**
- Modify: `src/cmd/rwfs/verify.go`
- Test: `src/cmd/rwfs/verify_test.go`
- Modify: `docs/components/rwfs.md`
- Modify: `docs/protocols/restore.md`

**Interfaces:**
- Consumes: `buildRestoreFilters`, `newRestoreResolver`, `(*restoreResolver).Feed`/`.NotFound`,
  `notFoundRule{Host, Path, Reason}` (Task 9); `pb.ListServiceClient.ResolveRestoreFiles` (Task 1).
- Produces: `runVerify` (same exported-within-package signature as today), now calling
  `ResolveRestoreFiles` instead of unscoped `ListFiles` in `--rules-stdin` mode, dispatching to the worker
  pool as rows stream in rather than after collecting a full slice.

- [ ] **Step 1: Delete the superseded `applyRulesStdin`**

Remove `applyRulesStdin` (lines 82-106 as read during design) from `src/cmd/rwfs/verify.go` — Task 9's
`restoreResolver.Feed`/`.NotFound` replace it. Everything else stays: `notFoundRule` (now carrying
`Reason`, since Task 9), `rulesStdinPayload`/`parseRulesStdin` (unchanged — still how `--rules-stdin`
reads its input), and `verifyFileWithRetry`/`verifyFile`.

Also remove the now-defunct `TestApplyRulesStdin_*` tests from `src/cmd/rwfs/verify_test.go` (`grep -n
"TestApplyRulesStdin_" src/cmd/rwfs/verify_test.go` to find them) — their coverage (file-level rule with
no match fails, folder-level rule with no match doesn't, zero-byte/directory rows found but not selected,
excluded rows not selected) is already carried forward, adapted to the new API, by Task 9's
`resolve_test.go`. Leave `TestParseRulesStdin_*` in place — `parseRulesStdin` itself is untouched.

```bash
cd src && go build ./cmd/rwfs/... 2>&1 | head -20 && cd ..
```

Expected: build errors in `verify.go` where `runVerify` still calls the now-deleted `applyRulesStdin` —
expected at this point, fixed by Step 3 below.

- [ ] **Step 2: Write the failing test for the new `runVerify` wiring**

This is the one integration-style test in the plan for `runVerify` itself, since (per the existing
codebase) `runVerify` has no prior unit-test coverage — only its pure sub-pieces
(`parseRulesStdin`/`applyRulesStdin`) were tested directly, and those pure pieces are already covered by
Task 9's `resolve_test.go`. Add to `src/cmd/rwfs/verify_test.go`:

```go
func TestRunVerify_RulesStdin_UsesResolveRestoreFilesAndReportsTimeframeNotFound(t *testing.T) {
	store, err := wfs.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	require.NoError(t, store.CreateFileData("fs://hosta:f:/etc/a.conf:1000", 4))
	require.NoError(t, store.FinalizeFileData("fs://hosta:f:/etc/a.conf:1000", expectedCRC32(t, [][]byte{{1, 2, 3, 4}})))
	require.NoError(t, store.RawDB().Create(&wfs.FileVersionRecord{ObjectID: "fs://hosta:f:/etc/a.conf:1000", JobID: "job1", CreatedAt: time.Unix(5000, 0)}).Error)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	listSrv := NewListServer(store, logger)
	restoreSrv := NewRestoreServer(store, logger)

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterListServiceServer(grpcSrv, listSrv)
	pb.RegisterRestoreServiceServer(grpcSrv, restoreSrv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	// A rule whose timeframe excludes the only version this file has --
	// exercises the "no version in timeframe" not-found path end to end.
	rulesJSON := `{"rules":[{"host":"hosta","path":"/etc/a.conf","include":true,"not_before":9000,"not_after":9999}]}`

	err = runVerifyWithDialer(t, logger, lis, rulesJSON)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "1 file(s) failed verification")
}
```

Check `verify_test.go`'s existing imports and any existing bufconn-dialer test helper in the `rwfs`
package before writing `runVerifyWithDialer` — if `runVerify` currently takes a `host string, port int`
and dials directly (per its signature read during design:
`runVerify(logger, host, port, serverName, pathFilter, filter string, rulesStdin bool, stdin io.Reader,
streams, retries int, quiet bool, certsDir, jobID string) error`), it has no dependency-injection seam for
a bufconn dialer today. Add one **only for this test's sake**, following whatever pattern
`connection.Connect` (`src/common/connection`, used by `runVerify` today) already supports for tests
elsewhere in the codebase (`grep -rn "bufconn" src/cmd/rwfs/ src/common/connection/` first) — if no such
seam exists anywhere yet, write `runVerifyWithDialer` as a small test-only wrapper that duplicates just
enough of `runVerify`'s dial step to use `grpc.WithContextDialer` against `lis`, calling the same
package-level resolution/dispatch logic `runVerify` itself will call after Step 3, so the test still
exercises real production code for everything except the transport-level dial.

- [ ] **Step 3: Run the test to verify it fails**

```bash
cd src && go test ./cmd/rwfs/... -run 'TestRunVerify_RulesStdin_' -v && cd ..
```

Expected: FAIL (build error from Step 1, or a logic failure once it builds).

- [ ] **Step 4: Rewrite `runVerify`'s `--rules-stdin` path**

Edit `src/cmd/rwfs/verify.go`. Replace the block that currently does (per the version read during design):

```go
	var rows []*pb.FileRow
	var notFound []notFoundRule
	if rulesStdin {
		rows, notFound = applyRulesStdin(resp.Rows, rules)
	} else {
		for _, r := range resp.Rows {
			if r.Type == "f" && r.Size > 0 {
				rows = append(rows, r)
			}
		}
	}

	if len(rows) == 0 && len(notFound) == 0 {
		logger.Info("summary", "verified", 0, "warnings", 0)
		return nil
	}

	restoreClient := pb.NewRestoreServiceClient(conn)
	workCh := make(chan *pb.FileRow, len(rows))
	for _, r := range rows {
		workCh <- r
	}
	close(workCh)
```

with a branch that, in `--rules-stdin` mode, calls `ResolveRestoreFiles` and streams into `workCh` from a
producer goroutine, while the non-`--rules-stdin` path keeps calling `ListFiles` exactly as before:

```go
	restoreClient := pb.NewRestoreServiceClient(conn)
	workCh := make(chan *pb.FileRow, streams)
	var notFound []notFoundRule
	streamErrCh := make(chan error, 1)

	if rulesStdin {
		filters, filterToRuleIndex := buildRestoreFilters(rules)
		resolver := newRestoreResolver(rules, filterToRuleIndex)

		listClient := pb.NewListServiceClient(conn)
		resolveCtx, resolveCancel := context.WithCancel(callCtx)
		defer resolveCancel()
		stream, err := listClient.ResolveRestoreFiles(resolveCtx, &pb.ResolveRestoreFilesRequest{Filters: filters})
		if err != nil {
			return fmt.Errorf("resolve restore files: %w", err)
		}

		go func() {
			defer close(workCh)
			for {
				resp, err := stream.Recv()
				if err == io.EOF {
					streamErrCh <- nil
					return
				}
				if err != nil {
					streamErrCh <- fmt.Errorf("resolve restore files: %w", err)
					return
				}
				if resolver.Feed(resp.GetRow(), resp.GetFilterIndex()) {
					workCh <- resp.GetRow()
				}
			}
		}()
	} else {
		listClient := pb.NewListServiceClient(conn)
		ctx, cancel := context.WithTimeout(callCtx, 30*time.Second)
		resp, err := listClient.ListFiles(ctx, &pb.ListRequest{
			ServerName: serverName,
			Path:       pathFilter,
			Filter:     filter,
		})
		cancel()
		if err != nil {
			return fmt.Errorf("list files: %w", err)
		}
		go func() {
			defer close(workCh)
			for _, r := range resp.Rows {
				if r.Type == "f" && r.Size > 0 {
					workCh <- r
				}
			}
		}()
		streamErrCh <- nil
	}
```

Note `resp`/the earlier unscoped `ListFiles` call and its 30s-timeout block, previously issued
unconditionally before branching on `rulesStdin`, is now only issued in the `else` branch — the
`--rules-stdin` branch never calls `ListFiles` at all anymore. Remove the old unconditional `ListFiles`
call block above this replaced section entirely (it becomes dead code once both branches issue their own
call).

Keep the existing worker-pool section (the `for i := 0; i < streams; i++ { go func() {...} }()` loop over
`workCh`, and the `resultCh` consumption loop) unchanged in shape — it already ranges over `workCh` until
it's closed, which now happens from the producer goroutine instead of immediately after a `for _, r :=
range rows` loop. After the worker `wg.Wait()`/result-draining loop, add:

```go
	if streamErr := <-streamErrCh; streamErr != nil {
		return streamErr
	}
	if rulesStdin {
		notFound = resolver.NotFound()
	}
```

(`resolver` needs to be declared outside the `if rulesStdin` block that creates it, e.g. via `var resolver
*restoreResolver` before the branch, assigned inside it, so it's still in scope here — adjust the Step 4
code above accordingly when implementing.)

Update the `notFound` reporting loop (further down in `runVerify`, currently `logger.Warn("verification
failed", "source", nf.Host, "path", nf.Path, "reason", "not found on this store")`) to use the new
`Reason` field instead of the hardcoded string:

```go
	for _, nf := range notFound {
		warnings++
		logger.Warn("verification failed", "source", nf.Host, "path", nf.Path, "reason", nf.Reason)
	}
```

- [ ] **Step 5: Run the test to verify it passes**

```bash
cd src && go test ./cmd/rwfs/... -run 'TestRunVerify_RulesStdin_' -v && cd ..
```

Expected: PASS.

- [ ] **Step 6: Run the full `rwfs` package test suite**

```bash
cd src && go test ./cmd/rwfs/... && cd ..
```

Expected: PASS — including every pre-existing `verify_test.go`/`rules_test.go` test not touched by this
plan.

- [ ] **Step 7: Update `docs/components/rwfs.md`**

Remove the "**Known limitation — the listing is unbounded.**" paragraph entirely (this design fixes
exactly that limitation). Replace the surrounding `--rules-stdin` section's description of the `ListFiles`
call with a description of `ResolveRestoreFiles`: one filter per included rule, scoped by host/path/
timeframe, streamed rather than fetched whole. Keep the existing prose about the empty-rule-set rejection
and the "never combined with `--filter`/positional filter" note — both still true (the positional
`[[server_name:]path]`/`--filter` arguments have no bearing on `ResolveRestoreFiles`, which is built
entirely from the piped rules).

- [ ] **Step 8: Update `docs/protocols/restore.md`**

In the "CLI → RPC Mapping" section's `--rules-stdin` paragraph (`grep -n "rules-stdin" docs/protocols/restore.md`), replace the description of `ListFiles` being called with `server_name`/`path` omitted, with a
description of the new `ResolveRestoreFiles` call — cross-reference `docs/protocols/list.md#resolverestorefiles` (added in Task 1) rather than duplicating the filter semantics here.

- [ ] **Step 9: Commit**

```bash
git add src/cmd/rwfs/verify.go src/cmd/rwfs/verify_test.go docs/components/rwfs.md docs/protocols/restore.md
git commit -m "feat(rwfs): stream restore-rule resolution through ResolveRestoreFiles"
```

---

### Task 11: Changelog

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add the entry**

Add a dated heading (today's date) at the top of `CHANGELOG.md`, above the previous most-recent entry,
following that file's existing entry format/tone:

```markdown
## 2026-08-15

Restore rules can now pin a per-rule timeframe (`not_before`/`not_after`): the latest backed-up version
inside that window is used, and anything outside it is ignored rather than falling back to whatever's
newest overall. Resolving a restore policy's rules into an actual file list no longer requires scanning a
destination store's entire catalog — `bwfs` gained a scoped, streaming `ResolveRestoreFiles` RPC backed by
decomposed, indexed storage columns, so a host-agnostic folder rule (previously an unbounded full-store
dump) now costs roughly what it matches, not what the store holds.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: changelog entry for restore file version resolution"
```

---

## Self-Review Notes

- **Spec coverage:** §1 (`RestoreRule` timeframe) → Tasks 1, 5, 6, 7, 8. §2 (decomposed columns) → Task 2.
  §3 (`ResolveRestoreFiles` RPC + per-filter resolution) → Tasks 1, 3, 4. §4 (streaming consumption +
  precedence tie-break + not-found reasons) → Tasks 9, 10. Documentation Impact section → the doc-update
  steps folded into Tasks 1, 5, 6, 10, 11 (every file the design doc names is covered; `docs/ARCHITECTURE.md`
  was explicitly "no change" in the design and is correctly not a task here).
- **Non-goals respected:** no `web`/`RestoreView.vue`/`restoreCart.js` task exists in this plan (UI is an
  explicit follow-on per the design doc); no `rwfs restore` (write-to-disk) task exists.
- **Placeholder scan:** every step has real, complete code — no "add validation"/"handle edge cases"
  placeholders. Two steps (Task 6 Step 1's `TestHandleCreateRestore_PassesThroughTimeframe`, Task 10 Step
  2's dial-seam question) explicitly instruct reading an existing test/helper first rather than guessing
  its shape blind, because `api-server`'s exact mock-capture pattern and `rwfs`'s exact dial mechanism
  weren't fully read during planning — flagged honestly rather than fabricated.
- **Type consistency:** `resolvedCandidate` (Task 3) → consumed only within `cmd/bwfs`, not exposed
  further. `notFoundRule{Host, Path, Reason}` (Task 9) is used consistently in Task 10 (both the
  `resolver.NotFound()` call and the `logger.Warn` loop use `.Reason`). `resolveRestoreFileRule`'s
  `(ruleIndex int, include bool, found bool)` signature (Task 8) matches exactly how Task 9's
  `restoreResolver.Feed` calls it. `buildRestoreFilters`'s `filterToRuleIndex []int` is threaded
  identically into `newRestoreResolver` in both Task 9's tests and Task 10's `runVerify` wiring.
