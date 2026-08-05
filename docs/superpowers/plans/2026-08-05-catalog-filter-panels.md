# Catalog Filter Panels Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add date-range and job/policy filtering to the backup catalog, alongside the existing client (source host) and path filters, as a three-row faceted-search filter bar with full cross-filtering between clients and policies.

**Architecture:** Two new `CatalogService` gRPC RPCs (`ListClientFacets`/`ListJobFacets`) return aggregate `{name, count, last_seen}` rows grouped by source host or by the policy name embedded in `job_id`, each excluding its own filter dimension so a facet list never narrows itself. `ListEntries` gains additive date-range and multi-value filter fields. The Vue catalog view becomes a three-row chip bar (date / clients+jobs / path) where clicking a chip swaps an expanding panel below it — a date-range picker or a searchable, checkbox-selectable facet table — reusing `@vuepic/vue-datepicker` (new dependency) and the already-installed `vue-good-table-next`'s built-in checkbox support.

**Tech Stack:** Go (GORM + `modernc.org/sqlite`), gRPC/protobuf, Vue 3 Composition API (`<script setup>`, `defineModel`), Pinia, `vue-good-table-next`, `@vuepic/vue-datepicker` (new), Vitest, `testify`.

## Global Constraints

- Design source of truth: `docs/superpowers/specs/2026-08-05-catalog-filter-panels-design.md`.
- TDD throughout: write the failing test before the implementation for every behavior change, per `superpowers:test-driven-development`.
- No backward-compatibility shims for the store/component API changes in this repo's own web frontend — `CatalogView.vue` is the only consumer of `useCatalogStore` (verified: no other file references it), so its internal shape can change freely; only the wire-level proto additions must stay additive (old singular `source_host`/`store_host`/`pattern` fields keep working unchanged).
- Proto changes require running `make proto` from the repo root and committing the regenerated `src/api/catalog.pb.go`/`catalog_grpc.pb.go` alongside the `.proto` edit (per `.claude/CLAUDE.md`'s gRPC Protocol Changes rule).
- Per `.claude/CLAUDE.md`: update `docs/protocols/catalog-sync.md`, `docs/components/catalog.md`, and `docs/api/rest-v1.md` before this feature is considered complete (Task 12), and add a `CHANGELOG.md` entry before merging to `main`.
- Every task ends with a passing test run and a commit.

---

## Task 1: Extend `catalog.proto` with facet RPCs and new filter fields

**Files:**
- Modify: `src/api/catalog.proto:27-56`
- Generated (via `make proto`, do not hand-edit): `src/api/catalog.pb.go`, `src/api/catalog_grpc.pb.go`

**Interfaces:**
- Produces: `pb.ListEntriesRequest` gains `ReceivedAfter`, `ReceivedBefore int64`, `SourceHosts`, `JobNames []string` (fields 6-9); new `pb.Facet{Name string, Count int64, LastSeen int64}`, `pb.ListFacetsRequest{ReceivedAfter, ReceivedBefore int64, Pattern string, SourceHosts, JobNames []string}`, `pb.ListFacetsResponse{Facets []*pb.Facet}`; new `pb.CatalogServiceClient`/`CatalogServiceServer` methods `ListClientFacets`, `ListJobFacets`. Consumed by Tasks 3-6.

- [ ] **Step 1: Edit the proto file**

Replace the `ListEntriesRequest` message and the `service CatalogService` block in `src/api/catalog.proto`:

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
  rpc ListClientFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListJobFacets(ListFacetsRequest) returns (ListFacetsResponse);
}
```

```protobuf
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
}

message Facet {
  string name       = 1; // hostname, or policy name
  int64  count       = 2; // matching entries in the current scope
  int64  last_seen   = 3; // unix seconds, max(received_at) in scope
}

message ListFacetsRequest {
  int64  received_after  = 1;
  int64  received_before = 2;
  string pattern         = 3;
  repeated string source_hosts = 4; // ignored by ListClientFacets (own dimension)
  repeated string job_names    = 5; // ignored by ListJobFacets (own dimension)
}

message ListFacetsResponse {
  repeated Facet facets = 1;
}
```

Insert `ListFacetsRequest`, `Facet`, and `ListFacetsResponse` after the existing `ListEntriesResponse`/`Entry` messages (i.e., after line 56), keeping `Entry` itself unchanged.

- [ ] **Step 2: Regenerate the Go protobuf code**

Run: `make proto`
Expected: `Protobuf code generated in src/api/` with no errors; `git status` shows `src/api/catalog.pb.go` and `src/api/catalog_grpc.pb.go` modified.

- [ ] **Step 3: Verify the whole module still builds**

Run: `cd src && go build ./...`
Expected: exits 0. Nothing references the new fields/RPCs yet, so this only proves the regenerated code itself is well-formed and nothing existing broke.

- [ ] **Step 4: Commit**

```bash
git add src/api/catalog.proto src/api/catalog.pb.go src/api/catalog_grpc.pb.go
git commit -m "feat(api): add ListClientFacets/ListJobFacets RPCs and date/multi-value filters to catalog.proto"
```

---

## Task 2: Store layer — date range and multi-value filters on `ListEntries`

**Files:**
- Modify: `src/storage/catalog/store.go:1-8,72-126`
- Modify: `src/storage/catalog/models.go:11-25`
- Test: `src/storage/catalog/store_test.go`

**Interfaces:**
- Consumes: nothing new from other tasks.
- Produces: `ListEntriesFilter` gains `ReceivedAfter`, `ReceivedBefore time.Time`, `SourceHosts`, `JobNames []string`. New unexported helper `jobNamesWhere(q *gorm.DB, names []string) *gorm.DB`, reused by Task 3's `ListClientFacets`.

- [ ] **Step 1: Write the failing tests**

Append to `src/storage/catalog/store_test.go`:

```go
func TestListEntries_FiltersByReceivedAtRange(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}))

	included, _, err := store.ListEntries(ListEntriesFilter{ReceivedAfter: time.Now().Add(-1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, included, 1)

	excluded, _, err := store.ListEntries(ListEntriesFilter{ReceivedAfter: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, excluded, 0)

	includedBefore, _, err := store.ListEntries(ListEntriesFilter{ReceivedBefore: time.Now().Add(1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, includedBefore, 1)

	excludedBefore, _, err := store.ListEntries(ListEntriesFilter{ReceivedBefore: time.Now().Add(-1 * time.Hour)})
	require.NoError(t, err)
	assert.Len(t, excludedBefore, 0)
}

func TestListEntries_FiltersBySourceHostsMultiValue(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", SourceHost: "mail", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{SourceHosts: []string{"database", "mail"}})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var hosts []string
	for _, e := range entries {
		hosts = append(hosts, e.SourceHost)
	}
	assert.ElementsMatch(t, []string{"database", "mail"}, hosts)
}

func TestListEntries_FiltersByJobNamesMultiValue(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1752400000", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:1752400010", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:root:fedcba98:1752400020", ObjectID: "obj-3", StoreCreatedAt: time.Now()},
	}))

	entries, _, err := store.ListEntries(ListEntriesFilter{JobNames: []string{"nightly-db", "weekly-full"}})
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var objIDs []string
	for _, e := range entries {
		objIDs = append(objIDs, e.ObjectID)
	}
	assert.ElementsMatch(t, []string{"obj-1", "obj-3"}, objIDs)
}

func TestNew_CreatesIndexesOnReceivedAtAndJobID(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	sqlDB, err := store.db.DB()
	require.NoError(t, err)

	rows, err := sqlDB.Query(`SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name='entry_records'`)
	require.NoError(t, err)
	defer rows.Close()

	var indexDefs []string
	for rows.Next() {
		var def sql.NullString
		require.NoError(t, rows.Scan(&def))
		if def.Valid {
			indexDefs = append(indexDefs, def.String)
		}
	}
	joined := strings.Join(indexDefs, "\n")
	assert.Contains(t, joined, "received_at")
	assert.Contains(t, joined, "job_id")
}
```

Add `"database/sql"` and `"strings"` to the existing `import` block at the top of `store_test.go` (alongside `"fmt"`, `"testing"`, `"time"`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./storage/catalog/... -run 'TestListEntries_FiltersByReceivedAtRange|TestListEntries_FiltersBySourceHostsMultiValue|TestListEntries_FiltersByJobNamesMultiValue|TestNew_CreatesIndexesOnReceivedAtAndJobID' -v`
Expected: FAIL — `ListEntriesFilter` has no field `ReceivedAfter`/`SourceHosts`/`JobNames`, compile error.

- [ ] **Step 3: Implement**

In `src/storage/catalog/models.go`, change the `JobID` and `ReceivedAt` field tags:

```go
	JobID          string `gorm:"uniqueIndex:idx_store_job_object;index"`
```

```go
	ReceivedAt time.Time `gorm:"index"`
```

In `src/storage/catalog/store.go`, add `"strings"` to the import block, extend `ListEntriesFilter`, and extend `ListEntries`:

```go
// ListEntriesFilter narrows and paginates a ListEntries query. A
// zero-valued filter matches every entry, newest first, first page.
type ListEntriesFilter struct {
	StoreNode      string    // exact match against the sending bwfs node; "" = all store nodes
	SourceHost     string    // exact match against the real originating host; "" = all source hosts
	Pattern        string    // substring match against object_id; "" = no filter
	Limit          int       // clamped to [1, 500]; 0 or negative defaults to 100
	StartingAfter  int64     // last-seen entry ID from a previous page; 0 = first page
	ReceivedAfter  time.Time // zero value = no lower bound
	ReceivedBefore time.Time // zero value = no upper bound
	SourceHosts    []string  // OR-matched; empty = no filter, additive to SourceHost
	JobNames       []string  // OR-matched against the policy name embedded in job_id
}
```

```go
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
	if !filter.ReceivedAfter.IsZero() {
		q = q.Where("received_at >= ?", filter.ReceivedAfter)
	}
	if !filter.ReceivedBefore.IsZero() {
		q = q.Where("received_at <= ?", filter.ReceivedBefore)
	}
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
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

// jobNamesWhere adds an OR of job_id LIKE 'backup:<name>:%' conditions, one
// per name -- job_id has no column for the policy name, so this is the
// only way to filter on it (see policyNameFromJobID in facets.go for the
// matching Go-side parse used by ListJobFacets).
func jobNamesWhere(q *gorm.DB, names []string) *gorm.DB {
	conds := make([]string, len(names))
	args := make([]interface{}, len(names))
	for i, name := range names {
		conds[i] = "job_id LIKE ?"
		args[i] = "backup:" + name + ":%"
	}
	return q.Where(strings.Join(conds, " OR "), args...)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS, all tests including the pre-existing ones in this file.

- [ ] **Step 5: Commit**

```bash
git add src/storage/catalog/store.go src/storage/catalog/models.go src/storage/catalog/store_test.go
git commit -m "feat(catalog): filter ListEntries by received_at range and multi-value source hosts/job names"
```

---

## Task 3: Store layer — `ListClientFacets`/`ListJobFacets` aggregate queries

**Files:**
- Modify: `src/storage/catalog/store.go` (append)
- Test: `src/storage/catalog/store_test.go` (append)

**Interfaces:**
- Consumes: `jobNamesWhere` from Task 2.
- Produces: `type Facet struct{ Name string; Count int64; LastSeen time.Time }`, `type FacetFilter struct{ ReceivedAfter, ReceivedBefore time.Time; Pattern string; SourceHosts, JobNames []string }`, `(*Store).ListClientFacets(FacetFilter) ([]Facet, error)`, `(*Store).ListJobFacets(FacetFilter) ([]Facet, error)`. Consumed by Task 4.

- [ ] **Step 1: Write the failing tests**

Append to `src/storage/catalog/store_test.go`:

```go
func TestPolicyNameFromJobID(t *testing.T) {
	assert.Equal(t, "nightly-db", policyNameFromJobID("backup:nightly-db:var-lib:abcd1234:1752400000"))
	assert.Equal(t, "", policyNameFromJobID("operating-refresh:1752400000"))
	assert.Equal(t, "", policyNameFromJobID("backup"))
	assert.Equal(t, "", policyNameFromJobID(""))
}

func TestListClientFacets_GroupsByHostWithCountAndLastSeen(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 2)

	byName := map[string]Facet{}
	for _, f := range facets {
		byName[f.Name] = f
	}
	assert.Equal(t, int64(2), byName["database"].Count)
	assert.Equal(t, int64(1), byName["webserver"].Count)
}

func TestListClientFacets_ExcludesEmptySourceHost(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListClientFacets_NarrowedByJobNames(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListClientFacets(FacetFilter{JobNames: []string{"nightly-db"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "database", facets[0].Name)
}

func TestListJobFacets_GroupsByPolicyNameAcrossManyRuns(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:ef567890:2", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:weekly-full:root:fedcba98:3", ObjectID: "obj-3", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 2)

	byName := map[string]Facet{}
	for _, f := range facets {
		byName[f.Name] = f
	}
	assert.Equal(t, int64(2), byName["nightly-db"].Count)
	assert.Equal(t, int64(1), byName["weekly-full"].Count)
}

func TestListJobFacets_ExcludesNonBackupJobKind(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "operating-refresh:1752400000", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(FacetFilter{})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}

func TestListJobFacets_NarrowedBySourceHosts(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	defer store.Close()

	require.NoError(t, store.EnsureEntries([]Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	facets, err := store.ListJobFacets(FacetFilter{SourceHosts: []string{"database"}})
	require.NoError(t, err)
	require.Len(t, facets, 1)
	assert.Equal(t, "nightly-db", facets[0].Name)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./storage/catalog/... -run 'TestPolicyNameFromJobID|TestListClientFacets|TestListJobFacets' -v`
Expected: FAIL — `FacetFilter`, `Facet`, `ListClientFacets`, `ListJobFacets`, `policyNameFromJobID` undefined.

- [ ] **Step 3: Implement**

Append to `src/storage/catalog/store.go`:

```go
// FacetFilter narrows a ListClientFacets/ListJobFacets aggregate query. A
// zero-valued filter matches every entry, no date bound.
type FacetFilter struct {
	ReceivedAfter  time.Time
	ReceivedBefore time.Time
	Pattern        string
	SourceHosts    []string // ignored by ListClientFacets -- its own dimension
	JobNames       []string // ignored by ListJobFacets -- its own dimension
}

// Facet is one aggregated row: a distinct client hostname or policy name,
// how many matching entries it has, and the most recent one.
type Facet struct {
	Name     string    `gorm:"column:name"`
	Count    int64     `gorm:"column:count"`
	LastSeen time.Time `gorm:"column:last_seen"`
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

// ListClientFacets groups entries matching filter by source_host, dropping
// rows where source_host is empty (a decodeSourceHost failure at sync time
// -- see cmd/catalog/server.go) rather than surfacing a blank-named facet.
// filter.SourceHosts is ignored: a client facet list is never narrowed by
// its own dimension's current selection.
func (s *Store) ListClientFacets(filter FacetFilter) ([]Facet, error) {
	q := s.db.Model(&EntryRecord{}).
		Select("source_host AS name, COUNT(*) AS count, MAX(received_at) AS last_seen").
		Where("source_host != ''").
		Group("source_host")
	q = filter.applyCommon(q)
	if len(filter.JobNames) > 0 {
		q = jobNamesWhere(q, filter.JobNames)
	}

	var facets []Facet
	if err := q.Scan(&facets).Error; err != nil {
		return nil, err
	}
	return facets, nil
}

// ListJobFacets groups entries matching filter by the policy name embedded
// in job_id. Grouping happens in Go, not SQL -- job_id's colon-delimited
// format isn't fixed-width, matching this codebase's existing preference
// for decoding a similar composite ID in Go (cmd/bwfs/list.go's
// parseFileID) over a SQL substr/instr split. filter.SourceHosts is
// applied (it narrows which entries are considered); filter.JobNames is
// ignored: a job facet list is never narrowed by its own dimension's
// current selection.
func (s *Store) ListJobFacets(filter FacetFilter) ([]Facet, error) {
	q := s.db.Model(&EntryRecord{}).Select("job_id, received_at")
	q = filter.applyCommon(q)
	if len(filter.SourceHosts) > 0 {
		q = q.Where("source_host IN ?", filter.SourceHosts)
	}

	var rows []struct {
		JobID      string
		ReceivedAt time.Time
	}
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}

	byName := make(map[string]*Facet)
	var order []string
	for _, r := range rows {
		name := policyNameFromJobID(r.JobID)
		if name == "" {
			continue
		}
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

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./storage/catalog/... -v`
Expected: PASS, all tests in the package.

- [ ] **Step 5: Commit**

```bash
git add src/storage/catalog/store.go src/storage/catalog/store_test.go
git commit -m "feat(catalog): add ListClientFacets/ListJobFacets aggregate queries to the store layer"
```

---

## Task 4: gRPC server — wire new `ListEntries` fields and implement facet RPCs

**Files:**
- Modify: `src/cmd/catalog/server.go:71-89` (and append)
- Test: `src/cmd/catalog/server_test.go` (append)

**Interfaces:**
- Consumes: `catalogstore.ListEntriesFilter`'s new fields (Task 2), `catalogstore.FacetFilter`/`Facet`/`ListClientFacets`/`ListJobFacets` (Task 3), `pb.ListFacetsRequest`/`ListFacetsResponse`/`Facet` (Task 1).
- Produces: `(*catalogServer).ListClientFacets`, `(*catalogServer).ListJobFacets` gRPC methods, satisfying the regenerated `pb.CatalogServiceServer` interface. Consumed by Task 6 (api-server calls these over the wire).

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/catalog/server_test.go`:

```go
func TestListEntries_FiltersByReceivedAtRange(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
	}))

	included, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{
		ReceivedAfter: time.Now().Add(-1 * time.Hour).Unix(),
	})
	require.NoError(t, err)
	assert.Len(t, included.GetEntries(), 1)

	excluded, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{
		ReceivedAfter: time.Now().Add(1 * time.Hour).Unix(),
	})
	require.NoError(t, err)
	assert.Len(t, excluded.GetEntries(), 0)
}

func TestListEntries_FiltersBySourceHostsAndJobNames(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:hourly-web:var-www:ef567890:2", ObjectID: "obj-2", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListEntries(context.Background(), &pb.ListEntriesRequest{
		SourceHosts: []string{"database"},
		JobNames:    []string{"nightly-db"},
	})
	require.NoError(t, err)
	require.Len(t, resp.GetEntries(), 1)
	assert.Equal(t, "obj-1", resp.GetEntries()[0].GetObjectId())
}

func TestListClientFacets_ReturnsGroupedCounts(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-1", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-2", SourceHost: "database", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "job-1", ObjectID: "obj-3", SourceHost: "webserver", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListClientFacets(context.Background(), &pb.ListFacetsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetFacets(), 2)

	byName := map[string]int64{}
	for _, f := range resp.GetFacets() {
		byName[f.GetName()] = f.GetCount()
	}
	assert.Equal(t, int64(2), byName["database"])
	assert.Equal(t, int64(1), byName["webserver"])
}

func TestListJobFacets_ReturnsGroupedCounts(t *testing.T) {
	srv, store := newTestCatalogServer(t)
	require.NoError(t, store.EnsureEntries([]catalogstore.Entry{
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:abcd1234:1", ObjectID: "obj-1", StoreCreatedAt: time.Now()},
		{StoreNode: "bwfs-a", JobID: "backup:nightly-db:var-lib:ef567890:2", ObjectID: "obj-2", StoreCreatedAt: time.Now()},
	}))

	resp, err := srv.ListJobFacets(context.Background(), &pb.ListFacetsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.GetFacets(), 1)
	assert.Equal(t, "nightly-db", resp.GetFacets()[0].GetName())
	assert.Equal(t, int64(2), resp.GetFacets()[0].GetCount())
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/catalog/... -run 'TestListEntries_FiltersByReceivedAtRange|TestListEntries_FiltersBySourceHostsAndJobNames|TestListClientFacets|TestListJobFacets' -v`
Expected: FAIL — `srv.ListClientFacets`/`ListJobFacets` undefined, and `ListEntries` ignores the new request fields (assertions fail).

- [ ] **Step 3: Implement**

Replace the `ListEntries` method and append two new methods in `src/cmd/catalog/server.go`:

```go
func (s *catalogServer) ListEntries(ctx context.Context, req *pb.ListEntriesRequest) (*pb.ListEntriesResponse, error) {
	records, hasMore, err := s.store.ListEntries(catalogstore.ListEntriesFilter{
		StoreNode:      req.GetStoreHost(),
		SourceHost:     req.GetSourceHost(),
		Pattern:        req.GetPattern(),
		Limit:          int(req.GetLimit()),
		StartingAfter:  req.GetStartingAfter(),
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		SourceHosts:    req.GetSourceHosts(),
		JobNames:       req.GetJobNames(),
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

// unixOrZero converts a unix-seconds timestamp to time.Time, leaving the
// zero time.Time{} (rather than the Unix epoch) when ts is 0 -- 0 means
// "no bound" on a ListEntriesRequest/ListFacetsRequest date field, and
// ListEntriesFilter/FacetFilter treat a zero time.Time as unbounded.
func unixOrZero(ts int64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

func (s *catalogServer) ListClientFacets(ctx context.Context, req *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error) {
	facets, err := s.store.ListClientFacets(catalogstore.FacetFilter{
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		Pattern:        req.GetPattern(),
		JobNames:       req.GetJobNames(),
	})
	if err != nil {
		s.logger.Error("ListClientFacets: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list client facets: %v", err)
	}
	return &pb.ListFacetsResponse{Facets: toProtoFacets(facets)}, nil
}

func (s *catalogServer) ListJobFacets(ctx context.Context, req *pb.ListFacetsRequest) (*pb.ListFacetsResponse, error) {
	facets, err := s.store.ListJobFacets(catalogstore.FacetFilter{
		ReceivedAfter:  unixOrZero(req.GetReceivedAfter()),
		ReceivedBefore: unixOrZero(req.GetReceivedBefore()),
		Pattern:        req.GetPattern(),
		SourceHosts:    req.GetSourceHosts(),
	})
	if err != nil {
		s.logger.Error("ListJobFacets: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list job facets: %v", err)
	}
	return &pb.ListFacetsResponse{Facets: toProtoFacets(facets)}, nil
}

func toProtoFacets(facets []catalogstore.Facet) []*pb.Facet {
	out := make([]*pb.Facet, len(facets))
	for i, f := range facets {
		out[i] = &pb.Facet{Name: f.Name, Count: f.Count, LastSeen: f.LastSeen.Unix()}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/catalog/... -v`
Expected: PASS, all tests in the package.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/catalog/server.go src/cmd/catalog/server_test.go
git commit -m "feat(catalog): implement ListClientFacets/ListJobFacets and wire new ListEntries filters"
```

---

## Task 5: api-server — new filter query params on `GET /api/v1/catalog`

**Files:**
- Modify: `src/cmd/api-server/catalog.go:51-92` (and append helpers)
- Test: `src/cmd/api-server/catalog_test.go` (append)

**Interfaces:**
- Consumes: `pb.ListEntriesRequest`'s new fields (Task 1). `catalogQueryClient.ListEntries`'s signature is unchanged, so no interface edit needed here.
- Produces: `parseUnixParam(raw string) (int64, bool)`, `splitCommaParam(raw string) []string` — both reused by Task 6.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/api-server/catalog_test.go`:

```go
func TestHandleListCatalog_PassesNewFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_after=1000&received_before=2000&source_hosts=database,webserver&job_names=nightly-db,weekly-full", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, int64(1000), fake.lastReq.GetReceivedAfter())
	assert.Equal(t, int64(2000), fake.lastReq.GetReceivedBefore())
	assert.Equal(t, []string{"database", "webserver"}, fake.lastReq.GetSourceHosts())
	assert.Equal(t, []string{"nightly-db", "weekly-full"}, fake.lastReq.GetJobNames())
}

func TestHandleListCatalog_OmittedNewFiltersLeaveFieldsZero(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, int64(0), fake.lastReq.GetReceivedAfter())
	assert.Nil(t, fake.lastReq.GetSourceHosts())
}

func TestHandleListCatalog_InvalidReceivedAfterReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_after=not-a-number", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_NegativeReceivedBeforeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_before=-5", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestHandleListCatalog_PassesNewFilterQueryParamsThrough|TestHandleListCatalog_OmittedNewFiltersLeaveFieldsZero|TestHandleListCatalog_InvalidReceivedAfterReturns400|TestHandleListCatalog_NegativeReceivedBeforeReturns400' -v`
Expected: FAIL — the handler doesn't read `received_after`/`received_before`/`source_hosts`/`job_names` yet, so the request sent has zero values for all of them and the 400 tests don't trigger.

- [ ] **Step 3: Implement**

Replace `handleListCatalog` in `src/cmd/api-server/catalog.go` and add the two helpers; add `"strings"` to the import block:

```go
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
		SourceHost:     q.Get("source_host"),
		StoreHost:      q.Get("store_host"),
		Pattern:        q.Get("pattern"),
		Limit:          int32(limit),
		StartingAfter:  startingAfter,
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		SourceHosts:    splitCommaParam(q.Get("source_hosts")),
		JobNames:       splitCommaParam(q.Get("job_names")),
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

// splitCommaParam splits a comma-separated query param into a slice,
// dropping empty segments; an empty input yields nil (no filter).
func splitCommaParam(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS, all tests in the package.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/catalog.go src/cmd/api-server/catalog_test.go
git commit -m "feat(api-server): accept received_after/received_before/source_hosts/job_names on GET /catalog"
```

---

## Task 6: api-server — `GET /catalog/clients` and `GET /catalog/jobs`

**Files:**
- Modify: `src/cmd/api-server/server.go:35-39,69-90` (interface + route registration)
- Modify: `src/cmd/api-server/catalog.go` (append handlers)
- Test: `src/cmd/api-server/catalog_test.go` (append; extend `fakeCatalogQueryClient`)

**Interfaces:**
- Consumes: `pb.ListFacetsRequest`/`ListFacetsResponse`/`Facet` (Task 1), `parseUnixParam`/`splitCommaParam` (Task 5).
- Produces: `(*server).handleListCatalogClients`, `(*server).handleListCatalogJobs`; routes `GET /api/v1/catalog/clients`, `GET /api/v1/catalog/jobs`.

- [ ] **Step 1: Write the failing tests**

In `src/cmd/api-server/catalog_test.go`, extend `fakeCatalogQueryClient` and add new tests:

```go
type fakeCatalogQueryClient struct {
	resp          *pb.ListEntriesResponse
	err           error
	lastReq       *pb.ListEntriesRequest
	facetsResp    *pb.ListFacetsResponse
	facetsErr     error
	lastFacetsReq *pb.ListFacetsRequest
}

func (f *fakeCatalogQueryClient) ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error) {
	f.lastReq = in
	return f.resp, f.err
}

func (f *fakeCatalogQueryClient) ListClientFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error) {
	f.lastFacetsReq = in
	return f.facetsResp, f.facetsErr
}

func (f *fakeCatalogQueryClient) ListJobFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error) {
	f.lastFacetsReq = in
	return f.facetsResp, f.facetsErr
}

func TestHandleListCatalogClients_ReturnsFacetData(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{
		Facets: []*pb.Facet{{Name: "database", Count: 3, LastSeen: 1752400000}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/clients", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	facet := data[0].(map[string]any)
	assert.Equal(t, "database", facet["name"])
	assert.Equal(t, float64(3), facet["count"])
	assert.Equal(t, float64(1752400000), facet["last_seen"])
}

func TestHandleListCatalogClients_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/clients?received_after=1000&received_before=2000&pattern=/var&job_names=nightly-db", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, int64(1000), fake.lastFacetsReq.GetReceivedAfter())
	assert.Equal(t, int64(2000), fake.lastFacetsReq.GetReceivedBefore())
	assert.Equal(t, "/var", fake.lastFacetsReq.GetPattern())
	assert.Equal(t, []string{"nightly-db"}, fake.lastFacetsReq.GetJobNames())
}

func TestHandleListCatalogJobs_ReturnsFacetData(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{
		Facets: []*pb.Facet{{Name: "nightly-db", Count: 7, LastSeen: 1752400000}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	facet := data[0].(map[string]any)
	assert.Equal(t, "nightly-db", facet["name"])
	assert.Equal(t, float64(7), facet["count"])
}

func TestHandleListCatalogJobs_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs?received_after=1000&source_hosts=database,webserver", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, int64(1000), fake.lastFacetsReq.GetReceivedAfter())
	assert.Equal(t, []string{"database", "webserver"}, fake.lastFacetsReq.GetSourceHosts())
}

func TestHandleListCatalogJobs_InvalidReceivedBeforeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs?received_before=-5", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: FAIL to compile — `catalogQueryClient` has no `ListClientFacets`/`ListJobFacets` methods, and `handleListCatalogClients`/`handleListCatalogJobs` don't exist.

- [ ] **Step 3: Implement**

In `src/cmd/api-server/server.go`, extend the interface and register the routes:

```go
// catalogQueryClient is the subset of pb.CatalogServiceClient the catalog
// handlers (Tasks 5-6) need.
type catalogQueryClient interface {
	ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error)
	ListClientFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
	ListJobFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
}
```

```go
	mux.HandleFunc("GET /api/v1/catalog", s.handleListCatalog)
	mux.HandleFunc("GET /api/v1/catalog/clients", s.handleListCatalogClients)
	mux.HandleFunc("GET /api/v1/catalog/jobs", s.handleListCatalogJobs)
```
(Insert the two new lines directly after the existing `GET /api/v1/catalog` line in `registerRoutes`.)

Append to `src/cmd/api-server/catalog.go`:

```go
type facetDTO struct {
	Name     string `json:"name"`
	Count    int64  `json:"count"`
	LastSeen int64  `json:"last_seen"`
}

func toFacetDTO(f *pb.Facet) facetDTO {
	return facetDTO{Name: f.GetName(), Count: f.GetCount(), LastSeen: f.GetLastSeen()}
}

func (s *server) handleListCatalogClients(w http.ResponseWriter, r *http.Request) {
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

	resp, err := s.catalog.ListClientFacets(r.Context(), &pb.ListFacetsRequest{
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		Pattern:        q.Get("pattern"),
		JobNames:       splitCommaParam(q.Get("job_names")),
	})
	if err != nil {
		s.logger.Error("handleListCatalogClients: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}

	facets := make([]facetDTO, len(resp.GetFacets()))
	for i, f := range resp.GetFacets() {
		facets[i] = toFacetDTO(f)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": facets})
}

func (s *server) handleListCatalogJobs(w http.ResponseWriter, r *http.Request) {
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

	resp, err := s.catalog.ListJobFacets(r.Context(), &pb.ListFacetsRequest{
		ReceivedAfter:  receivedAfter,
		ReceivedBefore: receivedBefore,
		Pattern:        q.Get("pattern"),
		SourceHosts:    splitCommaParam(q.Get("source_hosts")),
	})
	if err != nil {
		s.logger.Error("handleListCatalogJobs: backend call failed", "error", err)
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
Expected: PASS, all tests in the package.

- [ ] **Step 5: Run the full Go test suite and build**

Run: `cd src && go build ./... && go test ./...`
Expected: build succeeds, all tests across the module pass. This closes out the backend half of the feature.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/catalog.go src/cmd/api-server/catalog_test.go
git commit -m "feat(api-server): add GET /api/v1/catalog/clients and /catalog/jobs facet endpoints"
```

---

## Task 7: Web — add `@vuepic/vue-datepicker` and `DataTable.vue` checkbox support

**Files:**
- Modify: `web/package.json` (new dependency)
- Modify: `web/src/components/ui/DataTable.vue`
- Test: `web/src/components/ui/DataTable.spec.js` (append)

**Interfaces:**
- Produces: `DataTable.vue` gains prop `selectable: Boolean` (default `false`) and emits `selection-change` with the array of selected row objects. Consumed by Task 10's `FacetPanel.vue`.

- [ ] **Step 1: Add the dependency**

Run: `cd web && npm install @vuepic/vue-datepicker`
Expected: `web/package.json`'s `dependencies` gains an `@vuepic/vue-datepicker` entry; `web/package-lock.json` updates; exits 0.

- [ ] **Step 2: Write the failing tests**

Append to `web/src/components/ui/DataTable.spec.js`:

```js
  it('does not render checkboxes by default', () => {
    const wrapper = mount(DataTable, { props: { columns, rows } })
    expect(wrapper.find('td.vgt-checkbox-col').exists()).toBe(false)
  })

  it('renders row checkboxes when selectable is true', () => {
    const wrapper = mount(DataTable, { props: { columns, rows, selectable: true } })
    expect(wrapper.findAll('td.vgt-checkbox-col input[type="checkbox"]')).toHaveLength(2)
  })

  it('emits selection-change with the selected row objects', async () => {
    const wrapper = mount(DataTable, { props: { columns, rows, selectable: true } })
    const checkboxes = wrapper.findAll('td.vgt-checkbox-col input[type="checkbox"]')
    await checkboxes[1].setValue(true)

    expect(wrapper.emitted('selection-change')).toBeTruthy()
    const lastCall = wrapper.emitted('selection-change').at(-1)[0]
    expect(lastCall).toHaveLength(1)
    expect(lastCall[0]).toMatchObject({ id: 2, name: 'b' })
  })
```

(Add these inside the existing `describe('DataTable', ...)` block, before its closing `})`.)

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/components/ui/DataTable.spec.js`
Expected: FAIL — no `selectable` prop exists yet, so no checkboxes render.

- [ ] **Step 4: Implement**

Replace `web/src/components/ui/DataTable.vue`'s `<script setup>` and template:

```vue
<script setup>
import { VueGoodTable } from 'vue-good-table-next'
import 'vue-good-table-next/dist/vue-good-table-next.css'

defineProps({
  columns: { type: Array, required: true },
  rows: { type: Array, required: true },
  searchEnabled: { type: Boolean, default: true },
  perPage: { type: Number, default: 25 },
  selectable: { type: Boolean, default: false },
})
const emit = defineEmits(['row-click', 'selection-change'])

function handleRowClick({ row }) {
  emit('row-click', row)
}

function handleSelectionChange({ selectedRows }) {
  emit('selection-change', selectedRows)
}
</script>

<template>
  <vue-good-table
    :columns="columns"
    :rows="rows"
    :search-options="{ enabled: searchEnabled, placeholder: 'Search...' }"
    :pagination-options="{ enabled: true, perPage }"
    :select-options="{ enabled: selectable, selectOnCheckboxOnly: true }"
    @row-click="handleRowClick"
    @on-selected-rows-change="handleSelectionChange"
  >
    <template #table-row="props">
      <slot name="table-row" v-bind="props">
        <span>{{ props.formattedRow[props.column.field] }}</span>
      </slot>
    </template>
  </vue-good-table>
</template>
```

(The `<style scoped>` block is unchanged — leave it as-is.)

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/components/ui/DataTable.spec.js`
Expected: PASS, all tests in the file.

- [ ] **Step 6: Commit**

```bash
git add web/package.json web/package-lock.json web/src/components/ui/DataTable.vue web/src/components/ui/DataTable.spec.js
git commit -m "feat(web): add @vuepic/vue-datepicker and checkbox selection support to DataTable"
```

---

## Task 8: Web — rewrite `catalog.js` store for the new filter shape

**Files:**
- Modify: `web/src/stores/catalog.js`
- Test: `web/src/stores/catalog.spec.js` (full rewrite)

**Interfaces:**
- Produces: `useCatalogStore()` state `{ filters: { pattern, receivedAfter, receivedBefore, sourceHosts, jobNames }, entries, loading, error, clientFacets, clientFacetsLoading, clientFacetsError, jobFacets, jobFacetsLoading, jobFacetsError }`; actions `search()` (no args, reads `this.filters`), `fetchClientFacets()`, `fetchJobFacets()`. Consumed by Task 11 (`CatalogView.vue`) and Tasks 9-10 indirectly via `CatalogView.vue`'s wiring.

- [ ] **Step 1: Write the failing tests (full replacement of the spec file)**

Replace the entire contents of `web/src/stores/catalog.spec.js`:

```js
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useCatalogStore } from './catalog'
import { apiFetch } from '../api/client'

vi.mock('../api/client', () => ({
  apiFetch: vi.fn(),
}))

const DAY = 24 * 60 * 60

describe('catalog store', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-05T00:00:00Z'))
    setActivePinia(createPinia())
    apiFetch.mockReset()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('defaults the date range to the last 7 days', () => {
    const catalog = useCatalogStore()
    const now = Math.floor(Date.now() / 1000)
    expect(catalog.filters.receivedBefore).toBe(now)
    expect(catalog.filters.receivedAfter).toBe(now - 7 * DAY)
  })

  describe('search', () => {
    it('fetches a single page using the current filters and a limit of 500', async () => {
      apiFetch.mockResolvedValue({ data: [{ id: 1 }, { id: 2 }], has_more: false })
      const catalog = useCatalogStore()
      catalog.filters.pattern = 'dbdata'
      catalog.filters.sourceHosts = ['database']
      catalog.filters.jobNames = ['nightly-db']

      await catalog.search()

      const now = Math.floor(Date.now() / 1000)
      expect(apiFetch).toHaveBeenCalledWith(
        `/catalog?received_after=${now - 7 * DAY}&received_before=${now}&source_hosts=database&job_names=nightly-db&pattern=dbdata&limit=500`
      )
      expect(apiFetch).toHaveBeenCalledTimes(1)
      expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }])
      expect(catalog.loading).toBe(false)
      expect(catalog.error).toBe(null)
    })

    it('loops starting_after until has_more is false, concatenating every page', async () => {
      apiFetch
        .mockResolvedValueOnce({ data: [{ id: 1 }, { id: 2 }], has_more: true })
        .mockResolvedValueOnce({ data: [{ id: 3 }], has_more: false })
      const catalog = useCatalogStore()

      await catalog.search()

      expect(apiFetch).toHaveBeenCalledTimes(2)
      expect(catalog.entries).toEqual([{ id: 1 }, { id: 2 }, { id: 3 }])
    })

    it('stops looping when a page returns zero rows even if has_more is true', async () => {
      apiFetch.mockResolvedValue({ data: [], has_more: true })
      const catalog = useCatalogStore()

      await catalog.search()

      expect(apiFetch).toHaveBeenCalledTimes(1)
      expect(catalog.entries).toEqual([])
    })

    it('discards everything collected so far and sets error when a later page fails', async () => {
      apiFetch
        .mockResolvedValueOnce({ data: [{ id: 1 }], has_more: true })
        .mockRejectedValueOnce(new Error('boom'))
      const catalog = useCatalogStore()

      await catalog.search()

      expect(catalog.entries).toEqual([])
      expect(catalog.error).toBe('boom')
      expect(catalog.loading).toBe(false)
    })

    it('sets loading true while the fetch loop is in flight', async () => {
      let resolveFirst
      apiFetch.mockReturnValue(
        new Promise((resolve) => {
          resolveFirst = resolve
        })
      )
      const catalog = useCatalogStore()

      const promise = catalog.search()
      expect(catalog.loading).toBe(true)
      resolveFirst({ data: [], has_more: false })
      await promise
      expect(catalog.loading).toBe(false)
    })
  })

  describe('fetchClientFacets', () => {
    it('queries /catalog/clients excluding the sourceHosts filter', async () => {
      apiFetch.mockResolvedValue({ data: [{ name: 'database', count: 3, last_seen: 123 }] })
      const catalog = useCatalogStore()
      catalog.filters.sourceHosts = ['database']
      catalog.filters.jobNames = ['nightly-db']

      await catalog.fetchClientFacets()

      const now = Math.floor(Date.now() / 1000)
      expect(apiFetch).toHaveBeenCalledWith(
        `/catalog/clients?received_after=${now - 7 * DAY}&received_before=${now}&job_names=nightly-db`
      )
      expect(catalog.clientFacets).toEqual([{ name: 'database', count: 3, last_seen: 123 }])
    })

    it('sets clientFacetsError without touching the results error on failure', async () => {
      apiFetch.mockRejectedValue(new Error('boom'))
      const catalog = useCatalogStore()

      await catalog.fetchClientFacets()

      expect(catalog.clientFacetsError).toBe('boom')
      expect(catalog.error).toBe(null)
    })
  })

  describe('fetchJobFacets', () => {
    it('queries /catalog/jobs excluding the jobNames filter', async () => {
      apiFetch.mockResolvedValue({ data: [{ name: 'nightly-db', count: 7, last_seen: 123 }] })
      const catalog = useCatalogStore()
      catalog.filters.sourceHosts = ['database']
      catalog.filters.jobNames = ['nightly-db']

      await catalog.fetchJobFacets()

      const now = Math.floor(Date.now() / 1000)
      expect(apiFetch).toHaveBeenCalledWith(
        `/catalog/jobs?received_after=${now - 7 * DAY}&received_before=${now}&source_hosts=database`
      )
      expect(catalog.jobFacets).toEqual([{ name: 'nightly-db', count: 7, last_seen: 123 }])
    })
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/stores/catalog.spec.js`
Expected: FAIL — `catalog.filters.receivedAfter` is `undefined`, `search()` still expects an argument, `fetchClientFacets`/`fetchJobFacets` don't exist.

- [ ] **Step 3: Implement**

Replace the entire contents of `web/src/stores/catalog.js`:

```js
import { defineStore } from 'pinia'
import { apiFetch } from '../api/client'
import { withRequest } from './helpers'

const MAX_PAGE_LIMIT = 500
const DEFAULT_RANGE_SECONDS = 7 * 24 * 60 * 60

function buildQuery(filters, startingAfter, limit) {
  const params = new URLSearchParams()
  if (filters.receivedAfter) params.set('received_after', String(filters.receivedAfter))
  if (filters.receivedBefore) params.set('received_before', String(filters.receivedBefore))
  if (filters.sourceHosts?.length) params.set('source_hosts', filters.sourceHosts.join(','))
  if (filters.jobNames?.length) params.set('job_names', filters.jobNames.join(','))
  if (filters.pattern) params.set('pattern', filters.pattern)
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
      entries: [],
      loading: false,
      error: null,
      clientFacets: [],
      clientFacetsLoading: false,
      clientFacetsError: null,
      jobFacets: [],
      jobFacetsLoading: false,
      jobFacetsError: null,
    }
  },
  actions: {
    async search() {
      try {
        await withRequest(this, async () => {
          const collected = []
          let startingAfter
          for (;;) {
            const qs = buildQuery(this.filters, startingAfter, MAX_PAGE_LIMIT)
            const body = await apiFetch(`/catalog?${qs}`)
            collected.push(...body.data)
            if (!body.has_more || body.data.length === 0) break
            startingAfter = body.data[body.data.length - 1].id
          }
          this.entries = collected
        })
      } catch {
        // withRequest already recorded this.error; discard any partial or
        // stale results rather than leaving a previous search's rows on screen.
        this.entries = []
      }
    },
    async fetchClientFacets() {
      await withRequest(
        this,
        async () => {
          const qs = buildFacetQuery(this.filters, 'sourceHosts')
          const body = await apiFetch(`/catalog/clients?${qs}`)
          this.clientFacets = body.data
        },
        { rethrow: false, loadingKey: 'clientFacetsLoading', errorKey: 'clientFacetsError' }
      )
    },
    async fetchJobFacets() {
      await withRequest(
        this,
        async () => {
          const qs = buildFacetQuery(this.filters, 'jobNames')
          const body = await apiFetch(`/catalog/jobs?${qs}`)
          this.jobFacets = body.data
        },
        { rethrow: false, loadingKey: 'jobFacetsLoading', errorKey: 'jobFacetsError' }
      )
    },
  },
})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/stores/catalog.spec.js`
Expected: PASS, all tests in the file.

- [ ] **Step 5: Commit**

```bash
git add web/src/stores/catalog.js web/src/stores/catalog.spec.js
git commit -m "feat(web): rewrite catalog store for date-range and multi-value client/job filters"
```

---

## Task 9: Web — `DateRangePanel.vue`

**Files:**
- Create: `web/src/components/catalog/DateRangePanel.vue`
- Test: `web/src/components/catalog/DateRangePanel.spec.js`

**Interfaces:**
- Consumes: `@vuepic/vue-datepicker` (Task 7).
- Produces: `<DateRangePanel v-model:received-after="..." v-model:received-before="..." />` (unix-seconds numbers in, unix-seconds numbers out). Consumed by Task 11.

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/catalog/DateRangePanel.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import DateRangePanel from './DateRangePanel.vue'
import VueDatePicker from '@vuepic/vue-datepicker'

function mountPanel(props = {}) {
  return mount(DateRangePanel, {
    props: { receivedAfter: 1000, receivedBefore: 2000, ...props },
    global: { stubs: { VueDatePicker: true } },
  })
}

describe('DateRangePanel', () => {
  it("passes the current range as VueDatePicker's model value", () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    expect(picker.props('modelValue')).toEqual([new Date(1000 * 1000), new Date(2000 * 1000)])
  })

  it('updates receivedAfter/receivedBefore when the picker emits a new range', async () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    await picker.vm.$emit('update:modelValue', [new Date(5000 * 1000), new Date(6000 * 1000)])

    expect(wrapper.emitted('update:receivedAfter')[0]).toEqual([5000])
    expect(wrapper.emitted('update:receivedBefore')[0]).toEqual([6000])
  })

  it('includes a "Last 7 days" preset spanning exactly 7 days', () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    const presets = picker.props('presetDates')
    const week = presets.find((p) => p.label === 'Last 7 days')
    const [start, end] = week.value
    expect(end.getTime() - start.getTime()).toBe(7 * 24 * 60 * 60 * 1000)
  })

  it('disables the time picker (date-only range)', () => {
    const wrapper = mountPanel()
    const picker = wrapper.findComponent(VueDatePicker)
    expect(picker.props('enableTimePicker')).toBe(false)
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/components/catalog/DateRangePanel.spec.js`
Expected: FAIL — `DateRangePanel.vue` doesn't exist yet.

- [ ] **Step 3: Implement**

Create `web/src/components/catalog/DateRangePanel.vue`:

```vue
<script setup>
import { computed } from 'vue'
import VueDatePicker from '@vuepic/vue-datepicker'
import '@vuepic/vue-datepicker/dist/main.css'

const receivedAfter = defineModel('receivedAfter', { type: Number, required: true })
const receivedBefore = defineModel('receivedBefore', { type: Number, required: true })

const DAY = 24 * 60 * 60

function preset(label, spanSeconds) {
  const now = Math.floor(Date.now() / 1000)
  return { label, value: [new Date((now - spanSeconds) * 1000), new Date(now * 1000)] }
}

const presetDates = computed(() => [
  preset('Today', DAY),
  preset('Last 7 days', 7 * DAY),
  preset('Last 30 days', 30 * DAY),
  preset('This month', 30 * DAY),
])

const range = computed({
  get: () => [new Date(receivedAfter.value * 1000), new Date(receivedBefore.value * 1000)],
  set: ([start, end]) => {
    receivedAfter.value = Math.floor(start.getTime() / 1000)
    receivedBefore.value = Math.floor(end.getTime() / 1000)
  },
})
</script>

<template>
  <div class="border rounded p-4">
    <VueDatePicker
      v-model="range"
      range
      :preset-dates="presetDates"
      :enable-time-picker="false"
      :clearable="false"
    />
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/components/catalog/DateRangePanel.spec.js`
Expected: PASS, all tests in the file.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/catalog/DateRangePanel.vue web/src/components/catalog/DateRangePanel.spec.js
git commit -m "feat(web): add DateRangePanel wrapping vue-datepicker with day-range presets"
```

---

## Task 10: Web — `FacetPanel.vue` (shared clients/jobs checkbox table)

**Files:**
- Create: `web/src/components/catalog/FacetPanel.vue`
- Test: `web/src/components/catalog/FacetPanel.spec.js`

**Interfaces:**
- Consumes: `DataTable.vue`'s `selectable`/`selection-change` (Task 7).
- Produces: `<FacetPanel :facets :error name-label count-label v-model:selected />` — `selected` is an array of facet `name` strings. Consumed twice by Task 11 (once for clients, once for jobs), avoiding a near-duplicate `ClientsPanel`/`JobsPanel` pair.

- [ ] **Step 1: Write the failing tests**

Create `web/src/components/catalog/FacetPanel.spec.js`:

```js
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import FacetPanel from './FacetPanel.vue'

const facets = [
  { name: 'database', count: 3, last_seen: 1752400000 },
  { name: 'webserver', count: 1, last_seen: 1752400010 },
]

function mountPanel(props = {}) {
  return mount(FacetPanel, {
    props: { facets, error: null, nameLabel: 'Client', countLabel: 'Entries in range', selected: [], ...props },
  })
}

describe('FacetPanel', () => {
  it('renders one row per facet with the given column labels', () => {
    const wrapper = mountPanel()
    expect(wrapper.text()).toContain('Client')
    expect(wrapper.text()).toContain('Entries in range')
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(2)
    expect(rows[0].text()).toContain('database')
  })

  it('checks the checkbox for a name already in `selected`', () => {
    const wrapper = mountPanel({ selected: ['database'] })
    const checkbox = wrapper.find('td.vgt-checkbox-col input[type="checkbox"]')
    expect(checkbox.element.checked).toBe(true)
  })

  it('leaves other checkboxes unchecked', () => {
    const wrapper = mountPanel({ selected: ['database'] })
    const checkboxes = wrapper.findAll('td.vgt-checkbox-col input[type="checkbox"]')
    expect(checkboxes[1].element.checked).toBe(false)
  })

  it('emits update:selected with the new set of names when a checkbox is toggled', async () => {
    const wrapper = mountPanel()
    const checkboxes = wrapper.findAll('td.vgt-checkbox-col input[type="checkbox"]')
    await checkboxes[0].setValue(true)

    expect(wrapper.emitted('update:selected').at(-1)[0]).toEqual(['database'])
  })

  it('shows the error message when present', () => {
    const wrapper = mountPanel({ error: 'boom' })
    expect(wrapper.text()).toContain('boom')
  })
})
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd web && npx vitest run src/components/catalog/FacetPanel.spec.js`
Expected: FAIL — `FacetPanel.vue` doesn't exist yet.

- [ ] **Step 3: Implement**

Create `web/src/components/catalog/FacetPanel.vue`:

```vue
<script setup>
import { computed } from 'vue'
import { formatTimestamp } from '../../utils/format'
import DataTable from '../ui/DataTable.vue'

const props = defineProps({
  facets: { type: Array, required: true },
  error: { type: String, default: null },
  nameLabel: { type: String, required: true },
  countLabel: { type: String, required: true },
})

const selected = defineModel('selected', { type: Array, required: true })

const columns = computed(() => [
  { label: props.nameLabel, field: 'name', sortable: true },
  { label: props.countLabel, field: 'count', sortable: true, type: 'number' },
  {
    label: 'Last seen',
    field: 'last_seen',
    sortable: true,
    type: 'number',
    formatFn: (v) => formatTimestamp(v) || '—',
  },
])

const rows = computed(() =>
  props.facets.map((f) => ({ ...f, vgtSelected: selected.value.includes(f.name) }))
)

function onSelectionChange(selectedRows) {
  selected.value = selectedRows.map((r) => r.name)
}
</script>

<template>
  <div>
    <DataTable :columns="columns" :rows="rows" selectable @selection-change="onSelectionChange" />
    <p v-if="error" class="text-red-600 text-sm mt-2">{{ error }}</p>
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/components/catalog/FacetPanel.spec.js`
Expected: PASS, all tests in the file.

- [ ] **Step 5: Commit**

```bash
git add web/src/components/catalog/FacetPanel.vue web/src/components/catalog/FacetPanel.spec.js
git commit -m "feat(web): add shared FacetPanel checkbox table for catalog clients/jobs filters"
```

---

## Task 11: Web — rewrite `CatalogView.vue` with the three-row filter bar

**Files:**
- Modify: `web/src/views/CatalogView.vue` (full rewrite)
- Test: `web/src/views/CatalogView.spec.js` (full rewrite)

**Interfaces:**
- Consumes: `useCatalogStore()` (Task 8), `DateRangePanel.vue` (Task 9), `FacetPanel.vue` (Task 10).
- Produces: the finished feature's UI entry point. Nothing else in the codebase consumes `CatalogView.vue` (it's a router view).

- [ ] **Step 1: Write the failing tests (full replacement of the spec file)**

Replace the entire contents of `web/src/views/CatalogView.spec.js`:

```js
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { mount, flushPromises } from '@vue/test-utils'
import { createTestingPinia } from '@pinia/testing'
import CatalogView from './CatalogView.vue'
import { useCatalogStore } from '../stores/catalog'
import DateRangePanel from '../components/catalog/DateRangePanel.vue'
import FacetPanel from '../components/catalog/FacetPanel.vue'

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
        entries: [],
        loading: false,
        error: null,
        filters: { pattern: '', receivedAfter: 1000, receivedBefore: 2000, sourceHosts: [], jobNames: [] },
        clientFacets: [],
        clientFacetsError: null,
        jobFacets: [],
        jobFacetsError: null,
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

  it('fetches results and both facet lists on mount', () => {
    const { catalog } = mountView({})
    expect(catalog.search).toHaveBeenCalledTimes(1)
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

  it('re-fetches results and both facet lists when the date range changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.search.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.receivedAfter = 500
    await wrapper.vm.$nextTick()

    expect(catalog.search).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
  })

  it('re-fetches results and only the job facets when the client selection changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.search.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.sourceHosts.push('database')
    await wrapper.vm.$nextTick()

    expect(catalog.search).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).not.toHaveBeenCalled()
  })

  it('re-fetches results and only the client facets when the job selection changes', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.search.mockClear()
    catalog.fetchClientFacets.mockClear()
    catalog.fetchJobFacets.mockClear()

    catalog.filters.jobNames.push('nightly-db')
    await wrapper.vm.$nextTick()

    expect(catalog.search).toHaveBeenCalledTimes(1)
    expect(catalog.fetchClientFacets).toHaveBeenCalledTimes(1)
    expect(catalog.fetchJobFacets).not.toHaveBeenCalled()
  })

  it('debounces path input before searching', async () => {
    const { wrapper, catalog } = mountView({})
    catalog.search.mockClear()

    await wrapper.find('[data-test="path-input"]').setValue('dbdata')
    expect(catalog.search).not.toHaveBeenCalled()

    vi.advanceTimersByTime(300)
    await flushPromises()
    expect(catalog.search).toHaveBeenCalledTimes(1)
  })

  it('shows a no-results message when there are no entries', () => {
    const { wrapper } = mountView({})
    expect(wrapper.text()).toContain('No entries match this filter.')
  })

  it('groups entries sharing source_host and path into a single row with a version count', () => {
    const { wrapper } = mountView({
      entries: [
        entry({ id: 1, store_created_at: 1752300000, size: 8004 }),
        entry({ id: 2, store_created_at: 1752400000, size: 8192 }),
      ],
    })
    const rows = wrapper.findAll('tbody tr')
    expect(rows).toHaveLength(1)
    expect(rows[0].text()).toContain('/var/lib/dbdata/data.db')
    expect(rows[0].text()).toContain('8.0 KB')
    expect(rows[0].text()).toContain('2')
  })

  it('renders a single-version file without a version count', () => {
    const { wrapper } = mountView({ entries: [entry({ id: 1 })] })
    const rows = wrapper.findAll('tbody tr')
    const cells = rows[0].findAll('td')
    expect(cells[cells.length - 1].text()).toBe('')
  })

  it('opens the versions modal for the row actually clicked, even after sorting reorders the table', async () => {
    const { wrapper } = mountView({
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
    const { wrapper } = mountView({ entries: [entry({ id: 1 })] })
    await wrapper.find('tbody tr').trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('.fixed').exists()).toBe(false)
  })

  it('closes the versions modal via its Close button', async () => {
    const { wrapper } = mountView({
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
Expected: FAIL — the current `CatalogView.vue` has no chips, no `data-test` hooks, and calls `catalog.search(form)` only on submit, not on mount.

- [ ] **Step 3: Implement**

Replace the entire contents of `web/src/views/CatalogView.vue`:

```vue
<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { useCatalogStore } from '../stores/catalog'
import { formatBytes, formatTimestamp } from '../utils/format'
import { groupEntriesByFile } from '../utils/catalogGrouping'
import PageHeader from '../components/ui/PageHeader.vue'
import StatusMessage from '../components/ui/StatusMessage.vue'
import DataTable from '../components/ui/DataTable.vue'
import DateRangePanel from '../components/catalog/DateRangePanel.vue'
import FacetPanel from '../components/catalog/FacetPanel.vue'
import VersionsModal from '../components/VersionsModal.vue'

const catalog = useCatalogStore()
const activePanel = ref('date')
const selectedGroup = ref(null)

const groups = computed(() => groupEntriesByFile(catalog.entries))

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

function onRowClick(group) {
  if (group.versions.length > 1) selectedGroup.value = group
}

onMounted(() => {
  catalog.search()
  catalog.fetchClientFacets()
  catalog.fetchJobFacets()
})

let pathDebounce
watch(
  () => catalog.filters.pattern,
  () => {
    clearTimeout(pathDebounce)
    pathDebounce = setTimeout(() => {
      catalog.search()
      catalog.fetchClientFacets()
      catalog.fetchJobFacets()
    }, 300)
  }
)
watch(
  () => [catalog.filters.receivedAfter, catalog.filters.receivedBefore],
  () => {
    catalog.search()
    catalog.fetchClientFacets()
    catalog.fetchJobFacets()
  }
)
watch(
  () => catalog.filters.jobNames,
  () => {
    catalog.search()
    catalog.fetchClientFacets()
  },
  { deep: true }
)
watch(
  () => catalog.filters.sourceHosts,
  () => {
    catalog.search()
    catalog.fetchJobFacets()
  },
  { deep: true }
)

const columns = [
  { label: 'Path', field: 'path', sortable: true },
  { label: 'Source Host', field: 'sourceHost', sortable: true },
  { label: 'Store Host', field: 'representative.store_host', sortable: true },
  { label: 'Size', field: 'representative.size', sortable: true, type: 'number', formatFn: (v) => formatBytes(v) },
  { label: 'Mode', field: 'representative.mode', sortable: true },
  {
    label: 'Modified',
    field: 'representative.mod_time',
    sortable: true,
    type: 'number',
    formatFn: (v) => formatTimestamp(v) || '—',
  },
  { label: 'Versions', field: 'versions', sortable: false, formatFn: (v) => (v.length > 1 ? v.length : '') },
]
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

    <StatusMessage
      :loading="catalog.loading"
      :error="catalog.error"
      :empty="groups.length === 0"
      empty-text="No entries match this filter."
    >
      <DataTable :columns="columns" :rows="groups" :search-enabled="false" @row-click="onRowClick" />
    </StatusMessage>
    <VersionsModal v-if="selectedGroup" :group="selectedGroup" @close="selectedGroup = null" />
  </div>
</template>
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd web && npx vitest run src/views/CatalogView.spec.js`
Expected: PASS, all tests in the file.

- [ ] **Step 5: Run the full web test suite**

Run: `cd web && npm test`
Expected: all suites pass, including `DataTable.spec.js`, `catalog.spec.js`, `DateRangePanel.spec.js`, `FacetPanel.spec.js`, and `CatalogView.spec.js`.

- [ ] **Step 6: Commit**

```bash
git add web/src/views/CatalogView.vue web/src/views/CatalogView.spec.js
git commit -m "feat(web): rewrite CatalogView with a three-row date/client/job filter bar"
```

---

## Task 12: Documentation and changelog

**Files:**
- Modify: `docs/protocols/catalog-sync.md:7-13,63-70,111-117`
- Modify: `docs/components/catalog.md:5-7`
- Modify: `docs/api/rest-v1.md:110-146`
- Modify: `CHANGELOG.md` (prepend entry)
- Verify (no edit expected): `README.md:66-67,80`

**Interfaces:** none — documentation only.

- [ ] **Step 1: Update the protocol doc's service block**

In `docs/protocols/catalog-sync.md`, replace:

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
}
```

with:

```protobuf
service CatalogService {
  rpc SyncFileVersions(SyncRequest) returns (SyncResponse);
  rpc ListEntries(ListEntriesRequest) returns (ListEntriesResponse);
  rpc ListClientFacets(ListFacetsRequest) returns (ListFacetsResponse);
  rpc ListJobFacets(ListFacetsRequest) returns (ListFacetsResponse);
}
```

- [ ] **Step 2: Update the `ListEntries` section and add a Facets section**

In `docs/protocols/catalog-sync.md`, replace the `ListEntriesRequest` message block (currently lines 63-70) with:

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
}
```

Then, immediately after the existing `## ListEntries` section (after its closing bullet list, before `## See Also`), add:

```markdown
## ListClientFacets / ListJobFacets

Two read-only aggregate RPCs backing the web catalog view's faceted filter panels — for
[api-server](../components/api-server.md)'s `GET /api/v1/catalog/clients` and
`GET /api/v1/catalog/jobs`. Both share one request/response shape:

\`\`\`protobuf
message Facet {
  string name       = 1; // hostname, or policy name
  int64  count       = 2; // matching entries in the current scope
  int64  last_seen   = 3; // unix seconds, max(received_at) in scope
}

message ListFacetsRequest {
  int64  received_after  = 1;
  int64  received_before = 2;
  string pattern         = 3;
  repeated string source_hosts = 4; // ignored by ListClientFacets (own dimension)
  repeated string job_names    = 5; // ignored by ListJobFacets (own dimension)
}

message ListFacetsResponse {
  repeated Facet facets = 1;
}
\`\`\`

`ListClientFacets` groups by `source_host`; `ListJobFacets` groups by the policy name embedded in
`job_id` (the second colon-delimited segment of a `backup:<policyName>:...` job_id — see
[Identity](#identity)'s `job_id` convention). Each RPC ignores its own dimension's `source_hosts`/
`job_names` field on the request: a facet list is never narrowed by its own current selection, so
a caller can implement cross-filtering (client selection narrows the policy list and vice versa)
by passing the *other* dimension's active selection. Rows with an empty grouping key (an
undecoded `source_host`, or a `job_id` that isn't `backup:`-prefixed) are dropped rather than
surfaced as a blank-named facet.
```

(Remove the literal `\`\`\`` escaping shown above — it's only there to nest a fenced code block inside this plan's own fenced instructions; write plain triple-backtick fences in the actual file.)

- [ ] **Step 3: Update the See Also cross-link**

In `docs/protocols/catalog-sync.md`'s `## See Also` section, the existing REST API v1 line reads:

```markdown
- [REST API v1](../api/rest-v1.md) — the `GET /api/v1/catalog` endpoint backed by `ListEntries`
```

Replace with:

```markdown
- [REST API v1](../api/rest-v1.md) — `GET /api/v1/catalog` (`ListEntries`), `GET /api/v1/catalog/clients`
  (`ListClientFacets`), and `GET /api/v1/catalog/jobs` (`ListJobFacets`)
```

- [ ] **Step 4: Update the component doc's summary line**

In `docs/components/catalog.md`, replace:

```markdown
Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Also serves `ListEntries`, a read-only query RPC (filter by store host, real source host, and a
substring match against the underlying object ID, keyset-paginated) — see
[api-server](./api-server.md), the only intended caller today.
```

with:

```markdown
Receives `catalogsync`'s replicated `bwfs` file-version batches over gRPC and persists them
idempotently to its own SQLite database. **Control-plane component** — runs centrally, not
colocated with any single `bwfs` node. Also serves three read-only query RPCs: `ListEntries`
(filter by store host, real source host, a date range, and a substring match against the
underlying object ID, keyset-paginated) and the aggregate `ListClientFacets`/`ListJobFacets`
(grouped counts by client host or by policy name, backing the web catalog view's filter panels)
— see [api-server](./api-server.md), the only intended caller today.
```

- [ ] **Step 5: Update the REST API doc**

In `docs/api/rest-v1.md`, replace the `## GET /api/v1/catalog` section's parameter table (lines 112-121) with:

```markdown
Query parameters (all optional):

| Param | Type | Description |
|-------|------|--------------|
| `source_host` | string | Exact match on the real originating (backed-up) host |
| `store_host` | string | Exact match on the `bwfs` node that replicated the entry |
| `pattern` | string | Substring match against the entry's underlying object ID (which embeds the original file path) |
| `received_after` | int, unix seconds | Only entries received at or after this time |
| `received_before` | int, unix seconds | Only entries received at or before this time |
| `source_hosts` | comma-separated strings | OR-matched, additive to `source_host` |
| `job_names` | comma-separated strings | OR-matched against the policy name embedded in the entry's `job_id` |
| `limit` | int, 1–500 | Page size, default 100 |
| `starting_after` | int | Continue from this entry `id` (from a previous page's last entry) |
```

Then, immediately after that section's closing `` `400` if `limit`... `` line and before `## GET /api/v1/policies`, insert:

```markdown
## `GET /api/v1/catalog/clients`

Returns the distinct client (source host) facets matching the given filters, each with a count and
last-seen timestamp. Not paginated — a fleet's distinct client count is expected to stay in the
dozens. Query parameters: `received_after`, `received_before`, `pattern`, `job_names` (comma-separated)
— note there is no `source_hosts` parameter here, since a client facet list is never narrowed by its
own dimension.

\`\`\`json
{
  "data": [
    {"name": "database", "count": 42, "last_seen": 1752400010}
  ]
}
\`\`\`

## `GET /api/v1/catalog/jobs`

Same shape as `/catalog/clients`, grouped by policy name instead of client host. Query parameters:
`received_after`, `received_before`, `pattern`, `source_hosts` (comma-separated) — no `job_names`
parameter, for the same own-dimension reason.

\`\`\`json
{
  "data": [
    {"name": "nightly-db", "count": 7, "last_seen": 1752400010}
  ]
}
\`\`\`
```

(As in Step 2, write plain triple-backtick fences — the `\`\`\`` above is only escaped for nesting inside this plan.)

- [ ] **Step 6: Verify README cross-links need no change**

Read `README.md:66-67` (the `catalog` component bullet) and `README.md:80` (the `Catalog Sync Protocol`
documentation-index bullet). Both are one-line summaries that don't restate the RPC list, so neither
needs editing — confirm this by re-reading them after Steps 1-5, rather than editing on assumption.

- [ ] **Step 7: Add the changelog entry**

Prepend to `CHANGELOG.md`, immediately after the `# Changelog` header and its intro line (before the
existing `## 2026-08-04` entries):

```markdown
## 2026-08-05 — catalog: date-range and job/policy filtering with cross-filtering

The catalog UI and API grow two new filter dimensions — a date range (on `received_at`) and
job/policy (matched by the policy name embedded in `job_id`) — alongside the existing client and
path filters. `CatalogService` gains `ListClientFacets`/`ListJobFacets`, two aggregate RPCs that
back new `GET /api/v1/catalog/clients`/`GET /api/v1/catalog/jobs` endpoints and full cross-filtering
between clients and policies in the web filter bar: each facet list excludes its own dimension, so
selecting a client narrows the policy list and vice versa without ever narrowing itself out. All
proto/API additions are additive; the web catalog view's filter bar and store internals are
rewritten (no external consumers), now defaulting to and auto-fetching the last 7 days on load.
```

- [ ] **Step 8: Final full-stack verification**

Run: `cd src && go build ./... && go test ./...`
Expected: build succeeds, all Go tests pass.

Run: `cd web && npm test`
Expected: all Vitest suites pass.

- [ ] **Step 9: Commit**

```bash
git add docs/protocols/catalog-sync.md docs/components/catalog.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "docs: document catalog facet RPCs, new /catalog filters, and changelog entry"
```
