# api-server Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a unified, read-only REST API (`api-server`) in front of the control plane's client and catalog data, backed by a new `clientmanager-api` daemon and a new `catalog` query RPC.

**Architecture:** Three new/changed gRPC surfaces (`clientmanager-api`'s new `ClientManagerService`, `catalog`'s new `ListEntries` RPC) feed a new standalone `api-server` binary that translates them into REST/JSON, guarded by a single bearer token. Every new daemon enrolls into the mesh exactly like existing components (bootstrap → `certclient` → `issuer`, mTLS everywhere internally).

**Tech Stack:** Go 1.26, gRPC + protobuf (`google.golang.org/grpc`, existing `src/api/*.proto` pattern), GORM/SQLite (`storage/catalog`, `storage/clientmanager` — unchanged schemas), `net/http` (stdlib `ServeMux`, no framework), Cobra for CLI flags, `testify` for tests, Docker Compose for deployment.

## Global Constraints

- No RBAC, no per-user identity anywhere in this plan — a single shared bearer token guards `api-server`'s REST listener; internal gRPC calls keep today's convention of no per-caller RPC authorization.
- `client-manager` (`src/cmd/clientmanager`) is not modified at all — `clientmanager-api` is a wholly separate binary that opens the same `clientmanager.sqlite` file directly (mirrors `issuer`'s existing pattern), never a network client of `client-manager`.
- No catalog schema changes — `ListEntries`' `pattern` filter matches against the existing `object_id` column via SQL `LIKE`.
- New gRPC RPCs (`ClientManagerService.*`, `CatalogService.ListEntries`) return errors via `status.Errorf(codes.X, ...)` (the `google.golang.org/grpc/status`/`codes` packages, already used in `src/cmd/bwfs`), not bare `fmt.Errorf` — `api-server` depends on real gRPC status codes to translate to HTTP status codes.
- REST conventions: plain query params for filters, `limit` + `starting_after` cursor for pagination, `{"data": [...]}` envelope, `has_more` flag where paginated (Stripe/GitHub style).
- Every new `local.conf` key must be added to `common/config.Config`'s struct and `ParseConfig`'s switch — `ParseConfig` errors on any unrecognized key, so a deploy file referencing an unregistered key breaks that binary's startup.
- New binaries follow the existing `src/cmd/<name>/{main.go,arguments.go}` + Cobra + `common.ValidatePort` + `config.Resolve*` pattern used by every other control-plane component.

---

### Task 1: Export `Path`/`Owner`/`Group`/`Mode` getters on `workload/filesystem.FileInfo`

`FileInfo`'s fields are all unexported; `catalog`'s new `ListEntries` handler (Task 3) needs to read `path`, `owner`, `group`, and `mode` from a decoded `FileInfo` in a different package. `Size()`/`Source()`/`Mtime()`/`Ctime()`/`GetType()` already exist as the precedent to follow.

**Files:**
- Modify: `src/workload/filesystem/fileinfo.go`
- Test: `src/workload/filesystem/fileinfo_test.go` (new file)

**Interfaces:**
- Produces: `FileInfo.Path() string`, `FileInfo.Owner() uint32`, `FileInfo.Group() uint32`, `FileInfo.Mode() fs.FileMode` — used by Task 3's `toProtoEntry`.

- [ ] **Step 1: Write the failing test**

```go
// src/workload/filesystem/fileinfo_test.go
package filesystem

import (
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFileInfo_ExportedGetters(t *testing.T) {
	modTime := time.Now().Truncate(time.Second)
	fi := FileInfo{
		host:    "host-a",
		path:    "/var/log/syslog",
		name:    "syslog",
		size:    1234,
		mode:    fs.FileMode(0o644),
		owner:   1000,
		group:   1000,
		modTime: modTime,
	}

	assert.Equal(t, "/var/log/syslog", fi.Path())
	assert.Equal(t, uint32(1000), fi.Owner())
	assert.Equal(t, uint32(1000), fi.Group())
	assert.Equal(t, fs.FileMode(0o644), fi.Mode())
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./workload/filesystem/... -run TestFileInfo_ExportedGetters -v`
Expected: FAIL with `fi.Path undefined (type FileInfo has no field or method Path)`

- [ ] **Step 3: Add the getters**

In `src/workload/filesystem/fileinfo.go`, add after the existing `Ctime()` method (before `String()`):

```go
// Path returns the object's original filesystem path.
func (fi FileInfo) Path() string {
	return fi.path
}

// Owner returns the file's owning UID (Unix) or SID hash (Windows).
func (fi FileInfo) Owner() uint32 {
	return fi.owner
}

// Group returns the file's owning GID (Unix) or primary group SID hash
// (Windows).
func (fi FileInfo) Group() uint32 {
	return fi.group
}

// Mode returns the full file mode (type + permissions).
func (fi FileInfo) Mode() fs.FileMode {
	return fi.mode
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd src && go test ./workload/filesystem/... -run TestFileInfo_ExportedGetters -v`
Expected: PASS

- [ ] **Step 5: Run the full package test suite to check for regressions**

Run: `cd src && go test ./workload/filesystem/...`
Expected: PASS (all tests, including existing `encode_test.go` if present)

- [ ] **Step 6: Commit**

```bash
git add src/workload/filesystem/fileinfo.go src/workload/filesystem/fileinfo_test.go
git commit -m "feat(filesystem): export Path/Owner/Group/Mode getters on FileInfo"
```

---

### Task 2: `catalog` proto + store — `ListEntries` query support

Adds the wire contract and the storage-layer query (filter by source host, substring pattern match on `object_id`, keyset pagination) that Task 3's RPC handler will call.

**Files:**
- Modify: `src/api/catalog.proto`
- Modify: `src/storage/catalog/store.go`
- Test: `src/storage/catalog/store_test.go`
- Generated (via `make proto`): `src/api/catalog.pb.go`, `src/api/catalog_grpc.pb.go`

**Interfaces:**
- Produces: `catalog.ListEntriesFilter{SourceNode, Pattern string; Limit int; StartingAfter int64}`, `(*Store) ListEntries(filter ListEntriesFilter) (entries []EntryRecord, hasMore bool, err error)` — consumed by Task 3.
- Produces (proto): `pb.CatalogServiceClient.ListEntries`, `pb.ListEntriesRequest{SourceHost, Pattern string; Limit int32; StartingAfter int64}`, `pb.ListEntriesResponse{Entries []*pb.Entry; HasMore bool}`, `pb.Entry{Id int64; SourceHost, JobId, ObjectId string; Ctime, SourceCreatedAt, ReceivedAt int64; Path string; Size int64; Mode string; Owner, Group uint32; ModTime int64}` — consumed by Task 3 and Task 10.

- [ ] **Step 1: Write the failing store test**

Append to `src/storage/catalog/store_test.go`:

```go
func TestListEntries_FiltersBySourceNode(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceCreatedAt: time.Now()},
		{SourceNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-2", SourceCreatedAt: time.Now()},
	}))

	entries, hasMore, err := store.ListEntries(ListEntriesFilter{SourceNode: "bwfs-a"})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, entries, 1)
	assert.Equal(t, "bwfs-a", entries[0].SourceNode)
}

func TestListEntries_FiltersByPatternSubstringOnObjectID(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "fs://bwfs-a:f:/var/log/syslog:100", SourceCreatedAt: time.Now()},
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "fs://bwfs-a:f:/etc/passwd:100", SourceCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{Pattern: "/var/log"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].ObjectID, "/var/log/syslog")
}

func TestListEntries_PaginationHasMoreAndStartingAfter(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	for i := 0; i < 5; i++ {
		require.NoError(t, store.EnsureEntries([]Entry{
			{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: fmt.Sprintf("obj-%d", i), SourceCreatedAt: time.Now()},
		}))
	}

	page1, hasMore, err := store.ListEntries(ListEntriesFilter{Limit: 2})
	require.NoError(t, err)
	require.Len(t, page1, 2)
	assert.True(t, hasMore)
	// Newest first (highest ID first).
	assert.Greater(t, page1[0].ID, page1[1].ID)

	page2, hasMore, err := store.ListEntries(ListEntriesFilter{Limit: 2, StartingAfter: page1[1].ID})
	require.NoError(t, err)
	require.Len(t, page2, 2)
	assert.True(t, hasMore)
	assert.Less(t, page2[0].ID, page1[1].ID)

	page3, hasMore, err := store.ListEntries(ListEntriesFilter{Limit: 2, StartingAfter: page2[1].ID})
	require.NoError(t, err)
	require.Len(t, page3, 1)
	assert.False(t, hasMore)
}

func TestListEntries_LimitDefaultsAndCaps(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{Limit: 0})
	require.NoError(t, err)
	assert.Len(t, entries, 1) // default 100, well above the 1 row present

	entries, _, err = store.ListEntries(ListEntriesFilter{Limit: 10000})
	require.NoError(t, err)
	assert.Len(t, entries, 1) // capped at 500, still well above the 1 row present
}
```

Add `"fmt"` to the existing `import` block in `src/storage/catalog/store_test.go`.

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./storage/catalog/... -run TestListEntries -v`
Expected: FAIL with `undefined: ListEntriesFilter` / `store.ListEntries undefined`

- [ ] **Step 3: Add `ListEntriesFilter` and `Store.ListEntries` to `src/storage/catalog/store.go`**

Add after the existing `Count` method:

```go
// ListEntriesFilter narrows and paginates a ListEntries query. A
// zero-valued filter matches every entry, newest first, first page.
type ListEntriesFilter struct {
	SourceNode    string // exact match; "" = all source nodes
	Pattern       string // substring match against object_id; "" = no filter
	Limit         int    // clamped to [1, 500]; 0 or negative defaults to 100
	StartingAfter int64  // last-seen entry ID from a previous page; 0 = first page
}

const (
	defaultListEntriesLimit = 100
	maxListEntriesLimit     = 500
)

// ListEntries returns entries newest-first (highest ID first), matching
// filter, plus whether more entries exist beyond the returned page.
// pattern is an unindexed SQL LIKE '%pattern%' scan against object_id
// (which already embeds the original path -- see
// workload/filesystem.FileInfo.ID) rather than decoding Metadata per row.
func (s *Store) ListEntries(filter ListEntriesFilter) ([]EntryRecord, bool, error) {
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultListEntriesLimit
	}
	if limit > maxListEntriesLimit {
		limit = maxListEntriesLimit
	}

	q := s.db.Model(&EntryRecord{}).Order("id DESC")
	if filter.SourceNode != "" {
		q = q.Where("source_node = ?", filter.SourceNode)
	}
	if filter.Pattern != "" {
		q = q.Where("object_id LIKE ?", "%"+filter.Pattern+"%")
	}
	if filter.StartingAfter > 0 {
		q = q.Where("id < ?", filter.StartingAfter)
	}

	var entries []EntryRecord
	// Fetch one extra row to detect hasMore without a separate COUNT query.
	if err := q.Limit(limit + 1).Find(&entries).Error; err != nil {
		return nil, false, err
	}

	hasMore := len(entries) > limit
	if hasMore {
		entries = entries[:limit]
	}
	return entries, hasMore, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS (all tests, including the four new ones and existing `TestEnsureEntries_*`/`TestNew_*`)

- [ ] **Step 5: Extend the proto with `ListEntries`**

Replace the full contents of `src/api/catalog.proto`:

```protobuf
syntax = "proto3";

package catalogservice;

option go_package = "./proto";

service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
}

message FileVersionEntry {
  string job_id     = 1;
  string object_id  = 2;
  bytes  metadata   = 3;
  int64  ctime      = 4;
  int64  source_seq = 5; // bwfs's local file_versions.seq — informational only
  int64  created_at = 6; // unix seconds; bwfs's original recording time
}

message SyncRequest {
  repeated FileVersionEntry entries = 1;
}

message SyncResponse {} // empty ack — GrpcSender only checks error/nil

message ListEntriesRequest {
  string source_host    = 1; // exact match; empty = all hosts
  string pattern        = 2; // substring match against object_id; empty = no filter
  int32  limit           = 3; // 1..500, default 100
  int64  starting_after  = 4; // last-seen entry ID from a previous page; 0 = first page
}

message ListEntriesResponse {
  repeated Entry entries = 1;
  bool has_more = 2;
}

message Entry {
  int64  id                = 1;
  string source_host       = 2;
  string job_id            = 3;
  string object_id         = 4;
  int64  ctime             = 5;
  int64  source_created_at = 6;
  int64  received_at       = 7;
  // decoded server-side from the stored Metadata blob:
  string path      = 8;
  int64  size       = 9;
  string mode      = 10; // e.g. "-rw-r--r--", from fs.FileMode.String()
  uint32 owner     = 11; // Unix UID (or Windows SID hash) — numeric, no name resolution
  uint32 group     = 12; // Unix GID (or Windows SID hash) — numeric, no name resolution
  int64  mod_time   = 13;
}
```

- [ ] **Step 6: Regenerate protobuf code**

Run: `make proto`
Expected: `src/api/catalog.pb.go` and `src/api/catalog_grpc.pb.go` are regenerated with no errors; `CatalogServiceClient` now has a `ListEntries` method and `ListEntriesRequest`/`ListEntriesResponse`/`Entry` types exist.

- [ ] **Step 7: Confirm the whole module still builds**

Run: `cd src && go build ./...`
Expected: succeeds (the RPC has no server implementation yet — `catalog`'s `catalogServer` doesn't need to implement `ListEntries` until Task 3, since `pb.UnimplementedCatalogServiceServer` satisfies the interface in the meantime)

- [ ] **Step 8: Commit**

```bash
git add src/api/catalog.proto src/api/catalog.pb.go src/api/catalog_grpc.pb.go src/storage/catalog/store.go src/storage/catalog/store_test.go
git commit -m "feat(catalog): add ListEntries query support to the proto and store layer"
```

---

### Task 3: `catalog` server — implement the `ListEntries` RPC handler

**Files:**
- Modify: `src/cmd/catalog/server.go`
- Test: `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: Task 1's `FileInfo.Path()/Owner()/Group()/Mode()`, Task 2's `catalogstore.ListEntriesFilter`/`(*Store) ListEntries`/`pb.ListEntriesRequest`/`pb.ListEntriesResponse`/`pb.Entry`.
- Produces: `(*catalogServer) ListEntries(ctx, *pb.ListEntriesRequest) (*pb.ListEntriesResponse, error)` — the RPC Task 10's `api-server` dials.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/catalog/server_test.go` (reuses `newTestCatalogServer` already defined in that file):

```go
func TestListEntries_ReturnsPersistedEntriesNewestFirst(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceCreatedAt: time.Now()},
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 2)
	assert.Equal(t, "obj-2", resp.GetEntries()[0].GetObjectId())
	assert.False(t, resp.GetHasMore())
}

func TestListEntries_FiltersBySourceHost(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceCreatedAt: time.Now()},
		{SourceNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-2", SourceCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{SourceHost: "bwfs-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "bwfs-a", resp.GetEntries()[0].GetSourceHost())
}

func TestListEntries_DecodesMetadataIntoEntryFields(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	fi := filesystem.NewFileInfoForTest("bwfs-a", "/var/log/syslog", 4096, 0o644, 1000, 1000, time.Now())
	metadata, err := fi.Encode()
	require.NoError(t, err)

	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: fi.ID(), Metadata: metadata, SourceCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	entry := resp.GetEntries()[0]
	assert.Equal(t, "/var/log/syslog", entry.GetPath())
	assert.Equal(t, int64(4096), entry.GetSize())
	assert.Equal(t, uint32(1000), entry.GetOwner())
	assert.Equal(t, uint32(1000), entry.GetGroup())
}

func TestListEntries_MalformedMetadataStillReturnsEntryWithEmptyDecodedFields(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{SourceNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", Metadata: []byte("not-gob-encoded"), SourceCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "", resp.GetEntries()[0].GetPath())
}
```

Add `"github.com/alex-sviridov/miniprotector/workload/filesystem"` to the existing `import` block in `src/cmd/catalog/server_test.go`.

This test also needs a small test-only constructor on `FileInfo`, since all its fields are unexported and today it's only ever built via the filesystem walker. Add to `src/workload/filesystem/fileinfo.go` (below the `Mode()` getter added in Task 1):

```go
// NewFileInfoForTest builds a FileInfo directly from field values, for
// tests outside this package that need a specific FileInfo without a real
// filesystem walk (e.g. catalog's ListEntries decode test).
func NewFileInfoForTest(host, path string, size int64, mode fs.FileMode, owner, group uint32, modTime time.Time) FileInfo {
	return FileInfo{
		host: host, path: path, name: path, size: size, mode: mode,
		owner: owner, group: group, modTime: modTime,
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/catalog/... -run TestListEntries -v`
Expected: FAIL with `srv.ListEntries undefined` (method not implemented yet)

- [ ] **Step 3: Implement `ListEntries` in `src/cmd/catalog/server.go`**

Add these imports to the existing import block: `"github.com/alex-sviridov/miniprotector/workload/filesystem"`, `"google.golang.org/grpc/codes"`, `"google.golang.org/grpc/status"`. Then append:

```go
func (s *catalogServer) ListEntries(ctx context.Context, req *pb.ListEntriesRequest) (*pb.ListEntriesResponse, error) {
	records, hasMore, err := s.store.ListEntries(catalogstore.ListEntriesFilter{
		SourceNode:    req.GetSourceHost(),
		Pattern:       req.GetPattern(),
		Limit:         int(req.GetLimit()),
		StartingAfter: req.GetStartingAfter(),
	})
	if err != nil {
		s.logger.Error("ListEntries: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list entries: %v", err)
	}

	entries := make([]*pb.Entry, len(records))
	for i, rec := range records {
		entries[i] = toProtoEntry(rec)
	}
	return &pb.ListEntriesResponse{Entries: entries, HasMore: hasMore}, nil
}

// toProtoEntry decodes rec.Metadata (a gob-encoded filesystem.FileInfo)
// into Entry's path/size/mode/owner/group/mod_time fields. A decode
// failure (malformed or non-filesystem metadata) leaves those fields at
// their zero values rather than failing the whole ListEntries call --
// one bad row shouldn't hide every other entry in the response.
func toProtoEntry(rec catalogstore.EntryRecord) *pb.Entry {
	entry := &pb.Entry{
		Id:              rec.ID,
		SourceHost:      rec.SourceNode,
		JobId:           rec.JobID,
		ObjectId:        rec.ObjectID,
		Ctime:           rec.Ctime,
		SourceCreatedAt: rec.SourceCreatedAt.Unix(),
		ReceivedAt:      rec.ReceivedAt.Unix(),
	}
	if fi, err := filesystem.DecodeFileInfo(rec.Metadata); err == nil {
		entry.Path = fi.Path()
		entry.Size = fi.Size()
		entry.Mode = fi.Mode().String()
		entry.Owner = fi.Owner()
		entry.Group = fi.Group()
		entry.ModTime = fi.Mtime()
	}
	return entry
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS (all tests, including existing `TestSyncFileVersions_*`)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalog/server.go src/cmd/catalog/server_test.go src/workload/filesystem/fileinfo.go
git commit -m "feat(catalog): implement ListEntries RPC, decoding metadata into Entry fields"
```

---

### Task 4: `clientmanager.proto` — new `ClientManagerService`

**Files:**
- Create: `src/api/clientmanager.proto`
- Generated (via `make proto`): `src/api/clientmanager.pb.go`, `src/api/clientmanager_grpc.pb.go`

**Interfaces:**
- Produces: `pb.ClientManagerServiceClient`, `pb.ListClientsRequest{}`, `pb.ListClientsResponse{Clients []*pb.Client}`, `pb.GetClientRequest{Hostname string}`, `pb.Client{Hostname string; Revoked bool; RevokedAt, LastSeenAt int64; Sans []string; Attributes, Descriptions map[string]string}` — consumed by Task 5 and Task 9.

- [ ] **Step 1: Write the proto file**

```protobuf
// src/api/clientmanager.proto
syntax = "proto3";

package clientmanagerapiservice;

option go_package = "./proto";

// ClientManagerService is clientmanager-api's sole RPC surface: read-only
// access to the same clientmanager.sqlite file client-manager's CLI and
// issuer already share (see docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md
// and docs/superpowers/specs/2026-07-14-api-server-design.md). clientmanager-api
// never writes -- client-manager (the CLI) and issuer remain the only writers.
service ClientManagerService {
  rpc ListClients(ListClientsRequest) returns (ListClientsResponse);
  rpc GetClient(GetClientRequest) returns (Client);
}

message Client {
  string hostname               = 1;
  bool   revoked                 = 2;
  int64  revoked_at              = 3; // unix seconds, 0 if never revoked
  int64  last_seen_at            = 4; // unix seconds, 0 if never seen
  repeated string sans           = 5;
  map<string, string> attributes    = 6;
  map<string, string> descriptions  = 7;
}

message ListClientsRequest {}

message ListClientsResponse {
  repeated Client clients = 1;
}

message GetClientRequest {
  string hostname = 1;
}
```

- [ ] **Step 2: Regenerate protobuf code**

Run: `make proto`
Expected: `src/api/clientmanager.pb.go` and `src/api/clientmanager_grpc.pb.go` are generated with no errors.

- [ ] **Step 3: Confirm the module builds**

Run: `cd src && go build ./...`
Expected: succeeds

- [ ] **Step 4: Commit**

```bash
git add src/api/clientmanager.proto src/api/clientmanager.pb.go src/api/clientmanager_grpc.pb.go
git commit -m "feat(api): add ClientManagerService proto for clientmanager-api"
```

---

### Task 5: `clientmanager-api` — new daemon reading `client-manager`'s store directly

Mirrors `issuer`'s existing pattern exactly: opens `storage/clientmanager`'s SQLite file directly (via `config.ResolveVarDir`), no changes to `client-manager` itself.

**Files:**
- Create: `src/cmd/clientmanager-api/main.go`
- Create: `src/cmd/clientmanager-api/arguments.go`
- Create: `src/cmd/clientmanager-api/server.go`
- Test: `src/cmd/clientmanager-api/server_test.go`

**Interfaces:**
- Consumes: Task 4's `pb.ClientManagerServiceClient`/messages; existing `storage/clientmanager.Store` (`ListClients`, `GetClient`, `KV`); existing `common/connection.StartServer`, `common/config.Resolve*`, `common/logging.NewLogger`.
- Produces: the running `clientmanager-api` binary, listening on `clientmanager_api_port`.

- [ ] **Step 1: Write the failing server tests**

```go
// src/cmd/clientmanager-api/server_test.go
package main

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/alex-sviridov/miniprotector/api"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func newTestServer(t *testing.T) (*clientManagerAPIServer, *clientmanagerstore.Store) {
	t.Helper()
	store, err := clientmanagerstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewClientManagerAPIServer(store, logger), store
}

func TestListClients_ReturnsAllClientsWithAttributesAndDescriptions(t *testing.T) {
	srv, store := newTestServer(t)
	require.NoError(t, store.AddClient("node-1", []string{"alias.internal"}, time.Now()))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindAttribute, "role", "db"))
	require.NoError(t, store.SetKV("node-1", clientmanagerstore.KindDescription, "owner", "alice"))

	resp, err := srv.ListClients(context.Background(), &pb.ListClientsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetClients(), 1)

	client := resp.GetClients()[0]
	assert.Equal(t, "node-1", client.GetHostname())
	assert.False(t, client.GetRevoked())
	assert.Equal(t, []string{"alias.internal"}, client.GetSans())
	assert.Equal(t, "db", client.GetAttributes()["role"])
	assert.Equal(t, "alice", client.GetDescriptions()["owner"])
}

func TestGetClient_UnknownHostnameReturnsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	_, err := srv.GetClient(context.Background(), &pb.GetClientRequest{Hostname: "ghost"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestGetClient_RevokedAndLastSeenTimestampsRoundTrip(t *testing.T) {
	srv, store := newTestServer(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))
	revokedAt := time.Now().Truncate(time.Second)
	require.NoError(t, store.SetRevoked("node-1", true, revokedAt))
	seenAt := time.Now().Truncate(time.Second)
	require.NoError(t, store.UpdateLastSeen("node-1", seenAt))

	client, err := srv.GetClient(context.Background(), &pb.GetClientRequest{Hostname: "node-1"})
	require.NoError(t, err)
	assert.True(t, client.GetRevoked())
	assert.Equal(t, revokedAt.Unix(), client.GetRevokedAt())
	assert.Equal(t, seenAt.Unix(), client.GetLastSeenAt())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/clientmanager-api/... -v`
Expected: FAIL (package doesn't exist yet / `NewClientManagerAPIServer` undefined)

- [ ] **Step 3: Implement `server.go`**

```go
// src/cmd/clientmanager-api/server.go
package main

import (
	"context"
	"errors"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// clientManagerAPIServer implements ClientManagerService: read-only
// access to the same clientmanager.sqlite file client-manager's CLI and
// issuer already share. Never writes.
type clientManagerAPIServer struct {
	pb.UnimplementedClientManagerServiceServer
	store  *clientmanagerstore.Store
	logger *slog.Logger
}

func NewClientManagerAPIServer(store *clientmanagerstore.Store, logger *slog.Logger) *clientManagerAPIServer {
	return &clientManagerAPIServer{store: store, logger: logger}
}

func (s *clientManagerAPIServer) ListClients(ctx context.Context, _ *pb.ListClientsRequest) (*pb.ListClientsResponse, error) {
	recs, err := s.store.ListClients()
	if err != nil {
		s.logger.Error("ListClients: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list clients: %v", err)
	}

	clients := make([]*pb.Client, len(recs))
	for i, rec := range recs {
		client, err := s.toProtoClient(rec)
		if err != nil {
			s.logger.Error("ListClients: load kv failed", "hostname", rec.Hostname, "error", err)
			return nil, status.Errorf(codes.Internal, "list clients: %v", err)
		}
		clients[i] = client
	}
	return &pb.ListClientsResponse{Clients: clients}, nil
}

func (s *clientManagerAPIServer) GetClient(ctx context.Context, req *pb.GetClientRequest) (*pb.Client, error) {
	rec, err := s.store.GetClient(req.GetHostname())
	if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return nil, status.Errorf(codes.NotFound, "client %s not found", req.GetHostname())
	}
	if err != nil {
		s.logger.Error("GetClient: query failed", "hostname", req.GetHostname(), "error", err)
		return nil, status.Errorf(codes.Internal, "get client: %v", err)
	}
	return s.toProtoClient(*rec)
}

func (s *clientManagerAPIServer) toProtoClient(rec clientmanagerstore.ClientRecord) (*pb.Client, error) {
	client := &pb.Client{
		Hostname: rec.Hostname,
		Revoked:  rec.Revoked,
		Sans:     rec.SANsList(),
	}
	if rec.RevokedAt != nil {
		client.RevokedAt = rec.RevokedAt.Unix()
	}
	if rec.LastSeenAt != nil {
		client.LastSeenAt = rec.LastSeenAt.Unix()
	}

	descs, err := s.store.KV(rec.Hostname, clientmanagerstore.KindDescription)
	if err != nil {
		return nil, err
	}
	client.Descriptions = make(map[string]string, len(descs))
	for _, d := range descs {
		client.Descriptions[d.Key] = d.Value
	}

	attrs, err := s.store.KV(rec.Hostname, clientmanagerstore.KindAttribute)
	if err != nil {
		return nil, err
	}
	client.Attributes = make(map[string]string, len(attrs))
	for _, a := range attrs {
		client.Attributes[a.Key] = a.Value
	}

	return client, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/clientmanager-api/... -v`
Expected: PASS

- [ ] **Step 5: Implement `arguments.go`**

```go
// src/cmd/clientmanager-api/arguments.go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Port  int
	Debug bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "clientmanager-api",
		Short: "Read-only gRPC access to client-manager's enrolled-client data",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().IntVar(&args.Port, "port", conf.ClientManagerAPIPort, "Port to listen on")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	if err := common.ValidatePort(args.Port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}

	return args, nil
}
```

- [ ] **Step 6: Implement `main.go`**

```go
// src/cmd/clientmanager-api/main.go
// clientmanager-api exposes read-only gRPC access to client-manager's
// enrolled-client data (clientmanager.sqlite), the same file issuer
// already shares -- client-manager itself stays a network-surface-free
// CLI tool. See docs/components/clientmanager-api.md and
// docs/superpowers/specs/2026-07-14-api-server-design.md.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
	"google.golang.org/grpc"
)

func main() {
	const appName = "clientmanager-api"

	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		logger.Error("var directory resolution failed", "error", err)
		os.Exit(1)
	}
	store, err := clientmanagerstore.New(varDir)
	if err != nil {
		logger.Error("failed to open client-manager store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	srv := NewClientManagerAPIServer(store, logger)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("clientmanager-api started", "port", arguments.Port)

	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
		pb.RegisterClientManagerServiceServer(s, srv)
	}); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 7: Confirm build**

Run: `cd src && go build ./...`
Expected: succeeds

- [ ] **Step 8: Commit**

```bash
git add src/cmd/clientmanager-api/
git commit -m "feat(clientmanager-api): new daemon exposing read-only client data over gRPC"
```

---

### Task 6: `common/config` — register new config keys

`ParseConfig` errors on any unrecognized `local.conf` key, so every key used by later tasks' deploy files must be registered here first.

**Files:**
- Modify: `src/common/config/config.go`
- Test: `src/common/config/config_test.go` (check if it exists first; create if not)

**Interfaces:**
- Produces: `Config.ClientManagerAPIHost string`, `Config.ClientManagerAPIPort int` (default `9500`), `Config.APIServerPort int` (default `8090`), `Config.APIServerToken string` — consumed by Task 8 (`api-server`) and Task 5/11 (`clientmanager-api`'s own port default already covered by `ClientManagerAPIPort`).

- [ ] **Step 1: Check for an existing config test file**

Run: `ls src/common/config/`
If `config_test.go` exists, read it first to match its existing test style before adding to it.

- [ ] **Step 2: Write the failing test**

Add to `src/common/config/config_test.go` (create the file with `package config` plus `"os"`, `"path/filepath"`, `"testing"`, `github.com/stretchr/testify/require` imports if it doesn't already exist):

```go
func TestParseConfig_ParsesAPIServerAndClientManagerAPIKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte(
		"default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n"+
			"clientmanager_api_host=clientmanager-api\nclientmanager_api_port=9501\n"+
			"api_server_port=8091\napi_server_token=test-token\n",
	), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	require.Equal(t, "clientmanager-api", conf.ClientManagerAPIHost)
	require.Equal(t, 9501, conf.ClientManagerAPIPort)
	require.Equal(t, 8091, conf.APIServerPort)
	require.Equal(t, "test-token", conf.APIServerToken)
}

func TestParseConfig_ClientManagerAPIPortAndAPIServerPortDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	require.Equal(t, 9500, conf.ClientManagerAPIPort)
	require.Equal(t, 8090, conf.APIServerPort)
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd src && go test ./common/config/... -run "TestParseConfig_ParsesAPIServerAndClientManagerAPIKeys|TestParseConfig_ClientManagerAPIPortAndAPIServerPortDefaults" -v`
Expected: FAIL — `unknown configuration key at line 4: clientmanager_api_host`

- [ ] **Step 4: Add the new fields and parsing cases**

In `src/common/config/config.go`, add to the `Config` struct (after `LogGatewayPort`):

```go
	ClientManagerAPIHost              string
	ClientManagerAPIPort              int
	APIServerPort                     int
	APIServerToken                    string
```

Add to `ParseConfig`'s default-value map literal (after `LogGatewayPort: 9400,`):

```go
		ClientManagerAPIPort:             9500,
		APIServerPort:                    8090,
```

Add new `case` branches to the `switch key` block (after the `log_gateway_port` case):

```go
		case "clientmanager_api_host":
			config.ClientManagerAPIHost = value
			foundFields["clientmanager_api_host"] = true
		case "clientmanager_api_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid clientmanager_api_port value at line %d: %s", lineNum, value)
			}
			config.ClientManagerAPIPort = port
			foundFields["clientmanager_api_port"] = true
		case "api_server_port":
			port, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid api_server_port value at line %d: %s", lineNum, value)
			}
			config.APIServerPort = port
			foundFields["api_server_port"] = true
		case "api_server_token":
			config.APIServerToken = value
			foundFields["api_server_token"] = true
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests, new and pre-existing)

- [ ] **Step 6: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add clientmanager-api and api-server config keys"
```

---

### Task 7: Makefile — build targets for `clientmanager-api` and `api-server`

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add command-dir variables**

After `LOG_GATEWAY_CMD := cmd/log-gateway` (line 29), add:

```makefile
CLIENTMANAGER_API_CMD := cmd/clientmanager-api
API_SERVER_CMD := cmd/api-server
```

- [ ] **Step 2: Register both in `.PHONY`**

Change line 41 from:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certclient catalogsync catalog agent clientmanager issuer policy-server policyclient log-gateway test test-e2e lint control-plane-up demo-up demo-down
```

to:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certclient catalogsync catalog agent clientmanager issuer policy-server policyclient log-gateway clientmanager-api api-server test test-e2e lint control-plane-up demo-up demo-down
```

- [ ] **Step 3: Add the two build targets**

After the `log-gateway` target (after line 145, before `test:`), add:

```makefile
clientmanager-api: $(BINARY_DIR) ## Build clientmanager-api binary
	@printf "$(BLUE)Building clientmanager-api...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/clientmanager-api ./$(CLIENTMANAGER_API_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/clientmanager-api"

api-server: $(BINARY_DIR) ## Build api-server binary
	@printf "$(BLUE)Building api-server...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/api-server ./$(API_SERVER_CMD)
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/api-server"
```

Note: `$(BINARIES)` (used by `build:`) is `$(notdir $(wildcard src/cmd/*))`, so once `src/cmd/clientmanager-api/` and `src/cmd/api-server/` exist (Task 5, Task 8), `make build` picks them up automatically — no change needed there.

- [ ] **Step 4: Verify the `clientmanager-api` target builds**

Run: `make clientmanager-api`
Expected: `Built successfully: bin/clientmanager-api`

Do not run `make api-server` yet — `src/cmd/api-server` doesn't exist until Task 8, so it would fail; that's expected, not a regression to fix here.

- [ ] **Step 5: Commit**

```bash
git add Makefile
git commit -m "build: add clientmanager-api and api-server build targets"
```

---

### Task 8: `api-server` — binary skeleton (routing, auth, error translation)

**Files:**
- Create: `src/cmd/api-server/main.go`
- Create: `src/cmd/api-server/arguments.go`
- Create: `src/cmd/api-server/server.go`
- Create: `src/cmd/api-server/auth.go`
- Create: `src/cmd/api-server/errors.go`
- Test: `src/cmd/api-server/auth_test.go`
- Test: `src/cmd/api-server/errors_test.go`

**Interfaces:**
- Consumes: Task 4's `pb.ClientManagerServiceClient`, Task 2's `pb.CatalogServiceClient`, `common/connection.Connect`, `common/config`, `common/logging`.
- Produces: `requireBearerToken(token string, next http.Handler) http.Handler`, `writeJSON(w, statusCode, body)`, `writeJSONError(w, statusCode, message)`, `writeGRPCError(w, err)`, `type server struct{...}`, `newServer(cm clientManagerClient, catalog catalogQueryClient, logger *slog.Logger) *server`, `(*server) registerRoutes(mux *http.ServeMux)` — consumed by Task 9 and Task 10, which add the actual handler methods this task's `registerRoutes` wires up.

- [ ] **Step 1: Write the failing auth test**

```go
// src/cmd/api-server/auth_test.go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequireBearerToken_MissingHeaderReturns401(t *testing.T) {
	handler := requireBearerToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireBearerToken_WrongTokenReturns401(t *testing.T) {
	handler := requireBearerToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireBearerToken_CorrectTokenCallsNext(t *testing.T) {
	called := false
	handler := requireBearerToken("secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}
```

- [ ] **Step 2: Write the failing error-translation test**

```go
// src/cmd/api-server/errors_test.go
package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWriteGRPCError_NotFoundMapsTo404(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, status.Error(codes.NotFound, "client x not found"))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestWriteGRPCError_InvalidArgumentMapsTo400(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, status.Error(codes.InvalidArgument, "bad input"))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWriteGRPCError_OtherCodesMapTo502(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, status.Error(codes.Unavailable, "backend down"))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestWriteGRPCError_NonGRPCErrorMapsTo502(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGRPCError(rec, errors.New("plain error"))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: FAIL (package/functions don't exist yet)

- [ ] **Step 4: Implement `errors.go`**

```go
// src/cmd/api-server/errors.go
package main

import (
	"encoding/json"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func writeJSON(w http.ResponseWriter, statusCode int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(body)
}

func writeJSONError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{"error": message})
}

// writeGRPCError translates a gRPC error's status code into an HTTP
// response: codes.NotFound -> 404, codes.InvalidArgument -> 400,
// everything else -> 502 (the backend is reachable but returned something
// this layer has no more specific mapping for).
func writeGRPCError(w http.ResponseWriter, err error) {
	st, ok := status.FromError(err)
	if !ok {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	switch st.Code() {
	case codes.NotFound:
		writeJSONError(w, http.StatusNotFound, st.Message())
	case codes.InvalidArgument:
		writeJSONError(w, http.StatusBadRequest, st.Message())
	default:
		writeJSONError(w, http.StatusBadGateway, st.Message())
	}
}
```

- [ ] **Step 5: Implement `auth.go`**

```go
// src/cmd/api-server/auth.go
package main

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// requireBearerToken guards next with a single shared bearer token --
// api-server's only auth layer in v1 (see
// docs/superpowers/specs/2026-07-14-api-server-design.md, "no RBAC yet").
func requireBearerToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, prefix) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		presented := strings.TrimPrefix(auth, prefix)
		if subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS (`auth_test.go` and `errors_test.go`)

- [ ] **Step 7: Implement `server.go` (routing skeleton, no handlers yet)**

```go
// src/cmd/api-server/server.go
package main

import (
	"context"
	"log/slog"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
)

// clientManagerClient is the subset of pb.ClientManagerServiceClient the
// clients handlers (Task 9) need -- satisfied by the real generated
// client, and by a fake in tests.
type clientManagerClient interface {
	ListClients(ctx context.Context, in *pb.ListClientsRequest, opts ...grpc.CallOption) (*pb.ListClientsResponse, error)
	GetClient(ctx context.Context, in *pb.GetClientRequest, opts ...grpc.CallOption) (*pb.Client, error)
}

// catalogQueryClient is the subset of pb.CatalogServiceClient the catalog
// handler (Task 10) needs.
type catalogQueryClient interface {
	ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error)
}

type server struct {
	clientManager clientManagerClient
	catalog       catalogQueryClient
	logger        *slog.Logger
}

func newServer(cm clientManagerClient, catalog catalogQueryClient, logger *slog.Logger) *server {
	return &server{clientManager: cm, catalog: catalog, logger: logger}
}

// registerRoutes wires up every REST endpoint. handleListClients,
// handleGetClient (Task 9), and handleListCatalog (Task 10) are defined
// in their own files; declaring the routes here means this file compiles
// once those methods exist, without needing placeholder stubs.
func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clients", s.handleListClients)
	mux.HandleFunc("GET /api/v1/clients/{hostname}", s.handleGetClient)
	mux.HandleFunc("GET /api/v1/catalog", s.handleListCatalog)
}
```

- [ ] **Step 8: Implement `arguments.go`**

```go
// src/cmd/api-server/arguments.go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Port  int
	Token string
	Debug bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "api-server",
		Short: "Unified read-only REST API for the control plane",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().IntVar(&args.Port, "port", conf.APIServerPort, "Port to listen on")
	cmd.Flags().StringVar(&args.Token, "token", conf.APIServerToken, "Bearer token required on every REST request")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	if err := common.ValidatePort(args.Port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}
	if args.Token == "" {
		return nil, fmt.Errorf("bearer token must be set (--token flag or api_server_token in local.conf)")
	}

	return args, nil
}
```

`registerRoutes` references `s.handleListClients`, `s.handleGetClient`, and `s.handleListCatalog` as method values; those methods are added by Task 9 and Task 10. The package will not build until then — expected at this point in the plan (see Step 10 below).

- [ ] **Step 9: Implement `main.go`**

```go
// src/cmd/api-server/main.go
// api-server exposes a unified, read-only REST API in front of the
// control plane's clientmanager-api and catalog gRPC services, for
// browsers and admin tools that don't hold a mesh mTLS client
// certificate. See docs/components/api-server.md and
// docs/superpowers/specs/2026-07-14-api-server-design.md.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
	const appName = "api-server"

	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	cmConn, err := connection.Connect(conf.ClientManagerAPIHost, conf.ClientManagerAPIPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to clientmanager-api failed", "error", err)
		os.Exit(1)
	}
	defer cmConn.Close()

	catalogConn, err := connection.Connect(conf.CatalogHost, conf.CatalogPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to catalog failed", "error", err)
		os.Exit(1)
	}
	defer catalogConn.Close()

	srv := newServer(pb.NewClientManagerServiceClient(cmConn), pb.NewCatalogServiceClient(catalogConn), logger)

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	handler := requireBearerToken(arguments.Token, mux)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{Addr: fmt.Sprintf(":%d", arguments.Port), Handler: handler}
	go func() {
		<-signalCtx.Done()
		logger.Info("shutting down api-server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("api-server started", "port", arguments.Port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 10: Confirm auth/errors tests still pass and note the expected build gap**

Run: `cd src && go test ./cmd/api-server/... -v 2>&1 | tail -20`
Expected: the package fails to *build* (`s.handleListClients undefined`, `s.handleListCatalog undefined`) — expected at this point, since Task 9 and Task 10 haven't added those methods yet. Do not treat this as a failure to fix within this task; proceed to commit.

- [ ] **Step 11: Commit**

```bash
git add src/cmd/api-server/main.go src/cmd/api-server/arguments.go src/cmd/api-server/server.go src/cmd/api-server/auth.go src/cmd/api-server/errors.go src/cmd/api-server/auth_test.go src/cmd/api-server/errors_test.go
git commit -m "feat(api-server): binary skeleton with bearer-token auth and gRPC error translation"
```

---

### Task 9: `api-server` — `/api/v1/clients` handlers

**Files:**
- Create: `src/cmd/api-server/clients.go`
- Test: `src/cmd/api-server/clients_test.go`

**Interfaces:**
- Consumes: Task 8's `server`/`clientManagerClient` interface/`writeJSON`/`writeGRPCError`; Task 4's `pb.Client`/`pb.ListClientsRequest`/`pb.GetClientRequest`.
- Produces: `(*server) handleListClients`, `(*server) handleGetClient` — resolves Task 8's `registerRoutes` reference to these methods.

- [ ] **Step 1: Write the failing tests**

```go
// src/cmd/api-server/clients_test.go
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

type fakeClientManagerClient struct {
	listResp *pb.ListClientsResponse
	listErr  error
	getResp  *pb.Client
	getErr   error
}

func (f *fakeClientManagerClient) ListClients(ctx context.Context, in *pb.ListClientsRequest, opts ...grpc.CallOption) (*pb.ListClientsResponse, error) {
	return f.listResp, f.listErr
}

func (f *fakeClientManagerClient) GetClient(ctx context.Context, in *pb.GetClientRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	return f.getResp, f.getErr
}

func TestHandleListClients_ReturnsDataEnvelope(t *testing.T) {
	fake := &fakeClientManagerClient{listResp: &pb.ListClientsResponse{
		Clients: []*pb.Client{{Hostname: "node-1", Sans: []string{"a.internal"}}},
	}}
	srv := newServer(fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	assert.Equal(t, "node-1", data[0].(map[string]any)["hostname"])
}

func TestHandleListClients_BackendErrorTranslated(t *testing.T) {
	fake := &fakeClientManagerClient{listErr: status.Error(codes.Unavailable, "down")}
	srv := newServer(fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandleGetClient_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerClient{getErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServer(fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients/ghost", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleGetClient_ReturnsClientObject(t *testing.T) {
	fake := &fakeClientManagerClient{getResp: &pb.Client{Hostname: "node-1", Revoked: true}}
	srv := newServer(fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients/node-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "node-1", body["hostname"])
	assert.Equal(t, true, body["revoked"])
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: FAIL to compile — `s.handleListClients undefined` (also affects Task 8's auth/errors tests transitively, since the whole package fails to compile — expected until this step's implementation lands)

- [ ] **Step 3: Implement `clients.go`**

```go
// src/cmd/api-server/clients.go
package main

import (
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type clientDTO struct {
	Hostname     string            `json:"hostname"`
	Revoked      bool              `json:"revoked"`
	RevokedAt    int64             `json:"revoked_at"`
	LastSeenAt   int64             `json:"last_seen_at"`
	Sans         []string          `json:"sans"`
	Attributes   map[string]string `json:"attributes"`
	Descriptions map[string]string `json:"descriptions"`
}

func toClientDTO(c *pb.Client) clientDTO {
	return clientDTO{
		Hostname:     c.GetHostname(),
		Revoked:      c.GetRevoked(),
		RevokedAt:    c.GetRevokedAt(),
		LastSeenAt:   c.GetLastSeenAt(),
		Sans:         c.GetSans(),
		Attributes:   c.GetAttributes(),
		Descriptions: c.GetDescriptions(),
	}
}

func (s *server) handleListClients(w http.ResponseWriter, r *http.Request) {
	resp, err := s.clientManager.ListClients(r.Context(), &pb.ListClientsRequest{})
	if err != nil {
		s.logger.Error("handleListClients: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	clients := make([]clientDTO, len(resp.GetClients()))
	for i, c := range resp.GetClients() {
		clients[i] = toClientDTO(c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": clients})
}

func (s *server) handleGetClient(w http.ResponseWriter, r *http.Request) {
	hostname := r.PathValue("hostname")
	client, err := s.clientManager.GetClient(r.Context(), &pb.GetClientRequest{Hostname: hostname})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toClientDTO(client))
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS for `clients_test.go`, `auth_test.go`, `errors_test.go`. (`TestHandleListCatalog` doesn't exist yet — that's Task 10; there should be no other failures.)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/clients.go src/cmd/api-server/clients_test.go
git commit -m "feat(api-server): implement /api/v1/clients and /api/v1/clients/{hostname}"
```

---

### Task 10: `api-server` — `/api/v1/catalog` handler

**Files:**
- Create: `src/cmd/api-server/catalog.go`
- Test: `src/cmd/api-server/catalog_test.go`

**Interfaces:**
- Consumes: Task 8's `server`/`catalogQueryClient` interface/`writeJSON`/`writeJSONError`/`writeGRPCError`; Task 2's `pb.Entry`/`pb.ListEntriesRequest`/`pb.ListEntriesResponse`.
- Produces: `(*server) handleListCatalog` — resolves the last unimplemented route from Task 8's `registerRoutes`, making `api-server` fully buildable.

- [ ] **Step 1: Write the failing tests**

```go
// src/cmd/api-server/catalog_test.go
package main

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

type fakeCatalogQueryClient struct {
	resp     *pb.ListEntriesResponse
	err      error
	lastReq  *pb.ListEntriesRequest
}

func (f *fakeCatalogQueryClient) ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error) {
	f.lastReq = in
	return f.resp, f.err
}

func TestHandleListCatalog_ReturnsDataAndHasMore(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{
		Entries: []*pb.Entry{{Id: 1, SourceHost: "bwfs-a", Path: "/var/log/syslog"}},
		HasMore: true,
	}}
	srv := newServer(nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["has_more"])
	data := body["data"].([]any)
	require.Len(t, data, 1)
	assert.Equal(t, "/var/log/syslog", data[0].(map[string]any)["path"])
}

func TestHandleListCatalog_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?source_host=bwfs-a&pattern=/var/log&limit=10&starting_after=42", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, "bwfs-a", fake.lastReq.GetSourceHost())
	assert.Equal(t, "/var/log", fake.lastReq.GetPattern())
	assert.Equal(t, int32(10), fake.lastReq.GetLimit())
	assert.Equal(t, int64(42), fake.lastReq.GetStartingAfter())
}

func TestHandleListCatalog_InvalidLimitReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?limit=not-a-number", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_LimitOutOfRangeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?limit=501", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: FAIL to compile — `s.handleListCatalog undefined`

- [ ] **Step 3: Implement `catalog.go`**

```go
// src/cmd/api-server/catalog.go
package main

import (
	"net/http"
	"strconv"

	pb "github.com/alex-sviridov/miniprotector/api"
)

const (
	defaultCatalogLimit = 100
	maxCatalogLimit     = 500
)

type entryDTO struct {
	ID              int64  `json:"id"`
	SourceHost      string `json:"source_host"`
	JobID           string `json:"job_id"`
	ObjectID        string `json:"object_id"`
	Ctime           int64  `json:"ctime"`
	SourceCreatedAt int64  `json:"source_created_at"`
	ReceivedAt      int64  `json:"received_at"`
	Path            string `json:"path"`
	Size            int64  `json:"size"`
	Mode            string `json:"mode"`
	Owner           uint32 `json:"owner"`
	Group           uint32 `json:"group"`
	ModTime         int64  `json:"mod_time"`
}

func toEntryDTO(e *pb.Entry) entryDTO {
	return entryDTO{
		ID:              e.GetId(),
		SourceHost:      e.GetSourceHost(),
		JobID:           e.GetJobId(),
		ObjectID:        e.GetObjectId(),
		Ctime:           e.GetCtime(),
		SourceCreatedAt: e.GetSourceCreatedAt(),
		ReceivedAt:      e.GetReceivedAt(),
		Path:            e.GetPath(),
		Size:            e.GetSize(),
		Mode:            e.GetMode(),
		Owner:           e.GetOwner(),
		Group:           e.GetGroup(),
		ModTime:         e.GetModTime(),
	}
}

func (s *server) handleListCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := defaultCatalogLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxCatalogLimit {
			writeJSONError(w, http.StatusBadRequest, "limit must be an integer between 1 and 500")
			return
		}
		limit = parsed
	}

	var startingAfter int64
	if raw := q.Get("starting_after"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeJSONError(w, http.StatusBadRequest, "starting_after must be a non-negative integer")
			return
		}
		startingAfter = parsed
	}

	resp, err := s.catalog.ListEntries(r.Context(), &pb.ListEntriesRequest{
		SourceHost:    q.Get("source_host"),
		Pattern:       q.Get("pattern"),
		Limit:         int32(limit),
		StartingAfter: startingAfter,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}

	entries := make([]entryDTO, len(resp.GetEntries()))
	for i, e := range resp.GetEntries() {
		entries[i] = toEntryDTO(e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": entries, "has_more": resp.GetHasMore()})
}
```

- [ ] **Step 4: Run all `api-server` tests**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — every test in `auth_test.go`, `errors_test.go`, `clients_test.go`, `catalog_test.go`

- [ ] **Step 5: Confirm the whole module builds**

Run: `cd src && go build ./... && go vet ./...`
Expected: succeeds with no errors

- [ ] **Step 6: Build both new binaries end-to-end**

Run: `make clientmanager-api api-server`
Expected: `Built successfully: bin/clientmanager-api` and `Built successfully: bin/api-server`

- [ ] **Step 7: Run the full test suite**

Run: `make test`
Expected: PASS — no regressions in any existing package

- [ ] **Step 8: Commit**

```bash
git add src/cmd/api-server/catalog.go src/cmd/api-server/catalog_test.go
git commit -m "feat(api-server): implement /api/v1/catalog with filtering and pagination"
```

---

### Task 11: Deploy `clientmanager-api` (control-plane + demo)

**Files:**
- Create: `deploy/control-plane/clientmanager-api/Dockerfile`
- Create: `deploy/control-plane/clientmanager-api/entrypoint.sh`
- Create: `deploy/control-plane/clientmanager-api/local.conf`
- Modify: `deploy/control-plane/docker-compose.yml`
- Modify: `demo/docker-compose.yml`
- Modify: `demo/local.conf`

- [ ] **Step 1: Control-plane Dockerfile**

```dockerfile
# deploy/control-plane/clientmanager-api/Dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make clientmanager-api certclient agent policyclient

FROM timberio/vector:0.46.0-debian AS vector-source

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/clientmanager-api /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/clientmanager-api/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: Control-plane entrypoint.sh**

```sh
#!/bin/sh
set -e

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart) of the long-lived bootstrap credential.
if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent keeps both the bootstrap credential (daily) and the operating
# credential (every 15 min, talking to issuer) fresh continuously.
./agent serve &

# Wait for agent's first operating-refresh to produce client.crt/client.key
# before starting clientmanager-api.
timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
	sleep 1
	timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
	echo "agent did not produce an operating certificate within 60s" >&2
	exit 1
fi

exec ./clientmanager-api --debug="${DEBUG:-false}"
```

- [ ] **Step 3: Control-plane local.conf**

```
# default_port/default_streams/log_dir are required by every miniprotector
# binary's shared config parser, even though clientmanager-api itself only
# uses clientmanager_api_port, var_path, ca_host, and issuer_host below.
default_port=15722
default_streams=4
log_dir=/data/log

# The port clientmanager-api listens on.
clientmanager_api_port=9500

# Set to this deployment's CA host:port before first boot.
ca_host=ca.backup.internal:9000

# Where clientmanager-api's own agent-managed operating-refresh policy
# dials issuer.
issuer_host=issuer
issuer_port=9200

# Where clientmanager-api's own agent-managed policy-update job dials
# policy-server. Every agent-managed node runs this job unconditionally.
policy_server_host=policy-server

log_gateway_host=log-gateway
log_gateway_port=9400

# Points at the same directory client-manager's own enrollment commands
# write their SQLite database to (mounted as a shared volume in
# docker-compose.yml) -- clientmanager-api and client-manager share one
# database file, not a synced pair. Same pattern issuer already uses.
var_path=/data/client-manager

ReconcileIntervalSec=30
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
```

- [ ] **Step 4: Add the service to `deploy/control-plane/docker-compose.yml`**

Add after the `issuer` service block:

```yaml
  clientmanager-api:
    build:
      context: ../..
      dockerfile: deploy/control-plane/clientmanager-api/Dockerfile
    depends_on:
      - step-ca
      - issuer
    volumes:
      - ./clientmanager-api/data:/data
      - ./clientmanager-api/local.conf:/data/local.conf:ro
      - ./client-manager/data:/data/client-manager:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    ports:
      - "9500:9500"
    restart: unless-stopped
```

- [ ] **Step 5: Validate the control-plane compose file**

Run: `cd deploy/control-plane && docker compose config --quiet`
Expected: no output, exit code 0 (valid YAML + no unresolved references)

- [ ] **Step 6: Add the service to `demo/docker-compose.yml`**

Add after the `issuer` service block:

```yaml
  clientmanager-api:
    build:
      context: ..
      dockerfile: deploy/control-plane/clientmanager-api/Dockerfile
    depends_on:
      - ca
      - issuer
    volumes:
      - clientmanager-api-data:/data
      - ./local.conf:/data/local.conf:ro
      - client-manager-data:/data/client-manager:ro
    environment:
      - MP_CONFIG_PATH=/data
    ports:
      - "9500:9500"
    restart: unless-stopped
```

Add `clientmanager-api-data:` to the `volumes:` section at the bottom of the file (alongside the existing `client-manager-data:`, `issuer-data:`, etc.).

- [ ] **Step 7: Add `clientmanager_api_port` to `demo/local.conf`**

Append:

```

# The port clientmanager-api listens on.
clientmanager_api_port=9500
```

(`demo/local.conf` already sets `var_path=/data/client-manager` globally, reused as-is.)

- [ ] **Step 8: Validate the demo compose file**

Run: `cd demo && docker compose config --quiet`
Expected: no output, exit code 0

- [ ] **Step 9: Commit**

```bash
git add deploy/control-plane/clientmanager-api/ deploy/control-plane/docker-compose.yml demo/docker-compose.yml demo/local.conf
git commit -m "deploy: add clientmanager-api to control-plane and demo compose stacks"
```

---

### Task 12: Deploy `api-server` (control-plane + demo)

**Files:**
- Create: `deploy/control-plane/api-server/Dockerfile`
- Create: `deploy/control-plane/api-server/entrypoint.sh`
- Create: `deploy/control-plane/api-server/local.conf`
- Modify: `deploy/control-plane/docker-compose.yml`
- Modify: `demo/docker-compose.yml`
- Modify: `demo/local.conf`
- Modify: `demo/up.sh`

**Interfaces:**
- Consumes: Task 11's `clientmanager-api` service being reachable at `clientmanager-api:9500` inside each compose network.

- [ ] **Step 1: Control-plane Dockerfile**

```dockerfile
# deploy/control-plane/api-server/Dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make api-server certclient agent policyclient

FROM timberio/vector:0.46.0-debian AS vector-source

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/api-server /build/bin/certclient /build/bin/agent /build/bin/policyclient ./
COPY --from=vector-source /usr/bin/vector ./vector
COPY deploy/control-plane/api-server/entrypoint.sh ./entrypoint.sh
RUN chmod +x ./entrypoint.sh

ENTRYPOINT ["./entrypoint.sh"]
```

- [ ] **Step 2: Control-plane entrypoint.sh**

```sh
#!/bin/sh
set -e

if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

./agent serve &

timeout=60
while [ ! -f /data/certs/client.crt ] && [ "$timeout" -gt 0 ]; do
	sleep 1
	timeout=$((timeout - 1))
done
if [ ! -f /data/certs/client.crt ]; then
	echo "agent did not produce an operating certificate within 60s" >&2
	exit 1
fi

exec ./api-server --debug="${DEBUG:-false}"
```

- [ ] **Step 3: Control-plane local.conf**

```
default_port=15722
default_streams=4
log_dir=/data/log

# The port api-server's REST listener binds to.
api_server_port=8090

# Bearer token every REST request must present. This is a placeholder --
# no RBAC exists yet (see docs/superpowers/specs/2026-07-14-api-server-design.md),
# and this is the sole guard on the REST API. Change it for any real
# deployment; it is not treated as a secret by this repo's tooling the way
# the CA provisioner password is.
api_server_token=dev-placeholder-token-change-me

ca_host=ca.backup.internal:9000
issuer_host=issuer
issuer_port=9200
policy_server_host=policy-server
log_gateway_host=log-gateway
log_gateway_port=9400

# Where api-server dials clientmanager-api and catalog.
clientmanager_api_host=clientmanager-api
clientmanager_api_port=9500
catalog_host=catalog
catalog_port=15723

ReconcileIntervalSec=30
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
```

- [ ] **Step 4: Add the service to `deploy/control-plane/docker-compose.yml`**

Add after the `catalog` service block:

```yaml
  api-server:
    build:
      context: ../..
      dockerfile: deploy/control-plane/api-server/Dockerfile
    depends_on:
      - step-ca
      - issuer
      - clientmanager-api
      - catalog
    volumes:
      - ./api-server/data:/data
      - ./api-server/local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    ports:
      - "8090:8090"
    restart: unless-stopped
```

- [ ] **Step 5: Validate the control-plane compose file**

Run: `cd deploy/control-plane && docker compose config --quiet`
Expected: no output, exit code 0

- [ ] **Step 6: Add the service to `demo/docker-compose.yml`**

Add after the `catalog` service block:

```yaml
  api-server:
    build:
      context: ..
      dockerfile: deploy/control-plane/api-server/Dockerfile
    depends_on:
      - ca
      - issuer
      - clientmanager-api
      - catalog
    volumes:
      - api-server-data:/data
      - ./local.conf:/data/local.conf:ro
    environment:
      - MP_CONFIG_PATH=/data
      - MP_CERT_TOKEN=${MP_CERT_TOKEN:-}
    ports:
      - "8090:8090"
    restart: unless-stopped
```

Add `api-server-data:` to the `volumes:` section.

- [ ] **Step 7: Add api-server keys to `demo/local.conf`**

Append:

```

# The port api-server's REST listener binds to.
api_server_port=8090
api_server_token=dev-placeholder-token-change-me

# Where api-server dials clientmanager-api.
clientmanager_api_host=clientmanager-api
```

(`demo/local.conf` already has `catalog_host`/`catalog_port` set.)

- [ ] **Step 8: Update `demo/up.sh` to enroll `clientmanager-api` and `api-server`**

`demo/up.sh`'s `enroll()` helper (already defined earlier in the file) both enrolls a node with `client-manager` *and* brings its container up (`docker compose up -d --no-deps "$name"` is its last line) — no separate `docker compose up` call is needed after calling it.

The existing enrollment sequence, near the bottom of the file, reads:

```sh
enroll log-gateway
enroll catalog
enroll policy-server
enroll database
enroll webserver "role=web"
enroll store
```

Replace it with:

```sh
enroll log-gateway
enroll clientmanager-api
enroll catalog
enroll api-server
enroll policy-server
enroll database
enroll webserver "role=web"
enroll store
```

`clientmanager-api` is enrolled before `catalog` (no dependency between them, order doesn't matter here); `api-server` is enrolled immediately after `catalog`, once both of its dependencies (`clientmanager-api` and `catalog`) are already up — `api-server`'s `main.go` calls `connection.Connect` against both at startup and exits immediately if either is unreachable.

- [ ] **Step 9: Validate the demo compose file**

Run: `cd demo && docker compose config --quiet`
Expected: no output, exit code 0

- [ ] **Step 10: Commit**

```bash
git add deploy/control-plane/api-server/ deploy/control-plane/docker-compose.yml demo/docker-compose.yml demo/local.conf demo/up.sh
git commit -m "deploy: add api-server to control-plane and demo compose stacks"
```

- [ ] **Step 11: End-to-end smoke test against the demo lab**

Run: `make demo-up`
Expected: all services (including `clientmanager-api` and `api-server`) come up healthy; no crash-loop in `docker compose logs api-server` or `docker compose logs clientmanager-api`.

Then run:
```bash
curl -s -H "Authorization: Bearer dev-placeholder-token-change-me" http://localhost:8090/api/v1/clients | head -c 500
curl -s -H "Authorization: Bearer dev-placeholder-token-change-me" http://localhost:8090/api/v1/catalog | head -c 500
```
Expected: both return `200` with a `{"data": [...]}` JSON body (client list reflecting the demo lab's enrolled nodes; catalog list empty or populated depending on whether any backup jobs have run yet — either is a valid pass, the point is a well-formed response, not specific content).

Run `make demo-down` afterward to tear down.

---

### Task 13: Documentation

**Files:**
- Create: `docs/components/clientmanager-api.md`
- Create: `docs/components/api-server.md`
- Create: `docs/api/rest-v1.md`
- Modify: `docs/components/client-manager.md`
- Modify: `docs/components/catalog.md`
- Modify: `docs/protocols/catalog-sync.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: `docs/components/clientmanager-api.md`**

```markdown
# clientmanager-api

Read-only gRPC access to `client-manager`'s enrolled-client data (`clientmanager.sqlite`) — the
same file [`issuer`](./issuer.md) already shares. **Control-plane component**, runs on the CA host
(same requirement as `issuer`: needs filesystem access to the shared SQLite file).

`client-manager` itself stays exactly as it was before this component existed: a network-surface-free
CLI tool an operator runs by hand, holding the CA's provisioner password directly (see
[Design: Client Manager Phase 2](../superpowers/specs/2026-07-04-client-manager-phase2-design.md)
for why that's a deliberate security property, not an oversight). `clientmanager-api` never writes —
`client-manager` (CLI) and `issuer` remain the only writers to `clientmanager.sqlite`.

## Usage

```bash
clientmanager-api --port 9500
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `clientmanager_api_port` config value (default: 9500) | Port to listen on |
| `--debug` | false | Enable debug logging |

## How It Works

`ListClients` and `GetClient` are the only RPCs. Both read directly from the same
`storage/clientmanager` store `client-manager`'s CLI and `issuer` use — no caching, no independent
state. `GetClient` returns `NotFound` for an unknown hostname.

## Configuration Keys

- `clientmanager_api_port` — port to listen on *(default: 9500)*
- `var_path` — must point at the same directory `client-manager`'s SQLite database lives in (shared
  volume with `client-manager`/`issuer`)

## Certificates

Same mTLS pattern as every other mesh component: identity bootstrapped/renewed via `certclient`
against `MP_CONFIG_PATH/certs`.

## Deployment

Ships as part of the combined control-plane `docker compose` stack, alongside `issuer` — see
[`deploy/control-plane/README.md`](../../deploy/control-plane/README.md).

## Building

```bash
make clientmanager-api
```

## See Also

- [client-manager](./client-manager.md) — the CLI tool sharing this component's database
- [issuer](./issuer.md) — the existing precedent for a daemon sharing client-manager's database
- [api-server](./api-server.md) — the only intended caller of this service today
- [Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 2: `docs/components/api-server.md`**

```markdown
# api-server

Unified, read-only REST API in front of the control plane's client and catalog data — for browsers
and admin tools that don't hold a mesh mTLS client certificate. **Control-plane component.**

`api-server` is the system's first REST surface; every other inter-component call in this project
is gRPC over mTLS, including api-server's own outbound calls to
[`clientmanager-api`](./clientmanager-api.md) and [`catalog`](./catalog.md). It is a thin translation
layer — each REST endpoint maps to exactly one backend gRPC call, no cross-service aggregation.

## Usage

```bash
api-server --port 8090 --token <bearer-token>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `api_server_port` config value (default: 8090) | Port the REST listener binds to |
| `--token` | `api_server_token` config value | Bearer token required on every REST request |
| `--debug` | false | Enable debug logging |

## Endpoints

See [REST API v1](../api/rest-v1.md) for the full endpoint reference.

## Authentication

Every request must present `Authorization: Bearer <token>`, checked against the single
config-supplied token; missing or mismatched returns `401`. This is the only auth layer today — no
RBAC, no per-user identity (see
[Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)). Any node holding a
valid mesh operating credential can still call `clientmanager-api`/`catalog`'s RPCs directly,
bypassing this token — an accepted continuation of this project's existing "any operating-tier cert
may call any RPC it can reach" convention, not a new gap.

## Configuration Keys

- `api_server_port` — port the REST listener binds to *(default: 8090)*
- `api_server_token` — bearer token required on every REST request
- `clientmanager_api_host` / `clientmanager_api_port` — where to dial `clientmanager-api`
- `catalog_host` / `catalog_port` — where to dial `catalog`

## Certificates

Enrolls like any other mesh node (bootstrap credential → `certclient` → `issuer` operating cert) for
its *outbound* gRPC calls to `clientmanager-api`/`catalog`. The REST listener itself is plain
HTTP, guarded only by the bearer token above — it is not part of the mTLS mesh.

## Deployment

Ships as part of the combined control-plane `docker compose` stack — see
[`deploy/control-plane/README.md`](../../deploy/control-plane/README.md).

## Building

```bash
make api-server
```

## See Also

- [clientmanager-api](./clientmanager-api.md) — one of the two backends this component reads from
- [catalog](./catalog.md) — the other backend
- [REST API v1](../api/rest-v1.md)
- [Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 3: `docs/api/rest-v1.md`**

```markdown
# REST API v1

`api-server`'s REST surface: read-only, v1, no RBAC. See [api-server](../components/api-server.md)
for auth and deployment.

## Conventions

- Every response is JSON. Successful list responses are wrapped as `{"data": [...]}`, with
  `"has_more": bool` added when the endpoint paginates.
- Filters are plain query parameters (no `filter[...]` envelope).
- Pagination is cursor-based: `limit` (page size) + `starting_after` (the `id` of the last item on
  the previous page). Style follows Stripe/GitHub-list conventions, not JSON:API.
- Errors are `{"error": "<message>"}` with an appropriate HTTP status code.

## `GET /api/v1/clients`

Returns every enrolled client. Not paginated (the enrolled-client list is expected to stay small).

```json
{
  "data": [
    {
      "hostname": "database",
      "revoked": false,
      "revoked_at": 0,
      "last_seen_at": 1752400000,
      "sans": ["database.internal"],
      "attributes": {"role": "db"},
      "descriptions": {"owner": "alice"}
    }
  ]
}
```

## `GET /api/v1/clients/{hostname}`

Returns one client's full record (same shape as one entry above). `404` if `hostname` isn't
enrolled.

## `GET /api/v1/catalog`

Query parameters (all optional):

| Param | Type | Description |
|-------|------|--------------|
| `source_host` | string | Exact match on the backing-up node's hostname |
| `pattern` | string | Substring match against the entry's underlying object ID (which embeds the original file path) |
| `limit` | int, 1–500 | Page size, default 100 |
| `starting_after` | int | Continue from this entry `id` (from a previous page's last entry) |

```json
{
  "data": [
    {
      "id": 42,
      "source_host": "database",
      "job_id": "backup:daily-db-backup:...",
      "object_id": "fs://database:f:/var/lib/dbdata/data.db:1752400000",
      "ctime": 1752400000,
      "source_created_at": 1752400000,
      "received_at": 1752400010,
      "path": "/var/lib/dbdata/data.db",
      "size": 8192,
      "mode": "-rw-r--r--",
      "owner": 999,
      "group": 999,
      "mod_time": 1752400000
    }
  ],
  "has_more": false
}
```

`400` if `limit` isn't an integer in `[1, 500]`, or `starting_after` isn't a non-negative integer.

## See Also

- [Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)
- [Catalog Sync Protocol](../protocols/catalog-sync.md) — the internal gRPC protocol `ListEntries` (this API's `/catalog` backend) is part of
```

- [ ] **Step 4: Update `docs/components/client-manager.md`**

Add to the "See Also" section (after the `certclient` bullet):

```markdown
- [clientmanager-api](./clientmanager-api.md) — a separate daemon sharing this component's database
  for read-only access, the same way `issuer` already does; `client-manager` itself is unaffected
```

- [ ] **Step 5: Update `docs/components/catalog.md`**

Change the opening paragraph's `"Receive-and-store only today; no query/report API yet."` to:

```markdown
Also serves `ListEntries`, a read-only query RPC (filter by source host and a substring match
against the underlying object ID, keyset-paginated) — see [api-server](./api-server.md), the only
intended caller today.
```

Add to "See Also":

```markdown
- [api-server](./api-server.md) — exposes `ListEntries` over REST
```

- [ ] **Step 6: Update `docs/protocols/catalog-sync.md`**

Read the file first to match its existing structure, then add a new section documenting
`CatalogService.ListEntries` (request/response fields, filtering semantics, pagination) alongside
the existing `SyncFileVersions` documentation — mirror whatever heading level/style
`SyncFileVersions` currently uses.

- [ ] **Step 7: Update `docs/ARCHITECTURE.md`**

Read the file first, find wherever `catalog`/`policy-server`/`issuer` are listed as control-plane
components (component list and/or diagram), and add `clientmanager-api` and `api-server` following
the same format — `api-server` noted as the system's first REST (not gRPC) entry point.

- [ ] **Step 8: `CHANGELOG.md` entry**

Add to the top of `CHANGELOG.md` (after the `# Changelog` header, before the existing most-recent
entry), following the file's existing dated-entry format:

```markdown
## 2026-07-14 — api-server: unified read-only REST API for clients and catalog

`api-server` exposes a REST API in front of the control plane's client and catalog data — the first
REST surface in a system that's otherwise entirely gRPC-over-mTLS. `GET /api/v1/clients[/{hostname}]`
and `GET /api/v1/catalog` (filterable by source host and a path-pattern substring, cursor-paginated)
are backed by two gRPC additions: a new `clientmanager-api` daemon (mirroring `issuer`'s existing
pattern of opening `client-manager`'s SQLite file directly, rather than adding a network surface to
`client-manager` itself, which was a deliberate security property) and a new `ListEntries` RPC on
`catalog` (previously write-only). REST access is guarded by a single shared bearer token — no RBAC
yet, matching this phase's scope.
```

- [ ] **Step 9: Commit**

```bash
git add docs/components/clientmanager-api.md docs/components/api-server.md docs/api/rest-v1.md docs/components/client-manager.md docs/components/catalog.md docs/protocols/catalog-sync.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document clientmanager-api, api-server, and the new REST API"
```

---

## Final Verification

- [ ] Run `make lint` — expected: no `go vet` issues
- [ ] Run `make test` — expected: full suite passes
- [ ] Run `make build` — expected: all binaries, including `clientmanager-api` and `api-server`, build successfully
- [ ] Run `cd deploy/control-plane && docker compose config --quiet && cd ../../demo && docker compose config --quiet` — expected: both compose files valid
- [ ] Re-run the Task 12 Step 11 end-to-end smoke test (`make demo-up`, `curl` both endpoints, `make demo-down`) once more after all tasks are complete, to confirm nothing later broke it
