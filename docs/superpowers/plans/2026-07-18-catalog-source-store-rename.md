# Catalog Source/Store Rename Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the catalog's "source" fields (which actually mean the sending `bwfs`/storage node) to "store", and add a new, persisted, indexed `source_host` field that captures the real originating (backed-up) host — exposed through gRPC, REST, and the web frontend.

**Architecture:** `source_host` is derived once, server-side, in `catalog`'s `SyncFileVersions` handler, by decoding the entry's `Metadata` blob (the same `filesystem.FileInfo` gob-decode already used elsewhere) and reading its `Source()` — then persisted as a plain indexed SQLite column. `store_host`/`store_seq`/`store_created_at` are straight renames of the existing `SourceNode`/`SourceSeq`/`SourceCreatedAt` — no behavior change, only naming.

**Tech Stack:** Go (gorm/SQLite, gRPC/protobuf), Vue 3 + Pinia (frontend), protoc for code generation.

## Global Constraints

- **Working directory:** All work happens in the existing worktree at
  `/home/alex/miniprotector/.worktrees/policy-management-ui`, on the `policy-management-ui`
  branch — NOT in the main `/home/alex/miniprotector` checkout. Every command below assumes that
  directory as `cwd` unless stated otherwise.
- **No data migration.** `catalog` uses gorm `AutoMigrate` only (no migration framework). Renaming
  Go struct fields renames the derived SQLite column names; `AutoMigrate` will not rename existing
  columns, it will add new ones. This is an accepted trade-off (dev/demo data) — do not write a
  migration. Operationally, any existing `catalog.db` should be deleted before running the updated
  binary; this needs no code change, just a note in docs (Task 6).
- **The `(store_node, job_id, object_id)` idempotency key is unchanged** — `source_host` is an
  additional indexed column, never part of that key.
- Go tests: run with `cd src && go test <package> -v` from the worktree root. The full-repo
  `go test ./...` will NOT pass until Tasks 1–4 are all complete — Task 1 renames `storage/catalog`
  in isolation, which temporarily breaks `cmd/catalog` and `cmd/api-server` (they still reference
  the old field/method names) until Tasks 2–4 catch up. This is expected; each task's own steps
  tell you exactly which package to test.
- Frontend tests: no local `node`/`npm` — run via
  `docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run test`
  (from the worktree root). `node_modules` is already installed in the worktree checkout.
- `protoc`, `protoc-gen-go`, `protoc-gen-go-grpc` are available on `PATH` — `make proto` (run from
  the worktree root) regenerates `src/api/*.pb.go` from `src/api/catalog.proto`.

---

### Task 1: Rename storage layer, add persisted `SourceHost` column

**Files:**
- Modify: `src/storage/catalog/models.go`
- Modify: `src/storage/catalog/store.go`
- Test: `src/storage/catalog/store_test.go`

**Interfaces:**
- Produces: `catalog.EntryRecord{ID, StoreNode, JobID, ObjectID, Metadata, Ctime, StoreSeq, StoreCreatedAt, SourceHost, ReceivedAt}`; `catalog.Entry{StoreNode, JobID, ObjectID, Metadata, Ctime, StoreSeq, StoreCreatedAt, SourceHost}`; `catalog.ListEntriesFilter{StoreNode, SourceHost, Pattern, Limit, StartingAfter}`; `(*Store).EnsureEntries([]Entry) error`; `(*Store).ListEntries(ListEntriesFilter) ([]EntryRecord, bool, error)` — all unchanged in shape from before except the field renames and the new `SourceHost` field. Later tasks (`cmd/catalog/server.go`) construct `catalog.Entry` values and read `EntryRecord` fields using these exact names.

- [ ] **Step 1: Rewrite the test file to use the renamed fields and add a `SourceHost` filter test**

Replace the full contents of `src/storage/catalog/store_test.go`:

```go
package catalog

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEnsureEntries_PersistsBatch(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", Ctime: 100, StoreSeq: 1, StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", Ctime: 200, StoreSeq: 2, StoreCreatedAt: time.Now()},
	}
	require.NoError(t, store.EnsureEntries(batch))

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestEnsureEntries_DuplicateSameStoreNodeIsNoOp(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()}}
	require.NoError(t, store.EnsureEntries(batch))
	require.NoError(t, store.EnsureEntries(batch)) // resend, e.g. after a retried RPC

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestEnsureEntries_SameJobObjectDifferentStoreNodeAreDistinctRows(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	batch := []Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}
	require.NoError(t, store.EnsureEntries(batch))

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

func TestEnsureEntries_EmptyBatchSucceeds(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	assert.NoError(t, store.EnsureEntries(nil))
}

func TestEnsureEntries_PersistsSourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "database", entries[0].SourceHost)
}

func TestNew_CreatesMissingStorageDir(t *testing.T) {
	base := t.TempDir() + "/does/not/exist/yet"

	store, err := New(base)
	require.NoError(t, err)
	defer store.Close()
}

func TestListEntries_FiltersByStoreNode(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	entries, hasMore, err := store.ListEntries(ListEntriesFilter{StoreNode: "bwfs-a"})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, entries, 1)
	assert.Equal(t, "bwfs-a", entries[0].StoreNode)
}

func TestListEntries_FiltersBySourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	entries, hasMore, err := store.ListEntries(ListEntriesFilter{SourceHost: "database"})
	require.NoError(t, err)
	assert.False(t, hasMore)
	require.Len(t, entries, 1)
	assert.Equal(t, "database", entries[0].SourceHost)
}

func TestListEntries_FiltersByStoreNodeAndSourceHostCombined(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-3", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{StoreNode: "bwfs-a", SourceHost: "database"})
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "obj-1", entries[0].ObjectID)
}

func TestListEntries_FiltersByPatternSubstringOnObjectID(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "fs://bwfs-a:f:/var/log/syslog:100", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "fs://bwfs-a:f:/etc/passwd:100", StoreCreatedAt: time.Now()},
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
			{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: fmt.Sprintf("obj-%d", i), StoreCreatedAt: time.Now()},
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
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{Limit: 0})
	require.NoError(t, err)
	assert.Len(t, entries, 1) // default 100, well above the 1 row present

	entries, _, err = store.ListEntries(ListEntriesFilter{Limit: 10000})
	require.NoError(t, err)
	assert.Len(t, entries, 1) // capped at 500, still well above the 1 row present
}
```

- [ ] **Step 2: Run the test package, confirm it fails to compile**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: FAIL — compile errors like `unknown field StoreNode in struct literal of type catalog.Entry` (the fields don't exist yet).

- [ ] **Step 3: Rewrite `models.go`**

Replace the full contents of `src/storage/catalog/models.go`:

```go
package catalog

import "time"

// EntryRecord is one replicated file-version entry received from a bwfs
// node via catalogsync. (StoreNode, JobID, ObjectID) is the idempotency
// key: JobID/ObjectID alone are only unique within a single bwfs node, so
// StoreNode (the CA-verified hostname of the sending node, from the
// client's mTLS certificate) disambiguates across a fleet of bwfs nodes
// replicating to the same catalog.
type EntryRecord struct {
	ID             int64  `gorm:"primaryKey;autoIncrement"`
	StoreNode      string `gorm:"uniqueIndex:idx_store_job_object"`
	JobID          string `gorm:"uniqueIndex:idx_store_job_object"`
	ObjectID       string `gorm:"uniqueIndex:idx_store_job_object"`
	Metadata       []byte
	Ctime          int64
	StoreSeq       int64
	StoreCreatedAt time.Time
	// SourceHost is the real originating (backed-up) host, decoded from
	// Metadata at sync time -- distinct from StoreNode, the bwfs node that
	// sent the batch. Indexed so ListEntries can filter on it directly.
	SourceHost string `gorm:"index"`
	ReceivedAt time.Time
}
```

- [ ] **Step 4: Rewrite `store.go`**

Replace the full contents of `src/storage/catalog/store.go`:

```go
package catalog

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func New(basePath string) (*Store, error) {
	db, err := openDB(basePath)
	if err != nil {
		return nil, fmt.Errorf("open catalog db: %w", err)
	}
	return &Store{db: db}, nil
}

// Entry mirrors EntryRecord's replicated fields, decoupled from the gorm
// model so callers (the gRPC server) don't need to import gorm tags.
type Entry struct {
	StoreNode      string
	JobID          string
	ObjectID       string
	Metadata       []byte
	Ctime          int64
	StoreSeq       int64
	StoreCreatedAt time.Time
	SourceHost     string
}

// EnsureEntries idempotently persists batch: a row already present for a
// given (StoreNode, JobID, ObjectID) is left untouched rather than
// erroring — catalogsync retries a batch it isn't sure was received, so a
// resend after a partial success must be a safe no-op.
func (s *Store) EnsureEntries(batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]EntryRecord, len(batch))
	now := time.Now()
	for i, e := range batch {
		records[i] = EntryRecord{
			StoreNode:      e.StoreNode,
			JobID:          e.JobID,
			ObjectID:       e.ObjectID,
			Metadata:       e.Metadata,
			Ctime:          e.Ctime,
			StoreSeq:       e.StoreSeq,
			StoreCreatedAt: e.StoreCreatedAt,
			SourceHost:     e.SourceHost,
			ReceivedAt:     now,
		}
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "store_node"}, {Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&records).Error
}

// Count returns the total number of persisted entries.
func (s *Store) Count() (int64, error) {
	var n int64
	err := s.db.Model(&EntryRecord{}).Count(&n).Error
	return n, err
}

// ListEntriesFilter narrows and paginates a ListEntries query. A
// zero-valued filter matches every entry, newest first, first page.
type ListEntriesFilter struct {
	StoreNode     string // exact match against the sending bwfs node; "" = all store nodes
	SourceHost    string // exact match against the real originating host; "" = all source hosts
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
	if filter.StoreNode != "" {
		q = q.Where("store_node = ?", filter.StoreNode)
	}
	if filter.SourceHost != "" {
		q = q.Where("source_host = ?", filter.SourceHost)
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

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
```

- [ ] **Step 5: Run the test package, confirm it passes**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS (all tests, including the new `TestListEntries_FiltersBySourceHost` and
`TestListEntries_FiltersByStoreNodeAndSourceHostCombined`).

- [ ] **Step 6: Commit**

```bash
git add src/storage/catalog/models.go src/storage/catalog/store.go src/storage/catalog/store_test.go
git commit -m "refactor(catalog): rename source_node/seq/created_at to store_*, add indexed source_host"
```

---

### Task 2: Proto rename + add fields, regenerate, wire up `catalog` server's sync/query paths

**Files:**
- Modify: `src/api/catalog.proto`
- Regenerate: `src/api/catalog.pb.go`, `src/api/catalog_grpc.pb.go` (via `make proto`)
- Modify: `src/cmd/catalog/server.go`
- Test: `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: `catalogstore.Entry{StoreNode, JobID, ObjectID, Metadata, Ctime, StoreSeq, StoreCreatedAt, SourceHost}`, `catalogstore.ListEntriesFilter{StoreNode, SourceHost, Pattern, Limit, StartingAfter}`, `catalogstore.EntryRecord` (all from Task 1), `filesystem.DecodeFileInfo(data []byte) (*filesystem.FileInfo, error)` and `(*FileInfo).Source() string` (pre-existing, `src/workload/filesystem/fileinfo.go`).
- Produces: proto types `pb.FileVersionEntry.GetStoreSeq() int64`, `pb.ListEntriesRequest.GetStoreHost() string` / `.GetSourceHost() string`, `pb.Entry.GetStoreHost() string` / `.GetSourceHost() string` / `.GetStoreCreatedAt() int64` — consumed by Task 3 (`grpcsender.go`) and Task 4 (`api-server/catalog.go`).

- [ ] **Step 1: Edit the proto file**

In `src/api/catalog.proto`, replace the full contents:

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
  int64  store_seq  = 5; // bwfs's local file_versions.seq — informational only
  int64  created_at = 6; // unix seconds; bwfs's original recording time
}

message SyncRequest {
  repeated FileVersionEntry entries = 1;
}

message SyncResponse {} // empty ack — GrpcSender only checks error/nil

message ListEntriesRequest {
  string store_host     = 1; // exact match against the sending bwfs node's identity; empty = all
  string pattern        = 2; // substring match against object_id; empty = no filter
  int32  limit           = 3; // 1..500, default 100
  int64  starting_after  = 4; // last-seen entry ID from a previous page; 0 = first page
  string source_host    = 5; // exact match against the real originating (backed-up) host; empty = all
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
}
```

- [ ] **Step 2: Regenerate the proto Go code**

Run (from the worktree root, `/home/alex/miniprotector/.worktrees/policy-management-ui`):
```bash
make proto
```
Expected: `Protobuf code generated in src/api/` — `src/api/catalog.pb.go` now has `StoreSeq`,
`StoreHost`, `SourceHost` (on both `ListEntriesRequest` and `Entry`), `StoreCreatedAt` fields/getters.

- [ ] **Step 3: Confirm the expected build breakage (sanity check, not a real test)**

Run: `cd src && go build ./cmd/catalog/...`
Expected: FAIL — `server.go` still references the old proto/field names (e.g. `e.GetSourceSeq()`,
`SourceNode:`, `req.GetSourceHost()` used for the store filter). This confirms the regenerated
proto actually changed the Go API surface; Step 5 fixes it.

- [ ] **Step 4: Update `server_test.go` for the renamed fields and new `SourceHost` behavior**

Replace the full contents of `src/cmd/catalog/server_test.go`:

```go
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

const fixtureCertsDir = "../../common/testdata/certs"

func newTestCatalogServer(t *testing.T) (*catalogServer, *catalogstore.Store) {
	t.Helper()
	store, err := catalogstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return NewCatalogServer(store, logger), store
}

// fakeAuthContext builds a context carrying a self-signed certificate with
// the given hostname as its SAN, simulating what a real mTLS handshake
// leaves in a gRPC handler's context — without needing a real TLS
// connection or a CA-signed cert.
func fakeAuthContext(t *testing.T, hostname string) context.Context {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{
			State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
		},
	})
}

func TestSyncFileVersions_PersistsBatchUnderPeerHostname(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")

	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{
		{JobId: "job-1", ObjectId: "obj-1", Ctime: 100, StoreSeq: 1, CreatedAt: time.Now().Unix()},
	}}
	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestSyncFileVersions_NoPeerIdentityReturnsError(t *testing.T) {
	srv, _ := newTestCatalogServer(t)
	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}}}

	_, err := srv.SyncFileVersions(context.Background(), req)
	assert.Error(t, err)
}

func TestSyncFileVersions_DuplicateBatchIsIdempotent(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	ctx := fakeAuthContext(t, "bwfs-a.internal")
	req := &pb.SyncRequest{Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1", CreatedAt: time.Now().Unix()}}}

	_, err := srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)
	_, err = srv.SyncFileVersions(ctx, req)
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestSyncFileVersions_DerivesSourceHostFromMetadata(t *testing.T) {
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
	assert.Equal(t, "origin-host", entries[0].SourceHost)
}

func TestSyncFileVersions_MalformedMetadataLeavesSourceHostEmpty(t *testing.T) {
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
	assert.Equal(t, "", entries[0].SourceHost)
}

func TestSyncFileVersions_GRPCRoundTripWithoutTLSIsRejected(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	lis := bufconn.Listen(1 << 20)
	grpcSrv := grpc.NewServer()
	pb.RegisterCatalogServiceServer(grpcSrv, srv)
	go grpcSrv.Serve(lis)
	defer grpcSrv.GracefulStop()

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewCatalogServiceClient(conn)
	_, err = client.SyncFileVersions(context.Background(), &pb.SyncRequest{
		Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}},
	})
	// bufconn + insecure transport carries no peer certificate, so
	// PeerHostname fails and the RPC is rejected — proving identity is
	// enforced end to end, not just when a fake context is handed in.
	assert.Error(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestSyncFileVersions_RealMTLSRoundTrip uses the actual connection.StartServer/
// connection.Connect helpers production code uses, and the project's real
// testdata certs (whose client.crt SAN is "bwfs.internal" — see
// common/mtls/peer_test.go), to prove StoreNode extraction works against a
// genuine mTLS handshake, not just a fabricated context.
func TestSyncFileVersions_RealMTLSRoundTrip(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	require.NoError(t, ln.Close()) // release the port; connection.StartServer re-binds it

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(ctx, logger, port, fixtureCertsDir, func(s *grpc.Server) {
			pb.RegisterCatalogServiceServer(s, srv)
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})

	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "server did not start listening")

	conn, err := connection.Connect("localhost", port, 5, fixtureCertsDir)
	require.NoError(t, err)
	defer conn.Close()

	client := pb.NewCatalogServiceClient(conn)
	_, err = client.SyncFileVersions(context.Background(), &pb.SyncRequest{
		Entries: []*pb.FileVersionEntry{{JobId: "job-1", ObjectId: "obj-1"}},
	})
	require.NoError(t, err)

	count, err := store.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestListEntries_ReturnsPersistedEntriesNewestFirst(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 2)
	assert.Equal(t, "obj-2", resp.GetEntries()[0].GetObjectId())
	assert.False(t, resp.GetHasMore())
}

func TestListEntries_FiltersByStoreHost(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-b", JobID: "job-1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{StoreHost: "bwfs-a"})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "bwfs-a", resp.GetEntries()[0].GetStoreHost())
}

func TestListEntries_FiltersBySourceHost(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{SourceHost: "database"})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "database", resp.GetEntries()[0].GetSourceHost())
}

func TestListEntries_DecodesMetadataIntoEntryFields(t *testing.T) {
	srv, store := newTestCatalogServer(t)

	fi := filesystem.NewFileInfoForTest("bwfs-a", "/var/log/syslog", 4096, 0o644, 1000, 1000, time.Now())
	metadata, err := fi.Encode()
	require.NoError(t, err)

	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: fi.ID(), Metadata: metadata, StoreCreatedAt: time.Now()},
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
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", Metadata: []byte("not-gob-encoded"), StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "", resp.GetEntries()[0].GetPath())
}
```

- [ ] **Step 5: Rewrite `server.go`**

Replace the full contents of `src/cmd/catalog/server.go`:

```go
package main

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type catalogServer struct {
	pb.UnimplementedCatalogServiceServer
	store  *catalogstore.Store
	logger *slog.Logger
}

func NewCatalogServer(store *catalogstore.Store, logger *slog.Logger) *catalogServer {
	return &catalogServer{store: store, logger: logger}
}

func (s *catalogServer) SyncFileVersions(ctx context.Context, req *pb.SyncRequest) (*pb.SyncResponse, error) {
	storeNode, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("SyncFileVersions: could not determine peer identity", "error", err)
		return nil, err
	}

	entries := req.GetEntries()
	batch := make([]catalogstore.Entry, len(entries))
	for i, e := range entries {
		batch[i] = catalogstore.Entry{
			StoreNode:      storeNode,
			JobID:          e.GetJobId(),
			ObjectID:       e.GetObjectId(),
			Metadata:       e.GetMetadata(),
			Ctime:          e.GetCtime(),
			StoreSeq:       e.GetStoreSeq(),
			StoreCreatedAt: time.Unix(e.GetCreatedAt(), 0).UTC(),
			SourceHost:     decodeSourceHost(e.GetMetadata()),
		}
	}

	if err := s.store.EnsureEntries(batch); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}

	s.logger.Info("SyncFileVersions: batch persisted", "store_node", storeNode, "count", len(batch))
	return &pb.SyncResponse{}, nil
}

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

func (s *catalogServer) ListEntries(ctx context.Context, req *pb.ListEntriesRequest) (*pb.ListEntriesResponse, error) {
	records, hasMore, err := s.store.ListEntries(catalogstore.ListEntriesFilter{
		StoreNode:     req.GetStoreHost(),
		SourceHost:    req.GetSourceHost(),
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
// one bad row shouldn't hide every other entry in the response. SourceHost
// is NOT decoded here — it's read directly from rec.SourceHost, persisted
// once at sync time (see decodeSourceHost above).
func toProtoEntry(rec catalogstore.EntryRecord) *pb.Entry {
	entry := &pb.Entry{
		Id:             rec.ID,
		StoreHost:      rec.StoreNode,
		SourceHost:     rec.SourceHost,
		JobId:          rec.JobID,
		ObjectId:       rec.ObjectID,
		Ctime:          rec.Ctime,
		StoreCreatedAt: rec.StoreCreatedAt.Unix(),
		ReceivedAt:     rec.ReceivedAt.Unix(),
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

- [ ] **Step 6: Run the test package, confirm it passes**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS (all tests, including the two new `TestSyncFileVersions_DerivesSourceHostFromMetadata`
and `TestSyncFileVersions_MalformedMetadataLeavesSourceHostEmpty`, and the renamed
`TestListEntries_FiltersByStoreHost` / new `TestListEntries_FiltersBySourceHost`).

- [ ] **Step 7: Commit**

```bash
git add src/api/catalog.proto src/api/catalog.pb.go src/api/catalog_grpc.pb.go \
        src/cmd/catalog/server.go src/cmd/catalog/server_test.go
git commit -m "feat(catalog): rename proto source_* to store_*, derive+persist source_host at sync time"
```

---

### Task 3: `catalogsync`'s `GrpcSender` — rename `SourceSeq` → `StoreSeq`

**Files:**
- Modify: `src/cmd/catalogsync/grpcsender.go`
- Test: `src/cmd/catalogsync/grpcsender_test.go`

**Interfaces:**
- Consumes: `pb.FileVersionEntry.StoreSeq` (from Task 2).
- Produces: no change to `GrpcSender`'s own exported shape — `Send([]wfs.FileVersionRecord) error` is unchanged.

- [ ] **Step 1: Update the test's assertion for the renamed field**

In `src/cmd/catalogsync/grpcsender_test.go`, in `TestGrpcSender_Send_ConvertsBatchToSingleRequest`,
change:

```go
	assert.Equal(t, int64(1), fake.lastReq.Entries[0].SourceSeq)
```

to:

```go
	assert.Equal(t, int64(1), fake.lastReq.Entries[0].StoreSeq)
```

- [ ] **Step 2: Run the test package, confirm it fails to compile**

Run: `cd src && go test ./cmd/catalogsync/... -v`
Expected: FAIL — `fake.lastReq.Entries[0].StoreSeq undefined` is wrong direction; actually since
`pb.FileVersionEntry` was already regenerated in Task 2, the failure here will instead be in
`grpcsender.go` itself: `unknown field 'SourceSeq' in struct literal of type api.FileVersionEntry`.
Either way, the package fails to build until Step 3.

- [ ] **Step 3: Update `grpcsender.go`**

In `src/cmd/catalogsync/grpcsender.go`, in `(*GrpcSender).Send`, change:

```go
		entries[i] = &pb.FileVersionEntry{
			JobId:     r.JobID,
			ObjectId:  r.ObjectID,
			Metadata:  r.Metadata,
			Ctime:     r.Ctime,
			SourceSeq: r.Seq,
			CreatedAt: r.CreatedAt.Unix(),
		}
```

to:

```go
		entries[i] = &pb.FileVersionEntry{
			JobId:     r.JobID,
			ObjectId:  r.ObjectID,
			Metadata:  r.Metadata,
			Ctime:     r.Ctime,
			StoreSeq:  r.Seq,
			CreatedAt: r.CreatedAt.Unix(),
		}
```

- [ ] **Step 4: Run the test package, confirm it passes**

Run: `cd src && go test ./cmd/catalogsync/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalogsync/grpcsender.go src/cmd/catalogsync/grpcsender_test.go
git commit -m "refactor(catalogsync): use renamed store_seq proto field"
```

---

### Task 4: `api-server`'s REST catalog handler — rename + new `source_host` filter

**Files:**
- Modify: `src/cmd/api-server/catalog.go`
- Test: `src/cmd/api-server/catalog_test.go`

**Interfaces:**
- Consumes: `pb.ListEntriesRequest{StoreHost, SourceHost, Pattern, Limit, StartingAfter}`,
  `pb.Entry.GetStoreHost()`/`.GetSourceHost()`/`.GetStoreCreatedAt()` (from Task 2).
- Produces: `entryDTO` JSON shape with `source_host` and `store_host` (renamed from `source_host`/
  `source_created_at`) — consumed by the web frontend (Task 5).

- [ ] **Step 1: Update the test file for the renamed/new fields**

In `src/cmd/api-server/catalog_test.go`, replace `TestHandleListCatalog_ReturnsDataAndHasMore` and
`TestHandleListCatalog_PassesFilterQueryParamsThrough` with:

```go
func TestHandleListCatalog_ReturnsDataAndHasMore(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{
		Entries: []*pb.Entry{{Id: 1, StoreHost: "bwfs-a", SourceHost: "database", Path: "/var/log/syslog"}},
		HasMore: true,
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
	assert.Equal(t, true, body["has_more"])
	data := body["data"].([]any)
	require.Len(t, data, 1)
	entry := data[0].(map[string]any)
	assert.Equal(t, "/var/log/syslog", entry["path"])
	assert.Equal(t, "bwfs-a", entry["store_host"])
	assert.Equal(t, "database", entry["source_host"])
}

func TestHandleListCatalog_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?source_host=database&store_host=bwfs-a&pattern=/var/log&limit=10&starting_after=42", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, "database", fake.lastReq.GetSourceHost())
	assert.Equal(t, "bwfs-a", fake.lastReq.GetStoreHost())
	assert.Equal(t, "/var/log", fake.lastReq.GetPattern())
	assert.Equal(t, int32(10), fake.lastReq.GetLimit())
	assert.Equal(t, int64(42), fake.lastReq.GetStartingAfter())
}
```

(`TestHandleListCatalog_InvalidLimitReturns400` and `TestHandleListCatalog_LimitOutOfRangeReturns400`
are unchanged — leave them as-is.)

- [ ] **Step 2: Run the test package, confirm it fails**

Run: `cd src && go test ./cmd/api-server/... -v -run TestHandleListCatalog`
Expected: FAIL — `pb.Entry` has no field `StoreHost` set correctly reflected yet if Task 2 wasn't
already applied it would fail to compile; since Task 2 is already done, this instead fails on
assertions: `entry["store_host"]` is empty because `catalog.go` hasn't been updated yet.

- [ ] **Step 3: Rewrite `catalog.go`**

Replace the full contents of `src/cmd/api-server/catalog.go`:

```go
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
	ID             int64  `json:"id"`
	SourceHost     string `json:"source_host"`
	StoreHost      string `json:"store_host"`
	JobID          string `json:"job_id"`
	ObjectID       string `json:"object_id"`
	Ctime          int64  `json:"ctime"`
	StoreCreatedAt int64  `json:"store_created_at"`
	ReceivedAt     int64  `json:"received_at"`
	Path           string `json:"path"`
	Size           int64  `json:"size"`
	Mode           string `json:"mode"`
	Owner          uint32 `json:"owner"`
	Group          uint32 `json:"group"`
	ModTime        int64  `json:"mod_time"`
}

func toEntryDTO(e *pb.Entry) entryDTO {
	return entryDTO{
		ID:             e.GetId(),
		SourceHost:     e.GetSourceHost(),
		StoreHost:      e.GetStoreHost(),
		JobID:          e.GetJobId(),
		ObjectID:       e.GetObjectId(),
		Ctime:          e.GetCtime(),
		StoreCreatedAt: e.GetStoreCreatedAt(),
		ReceivedAt:     e.GetReceivedAt(),
		Path:           e.GetPath(),
		Size:           e.GetSize(),
		Mode:           e.GetMode(),
		Owner:          e.GetOwner(),
		Group:          e.GetGroup(),
		ModTime:        e.GetModTime(),
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
		StoreHost:     q.Get("store_host"),
		Pattern:       q.Get("pattern"),
		Limit:         int32(limit),
		StartingAfter: startingAfter,
	})
	if err != nil {
		s.logger.Error("handleListCatalog: backend call failed", "error", err)
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

- [ ] **Step 4: Run the test package, confirm it passes**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS

- [ ] **Step 5: Run the full non-e2e Go test suite as a checkpoint**

Run: `cd src && go test ./...`
Expected: PASS across every package (this is the first point since Task 1 where the whole
non-e2e Go build is green again).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/catalog.go src/cmd/api-server/catalog_test.go
git commit -m "feat(api-server): expose renamed store_host and new source_host on GET /api/v1/catalog"
```

---

### Task 5: Web frontend — Catalog view gets both `source_host` and `store_host`

**Files:**
- Modify: `web/src/stores/catalog.js`
- Modify: `web/src/views/CatalogView.vue`
- Test: `web/src/stores/catalog.spec.js`
- Test: `web/src/views/CatalogView.spec.js`

**Interfaces:**
- Consumes: `GET /api/v1/catalog` query params `source_host`, `store_host`, `pattern`, `limit`,
  `starting_after`; response entries with `source_host`/`store_host` fields (from Task 4).
- Produces: `useCatalogStore().filters = {sourceHost, storeHost, pattern}`; `search(filters)`
  taking that same shape.

- [ ] **Step 1: Update the store test**

Replace the full contents of `web/src/stores/catalog.spec.js`:

```js
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useCatalogStore } from './catalog'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

describe('catalog store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  it('search resets the cursor stack and fetches page 1 with filters', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()

    await catalog.search({ sourceHost: 'database', storeHost: 'bwfs-a', pattern: 'dbdata' })

    expect(apiFetch).toHaveBeenCalledWith('/catalog?source_host=database&store_host=bwfs-a&pattern=dbdata')
    expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }])
    expect(catalog.hasMore).toBe(true)
    expect(catalog.canGoPrev).toBe(false)
  })

  it('nextPage requests starting_after the last entry id and pushes the cursor stack', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })

    apiFetch.mockResolvedValue({ data: [{ id: 3 }, { id: 4 }], has_more: false })
    await catalog.nextPage()

    expect(apiFetch).toHaveBeenLastCalledWith('/catalog?starting_after=2')
    expect(catalog.entries).toEqual([{ id: 3 }, { id: 4 }])
    expect(catalog.canGoPrev).toBe(true)
  })

  it('prevPage pops the cursor stack and refetches the prior page', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })
    apiFetch.mockResolvedValue({ data: [{ id: 3 }, { id: 4 }], has_more: false })
    await catalog.nextPage()

    apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: true })
    await catalog.prevPage()

    expect(apiFetch).toHaveBeenLastCalledWith('/catalog')
    expect(catalog.canGoPrev).toBe(false)
  })

  it('nextPage does nothing when has_more is false', async () => {
    apiFetch.mockResolvedValue({ data: [{ id: 1 }], has_more: false })
    const catalog = useCatalogStore()
    await catalog.search({ sourceHost: '', storeHost: '', pattern: '' })

    apiFetch.mockClear()
    await catalog.nextPage()

    expect(apiFetch).not.toHaveBeenCalled()
  })
})
```

- [ ] **Step 2: Update the view test**

Replace the full contents of `web/src/views/CatalogView.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'

function mountView(state) {
  const pinia = createTestingPinia({
    stubActions: true,
    initialState: { catalog: { cursorStack: [], ...state } },
  })
  const wrapper = mount(CatalogView, { global: { plugins: [pinia] } })
  return { wrapper, catalog: useCatalogStore() }
}

describe('CatalogView', () => {
  it('calls search with empty filters on mount', () => {
    const { catalog } = mountView({ entries: [], hasMore: false, loading: false, error: null })
    expect(catalog.search).toHaveBeenCalledWith({ sourceHost: '', storeHost: '', pattern: '' })
  })

  it('submits the filter form via search', async () => {
    const { wrapper, catalog } = mountView({ entries: [], hasMore: false, loading: false, error: null })
    const inputs = wrapper.findAll('input')
    await inputs[0].setValue('database')
    await inputs[1].setValue('bwfs-a')
    await inputs[2].setValue('dbdata')
    await wrapper.find('form').trigger('submit.prevent')
    expect(catalog.search).toHaveBeenLastCalledWith({ sourceHost: 'database', storeHost: 'bwfs-a', pattern: 'dbdata' })
  })

  it('disables Next when hasMore is false and Prev when canGoPrev is false', () => {
    const { wrapper } = mountView({
      entries: [{ id: 1, path: '/x', source_host: 'h', store_host: 's', size: 1, mode: '-rw', mod_time: 0 }],
      hasMore: false,
      loading: false,
      error: null,
    })
    const buttons = wrapper.findAll('button')
    const next = buttons.find((b) => b.text() === 'Next')
    const prev = buttons.find((b) => b.text() === 'Prev')
    expect(next.attributes('disabled')).toBeDefined()
    expect(prev.attributes('disabled')).toBeDefined()
  })

  it('clicking Next calls catalog.nextPage', async () => {
    const { wrapper, catalog } = mountView({
      entries: [{ id: 1, path: '/x', source_host: 'h', store_host: 's', size: 1, mode: '-rw', mod_time: 0 }],
      hasMore: true,
      loading: false,
      error: null,
    })
    const next = wrapper.findAll('button').find((b) => b.text() === 'Next')
    await next.trigger('click')
    expect(catalog.nextPage).toHaveBeenCalledTimes(1)
  })
})
```

- [ ] **Step 3: Run the frontend tests, confirm they fail**

Run (from the worktree root):
```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run test
```
Expected: FAIL — `catalog.spec.js` and `CatalogView.spec.js` assertions don't match the current
store/view implementation yet (still only has `sourceHost`, no `storeHost`).

- [ ] **Step 4: Rewrite `catalog.js`**

Replace the full contents of `web/src/stores/catalog.js`:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'

function buildQuery(filters, startingAfter) {
  const params = new URLSearchParams()
  if (filters.sourceHost) params.set('source_host', filters.sourceHost)
  if (filters.storeHost) params.set('store_host', filters.storeHost)
  if (filters.pattern) params.set('pattern', filters.pattern)
  if (startingAfter !== undefined) params.set('starting_after', String(startingAfter))
  return params.toString()
}

export const useCatalogStore = defineStore('catalog', {
  state: () => ({
    filters: { sourceHost: '', storeHost: '', pattern: '' },
    cursorStack: [],
    entries: [],
    hasMore: false,
    loading: false,
    error: null,
  }),
  getters: {
    canGoPrev: (state) => state.cursorStack.length > 0,
  },
  actions: {
    async _fetchPage(startingAfter) {
      this.loading = true
      this.error = null
      try {
        const qs = buildQuery(this.filters, startingAfter)
        const body = await apiFetch(`/catalog${qs ? `?${qs}` : ''}`)
        this.entries = body.data
        this.hasMore = body.has_more
      } catch (err) {
        this.error = err.message
      } finally {
        this.loading = false
      }
    },
    async search(filters) {
      this.filters = { ...filters }
      this.cursorStack = []
      await this._fetchPage(undefined)
    },
    async nextPage() {
      if (!this.hasMore || this.entries.length === 0) return
      const lastId = this.entries[this.entries.length - 1].id
      this.cursorStack.push(lastId)
      await this._fetchPage(lastId)
    },
    async prevPage() {
      if (this.cursorStack.length === 0) return
      this.cursorStack.pop()
      const prevCursor = this.cursorStack[this.cursorStack.length - 1]
      await this._fetchPage(prevCursor)
    },
  },
})
```

- [ ] **Step 5: Rewrite `CatalogView.vue`**

Replace the full contents of `web/src/views/CatalogView.vue`:

```vue
<script setup>
import { onMounted, reactive } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { formatTimestamp } from '../utils/format'

const catalog = useCatalogStore()
const form = reactive({ sourceHost: '', storeHost: '', pattern: '' })

function submit() {
  catalog.search({ ...form })
}

onMounted(() => {
  catalog.search({ ...form })
})
</script>

<template>
  <div>
    <h1 class="text-xl font-semibold mb-4">Catalog</h1>
    <form @submit.prevent="submit" class="flex gap-2 mb-4">
      <input v-model="form.sourceHost" placeholder="source host" class="border rounded px-2 py-1" />
      <input v-model="form.storeHost" placeholder="store host" class="border rounded px-2 py-1" />
      <input v-model="form.pattern" placeholder="path pattern" class="border rounded px-2 py-1" />
      <button type="submit" class="bg-blue-600 text-white rounded px-3 py-1">Search</button>
    </form>
    <p v-if="catalog.loading">Loading...</p>
    <p v-else-if="catalog.error" class="text-red-600">{{ catalog.error }}</p>
    <table v-else class="w-full text-left border-collapse">
      <thead>
        <tr class="border-b">
          <th class="py-2 pr-4">Path</th>
          <th class="py-2 pr-4">Source Host</th>
          <th class="py-2 pr-4">Store Host</th>
          <th class="py-2 pr-4">Size</th>
          <th class="py-2 pr-4">Mode</th>
          <th class="py-2 pr-4">Modified</th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="entry in catalog.entries" :key="entry.id" class="border-b">
          <td class="py-2 pr-4">{{ entry.path }}</td>
          <td class="py-2 pr-4">{{ entry.source_host }}</td>
          <td class="py-2 pr-4">{{ entry.store_host }}</td>
          <td class="py-2 pr-4">{{ entry.size }}</td>
          <td class="py-2 pr-4">{{ entry.mode }}</td>
          <td class="py-2 pr-4">{{ formatTimestamp(entry.mod_time) }}</td>
        </tr>
      </tbody>
    </table>
    <div class="flex gap-2 mt-4">
      <button :disabled="!catalog.canGoPrev" @click="catalog.prevPage()" class="border rounded px-3 py-1 disabled:opacity-50">
        Prev
      </button>
      <button :disabled="!catalog.hasMore" @click="catalog.nextPage()" class="border rounded px-3 py-1 disabled:opacity-50">
        Next
      </button>
    </div>
  </div>
</template>
```

- [ ] **Step 6: Run the frontend tests, confirm they pass**

Run (from the worktree root):
```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run test
```
Expected: PASS (all suites, including `catalog.spec.js` and `CatalogView.spec.js`).

- [ ] **Step 7: Commit**

```bash
git add web/src/stores/catalog.js web/src/views/CatalogView.vue \
        web/src/stores/catalog.spec.js web/src/views/CatalogView.spec.js
git commit -m "feat(web): show and filter by both source host and store host in the Catalog view"
```

---

### Task 6: Documentation updates

Per this project's documentation rules (`.claude/CLAUDE.md`): protocol changes require updating
`docs/protocols/`, and feature changes require updating the relevant `docs/components/*.md` and
`README.md` (index only — no quick-start or component-list changes needed here, so `README.md`
itself needs no edit this time).

**Files:**
- Modify: `docs/protocols/catalog-sync.md`
- Modify: `docs/components/catalog.md`
- Modify: `docs/components/catalogsync.md`
- Modify: `docs/components/web.md`
- Modify: `docs/api/rest-v1.md`

- [ ] **Step 1: `docs/protocols/catalog-sync.md`**

Replace the `## Messages` code block:

```protobuf
message FileVersionEntry {
  string job_id     = 1;
  string object_id  = 2;
  bytes  metadata   = 3;
  int64  ctime      = 4;
  int64  store_seq  = 5; // bwfs's local file_versions.seq — informational only
  int64  created_at = 6; // unix seconds; bwfs's original recording time
}

message SyncRequest {
  repeated FileVersionEntry entries = 1;
}

message SyncResponse {} // empty ack
```

Replace the `## Identity` section:

```markdown
## Identity

`catalog` does not trust any node identifier carried in the request payload. The persisted
`store_node` for every entry in a batch comes from the CA-verified hostname on the caller's mTLS
client certificate (first SAN, falling back to CommonName — see `common/mtls.PeerHostname`). This
is what lets `(store_node, job_id, object_id)` serve as a safe idempotency key across a fleet of
`bwfs` nodes whose `job_id`/`object_id` values are otherwise only unique per-node.

`catalog` also derives `source_host` at sync time — the real originating (backed-up) host, decoded
from each entry's `metadata` blob (a gob-encoded `filesystem.FileInfo`; see
`workload/filesystem.FileInfo.Source()`) and persisted as a plain indexed column. This is distinct
from `store_node`: `store_node` identifies the `bwfs` node that sent the batch, `source_host`
identifies the machine whose files were actually backed up — they coincide only when a `bwfs`
node backs up its own filesystem. A metadata decode failure leaves `source_host` empty for that
entry rather than failing the whole batch.
```

Replace the `## ListEntries` proto block and the bullet list beneath it:

```protobuf
message ListEntriesRequest {
  string store_host     = 1; // exact match against the sending bwfs node's identity; empty = all
  string pattern        = 2; // substring match against object_id; empty = no filter
  int32  limit           = 3; // 1..500, default 100
  int64  starting_after  = 4; // last-seen entry ID from a previous page; 0 = first page
  string source_host    = 5; // exact match against the real originating (backed-up) host; empty = all
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
}
```

```markdown
- `store_host` — exact match against the same CA-verified hostname `SyncFileVersions` persists as
  `store_node`; empty matches every store node.
- `source_host` — exact match against the real originating (backed-up) host, persisted at sync
  time (see [Identity](#identity) above); empty matches every source host.
- `pattern` — substring match against `object_id` (which embeds the original file path, e.g.
  `fs://database:f:/var/lib/dbdata/data.db:1752400000`); empty applies no filter.
- Pagination is keyset-based on `id`: request the first page with `starting_after` unset (or `0`),
  then pass the last entry's `id` from the previous page as `starting_after` to get the next one.
  `has_more` is `true` when additional entries exist beyond the current page. `limit` defaults to
  100 and is capped at 500.
- `path`, `size`, `mode`, `owner`, `group`, and `mod_time` are decoded server-side from the same
  opaque `metadata` blob `SyncFileVersions` stores verbatim — `ListEntries` is the first RPC to
  interpret that blob's contents rather than just persisting it (`source_host` is decoded once,
  at sync time, not on every `ListEntries` call — see [Identity](#identity)).
```

- [ ] **Step 2: `docs/components/catalog.md`**

In the top summary paragraph, change:

```markdown
Also serves `ListEntries`, a read-only query RPC (filter by
source host and a substring match against the underlying object ID, keyset-paginated) — see
```

to:

```markdown
Also serves `ListEntries`, a read-only query RPC (filter by store host, real source host, and a
substring match against the underlying object ID, keyset-paginated) — see
```

Replace the `## How It Works` section:

```markdown
## How It Works

`SyncFileVersions` is the write path: one call per batch `catalogsync` sends. Each entry is
persisted keyed by `(store_node, job_id, object_id)`:

- `store_node` is the CA-verified hostname from the caller's mTLS client certificate
  (`mtls.PeerHostname`), never taken from the RPC payload. `job_id`/`object_id` alone are only
  unique within a single `bwfs` node; `store_node` disambiguates across a fleet of nodes
  replicating to the same catalog.
- `source_host` — the real originating (backed-up) host — is derived at the same time, by decoding
  each entry's `metadata` blob and reading its embedded host. It's distinct from `store_node`: a
  `bwfs` node forwards entries for whatever host was actually backed up, which is not necessarily
  itself.
- A batch containing an entry already stored for its `(store_node, job_id, object_id)` is a
  no-op for that entry (`ON CONFLICT DO NOTHING`) — safe for `catalogsync` to resend a batch it
  isn't sure was received.
```

- [ ] **Step 3: `docs/components/catalogsync.md`**

Change:

```markdown
The cursor is a single integer stored in `<storage_path>/catalogsync.cursor`, written atomically
(temp file + rename) after each confirmed send. If it's missing or corrupt, `catalogsync` starts
from the beginning (`seq=0`) — safe, because the catalog is expected to treat `(source_node, job_id, object_id)`
as an idempotency key for the resulting at-least-once delivery.
```

to:

```markdown
The cursor is a single integer stored in `<storage_path>/catalogsync.cursor`, written atomically
(temp file + rename) after each confirmed send. If it's missing or corrupt, `catalogsync` starts
from the beginning (`seq=0`) — safe, because the catalog is expected to treat `(store_node, job_id, object_id)`
as an idempotency key for the resulting at-least-once delivery.
```

- [ ] **Step 4: `docs/components/web.md`**

Change:

```markdown
- `/catalog` — catalog entries, filterable by source host and a path-pattern substring,
  paginated with Prev/Next (the catalog API only supports cursor pagination — no total count, so
  there's no page-number jump)
```

to:

```markdown
- `/catalog` — catalog entries, filterable by real source host, store host (the `bwfs` node that
  replicated the entry), and a path-pattern substring, paginated with Prev/Next (the catalog API
  only supports cursor pagination — no total count, so there's no page-number jump)
```

- [ ] **Step 5: `docs/api/rest-v1.md`**

Replace the `GET /api/v1/catalog` param table and example:

```markdown
## `GET /api/v1/catalog`

Query parameters (all optional):

| Param | Type | Description |
|-------|------|--------------|
| `source_host` | string | Exact match on the real originating (backed-up) host |
| `store_host` | string | Exact match on the `bwfs` node that replicated the entry |
| `pattern` | string | Substring match against the entry's underlying object ID (which embeds the original file path) |
| `limit` | int, 1–500 | Page size, default 100 |
| `starting_after` | int | Continue from this entry `id` (from a previous page's last entry) |

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
      "mod_time": 1752400000
    }
  ],
  "has_more": false
}
```

`400` if `limit` isn't an integer in `[1, 500]`, or `starting_after` isn't a non-negative integer.
```

- [ ] **Step 6: Commit**

```bash
git add docs/protocols/catalog-sync.md docs/components/catalog.md docs/components/catalogsync.md \
        docs/components/web.md docs/api/rest-v1.md
git commit -m "docs: document catalog store_host/source_host rename and new field"
```

---

### Task 7: e2e test updates (manual verification — requires Docker)

**Files:**
- Modify: `src/e2e/catalog_validate.go`
- Modify: `src/e2e/catalog_test.go`

These files carry the `//go:build e2e` tag, so they are not compiled or run by `go test ./...` —
only by `make test-e2e` (requires a Docker daemon, ~3 minutes). Update them for correctness, but
treat running them as an optional final check rather than a blocking step in this plan.

**Interfaces:**
- Consumes: `catalog.db`'s `entry_records` table columns `store_node`, `source_host`, `job_id`,
  `object_id` (from Task 1/2's renamed/new schema).

- [ ] **Step 1: Update `catalog_validate.go`**

In `src/e2e/catalog_validate.go`, change the `catalogEntryRow` struct and the query in
`waitForCatalogEntryCount`:

```go
type catalogEntryRow struct {
	StoreNode  string
	SourceHost string
	JobID      string
	ObjectID   string
}
```

```go
				if err := db.Table("entry_records").
					Select("store_node, source_host, job_id, object_id").
					Find(&got).Error; err == nil && len(got) >= wantCount {
```

(Only the `Select` column list and the struct's `SourceNode` field name change; everything else in
the function is unchanged.)

- [ ] **Step 2: Update `catalog_test.go`**

In `src/e2e/catalog_test.go`, in `TestE2E_CatalogReceivesReplicatedFileVersions`, change the final
assertion loop:

```go
	rows := waitForCatalogEntryCount(t, catalogStorageDir, wantCount)
	assert.Len(t, rows, wantCount)
	for _, row := range rows {
		assert.Equal(t, "bwfs.internal", row.StoreNode)
		assert.Equal(t, "e2e-src-host", row.SourceHost)
		assert.NotEmpty(t, row.JobID)
		assert.NotEmpty(t, row.ObjectID)
	}
```

This asserts both identities: `row.StoreNode` is `"bwfs.internal"` (the `bwfs` container's network
alias, per `startBwfsContainer`) and `row.SourceHost` is `"e2e-src-host"` (the hostname
`runBrfsContainer` sets on the backup-source container — see the `hostname` parameter in its
`runBrfsContainer(ctx, t, testImageID, networkID, dataDir, "bwfs.internal", 4, "e2e-src-host")`
call earlier in the same test).

- [ ] **Step 3 (optional, requires Docker): run the e2e suite**

Run: `make test-e2e`
Expected: PASS, including the new `SourceHost` assertion.

- [ ] **Step 4: Commit**

```bash
git add src/e2e/catalog_validate.go src/e2e/catalog_test.go
git commit -m "test(e2e): assert both store_node and source_host on replicated catalog entries"
```

---

### Task 8: Changelog + final full verification

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add a changelog entry**

At the top of `CHANGELOG.md`, immediately after the `# Changelog` header and its intro line, insert
a new entry (most-recent-first ordering, matching the existing entries below it):

```markdown
## 2026-07-18 — catalog: rename source_* to store_*, add a real source_host

The catalog's `source_node`/`source_seq`/`source_created_at`/`source_host` fields all actually
identified the `bwfs` node that replicated a batch, not the machine whose files were backed up —
confusing given "source" means the backup source everywhere else in the system. They're renamed to
`store_node`/`store_seq`/`store_created_at`/`store_host`. A new `source_host` is added in their
place: the real originating host, decoded once from each entry's metadata at sync time and
persisted as an indexed column, so it's independently filterable from `store_host`. Both are now
exposed through `ListEntries`, `GET /api/v1/catalog`, and the web frontend's Catalog view. No data
migration — existing `catalog.db` files should be deleted before running the updated binary.
```

- [ ] **Step 2: Run the full non-e2e Go suite one more time**

Run: `cd src && go test ./...`
Expected: PASS

- [ ] **Step 3: Run the full frontend suite one more time**

Run (from the worktree root):
```bash
docker run --rm --user "$(id -u):$(id -g)" -v "$(pwd)/web":/app -w /app node:20-alpine npm run test
```
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: add changelog entry for catalog source/store rename"
```
