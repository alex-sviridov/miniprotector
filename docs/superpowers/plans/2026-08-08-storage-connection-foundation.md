# Storage Connection Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace four hand-duplicated SQLite-open sequences with one shared `storage/sqlitedb` package, thread `context.Context` through every method of `catalog.Store`, `policyserver.Store`, `clientmanager.Store`, and `filesystem.ReplicaReader`, and give `catalog.Store` a read/write connection-pool split so its read-heavy query RPCs stop serializing behind fleet-wide sync writes.

**Architecture:** One new leaf package (`storage/sqlitedb`) provides a single `Open(Options) (*gorm.DB, error)` used by all four stores. Each store's constructor becomes a thin wrapper around it. Every store method gains a `ctx context.Context` first parameter and calls `.WithContext(ctx)` before issuing its query. `catalog.Store` is the only one of the four that opens two `*gorm.DB` handles (one writer, one read-only multi-connection pool) — the other three keep their existing single-connection topology.

**Tech Stack:** Go 1.26, GORM (`gorm.io/gorm`, `gorm.io/driver/sqlite`), `modernc.org/sqlite` (pure-Go SQLite driver), `testify` (`require`/`assert`).

## Global Constraints

- Go 1.26 — no need to guard `t.Context()` usage (stdlib since Go 1.24).
- No generics — this codebase has none in hand-written code; don't introduce them here.
- No new config values — `sqlitedb.Options.MaxConns` and the busy-timeout constant are fixed in code, not exposed via `local.conf`, matching this project's existing pattern of hardcoding constants nothing has ever needed to tune (see `docs/superpowers/specs/2026-08-08-storage-connection-foundation-design.md`).
- `filesystem.Store` (the 25-method `bwfs`/`brfs` backing store) is explicitly out of scope — do not touch `storage/filesystem/db.go`, `storage/filesystem/store.go`, `storage/filesystem/chunks.go`, `storage/filesystem/filedata.go`, `storage/filesystem/backupjob.go`, or `storage/filesystem/info.go`. Only `storage/filesystem/replicareader.go` (and its test) are in scope.
- Every task ends with `go build ./... && go test ./...` passing from `src/`.

---

### Task 1: `storage/sqlitedb` — shared Open helper

**Files:**
- Create: `src/storage/sqlitedb/sqlitedb.go`
- Test: `src/storage/sqlitedb/sqlitedb_test.go`

**Interfaces:**
- Produces: `sqlitedb.Options{Path string; ReadOnly bool; MaxConns int; Models []any}` and `sqlitedb.Open(Options) (*gorm.DB, error)` — every later task depends on these exact names and this exact signature.

- [ ] **Step 1: Write the test file**

```go
// src/storage/sqlitedb/sqlitedb_test.go
package sqlitedb

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testRecord struct {
	ID   int64 `gorm:"primaryKey;autoIncrement"`
	Name string
}

func TestOpen_WritableCreatesAndMigrates(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "test.db")

	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	defer func() { sqlDB, _ := db.DB(); sqlDB.Close() }()

	require.NoError(t, db.Create(&testRecord{Name: "a"}).Error)

	var count int64
	require.NoError(t, db.Model(&testRecord{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)
}

func TestOpen_ReadOnlyRejectsWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	writer, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	require.NoError(t, writer.Create(&testRecord{Name: "a"}).Error)
	writerSQL, err := writer.DB()
	require.NoError(t, err)
	require.NoError(t, writerSQL.Close())

	reader, err := Open(Options{Path: dbPath, ReadOnly: true})
	require.NoError(t, err)
	defer func() { sqlDB, _ := reader.DB(); sqlDB.Close() }()

	var recs []testRecord
	require.NoError(t, reader.Find(&recs).Error)
	assert.Len(t, recs, 1)

	err = reader.Create(&testRecord{Name: "b"}).Error
	assert.Error(t, err, "a read-only connection must reject writes")
}

func TestOpen_MaxConnsSetsPoolSize(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}, MaxConns: 4})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	assert.Equal(t, 4, sqlDB.Stats().MaxOpenConnections)
}

func TestOpen_DefaultMaxConnsIsOne(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	assert.Equal(t, 1, sqlDB.Stats().MaxOpenConnections)
}

func TestOpen_RespectsContextCancellation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(Options{Path: dbPath, Models: []any{&testRecord{}}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before issuing the query

	var recs []testRecord
	err = db.WithContext(ctx).Find(&recs).Error
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./storage/sqlitedb/... -v`
Expected: FAIL — `package sqlitedb is not in std` / `undefined: Open` (the package doesn't exist yet).

- [ ] **Step 3: Implement `sqlitedb.go`**

```go
// src/storage/sqlitedb/sqlitedb.go

// Package sqlitedb provides the shared SQLite-open sequence used by every
// GORM-backed store in this project: create the containing directory (for
// a writable open), open via database/sql through the modernc.org/sqlite
// driver, set a busy timeout and WAL journal mode, hand the connection to
// GORM, and run AutoMigrate.
package sqlitedb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

// busyTimeoutMS is how long a connection waits on SQLITE_BUSY before giving
// up. Not exposed as an Options field -- every caller across this project
// has used the same value, and none has ever needed a different one.
const busyTimeoutMS = 5000

// Options configures Open.
type Options struct {
	// Path is the full path to the database file, e.g.
	// filepath.Join(basePath, "catalog.db"). The caller builds this --
	// Open only knows how to open a file, not where a package's database
	// belongs.
	Path string
	// ReadOnly opens the database with SQLite's mode=ro URI flag, enforced
	// by the driver. A read-only Open skips MkdirAll (a read-only opener
	// must never be the thing that creates a store's directory), the WAL
	// pragma (WAL is a database-file-level setting; a writer must set it
	// before any reader connects, see storage/catalog/db.go), and
	// AutoMigrate (the schema must already exist).
	ReadOnly bool
	// MaxConns sets the connection pool size. 0 defaults to 1 -- SQLite
	// allows only one writer at a time, so every write-capable store keeps
	// this default; a read-only pool serving genuine concurrent read
	// traffic (see storage/catalog) sets this above 1 to let WAL-mode
	// readers run concurrently.
	MaxConns int
	// Models are AutoMigrate's targets. Ignored when ReadOnly is true, or
	// when empty.
	Models []any
}

// Open opens a GORM handle to a SQLite database per opts.
func Open(opts Options) (*gorm.DB, error) {
	dsn := opts.Path + fmt.Sprintf("?_busy_timeout=%d", busyTimeoutMS)
	if opts.ReadOnly {
		dsn = fmt.Sprintf("file:%s?mode=ro&_busy_timeout=%d", opts.Path, busyTimeoutMS)
	} else if err := os.MkdirAll(filepath.Dir(opts.Path), 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	maxConns := opts.MaxConns
	if maxConns == 0 {
		maxConns = 1
	}
	sqlDB.SetMaxOpenConns(maxConns)

	if !opts.ReadOnly {
		if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("set WAL mode: %w", err)
		}
	}

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	if !opts.ReadOnly && len(opts.Models) > 0 {
		if err := db.AutoMigrate(opts.Models...); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("automigrate: %w", err)
		}
	}
	return db, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./storage/sqlitedb/... -v`
Expected: PASS — all 5 tests green.

- [ ] **Step 5: Commit**

```bash
cd src && git add storage/sqlitedb/sqlitedb.go storage/sqlitedb/sqlitedb_test.go
git commit -m "$(cat <<'EOF'
feat: add shared sqlitedb.Open helper

One implementation of the open/WAL/migrate sequence duplicated across
catalog, policyserver, clientmanager, and filesystem.ReplicaReader.
EOF
)"
```

---

### Task 2: Migrate `storage/catalog` — dual pool + context propagation

**Files:**
- Modify: `src/storage/catalog/db.go`
- Modify: `src/storage/catalog/store.go`
- Modify: `src/storage/catalog/store_test.go`
- Modify: `src/cmd/catalog/server.go`
- Modify: `src/cmd/catalog/server_test.go`

**Interfaces:**
- Consumes: `sqlitedb.Open(sqlitedb.Options) (*gorm.DB, error)` from Task 1.
- Produces: every `catalog.Store` method now takes `ctx context.Context` as its first parameter (`EnsureEntries(ctx, batch)`, `EnsureDirectories(ctx, batch)`, `ListDirectoryChildren(ctx, parentPath, filter)`, `Count(ctx)`, `ListEntries(ctx, filter)`, `ListClientFacets(ctx, filter)`, `ListJobFacets(ctx, filter)`, `ListDirectoryFacets(ctx, filter)`); `Close()` is unchanged. Later tasks don't depend on this package, so this is a leaf change for the rest of the plan.

- [ ] **Step 1: Rewrite `db.go` to open both pools via `sqlitedb.Open`**

```go
// src/storage/catalog/db.go
package catalog

import (
	"fmt"
	"path/filepath"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage/sqlitedb"
)

// readerPoolSize is a small fixed constant, not a config knob: SQLite's
// read concurrency benefit under WAL plateaus quickly, and there's no
// measured read-QPS to size against yet (see
// docs/superpowers/specs/2026-08-08-storage-connection-foundation-design.md).
const readerPoolSize = 4

// openDBs opens catalog's two connections against basePath/catalog.db: a
// single-connection writer that also migrates the schema, and a
// multi-connection read-only pool for the read-heavy query RPCs (see
// store.go). The writer must open first -- WAL is a database-file-level
// setting the writer establishes, and the schema must exist before the
// reader pool touches the file.
func openDBs(basePath string) (writeDB, readDB *gorm.DB, err error) {
	dbPath := filepath.Join(basePath, "catalog.db")

	writeDB, err = sqlitedb.Open(sqlitedb.Options{
		Path:   dbPath,
		Models: []any{&EntryRecord{}, &DirectoryRecord{}},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("open catalog db: %w", err)
	}

	readDB, err = sqlitedb.Open(sqlitedb.Options{Path: dbPath, ReadOnly: true, MaxConns: readerPoolSize})
	if err != nil {
		if sqlDB, dbErr := writeDB.DB(); dbErr == nil {
			sqlDB.Close()
		}
		return nil, nil, fmt.Errorf("open catalog reader pool: %w", err)
	}
	return writeDB, readDB, nil
}
```

- [ ] **Step 2: Update `store.go`'s struct, constructor, and `Close`**

Replace:

```go
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
```

with:

```go
type Store struct {
	writeDB *gorm.DB
	readDB  *gorm.DB
}

func New(basePath string) (*Store, error) {
	writeDB, readDB, err := openDBs(basePath)
	if err != nil {
		return nil, err
	}
	return &Store{writeDB: writeDB, readDB: readDB}, nil
}
```

Replace:

```go
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
```

with:

```go
func (s *Store) Close() error {
	writeSQL, err := s.writeDB.DB()
	if err != nil {
		return err
	}
	if err := writeSQL.Close(); err != nil {
		return err
	}
	readSQL, err := s.readDB.DB()
	if err != nil {
		return err
	}
	return readSQL.Close()
}
```

Add `"context"` to the import block (alongside `"fmt"`, `"strings"`, `"time"`).

- [ ] **Step 3: Thread `ctx` through the two write methods**

```go
func (s *Store) EnsureEntries(ctx context.Context, batch []Entry) error {
```
...its body's `s.db.Clauses(...)` becomes `s.writeDB.WithContext(ctx).Clauses(...)`.

```go
func (s *Store) EnsureDirectories(ctx context.Context, batch []DirectoryAncestor) error {
```
...its body's `s.db.Clauses(...)` becomes `s.writeDB.WithContext(ctx).Clauses(...)`.

- [ ] **Step 4: Thread `ctx` through the six read methods**

For each of `ListDirectoryChildren`, `Count`, `ListEntries`, `ListClientFacets`, `ListJobFacets`, `ListDirectoryFacets`: add `ctx context.Context` as the first parameter, and replace every `s.db` reference in that method's body with `s.readDB.WithContext(ctx)`. `ListDirectoryChildren` has three separate `s.db` references (the directory query, the entry-aggregation query, the grandchildren query) — all three become `s.readDB.WithContext(ctx)`.

Resulting signatures:

```go
func (s *Store) ListDirectoryChildren(ctx context.Context, parentPath string, filter FacetFilter) ([]DirectoryChild, error) {
func (s *Store) Count(ctx context.Context) (int64, error) {
func (s *Store) ListEntries(ctx context.Context, filter ListEntriesFilter) ([]EntryRecord, bool, error) {
func (s *Store) ListClientFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
func (s *Store) ListJobFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
func (s *Store) ListDirectoryFacets(ctx context.Context, filter FacetFilter) ([]Facet, error) {
```

`jobNamesWhere` and `FacetFilter.applyCommon` are unaffected — both take an already-`WithContext`'d `*gorm.DB` as a parameter, not `s.db` directly.

- [ ] **Step 5: Update `store_test.go` call sites**

Run:

```bash
cd src && sed -i -E 's/\bstore\.(EnsureEntries|EnsureDirectories|ListDirectoryChildren|Count|ListEntries|ListClientFacets|ListJobFacets|ListDirectoryFacets)\(/store.\1(t.Context(), /g' storage/catalog/store_test.go
gofmt -w storage/catalog/store_test.go
```

- [ ] **Step 6: Run catalog store tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS. If it fails, inspect the diff `sed` produced (most likely cause: a call spanning multiple lines that the single-line regex didn't match — fix by hand, matching the `t.Context(), ` insertion pattern above).

- [ ] **Step 7: Update `cmd/catalog/server.go` call sites**

In `SyncFileVersions`, replace:

```go
	if err := s.store.EnsureEntries(batch); err != nil {
```
with
```go
	if err := s.store.EnsureEntries(ctx, batch); err != nil {
```

and:

```go
		if err := s.store.EnsureDirectories(directories); err != nil {
```
with
```go
		if err := s.store.EnsureDirectories(ctx, directories); err != nil {
```

In `ListEntries`, replace:

```go
	records, hasMore, err := s.store.ListEntries(catalogstore.ListEntriesFilter{
```
with
```go
	records, hasMore, err := s.store.ListEntries(ctx, catalogstore.ListEntriesFilter{
```

In `ListClientFacets`, replace:

```go
	facets, err := s.store.ListClientFacets(catalogstore.FacetFilter{
```
with
```go
	facets, err := s.store.ListClientFacets(ctx, catalogstore.FacetFilter{
```

In `ListJobFacets`, replace:

```go
	facets, err := s.store.ListJobFacets(catalogstore.FacetFilter{
```
with
```go
	facets, err := s.store.ListJobFacets(ctx, catalogstore.FacetFilter{
```

In `ListDirectoryFacets`, replace:

```go
	facets, err := s.store.ListDirectoryFacets(catalogstore.FacetFilter{
```
with
```go
	facets, err := s.store.ListDirectoryFacets(ctx, catalogstore.FacetFilter{
```

In `ListDirectoryChildren`, replace:

```go
	children, err := s.store.ListDirectoryChildren(req.GetParentPath(), catalogstore.FacetFilter{
```
with
```go
	children, err := s.store.ListDirectoryChildren(ctx, req.GetParentPath(), catalogstore.FacetFilter{
```

- [ ] **Step 8: Update `cmd/catalog/server_test.go` call sites**

Run:

```bash
cd src && sed -i -E 's/\bstore\.(EnsureEntries|EnsureDirectories|ListDirectoryChildren|Count|ListEntries|ListClientFacets|ListJobFacets|ListDirectoryFacets)\(/store.\1(t.Context(), /g' cmd/catalog/server_test.go
gofmt -w cmd/catalog/server_test.go
```

This targets `store.` (the raw `*catalogstore.Store` some tests construct directly), not `srv.` (the gRPC handler, whose methods already take `ctx` as their normal RPC signature and are untouched).

- [ ] **Step 9: Run all catalog tests to verify they pass**

Run: `cd src && go build ./storage/catalog/... ./cmd/catalog/... && go test ./storage/catalog/... ./cmd/catalog/... -v`
Expected: PASS.

- [ ] **Step 10: Run the full build to catch any other caller**

Run: `cd src && go build ./...`
Expected: succeeds. (`catalog.Store` is only constructed and called from `cmd/catalog`, so this should be a no-op check, but it's the cheapest way to be sure.)

- [ ] **Step 11: Commit**

```bash
cd src && git add storage/catalog/db.go storage/catalog/store.go storage/catalog/store_test.go cmd/catalog/server.go cmd/catalog/server_test.go
git commit -m "$(cat <<'EOF'
refactor: split catalog's connection pool and thread context through Store

catalog.Store now opens a single-connection writer plus a 4-connection
read-only pool (WAL supports concurrent readers), so the web catalog
view's queries no longer serialize behind fleet-wide sync writes. Every
Store method now takes ctx and honors caller cancellation/deadlines.
EOF
)"
```

---

### Task 3: Migrate `storage/policyserver` — context propagation

**Files:**
- Delete: `src/storage/policyserver/db.go`
- Modify: `src/storage/policyserver/store.go`
- Modify: `src/storage/policyserver/store_test.go`
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/checkin.go`
- Modify: `src/cmd/policy-server/write.go`
- Modify: `src/cmd/policy-server/checkin_test.go`
- Modify: `src/cmd/policy-server/server_test.go`
- Modify: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: `sqlitedb.Open` from Task 1.
- Produces: `policyserver.Store`'s methods now take `ctx context.Context` first: `RecordCheckin(ctx, policyID, hostname, at)`, `CheckinsForPolicy(ctx, policyID)`, `DeleteOlderThan(ctx, cutoff)`, `DeleteForPolicy(ctx, policyID)`. `Close()` unchanged. The free functions `attachDestination`/`attachCheckins` in `cmd/policy-server/server.go` now take `ctx context.Context` as their first parameter too.

- [ ] **Step 1: Delete `db.go`, inline the open call into `store.go`**

Delete `src/storage/policyserver/db.go`.

In `store.go`, replace:

```go
type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := openDB(varDir)
	if err != nil {
		return nil, fmt.Errorf("open policy-server db: %w", err)
	}
	return &Store{db: db}, nil
}
```

with:

```go
type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := sqlitedb.Open(sqlitedb.Options{
		Path:   filepath.Join(varDir, "policy-server.sqlite"),
		Models: []any{&CheckinRecord{}},
	})
	if err != nil {
		return nil, fmt.Errorf("open policy-server db: %w", err)
	}
	return &Store{db: db}, nil
}
```

Update the import block to:

```go
import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage/sqlitedb"
)
```

- [ ] **Step 2: Thread `ctx` through the four query methods**

```go
func (s *Store) RecordCheckin(ctx context.Context, policyID, hostname string, at time.Time) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
```

```go
func (s *Store) CheckinsForPolicy(ctx context.Context, policyID string) ([]CheckinRecord, error) {
	var out []CheckinRecord
	err := s.db.WithContext(ctx).Where("policy_id = ?", policyID).Order("last_seen_at DESC, hostname").Find(&out).Error
	return out, err
}
```

```go
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Where("last_seen_at < ?", cutoff).Delete(&CheckinRecord{})
	return res.RowsAffected, res.Error
}
```

```go
func (s *Store) DeleteForPolicy(ctx context.Context, policyID string) error {
	return s.db.WithContext(ctx).Where("policy_id = ?", policyID).Delete(&CheckinRecord{}).Error
}
```

`Close()` is unchanged.

- [ ] **Step 3: Update `storage/policyserver/store_test.go` call sites**

Run:

```bash
cd src && sed -i -E 's/\bstore\.(RecordCheckin|CheckinsForPolicy|DeleteOlderThan|DeleteForPolicy)\(/store.\1(t.Context(), /g' storage/policyserver/store_test.go
gofmt -w storage/policyserver/store_test.go
```

- [ ] **Step 4: Run policyserver store tests to verify they pass**

Run: `cd src && go test ./storage/policyserver/... -v`
Expected: PASS.

- [ ] **Step 5: Update `cmd/policy-server/server.go` call sites**

`GetPolicies` already has `ctx`. Replace:

```go
		if err := s.checkins.RecordCheckin(pp.GetId(), hostname, now); err != nil {
```
with
```go
		if err := s.checkins.RecordCheckin(ctx, pp.GetId(), hostname, now); err != nil {
```

`attachDestination` and `attachCheckins` don't currently receive `ctx`. Replace:

```go
func attachDestination(pp *pb.Policy, cache *Cache, checkins *checkinstore.Store, logger *slog.Logger) {
```
with
```go
func attachDestination(ctx context.Context, pp *pb.Policy, cache *Cache, checkins *checkinstore.Store, logger *slog.Logger) {
```
and inside its body, replace:
```go
	records, err := checkins.CheckinsForPolicy(pp.GetStoragePolicyId())
```
with
```go
	records, err := checkins.CheckinsForPolicy(ctx, pp.GetStoragePolicyId())
```

Replace:

```go
func attachCheckins(pp *pb.Policy, store *checkinstore.Store, logger *slog.Logger) {
	records, err := store.CheckinsForPolicy(pp.GetId())
```
with
```go
func attachCheckins(ctx context.Context, pp *pb.Policy, store *checkinstore.Store, logger *slog.Logger) {
	records, err := store.CheckinsForPolicy(ctx, pp.GetId())
```

Update `GetPolicies`'s call site (inside its match loop):

```go
			attachDestination(pp, s.cache, s.checkins, s.logger)
```
with
```go
			attachDestination(ctx, pp, s.cache, s.checkins, s.logger)
```

Update `ListPolicies`'s two call sites (`ListPolicies` already has `ctx`):

```go
		attachDestination(pp, s.cache, s.checkins, s.logger)
		attachCheckins(pp, s.checkins, s.logger)
```
with
```go
		attachDestination(ctx, pp, s.cache, s.checkins, s.logger)
		attachCheckins(ctx, pp, s.checkins, s.logger)
```

- [ ] **Step 6: Update `cmd/policy-server/checkin.go`'s call site**

`runCheckinCleanup` already has `ctx` in scope from its own signature. Replace:

```go
			deleted, err := store.DeleteOlderThan(time.Now().Add(-retention))
```
with
```go
			deleted, err := store.DeleteOlderThan(ctx, time.Now().Add(-retention))
```

- [ ] **Step 7: Update `cmd/policy-server/write.go`'s call site**

`DeletePolicy` already has `ctx` (its own gRPC handler signature). Replace:

```go
	if err := s.checkins.DeleteForPolicy(req.GetId()); err != nil {
```
with
```go
	if err := s.checkins.DeleteForPolicy(ctx, req.GetId()); err != nil {
```

- [ ] **Step 8: Update `cmd/policy-server` test call sites**

Run:

```bash
cd src && sed -i -E 's/\b(checkins|store)\.(RecordCheckin|CheckinsForPolicy|DeleteOlderThan|DeleteForPolicy)\(/\1.\2(t.Context(), /g' cmd/policy-server/checkin_test.go cmd/policy-server/server_test.go cmd/policy-server/write_test.go
gofmt -w cmd/policy-server/checkin_test.go cmd/policy-server/server_test.go cmd/policy-server/write_test.go
```

If `server_test.go` or `write_test.go` call `attachDestination`/`attachCheckins` directly (rather than only indirectly through `GetPolicies`/`ListPolicies`), those calls also need a leading `t.Context()` argument — check with `grep -n "attachDestination(\|attachCheckins(" cmd/policy-server/*_test.go` and fix any direct calls by hand, following the same `ctx, ...` positional pattern as the production call sites in Step 5.

- [ ] **Step 9: Run all policy-server tests to verify they pass**

Run: `cd src && go build ./storage/policyserver/... ./cmd/policy-server/... && go test ./storage/policyserver/... ./cmd/policy-server/... -v`
Expected: PASS.

- [ ] **Step 10: Run the full build**

Run: `cd src && go build ./...`
Expected: succeeds.

- [ ] **Step 11: Commit**

```bash
cd src && git add storage/policyserver/store.go storage/policyserver/store_test.go cmd/policy-server/server.go cmd/policy-server/checkin.go cmd/policy-server/write.go cmd/policy-server/checkin_test.go cmd/policy-server/server_test.go cmd/policy-server/write_test.go
git rm src/storage/policyserver/db.go 2>/dev/null || git add -u storage/policyserver/db.go
git commit -m "$(cat <<'EOF'
refactor: thread context through policyserver.Store

Adopts the shared sqlitedb.Open helper and propagates ctx from every
gRPC handler and background task down to the database call, so
cancellation/deadlines actually take effect.
EOF
)"
```

---

### Task 4: Migrate `storage/clientmanager` — context propagation

**Files:**
- Delete: `src/storage/clientmanager/db.go`
- Modify: `src/storage/clientmanager/store.go`
- Modify: `src/storage/clientmanager/store_test.go`
- Modify: `src/cmd/clientmanager-api/server.go`
- Modify: `src/cmd/clientmanager-api/server_test.go`
- Modify: `src/cmd/clientmanager-admin-api/server.go`
- Modify: `src/cmd/clientmanager-admin-api/server_test.go`
- Modify: `src/cmd/clientmanager/main.go`
- Modify: `src/cmd/clientmanager/add.go`
- Modify: `src/cmd/clientmanager/list.go`
- Modify: `src/cmd/clientmanager/label.go`
- Modify: `src/cmd/clientmanager/san.go`
- Modify: `src/cmd/clientmanager/add_test.go`
- Modify: `src/cmd/clientmanager/list_test.go`
- Modify: `src/cmd/clientmanager/label_test.go`
- Modify: `src/cmd/clientmanager/san_test.go`

**Interfaces:**
- Consumes: `sqlitedb.Open` from Task 1.
- Produces: every `clientmanager.Store` method now takes `ctx context.Context` first: `AddClient(ctx, hostname, sans, addedAt)`, `GetClient(ctx, hostname)`, `LoadClientView(ctx, hostname)`, `ListClients(ctx)`, `SetRevoked(ctx, hostname, revoked, at)`, `UpdateLastSeen(ctx, hostname, at)`, `KV(ctx, hostname, kind)`, `SetKV(ctx, hostname, kind, key, value)`, `UnsetKV(ctx, hostname, kind, key)`, `AddSAN(ctx, hostname, alias)`, `RemoveSAN(ctx, hostname, alias)`. `Close()` unchanged.

- [ ] **Step 1: Delete `db.go`, inline the open call into `store.go`**

Delete `src/storage/clientmanager/db.go`.

In `store.go`, replace:

```go
type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := openDB(varDir)
	if err != nil {
		return nil, fmt.Errorf("open client-manager db: %w", err)
	}
	return &Store{db: db}, nil
}
```

with:

```go
type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := sqlitedb.Open(sqlitedb.Options{
		Path:   filepath.Join(varDir, "clientmanager.sqlite"),
		Models: []any{&ClientRecord{}, &ClientKVRecord{}},
	})
	if err != nil {
		return nil, fmt.Errorf("open client-manager db: %w", err)
	}
	return &Store{db: db}, nil
}
```

Update the import block to add `"context"` and `"path/filepath"`, and add the `sqlitedb` import:

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage/sqlitedb"
)
```

- [ ] **Step 2: Thread `ctx` through the methods with no internal cross-calls**

```go
func (s *Store) AddClient(ctx context.Context, hostname string, sans []string, addedAt time.Time) error {
	sansJSON, err := json.Marshal(sans)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&ClientRecord{}).Where("hostname = ?", hostname).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrClientExists
		}
		return tx.Create(&ClientRecord{Hostname: hostname, SANs: string(sansJSON), AddedAt: addedAt}).Error
	})
}

func (s *Store) GetClient(ctx context.Context, hostname string) (*ClientRecord, error) {
	var rec ClientRecord
	err := s.db.WithContext(ctx).First(&rec, "hostname = ?", hostname).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrClientNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (s *Store) ListClients(ctx context.Context) ([]ClientRecord, error) {
	var recs []ClientRecord
	err := s.db.WithContext(ctx).Order("hostname").Find(&recs).Error
	return recs, err
}

func (s *Store) SetRevoked(ctx context.Context, hostname string, revoked bool, at time.Time) error {
	updates := map[string]any{"revoked": revoked}
	if revoked {
		updates["revoked_at"] = at
	} else {
		updates["revoked_at"] = nil
	}
	res := s.db.WithContext(ctx).Model(&ClientRecord{}).Where("hostname = ?", hostname).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrClientNotFound
	}
	return nil
}

func (s *Store) UpdateLastSeen(ctx context.Context, hostname string, at time.Time) error {
	res := s.db.WithContext(ctx).Model(&ClientRecord{}).Where("hostname = ?", hostname).Update("last_seen_at", at)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrClientNotFound
	}
	return nil
}

func (s *Store) KV(ctx context.Context, hostname string, kind KVKind) ([]ClientKVRecord, error) {
	var recs []ClientKVRecord
	err := s.db.WithContext(ctx).Where("hostname = ? AND kind = ?", hostname, kind).Order("key").Find(&recs).Error
	return recs, err
}
```

- [ ] **Step 3: Thread `ctx` through the methods that call other `Store` methods**

```go
func (s *Store) LoadClientView(ctx context.Context, hostname string) (*ClientView, error) {
	rec, err := s.GetClient(ctx, hostname)
	if err != nil {
		return nil, err
	}

	view := &ClientView{
		Hostname:   rec.Hostname,
		Revoked:    rec.Revoked,
		RevokedAt:  rec.RevokedAt,
		LastSeenAt: rec.LastSeenAt,
		SANs:       rec.SANsList(),
	}

	descs, err := s.KV(ctx, hostname, KindDescription)
	if err != nil {
		return nil, err
	}
	view.Descriptions = make(map[string]string, len(descs))
	for _, d := range descs {
		view.Descriptions[d.Key] = d.Value
	}

	attrs, err := s.KV(ctx, hostname, KindAttribute)
	if err != nil {
		return nil, err
	}
	view.Attributes = make(map[string]string, len(attrs))
	for _, a := range attrs {
		view.Attributes[a.Key] = a.Value
	}

	return view, nil
}

func (s *Store) SetKV(ctx context.Context, hostname string, kind KVKind, key, value string) error {
	if _, err := s.GetClient(ctx, hostname); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}, {Name: "kind"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&ClientKVRecord{Hostname: hostname, Kind: kind, Key: key, Value: value}).Error
}

func (s *Store) UnsetKV(ctx context.Context, hostname string, kind KVKind, key string) error {
	if _, err := s.GetClient(ctx, hostname); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Delete(&ClientKVRecord{}, "hostname = ? AND kind = ? AND key = ?", hostname, kind, key).Error
}

func (s *Store) AddSAN(ctx context.Context, hostname, alias string) error {
	rec, err := s.GetClient(ctx, hostname)
	if err != nil {
		return err
	}
	sans := rec.SANsList()
	for _, existing := range sans {
		if existing == alias {
			return nil
		}
	}
	return s.setSANs(ctx, hostname, append(sans, alias))
}

func (s *Store) RemoveSAN(ctx context.Context, hostname, alias string) error {
	rec, err := s.GetClient(ctx, hostname)
	if err != nil {
		return err
	}
	sans := rec.SANsList()
	filtered := make([]string, 0, len(sans))
	for _, existing := range sans {
		if existing != alias {
			filtered = append(filtered, existing)
		}
	}
	return s.setSANs(ctx, hostname, filtered)
}

func (s *Store) setSANs(ctx context.Context, hostname string, sans []string) error {
	sansJSON, err := json.Marshal(sans)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	return s.db.WithContext(ctx).Model(&ClientRecord{}).Where("hostname = ?", hostname).Update("sa_ns", string(sansJSON)).Error
}
```

`Close()` is unchanged.

- [ ] **Step 4: Update `storage/clientmanager/store_test.go` call sites**

Run:

```bash
cd src && sed -i -E 's/\bstore\.(AddClient|GetClient|LoadClientView|ListClients|SetRevoked|UpdateLastSeen|KV|SetKV|UnsetKV|AddSAN|RemoveSAN)\(/store.\1(t.Context(), /g' storage/clientmanager/store_test.go
gofmt -w storage/clientmanager/store_test.go
```

- [ ] **Step 5: Run clientmanager store tests to verify they pass**

Run: `cd src && go test ./storage/clientmanager/... -v`
Expected: PASS.

- [ ] **Step 6: Update `cmd/clientmanager-api/server.go` call sites**

`ListClients` and `GetClient` already have `ctx`. Replace:

```go
func (s *clientManagerAPIServer) ListClients(ctx context.Context, _ *pb.ListClientsRequest) (*pb.ListClientsResponse, error) {
	recs, err := s.store.ListClients()
	if err != nil {
		s.logger.Error("ListClients: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list clients: %v", err)
	}

	clients := make([]*pb.Client, len(recs))
	for i, rec := range recs {
		view, err := s.store.LoadClientView(rec.Hostname)
```
with
```go
func (s *clientManagerAPIServer) ListClients(ctx context.Context, _ *pb.ListClientsRequest) (*pb.ListClientsResponse, error) {
	recs, err := s.store.ListClients(ctx)
	if err != nil {
		s.logger.Error("ListClients: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list clients: %v", err)
	}

	clients := make([]*pb.Client, len(recs))
	for i, rec := range recs {
		view, err := s.store.LoadClientView(ctx, rec.Hostname)
```

Replace:

```go
func (s *clientManagerAPIServer) GetClient(ctx context.Context, req *pb.GetClientRequest) (*pb.Client, error) {
	view, err := s.store.LoadClientView(req.GetHostname())
```
with
```go
func (s *clientManagerAPIServer) GetClient(ctx context.Context, req *pb.GetClientRequest) (*pb.Client, error) {
	view, err := s.store.LoadClientView(ctx, req.GetHostname())
```

- [ ] **Step 7: Update `cmd/clientmanager-admin-api/server.go` call sites**

`AddClient` already has `ctx`. Replace:

```go
	if _, err := s.store.GetClient(hostname); err == nil {
```
with
```go
	if _, err := s.store.GetClient(ctx, hostname); err == nil {
```
and
```go
	if err := s.store.AddClient(hostname, req.GetSans(), time.Now()); err != nil {
```
with
```go
	if err := s.store.AddClient(ctx, hostname, req.GetSans(), time.Now()); err != nil {
```

`ReEnrollClient` already has `ctx`. Replace:

```go
	rec, err := s.store.GetClient(hostname)
```
with
```go
	rec, err := s.store.GetClient(ctx, hostname)
```
(inside `ReEnrollClient`, not the identical-looking line inside `AddClient` already handled above).

`setRevoked`, `updateKV`, and `loadClient` are unexported helpers with no `ctx` today; their callers (`RevokeClient`, `UnrevokeClient`, `UpdateDescription`, `UpdateAttributes`, `UpdateSANs`) already receive one. Replace:

```go
func (s *clientManagerAdminServer) RevokeClient(ctx context.Context, req *pb.RevokeClientRequest) (*pb.Client, error) {
	return s.setRevoked(req.GetHostname(), true)
}

func (s *clientManagerAdminServer) UnrevokeClient(ctx context.Context, req *pb.UnrevokeClientRequest) (*pb.Client, error) {
	return s.setRevoked(req.GetHostname(), false)
}

func (s *clientManagerAdminServer) setRevoked(hostname string, revoked bool) (*pb.Client, error) {
	if err := s.store.SetRevoked(hostname, revoked, time.Now()); err != nil {
		if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
			return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
		}
		s.logger.Error("setRevoked: update failed", "hostname", hostname, "revoked", revoked, "error", err)
		return nil, status.Errorf(codes.Internal, "update revoked: %v", err)
	}
	return s.loadClient(hostname)
}
```
with
```go
func (s *clientManagerAdminServer) RevokeClient(ctx context.Context, req *pb.RevokeClientRequest) (*pb.Client, error) {
	return s.setRevoked(ctx, req.GetHostname(), true)
}

func (s *clientManagerAdminServer) UnrevokeClient(ctx context.Context, req *pb.UnrevokeClientRequest) (*pb.Client, error) {
	return s.setRevoked(ctx, req.GetHostname(), false)
}

func (s *clientManagerAdminServer) setRevoked(ctx context.Context, hostname string, revoked bool) (*pb.Client, error) {
	if err := s.store.SetRevoked(ctx, hostname, revoked, time.Now()); err != nil {
		if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
			return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
		}
		s.logger.Error("setRevoked: update failed", "hostname", hostname, "revoked", revoked, "error", err)
		return nil, status.Errorf(codes.Internal, "update revoked: %v", err)
	}
	return s.loadClient(ctx, hostname)
}
```

Replace:

```go
func (s *clientManagerAdminServer) UpdateDescription(ctx context.Context, req *pb.UpdateClientKVRequest) (*pb.Client, error) {
	return s.updateKV(req, clientmanagerstore.KindDescription)
}

func (s *clientManagerAdminServer) UpdateAttributes(ctx context.Context, req *pb.UpdateClientKVRequest) (*pb.Client, error) {
	return s.updateKV(req, clientmanagerstore.KindAttribute)
}

func (s *clientManagerAdminServer) updateKV(req *pb.UpdateClientKVRequest, kind clientmanagerstore.KVKind) (*pb.Client, error) {
	hostname := req.GetHostname()
	for key, value := range req.GetSet() {
		if err := s.store.SetKV(hostname, kind, key, value); err != nil {
```
with
```go
func (s *clientManagerAdminServer) UpdateDescription(ctx context.Context, req *pb.UpdateClientKVRequest) (*pb.Client, error) {
	return s.updateKV(ctx, req, clientmanagerstore.KindDescription)
}

func (s *clientManagerAdminServer) UpdateAttributes(ctx context.Context, req *pb.UpdateClientKVRequest) (*pb.Client, error) {
	return s.updateKV(ctx, req, clientmanagerstore.KindAttribute)
}

func (s *clientManagerAdminServer) updateKV(ctx context.Context, req *pb.UpdateClientKVRequest, kind clientmanagerstore.KVKind) (*pb.Client, error) {
	hostname := req.GetHostname()
	for key, value := range req.GetSet() {
		if err := s.store.SetKV(ctx, hostname, kind, key, value); err != nil {
```

and further down in the same method:

```go
	for _, key := range req.GetUnset() {
		if err := s.store.UnsetKV(hostname, kind, key); err != nil {
```
with
```go
	for _, key := range req.GetUnset() {
		if err := s.store.UnsetKV(ctx, hostname, kind, key); err != nil {
```

and its final line:

```go
	return s.loadClient(hostname)
}

func (s *clientManagerAdminServer) UpdateSANs(ctx context.Context, req *pb.UpdateClientSANsRequest) (*pb.Client, error) {
	hostname := req.GetHostname()
	for _, alias := range req.GetAdd() {
		if err := s.store.AddSAN(hostname, alias); err != nil {
```
with
```go
	return s.loadClient(ctx, hostname)
}

func (s *clientManagerAdminServer) UpdateSANs(ctx context.Context, req *pb.UpdateClientSANsRequest) (*pb.Client, error) {
	hostname := req.GetHostname()
	for _, alias := range req.GetAdd() {
		if err := s.store.AddSAN(ctx, hostname, alias); err != nil {
```

and the SANs removal loop plus the method's own final line:

```go
	for _, alias := range req.GetRemove() {
		if err := s.store.RemoveSAN(hostname, alias); err != nil {
```
```go
	return s.loadClient(hostname)
}

// loadClient loads hostname's full record for a response, used by every
// RPC below AddClient/ReEnrollClient that returns the updated Client.
func (s *clientManagerAdminServer) loadClient(hostname string) (*pb.Client, error) {
	view, err := s.store.LoadClientView(hostname)
```
with
```go
	for _, alias := range req.GetRemove() {
		if err := s.store.RemoveSAN(ctx, hostname, alias); err != nil {
```
```go
	return s.loadClient(ctx, hostname)
}

// loadClient loads hostname's full record for a response, used by every
// RPC below AddClient/ReEnrollClient that returns the updated Client.
func (s *clientManagerAdminServer) loadClient(ctx context.Context, hostname string) (*pb.Client, error) {
	view, err := s.store.LoadClientView(ctx, hostname)
```

- [ ] **Step 8: Thread `ctx` through the `clientmanager` CLI**

Unlike the gRPC servers, the CLI's `run` dispatcher has no `context.Context` today — it's a synchronous, single-shot invocation with no real cancellation source (no signal handling is wired into it). Use `context.Background()` at the one place a `ctx` needs to come from, and thread it down as a plain parameter — this is honest about there being no actual cancellation semantics here, unlike the gRPC/background-task call sites above.

In `cmd/clientmanager/main.go`, replace:

```go
import (
	"fmt"
	"io"
	"os"

	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/config"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)
```
with
```go
import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/config"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)
```

Replace:

```go
	if err := run(mintOpts, store, args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// run dispatches on args.Action. Broken out from main so tests can drive
// it directly against a temp-dir store without touching os.Exit.
func run(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	switch args.Action {
	case "add":
		return runAdd(mintOpts, store, args, certmint.Mint, out)
	case "re-enroll":
		return runReEnroll(mintOpts, store, args, certmint.Mint, out)
	case "list":
		return runList(store, out)
	case "show":
		return runShow(store, args, out)
	case "revoke":
		return runRevoke(store, args)
	case "unrevoke":
		return runUnrevoke(store, args)
	case "description-set":
		return runKVSet(store, clientmanagerstore.KindDescription, args)
	case "description-unset":
		return runKVUnset(store, clientmanagerstore.KindDescription, args)
	case "attribute-set":
		return runKVSet(store, clientmanagerstore.KindAttribute, args)
	case "attribute-unset":
		return runKVUnset(store, clientmanagerstore.KindAttribute, args)
	case "san-add":
		return runSanAdd(store, args)
	case "san-remove":
		return runSanRemove(store, args)
	default:
		return fmt.Errorf("unknown action %q", args.Action)
	}
}
```
with
```go
	if err := run(context.Background(), mintOpts, store, args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// run dispatches on args.Action. Broken out from main so tests can drive
// it directly against a temp-dir store without touching os.Exit. ctx has
// no real cancellation source here -- this is a synchronous, one-shot CLI
// invocation, not a long-running server -- it exists only so the CLI's
// store calls share the same Store method signatures the gRPC servers use.
func run(ctx context.Context, mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	switch args.Action {
	case "add":
		return runAdd(ctx, mintOpts, store, args, certmint.Mint, out)
	case "re-enroll":
		return runReEnroll(ctx, mintOpts, store, args, certmint.Mint, out)
	case "list":
		return runList(ctx, store, out)
	case "show":
		return runShow(ctx, store, args, out)
	case "revoke":
		return runRevoke(ctx, store, args)
	case "unrevoke":
		return runUnrevoke(ctx, store, args)
	case "description-set":
		return runKVSet(ctx, store, clientmanagerstore.KindDescription, args)
	case "description-unset":
		return runKVUnset(ctx, store, clientmanagerstore.KindDescription, args)
	case "attribute-set":
		return runKVSet(ctx, store, clientmanagerstore.KindAttribute, args)
	case "attribute-unset":
		return runKVUnset(ctx, store, clientmanagerstore.KindAttribute, args)
	case "san-add":
		return runSanAdd(ctx, store, args)
	case "san-remove":
		return runSanRemove(ctx, store, args)
	default:
		return fmt.Errorf("unknown action %q", args.Action)
	}
}
```

In `cmd/clientmanager/add.go`, add `"context"` to imports and replace:

```go
func runAdd(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	if _, err := store.GetClient(args.Hostname); err == nil {
		return fmt.Errorf("client %q already exists; use re-enroll or description/attribute set instead", args.Hostname)
	} else if !errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return fmt.Errorf("check existing client: %w", err)
	}

	token, err := mint(args.Hostname, args.SANs, mintOpts)
	if err != nil {
		return fmt.Errorf("add %s: %w", args.Hostname, err)
	}

	if err := store.AddClient(args.Hostname, args.SANs, time.Now()); err != nil {
		return fmt.Errorf("record client %s: %w", args.Hostname, err)
	}

	fmt.Fprintln(out, token)
	return nil
}

func runReEnroll(mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	client, err := store.GetClient(args.Hostname)
```
with
```go
func runAdd(ctx context.Context, mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	if _, err := store.GetClient(ctx, args.Hostname); err == nil {
		return fmt.Errorf("client %q already exists; use re-enroll or description/attribute set instead", args.Hostname)
	} else if !errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return fmt.Errorf("check existing client: %w", err)
	}

	token, err := mint(args.Hostname, args.SANs, mintOpts)
	if err != nil {
		return fmt.Errorf("add %s: %w", args.Hostname, err)
	}

	if err := store.AddClient(ctx, args.Hostname, args.SANs, time.Now()); err != nil {
		return fmt.Errorf("record client %s: %w", args.Hostname, err)
	}

	fmt.Fprintln(out, token)
	return nil
}

func runReEnroll(ctx context.Context, mintOpts certmint.Options, store *clientmanagerstore.Store, args *Arguments, mint minter, out io.Writer) error {
	client, err := store.GetClient(ctx, args.Hostname)
```

In `cmd/clientmanager/list.go`, add `"context"` to imports and replace:

```go
func runList(store *clientmanagerstore.Store, out io.Writer) error {
	clients, err := store.ListClients()
```
with
```go
func runList(ctx context.Context, store *clientmanagerstore.Store, out io.Writer) error {
	clients, err := store.ListClients(ctx)
```

and:

```go
func runShow(store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	client, err := store.GetClient(args.Hostname)
```
with
```go
func runShow(ctx context.Context, store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	client, err := store.GetClient(ctx, args.Hostname)
```

and further down in the same function:

```go
	descs, err := store.KV(args.Hostname, clientmanagerstore.KindDescription)
```
```go
	attrs, err := store.KV(args.Hostname, clientmanagerstore.KindAttribute)
```
with
```go
	descs, err := store.KV(ctx, args.Hostname, clientmanagerstore.KindDescription)
```
```go
	attrs, err := store.KV(ctx, args.Hostname, clientmanagerstore.KindAttribute)
```

and:

```go
func runRevoke(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.SetRevoked(args.Hostname, true, time.Now()); err != nil {
		return fmt.Errorf("revoke %s: %w", args.Hostname, err)
	}
	return nil
}

func runUnrevoke(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.SetRevoked(args.Hostname, false, time.Now()); err != nil {
		return fmt.Errorf("unrevoke %s: %w", args.Hostname, err)
	}
	return nil
}
```
with
```go
func runRevoke(ctx context.Context, store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.SetRevoked(ctx, args.Hostname, true, time.Now()); err != nil {
		return fmt.Errorf("revoke %s: %w", args.Hostname, err)
	}
	return nil
}

func runUnrevoke(ctx context.Context, store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.SetRevoked(ctx, args.Hostname, false, time.Now()); err != nil {
		return fmt.Errorf("unrevoke %s: %w", args.Hostname, err)
	}
	return nil
}
```

In `cmd/clientmanager/label.go`, add `"context"` to imports and replace:

```go
func runKVSet(store *clientmanagerstore.Store, kind clientmanagerstore.KVKind, args *Arguments) error {
	for _, pair := range args.KVPairs {
		key, value, err := parseKV(pair)
		if err != nil {
			return err
		}
		if err := store.SetKV(args.Hostname, kind, key, value); err != nil {
			return fmt.Errorf("set %s %s on %s: %w", kind, key, args.Hostname, err)
		}
	}
	return nil
}

func runKVUnset(store *clientmanagerstore.Store, kind clientmanagerstore.KVKind, args *Arguments) error {
	if err := store.UnsetKV(args.Hostname, kind, args.Key); err != nil {
		return fmt.Errorf("unset %s %s on %s: %w", kind, args.Key, args.Hostname, err)
	}
	return nil
}
```
with
```go
func runKVSet(ctx context.Context, store *clientmanagerstore.Store, kind clientmanagerstore.KVKind, args *Arguments) error {
	for _, pair := range args.KVPairs {
		key, value, err := parseKV(pair)
		if err != nil {
			return err
		}
		if err := store.SetKV(ctx, args.Hostname, kind, key, value); err != nil {
			return fmt.Errorf("set %s %s on %s: %w", kind, key, args.Hostname, err)
		}
	}
	return nil
}

func runKVUnset(ctx context.Context, store *clientmanagerstore.Store, kind clientmanagerstore.KVKind, args *Arguments) error {
	if err := store.UnsetKV(ctx, args.Hostname, kind, args.Key); err != nil {
		return fmt.Errorf("unset %s %s on %s: %w", kind, args.Key, args.Hostname, err)
	}
	return nil
}
```

In `cmd/clientmanager/san.go`, add `"context"` to imports and replace:

```go
func runSanAdd(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.AddSAN(args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("add san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}

func runSanRemove(store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.RemoveSAN(args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("remove san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}
```
with
```go
func runSanAdd(ctx context.Context, store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.AddSAN(ctx, args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("add san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}

func runSanRemove(ctx context.Context, store *clientmanagerstore.Store, args *Arguments) error {
	if err := store.RemoveSAN(ctx, args.Hostname, args.SanAlias); err != nil {
		return fmt.Errorf("remove san %s on %s: %w", args.SanAlias, args.Hostname, err)
	}
	return nil
}
```

- [ ] **Step 9: Update every affected test file's call sites**

Run:

```bash
cd src && sed -i -E 's/\bstore\.(AddClient|GetClient|LoadClientView|ListClients|SetRevoked|UpdateLastSeen|KV|SetKV|UnsetKV|AddSAN|RemoveSAN)\(/store.\1(t.Context(), /g' \
  cmd/clientmanager-api/server_test.go \
  cmd/clientmanager-admin-api/server_test.go \
  cmd/clientmanager/add_test.go \
  cmd/clientmanager/list_test.go \
  cmd/clientmanager/label_test.go \
  cmd/clientmanager/san_test.go
gofmt -w cmd/clientmanager-api/server_test.go cmd/clientmanager-admin-api/server_test.go cmd/clientmanager/add_test.go cmd/clientmanager/list_test.go cmd/clientmanager/label_test.go cmd/clientmanager/san_test.go
```

This targets `store.` calls only, not `srv.` (the gRPC handler methods, unaffected). It does not add `ctx` to the `clientmanager` CLI test files' calls into `run`/`runAdd`/`runList`/etc. themselves (those functions gained a new `ctx` *parameter*, not a `Store`-method call) — check each of `add_test.go`, `list_test.go`, `label_test.go`, `san_test.go` for direct calls to `run*` functions with `grep -n "runAdd(\|runReEnroll(\|runList(\|runShow(\|runRevoke(\|runUnrevoke(\|runKVSet(\|runKVUnset(\|runSanAdd(\|runSanRemove(" cmd/clientmanager/*_test.go` and add `t.Context()` (or `context.Background()`, matching `main.go`'s choice) as the new first argument to each, by hand, following the signature changes from Step 8.

- [ ] **Step 10: Run all clientmanager tests to verify they pass**

Run: `cd src && go build ./storage/clientmanager/... ./cmd/clientmanager/... ./cmd/clientmanager-api/... ./cmd/clientmanager-admin-api/... && go test ./storage/clientmanager/... ./cmd/clientmanager/... ./cmd/clientmanager-api/... ./cmd/clientmanager-admin-api/... -v`
Expected: PASS.

- [ ] **Step 11: Run the full build**

Run: `cd src && go build ./...`
Expected: succeeds.

- [ ] **Step 12: Commit**

```bash
cd src && git add storage/clientmanager/store.go storage/clientmanager/store_test.go \
  cmd/clientmanager-api/server.go cmd/clientmanager-api/server_test.go \
  cmd/clientmanager-admin-api/server.go cmd/clientmanager-admin-api/server_test.go \
  cmd/clientmanager/main.go cmd/clientmanager/add.go cmd/clientmanager/list.go cmd/clientmanager/label.go cmd/clientmanager/san.go \
  cmd/clientmanager/add_test.go cmd/clientmanager/list_test.go cmd/clientmanager/label_test.go cmd/clientmanager/san_test.go
git rm src/storage/clientmanager/db.go 2>/dev/null || git add -u storage/clientmanager/db.go
git commit -m "$(cat <<'EOF'
refactor: thread context through clientmanager.Store

Adopts the shared sqlitedb.Open helper and propagates ctx from every
gRPC handler and CLI command down to the database call. The CLI has no
real cancellation source (single-shot invocation), so it threads
context.Background() -- this keeps its call sites on the same Store
method signatures as the gRPC servers rather than special-casing it.
EOF
)"
```

---

### Task 5: Migrate `filesystem.ReplicaReader` and `catalogsync`

**Files:**
- Modify: `src/storage/filesystem/replicareader.go`
- Modify: `src/storage/filesystem/replicareader_test.go`
- Modify: `src/cmd/catalogsync/sync.go`
- Modify: `src/cmd/catalogsync/sync_test.go`

**Interfaces:**
- Consumes: `sqlitedb.Open` from Task 1.
- Produces: `filesystem.ReplicaReader.FileVersionsSince(ctx context.Context, cursor int64, limit int) ([]FileVersionRecord, error)`; the `catalogsync` `reader` interface matches this signature.

- [ ] **Step 1: Rewrite `OpenReplicaReader` to use `sqlitedb.Open`**

Replace:

```go
package filesystem

import (
	"database/sql"
	"fmt"
	"path/filepath"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	_ "modernc.org/sqlite"
)

// ReplicaReader is a strictly read-only accessor for an existing bwfs
// store's metadata.db, for use by a separate process (catalogsync) that
// must never be able to write to bwfs's data, even by accident. It opens
// the database via SQLite's `mode=ro` URI flag — enforced by the driver —
// unlike Store's NewReadOnly, which still opens a normal read-write
// connection (needed elsewhere for MarkChunkCorrupted).
type ReplicaReader struct {
	db *gorm.DB
}

// OpenReplicaReader opens basePath/metadata.db read-only. The database must
// already exist and have its schema migrated (by a real bwfs Store) — a
// read-only connection cannot create it.
func OpenReplicaReader(basePath string) (*ReplicaReader, error) {
	dbPath := fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", filepath.Join(basePath, "metadata.db"))

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open read-only: %w", err)
	}
	return &ReplicaReader{db: db}, nil
}

// FileVersionsSince returns up to limit file_versions rows with seq greater
// than cursor, ordered ascending by seq — catalogsync's replication cursor.
func (r *ReplicaReader) FileVersionsSince(cursor int64, limit int) ([]FileVersionRecord, error) {
	var records []FileVersionRecord
	err := r.db.
		Where("seq > ?", cursor).
		Order("seq ASC").
		Limit(limit).
		Find(&records).Error
	return records, err
}
```

with:

```go
package filesystem

import (
	"context"
	"fmt"
	"path/filepath"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage/sqlitedb"
)

// ReplicaReader is a strictly read-only accessor for an existing bwfs
// store's metadata.db, for use by a separate process (catalogsync) that
// must never be able to write to bwfs's data, even by accident. It opens
// the database via SQLite's `mode=ro` URI flag — enforced by the driver —
// unlike Store's NewReadOnly, which still opens a normal read-write
// connection (needed elsewhere for MarkChunkCorrupted).
type ReplicaReader struct {
	db *gorm.DB
}

// OpenReplicaReader opens basePath/metadata.db read-only. The database must
// already exist and have its schema migrated (by a real bwfs Store) — a
// read-only connection cannot create it.
func OpenReplicaReader(basePath string) (*ReplicaReader, error) {
	db, err := sqlitedb.Open(sqlitedb.Options{
		Path:     filepath.Join(basePath, "metadata.db"),
		ReadOnly: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open sqlite read-only: %w", err)
	}
	return &ReplicaReader{db: db}, nil
}

// FileVersionsSince returns up to limit file_versions rows with seq greater
// than cursor, ordered ascending by seq — catalogsync's replication cursor.
func (r *ReplicaReader) FileVersionsSince(ctx context.Context, cursor int64, limit int) ([]FileVersionRecord, error) {
	var records []FileVersionRecord
	err := r.db.WithContext(ctx).
		Where("seq > ?", cursor).
		Order("seq ASC").
		Limit(limit).
		Find(&records).Error
	return records, err
}
```

`Close()` is unchanged. Note this also means `ReplicaReader` now goes through the WAL pragma path consistently with `bwfs`'s own writer connection (the old hand-rolled open never set it explicitly on the read-only side, relying on it already being WAL from the writer) — `sqlitedb.Open` with `ReadOnly: true` still correctly skips re-setting the pragma, matching that same assumption.

- [ ] **Step 2: Update `storage/filesystem/replicareader_test.go` call sites**

Run:

```bash
cd src && sed -i -E 's/\breader\.FileVersionsSince\(/reader.FileVersionsSince(t.Context(), /g' storage/filesystem/replicareader_test.go
gofmt -w storage/filesystem/replicareader_test.go
```

- [ ] **Step 3: Run filesystem tests to verify they pass**

Run: `cd src && go test ./storage/filesystem/... -run TestReplicaReader -v`
Expected: PASS. (Scoped to `ReplicaReader` tests specifically — this task must not touch or need to fix anything in `filesystem.Store`'s own, out-of-scope, test suite.)

- [ ] **Step 4: Update the `reader` interface and its call site in `cmd/catalogsync/sync.go`**

Replace:

```go
// reader is the subset of *filesystem.ReplicaReader that run depends on.
type reader interface {
	FileVersionsSince(cursor int64, limit int) ([]wfs.FileVersionRecord, error)
}
```
with
```go
// reader is the subset of *filesystem.ReplicaReader that run depends on.
type reader interface {
	FileVersionsSince(ctx context.Context, cursor int64, limit int) ([]wfs.FileVersionRecord, error)
}
```

Replace:

```go
		batch, err := rd.FileVersionsSince(cursor, cfg.BatchSize)
```
with
```go
		batch, err := rd.FileVersionsSince(ctx, cursor, cfg.BatchSize)
```

`ctx` is already in scope (it's `run`'s own first parameter, already used a few lines above for `ctx.Err()` and `sleepOrDone(ctx, ...)`), so no import changes are needed here.

- [ ] **Step 5: Update `cmd/catalogsync/sync_test.go`'s fake reader**

Replace:

```go
func (f *fakeReader) FileVersionsSince(cursor int64, limit int) ([]wfs.FileVersionRecord, error) {
```
with
```go
func (f *fakeReader) FileVersionsSince(ctx context.Context, cursor int64, limit int) ([]wfs.FileVersionRecord, error) {
```

If `sync_test.go` doesn't already import `"context"`, add it. Check any direct test calls to `FileVersionsSince` (as opposed to calls that go through `run`, which already supplies `ctx`) with `grep -n "FileVersionsSince(" cmd/catalogsync/sync_test.go` and add a leading `t.Context()` argument to any found, matching the interface change above.

- [ ] **Step 6: Run all catalogsync tests to verify they pass**

Run: `cd src && go build ./storage/filesystem/... ./cmd/catalogsync/... && go test ./storage/filesystem/... ./cmd/catalogsync/... -v`
Expected: PASS.

- [ ] **Step 7: Run the full build and full test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: everything builds and passes — this is the last task in the plan, so this is the final confirmation that all five packages' changes compose correctly.

- [ ] **Step 8: Commit**

```bash
cd src && git add storage/filesystem/replicareader.go storage/filesystem/replicareader_test.go cmd/catalogsync/sync.go cmd/catalogsync/sync_test.go
git commit -m "$(cat <<'EOF'
refactor: thread context through filesystem.ReplicaReader

Completes the storage-connection-foundation work: ReplicaReader (used
by catalogsync) now adopts the shared sqlitedb.Open helper and honors
caller cancellation via ctx, matching catalog/policyserver/clientmanager.
EOF
)"
```
