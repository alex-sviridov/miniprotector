# Policy Check-in Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `policy-server` records, in a local SQLite database, the most recent time each host received each policy from `GetPolicies`, exposes that per-policy host/timestamp list through `ListPolicies` (and therefore `api-server`'s `GET /api/v1/policies` / `GET /api/v1/policies/{id}`), and purges stale check-ins on a fixed 1-minute cleanup tick.

**Architecture:** A new `src/storage/policyserver` package (mirroring the existing `src/storage/catalog` / `src/storage/clientmanager` `db.go`/`models.go`/`store.go` layout) owns a `gorm` + `modernc.org/sqlite` database at `<var-dir>/policy-server.sqlite`, storing one upserted row per `(policy_id, hostname)` pair. `GetPolicies` upserts a row for every policy it hands back to a host (any type), failing the RPC if the write fails. `ListPolicies` attaches each policy's check-in rows to the response. A background goroutine, ticking every fixed 1 minute, deletes rows older than a new configurable retention window.

**Tech Stack:** Go, gRPC/protobuf, GORM + `modernc.org/sqlite` (already a project dependency), `testify`.

## Global Constraints

- New config key: `CheckinRetentionSec` (int, `local.conf` key parsed into `common/config.Config`, **not** an OS environment variable) — default `86400`, must be positive (same validation shape as `AdhocPolicyTimeoutSec`).
- Cleanup tick interval is a fixed 1 minute, not configurable.
- A check-in write failure inside `GetPolicies` fails the whole RPC (fail-closed) — matches how every other failure in that method (`mtls.PeerHostname`, `jobid.FromIncoming`, `mtls.PeerAttributes`) already aborts the call.
- `Policy.checkins` (new proto field) is populated **only** by `ListPolicies` — `GetPolicies`, `CreatePolicy`, `UpdatePolicy` leave it empty, the same way `GetPolicies` never echoes back `client_filters`.
- Follow this repo's `.claude/CLAUDE.md` documentation rules: update `docs/components/policy-server.md`, `docs/protocols/policy-server.md`, `docs/api/rest-v1.md`, `docs/ARCHITECTURE.md` (as needed), and add a `CHANGELOG.md` entry before this branch merges to `main`.
- Every new Go file matches this codebase's existing style: package comment where the directory already has one, no doc-comment restating what a name already says, errors wrapped with `fmt.Errorf("...: %w", err)`.

---

### Task 1: `storage/policyserver` package — check-in store

**Files:**
- Create: `src/storage/policyserver/models.go`
- Create: `src/storage/policyserver/db.go`
- Create: `src/storage/policyserver/store.go`
- Create: `src/storage/policyserver/store_test.go`

**Interfaces:**
- Produces: `policyserver.CheckinRecord{PolicyID, Hostname string; LastSeenAt time.Time}`; `policyserver.New(varDir string) (*Store, error)`; `(*Store).RecordCheckin(policyID, hostname string, at time.Time) error`; `(*Store).CheckinsForPolicy(policyID string) ([]CheckinRecord, error)`; `(*Store).DeleteOlderThan(cutoff time.Time) (int64, error)`; `(*Store).Close() error`.

- [ ] **Step 1: Write the failing tests**

Create `src/storage/policyserver/store_test.go`:

```go
package policyserver

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func TestNew_OpensAndClosesCleanly(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

func TestRecordCheckin_ThenCheckinsForPolicy_RoundTrips(t *testing.T) {
	store := newTestStore(t)
	seenAt := time.Now().Truncate(time.Second)

	require.NoError(t, store.RecordCheckin("policy-1", "host-a", seenAt))

	records, err := store.CheckinsForPolicy("policy-1")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "policy-1", records[0].PolicyID)
	assert.Equal(t, "host-a", records[0].Hostname)
	assert.True(t, seenAt.Equal(records[0].LastSeenAt))
}

func TestRecordCheckin_UpsertOverwritesTimestampRatherThanDuplicating(t *testing.T) {
	store := newTestStore(t)
	first := time.Now().Add(-time.Hour).Truncate(time.Second)
	second := time.Now().Truncate(time.Second)

	require.NoError(t, store.RecordCheckin("policy-1", "host-a", first))
	require.NoError(t, store.RecordCheckin("policy-1", "host-a", second))

	records, err := store.CheckinsForPolicy("policy-1")
	require.NoError(t, err)
	require.Len(t, records, 1, "same (policy, host) pair must upsert, not duplicate")
	assert.True(t, second.Equal(records[0].LastSeenAt))
}

func TestCheckinsForPolicy_ScopesByPolicyID(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.RecordCheckin("policy-1", "host-a", time.Now()))
	require.NoError(t, store.RecordCheckin("policy-2", "host-b", time.Now()))

	records, err := store.CheckinsForPolicy("policy-1")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "host-a", records[0].Hostname)
}

func TestCheckinsForPolicy_OrderedByHostname(t *testing.T) {
	store := newTestStore(t)
	require.NoError(t, store.RecordCheckin("policy-1", "zebra", time.Now()))
	require.NoError(t, store.RecordCheckin("policy-1", "apple", time.Now()))

	records, err := store.CheckinsForPolicy("policy-1")
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "apple", records[0].Hostname)
	assert.Equal(t, "zebra", records[1].Hostname)
}

func TestCheckinsForPolicy_UnknownPolicyReturnsEmpty(t *testing.T) {
	store := newTestStore(t)
	records, err := store.CheckinsForPolicy("ghost")
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestDeleteOlderThan_RemovesOnlyStaleRecords(t *testing.T) {
	store := newTestStore(t)
	stale := time.Now().Add(-2 * time.Hour)
	fresh := time.Now()
	require.NoError(t, store.RecordCheckin("policy-1", "stale-host", stale))
	require.NoError(t, store.RecordCheckin("policy-1", "fresh-host", fresh))

	deleted, err := store.DeleteOlderThan(time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	records, err := store.CheckinsForPolicy("policy-1")
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "fresh-host", records[0].Hostname)
}

func TestDeleteOlderThan_ExactlyAtCutoffIsNotDeleted(t *testing.T) {
	store := newTestStore(t)
	cutoff := time.Now().Add(-time.Hour).Truncate(time.Second)
	require.NoError(t, store.RecordCheckin("policy-1", "host-a", cutoff))

	deleted, err := store.DeleteOlderThan(cutoff)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted, "a record exactly at cutoff is not strictly older than it")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./storage/policyserver/... -v`
Expected: FAIL — the package doesn't exist yet (build error: no Go files in `storage/policyserver`).

- [ ] **Step 3: Write `models.go`**

```go
package policyserver

import "time"

// CheckinRecord is the most recent time hostname received policyID from
// GetPolicies. One row per (PolicyID, Hostname) pair -- upserted on every
// check-in rather than appended, so listing a policy's hosts is a direct
// scan with no aggregation, and a host that stops checking in ages out on
// its own once its one row passes the retention cutoff.
type CheckinRecord struct {
	PolicyID   string `gorm:"primaryKey"`
	Hostname   string `gorm:"primaryKey"`
	LastSeenAt time.Time
}
```

- [ ] **Step 4: Write `db.go`**

```go
package policyserver

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

func openDB(varDir string) (*gorm.DB, error) {
	if err := os.MkdirAll(varDir, 0755); err != nil {
		return nil, fmt.Errorf("create var dir: %w", err)
	}

	dbPath := filepath.Join(varDir, "policy-server.sqlite") + "?_busy_timeout=5000"

	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	db, err := gorm.Open(sqlite.Dialector{Conn: sqlDB}, &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("gorm open: %w", err)
	}

	if err := db.AutoMigrate(&CheckinRecord{}); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("automigrate: %w", err)
	}
	return db, nil
}
```

- [ ] **Step 5: Write `store.go`**

```go
package policyserver

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

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

// RecordCheckin upserts hostname's check-in for policyID, setting
// LastSeenAt to at -- overwrites any existing row for the same
// (policyID, hostname) pair rather than appending. GORM's Save does not
// insert a new row for an already-set composite primary key, so the
// upsert must go through an explicit ON CONFLICT clause.
func (s *Store) RecordCheckin(policyID, hostname string, at time.Time) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "policy_id"}, {Name: "hostname"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_seen_at"}),
	}).Create(&CheckinRecord{PolicyID: policyID, Hostname: hostname, LastSeenAt: at}).Error
}

// CheckinsForPolicy returns every host that has checked in for policyID,
// ordered by hostname, each already holding its most recent check-in time
// (see CheckinRecord). Returns an empty slice, not an error, for a policyID
// with no check-ins.
func (s *Store) CheckinsForPolicy(policyID string) ([]CheckinRecord, error) {
	var out []CheckinRecord
	err := s.db.Where("policy_id = ?", policyID).Order("hostname").Find(&out).Error
	return out, err
}

// DeleteOlderThan removes every check-in whose LastSeenAt is strictly
// before cutoff, returning how many rows were removed.
func (s *Store) DeleteOlderThan(cutoff time.Time) (int64, error) {
	res := s.db.Where("last_seen_at < ?", cutoff).Delete(&CheckinRecord{})
	return res.RowsAffected, res.Error
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd src && go test ./storage/policyserver/... -v`
Expected: PASS (all tests from Step 1).

- [ ] **Step 7: Commit**

```bash
git add src/storage/policyserver
git commit -m "$(cat <<'EOF'
feat(storage): add policyserver check-in store

New SQLite-backed store tracking the most recent time each host
received each policy, ready for policy-server's GetPolicies/
ListPolicies to record and surface check-ins.
EOF
)"
```

---

### Task 2: `CheckinRetentionSec` config key

**Files:**
- Modify: `src/common/config/config.go:120` (add field), `:167` (add default), `:408-417` (add parse case)
- Modify (tests): `src/common/config/config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `config.Config.CheckinRetentionSec int` — consumed by Task 6's cleanup goroutine wiring in `cmd/policy-server/main.go`.

- [ ] **Step 1: Write the failing tests**

Add to `src/common/config/config_test.go` (mirroring the adjacent `AdhocPolicyTimeoutSec` tests):

```go
func TestParseConfig_CheckinRetentionSecDefaultsTo86400(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlog_dir=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 86400, conf.CheckinRetentionSec)
}

func TestParseConfig_CheckinRetentionSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\nCheckinRetentionSec=3600\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 3600, conf.CheckinRetentionSec)
}

func TestParseConfig_CheckinRetentionSecRejectsZeroOrNegative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlog_dir=/tmp\nCheckinRetentionSec=0\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	_, err := ParseConfig(path)
	require.Error(t, err)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./common/config/... -run TestParseConfig_CheckinRetentionSec -v`
Expected: FAIL — `conf.CheckinRetentionSec` doesn't exist (compile error) and/or `CheckinRetentionSec` is an unknown key.

- [ ] **Step 3: Add the field, default, and parse case**

In `src/common/config/config.go`, add to the `Config` struct (after `AdhocPolicyTimeoutSec`, line 120):

```go
	AdhocPolicyTimeoutSec            int
	CheckinRetentionSec              int
}
```

Add to the defaults literal inside `ParseConfig` (after `AdhocPolicyTimeoutSec: 3600,`, line 167):

```go
		AdhocPolicyTimeoutSec:            3600,
		CheckinRetentionSec:              86400,
	}
```

Add a parse case (after the `"AdhocPolicyTimeoutSec"` case, before `default:`, around line 417):

```go
		case "CheckinRetentionSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid CheckinRetentionSec value at line %d: %s", lineNum, value)
			}
			if number <= 0 {
				return nil, fmt.Errorf("CheckinRetentionSec must be positive at line %d: %s", lineNum, value)
			}
			config.CheckinRetentionSec = number
			foundFields["CheckinRetentionSec"] = true
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS, including every pre-existing config test (no regressions).

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): add CheckinRetentionSec

New local.conf key for policy-server's check-in cleanup routine,
default 86400s (24h), validated positive like AdhocPolicyTimeoutSec.
EOF
)"
```

---

### Task 3: Proto — `PolicyCheckin` / `Policy.checkins`

**Files:**
- Modify: `src/api/policyserver.proto`
- Generated (via `make proto`, do not hand-edit): `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go`

**Interfaces:**
- Produces: `pb.PolicyCheckin{Hostname string; LastSeenAt *timestamppb.Timestamp}` and `pb.Policy.Checkins []*pb.PolicyCheckin` (with generated `GetHostname()`, `GetLastSeenAt()`, `GetCheckins()` accessors) — consumed by Task 5 (`ListPolicies`) and Task 7 (`api-server` DTO).

- [ ] **Step 1: Add the new message and field to the proto**

In `src/api/policyserver.proto`, add a new message near the other small messages (after `ObjectFilter`, before `Policy`):

```proto
message PolicyCheckin {
  string hostname = 1;
  google.protobuf.Timestamp last_seen_at = 2;
}
```

Add a field to `Policy` (it currently ends at field 15, `storage_policy_id`):

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7; // derived, read-only -- see below
  string id = 8;
  ClientFilters client_filters = 9;
  string type = 10;
  reserved 11; reserved "hostname"; // formerly hostname -- removed, see below
  int32 port = 12;
  string config = 13;
  google.protobuf.Timestamp disabled_at = 14;
  string storage_policy_id = 15; // backup policy only, required
  repeated PolicyCheckin checkins = 16; // ListPolicies only -- see below
}
```

- [ ] **Step 2: Regenerate the Go protobuf code**

Run: `make proto`
Expected: regenerates `src/api/policyserver.pb.go` and `src/api/policyserver_grpc.pb.go` with no errors; `git diff --stat src/api/` shows both files changed.

- [ ] **Step 3: Verify the build**

Run: `cd src && go build ./...`
Expected: succeeds (no code references `pb.PolicyCheckin`/`Policy.Checkins` yet, so this only confirms the regenerated code itself compiles).

- [ ] **Step 4: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go
git commit -m "$(cat <<'EOF'
feat(api): add PolicyCheckin / Policy.checkins to policy-server proto

New repeated field on Policy carrying each host that has checked in
for it and when, populated by ListPolicies only. Not yet wired to any
handler.
EOF
)"
```

---

### Task 4: `GetPolicies` records a check-in per matched policy (fail-closed)

**Files:**
- Modify: `src/cmd/policy-server/server.go:1-82` (imports, struct, constructor, `GetPolicies`)
- Modify: `src/cmd/policy-server/main.go:1-94` (open store, pass to constructor)
- Modify: `src/cmd/policy-server/server_test.go:79-84` (`newTestServerWithPolicies` helper)
- Modify: `src/cmd/policy-server/write_test.go:51-56` (`newTestWriteServer` helper)

**Interfaces:**
- Consumes: `checkinstore.New(varDir string) (*checkinstore.Store, error)`, `(*checkinstore.Store).RecordCheckin(policyID, hostname string, at time.Time) error` (Task 1); `config.Config.CheckinRetentionSec` unused here (Task 6 uses it), `config.ResolveVarDir` (already exists).
- Produces: `NewPolicyServerServer(cache *Cache, policiesDir string, logger *slog.Logger, checkins *checkinstore.Store) *policyServerServer` — new 4th parameter every caller (including Task 5's `ListPolicies` work, already in the same struct) relies on via `s.checkins`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/server_test.go` (after `TestGetPolicies_ResponseFieldsRoundTrip`):

```go
func TestGetPolicies_RecordsCheckinForEachMatchedPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]},
		"storage_policy_id": "sp-1"
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	checkins := newTestCheckinStore(t)
	srv := NewPolicyServerServer(c, dir, testLogger(), checkins)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	policyID := resp.Policies[0].Id

	records, err := checkins.CheckinsForPolicy(policyID)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, "web-01", records[0].Hostname)
}

func TestGetPolicies_CheckinStoreFailureFailsTheRPC(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]},
		"storage_policy_id": "sp-1"
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	checkins := newTestCheckinStore(t)
	require.NoError(t, checkins.Close()) // force every subsequent write to fail
	srv := NewPolicyServerServer(c, dir, testLogger(), checkins)

	_, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	assert.Error(t, err, "a check-in write failure must fail GetPolicies, not be swallowed")
}
```

Add the shared test helper (used by this file and Task 5/write_test.go) to `src/cmd/policy-server/server_test.go`, next to `newTestServerWithPolicies`:

```go
func newTestCheckinStore(t *testing.T) *checkinstore.Store {
	t.Helper()
	store, err := checkinstore.New(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}
```

Update `newTestServerWithPolicies` in the same file to pass a check-in store:

```go
func newTestServerWithPolicies(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, dir, testLogger(), newTestCheckinStore(t))
}
```

Add the import to `server_test.go`'s import block:

```go
	checkinstore "github.com/alex-sviridov/miniprotector/storage/policyserver"
```

Update `newTestWriteServer` in `src/cmd/policy-server/write_test.go` the same way:

```go
func newTestWriteServer(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, dir, testLogger(), newTestCheckinStore(t))
}
```

(`newTestCheckinStore` is defined in `server_test.go`; both files are `package main` in the same test binary, so it's already visible to `write_test.go` — no separate import needed there.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go build ./cmd/policy-server/...`
Expected: FAIL — `NewPolicyServerServer` doesn't accept a 4th argument yet, `checkinstore` is undefined.

- [ ] **Step 3: Update `server.go`**

Modify the import block (add after the existing `mtls` import):

```go
import (
	"context"
	"log/slog"
	"sync"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	checkinstore "github.com/alex-sviridov/miniprotector/storage/policyserver"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)
```

Modify the struct and constructor:

```go
type policyServerServer struct {
	pb.UnimplementedPolicyServiceServer
	cache       *Cache
	policiesDir string
	logger      *slog.Logger
	checkins    *checkinstore.Store

	// writeMu serializes CreatePolicy/UpdatePolicy/DeletePolicy against each
	// other. gRPC dispatches each unary RPC to its own goroutine, so without
	// this, two concurrent writes could race: one RPC's Reload can glob+parse
	// a stale snapshot of the directory before another RPC's write lands on
	// disk, then overwrite the cache with that stale snapshot after the other
	// RPC's own (fresher) Reload already ran -- silently reverting the other
	// write from the in-memory cache even though its file is correctly on
	// disk. Readers (GetPolicies/ListPolicies) only ever call Cache.Policies(),
	// never Reload, so they're unaffected and stay fully concurrent via
	// Cache's own sync.RWMutex.
	writeMu sync.Mutex
}

func NewPolicyServerServer(cache *Cache, policiesDir string, logger *slog.Logger, checkins *checkinstore.Store) *policyServerServer {
	return &policyServerServer{cache: cache, policiesDir: policiesDir, logger: logger, checkins: checkins}
}
```

Modify `GetPolicies` to record a check-in per matched policy, fixing `now` once for the whole call and failing closed on a store error:

```go
func (s *policyServerServer) GetPolicies(ctx context.Context, _ *pb.GetPoliciesRequest) (*pb.GetPoliciesResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not determine peer identity", "error", err)
		return nil, err
	}

	jobID, err := jobid.FromIncoming(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: job-id metadata required", "hostname", hostname, "error", err)
		return nil, err
	}

	labels, err := mtls.PeerAttributes(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not read peer attributes", "hostname", hostname, "job_id", jobID, "error", err)
		return nil, err
	}

	now := time.Now()
	var matched []*pb.Policy
	for _, p := range s.cache.Policies() {
		if isDisabled(p.Meta(), now) {
			continue
		}
		if !p.Matches(hostname, labels) {
			continue
		}
		pp := p.ToProto(false)
		attachDestination(pp, s.cache)
		if err := s.checkins.RecordCheckin(pp.GetId(), hostname, now); err != nil {
			s.logger.Error("GetPolicies: failed to record check-in", "hostname", hostname, "job_id", jobID, "policy_id", pp.GetId(), "error", err)
			return nil, status.Error(codes.Internal, "failed to record check-in")
		}
		matched = append(matched, pp)
	}

	s.logger.Info("GetPolicies", "hostname", hostname, "job_id", jobID, "matched", len(matched))
	return &pb.GetPoliciesResponse{Policies: matched}, nil
}
```

- [ ] **Step 4: Wire the store into `main.go`**

Add the store import to `src/cmd/policy-server/main.go`'s import block (`"time"` is intentionally not added here — nothing in this task uses it yet, and an unused import is a compile error, not just a lint warning; Task 6 adds `"time"` when it actually needs it):

```go
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
	checkinstore "github.com/alex-sviridov/miniprotector/storage/policyserver"
	"google.golang.org/grpc"
)
```

After the existing `cache.Reload(...)` block and before `certsDir, err := config.ResolveCertsDir()`, open the check-in store:

```go
	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		logger.Error("var directory resolution failed", "error", err)
		os.Exit(1)
	}
	checkins, err := checkinstore.New(varDir)
	if err != nil {
		logger.Error("failed to open check-in store", "error", err)
		os.Exit(1)
	}
	defer checkins.Close()
```

Update the `NewPolicyServerServer` call:

```go
	srv := NewPolicyServerServer(cache, policiesDir, logger, checkins)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go build ./... && go test ./cmd/policy-server/... -v`
Expected: PASS — every existing `policy-server` test (unaffected by the new 4th constructor argument, now supplied everywhere) plus the two new tests from Step 1.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/main.go src/cmd/policy-server/server_test.go src/cmd/policy-server/write_test.go
git commit -m "$(cat <<'EOF'
feat(policy-server): record a check-in for every policy GetPolicies returns

Each matched policy upserts (policy_id, hostname, now) into the new
check-in store before being added to the response. A store write
failure fails the whole RPC (fail-closed), matching GetPolicies's
existing error-handling shape.
EOF
)"
```

---

### Task 5: `ListPolicies` attaches check-ins

**Files:**
- Modify: `src/cmd/policy-server/server.go` (add `attachCheckins`, call it from `ListPolicies`)
- Modify: `src/cmd/policy-server/server_test.go` (new tests)

**Interfaces:**
- Consumes: `(*checkinstore.Store).CheckinsForPolicy(policyID string) ([]checkinstore.CheckinRecord, error)` (Task 1); `pb.Policy.Checkins`, `pb.PolicyCheckin` (Task 3); `s.checkins`, `s.logger` (Task 4's struct fields).
- Produces: `attachCheckins(pp *pb.Policy, store *checkinstore.Store, logger *slog.Logger)` — free function, same shape as the existing `attachDestination`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/server_test.go`:

```go
func TestListPolicies_IncludesCheckins(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]},
		"storage_policy_id": "sp-1"
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	checkins := newTestCheckinStore(t)
	srv := NewPolicyServerServer(c, dir, testLogger(), checkins)

	getResp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, getResp.Policies, 1)

	listResp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, listResp.Policies, 1)
	require.Len(t, listResp.Policies[0].Checkins, 1)
	assert.Equal(t, "web-01", listResp.Policies[0].Checkins[0].Hostname)
	assert.NotNil(t, listResp.Policies[0].Checkins[0].LastSeenAt)
}

func TestListPolicies_NoCheckinsYieldsEmptyCheckins(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"storage_policy_id": "sp-1"
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Empty(t, resp.Policies[0].Checkins)
}

func TestGetPolicies_NeverEchoesCheckins(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]},
		"storage_policy_id": "sp-1"
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Empty(t, resp.Policies[0].Checkins, "GetPolicies must not populate checkins, only ListPolicies does")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestListPolicies_IncludesCheckins|TestListPolicies_NoCheckinsYieldsEmptyCheckins|TestGetPolicies_NeverEchoesCheckins' -v`
Expected: FAIL — `listResp.Policies[0].Checkins` is always empty (nothing attaches it yet); the third test passes vacuously today but is here to lock the behavior in before it could regress.

- [ ] **Step 3: Add `attachCheckins` and call it from `ListPolicies`**

In `src/cmd/policy-server/server.go`, add the `timestamppb` import:

```go
	"google.golang.org/protobuf/types/known/timestamppb"
```

Add the function (after `attachDestination`):

```go
// attachCheckins populates pp.Checkins from store's per-host check-in
// records for pp's id. Called only by ListPolicies -- GetPolicies never
// echoes checkins back, the same way it never echoes client_filters. A
// lookup failure is logged and leaves pp.Checkins empty rather than
// failing the whole ListPolicies call -- the same "loud skip, don't block
// the rest" treatment this codebase already gives a single malformed
// policy file during Cache.Reload.
func attachCheckins(pp *pb.Policy, store *checkinstore.Store, logger *slog.Logger) {
	records, err := store.CheckinsForPolicy(pp.GetId())
	if err != nil {
		logger.Error("ListPolicies: failed to load checkins", "policy_id", pp.GetId(), "error", err)
		return
	}
	for _, r := range records {
		pp.Checkins = append(pp.Checkins, &pb.PolicyCheckin{
			Hostname:   r.Hostname,
			LastSeenAt: timestamppb.New(r.LastSeenAt),
		})
	}
}
```

Update `ListPolicies` to call it:

```go
func (s *policyServerServer) ListPolicies(ctx context.Context, req *pb.ListPoliciesRequest) (*pb.ListPoliciesResponse, error) {
	policies := s.cache.Policies()
	var out []*pb.Policy
	for _, p := range policies {
		if req.GetType() != "" && p.Kind() != req.GetType() {
			continue
		}
		pp := p.ToProto(true)
		attachDestination(pp, s.cache)
		attachCheckins(pp, s.checkins, s.logger)
		out = append(out, pp)
	}
	s.logger.Info("ListPolicies", "type", req.GetType(), "count", len(out))
	return &pb.ListPoliciesResponse{Policies: out}, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS — full `policy-server` suite, including the three new tests.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go
git commit -m "$(cat <<'EOF'
feat(policy-server): ListPolicies surfaces each policy's checkins

GetPolicies still never echoes checkins back, same as client_filters.
EOF
)"
```

---

### Task 6: Cleanup goroutine

**Files:**
- Create: `src/cmd/policy-server/checkin.go`
- Create: `src/cmd/policy-server/checkin_test.go`
- Modify: `src/cmd/policy-server/main.go` (start the goroutine)

**Interfaces:**
- Consumes: `(*checkinstore.Store).DeleteOlderThan(cutoff time.Time) (int64, error)` (Task 1); `conf.CheckinRetentionSec` (Task 2); `checkins` (Task 4's `main.go` wiring); `signalCtx` (already constructed in `main.go`).
- Produces: `runCheckinCleanup(ctx context.Context, store *checkinstore.Store, interval, retention time.Duration, logger *slog.Logger)`, `checkinCleanupInterval = time.Minute` constant.

- [ ] **Step 1: Write the failing test**

Create `src/cmd/policy-server/checkin_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunCheckinCleanup_RemovesRecordsOlderThanRetention(t *testing.T) {
	store := newTestCheckinStore(t)
	require.NoError(t, store.RecordCheckin("policy-1", "stale-host", time.Now().Add(-time.Hour)))
	require.NoError(t, store.RecordCheckin("policy-1", "fresh-host", time.Now()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runCheckinCleanup(ctx, store, 5*time.Millisecond, 10*time.Minute, testLogger())

	require.Eventually(t, func() bool {
		records, err := store.CheckinsForPolicy("policy-1")
		return err == nil && len(records) == 1 && records[0].Hostname == "fresh-host"
	}, 2*time.Second, 10*time.Millisecond, "cleanup should remove only the stale record")
}

func TestRunCheckinCleanup_StopsWhenContextCancelled(t *testing.T) {
	store := newTestCheckinStore(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runCheckinCleanup(ctx, store, 5*time.Millisecond, time.Minute, testLogger())
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCheckinCleanup did not return after context cancellation")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/policy-server/... -run TestRunCheckinCleanup -v`
Expected: FAIL — `runCheckinCleanup` is undefined.

- [ ] **Step 3: Write `checkin.go`**

```go
// checkin.go runs policy-server's background check-in cleanup: on a fixed
// tick, delete every CheckinRecord older than the configured retention
// window. See docs/superpowers/specs/2026-08-03-policy-checkin-tracking-design.md.
package main

import (
	"context"
	"log/slog"
	"time"

	checkinstore "github.com/alex-sviridov/miniprotector/storage/policyserver"
)

// checkinCleanupInterval is how often the cleanup tick fires -- fixed, not
// configurable. Only the retention window (how old a record must be to be
// deleted) is a config value.
const checkinCleanupInterval = time.Minute

// runCheckinCleanup deletes check-in records older than retention every
// interval, until ctx is cancelled. Mirrors watchForReload's ticker-driven
// background-loop shape.
func runCheckinCleanup(ctx context.Context, store *checkinstore.Store, interval, retention time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := store.DeleteOlderThan(time.Now().Add(-retention))
			if err != nil {
				logger.Error("checkin cleanup failed", "error", err)
				continue
			}
			if deleted > 0 {
				logger.Info("checkin cleanup removed stale check-ins", "count", deleted)
			}
		}
	}
}
```

- [ ] **Step 4: Start the goroutine from `main.go`**

Add `"time"` to `src/cmd/policy-server/main.go`'s import block (first use in this file — Task 4 deliberately left it out since nothing there needed it yet):

```go
import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	checkinstore "github.com/alex-sviridov/miniprotector/storage/policyserver"
	"google.golang.org/grpc"
)
```

After the `go func() { watchForReload(...) }()` block, add:

```go
	go runCheckinCleanup(signalCtx, checkins, checkinCleanupInterval, time.Duration(conf.CheckinRetentionSec)*time.Second, logger)
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd src && go build ./... && go test ./cmd/policy-server/... -v`
Expected: PASS — full `policy-server` suite, including both new cleanup tests.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/checkin.go src/cmd/policy-server/checkin_test.go src/cmd/policy-server/main.go
git commit -m "$(cat <<'EOF'
feat(policy-server): purge stale check-ins on a 1-minute tick

New background goroutine deletes CheckinRecords older than
CheckinRetentionSec every fixed minute, started alongside the
existing policy-reload fsnotify watcher.
EOF
)"
```

---

### Task 7: `api-server` REST DTO — `checkins`

**Files:**
- Modify: `src/cmd/api-server/policies.go:25-40` (`policyDTO`), `:42-69` (`toPolicyDTO`)
- Modify: `src/cmd/api-server/policies_test.go` (new test)

**Interfaces:**
- Consumes: `pb.Policy.Checkins`, `pb.PolicyCheckin.Hostname`/`.LastSeenAt` (Task 3).
- Produces: `policyDTO.Checkins []checkinDTO` — REST response shape for `GET /api/v1/policies` and `GET /api/v1/policies/{id}` (both already route through `toPolicyDTO`, no handler changes needed).

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/api-server/policies_test.go` (near `TestToPolicyDTO_IncludesStorageFields`):

```go
func TestToPolicyDTO_IncludesCheckins(t *testing.T) {
	p := &pb.Policy{
		Id:   "p1",
		Name: "nightly",
		Type: "backup",
		Checkins: []*pb.PolicyCheckin{
			{Hostname: "web-01", LastSeenAt: timestamppb.New(time.Unix(1752400000, 0))},
			{Hostname: "web-02", LastSeenAt: timestamppb.New(time.Unix(1752400010, 0))},
		},
	}

	dto := toPolicyDTO(p)

	require.Len(t, dto.Checkins, 2)
	assert.Equal(t, "web-01", dto.Checkins[0].Hostname)
	assert.Equal(t, int64(1752400000), dto.Checkins[0].LastSeenAt)
	assert.Equal(t, "web-02", dto.Checkins[1].Hostname)
	assert.Equal(t, int64(1752400010), dto.Checkins[1].LastSeenAt)
}

func TestToPolicyDTO_NoCheckinsYieldsEmptySlice(t *testing.T) {
	p := &pb.Policy{Id: "p1", Name: "nightly", Type: "backup"}

	dto := toPolicyDTO(p)

	assert.Empty(t, dto.Checkins)
}
```

(`require` must already be imported in `policies_test.go`; if it isn't, add `"github.com/stretchr/testify/require"` to the import block.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/api-server/... -run TestToPolicyDTO_IncludesCheckins -v`
Expected: FAIL — `policyDTO` has no `Checkins` field (compile error).

- [ ] **Step 3: Add `checkinDTO` and wire it into `policyDTO`/`toPolicyDTO`**

In `src/cmd/api-server/policies.go`, add after `objectFilterDTO`:

```go
type checkinDTO struct {
	Hostname   string `json:"hostname"`
	LastSeenAt int64  `json:"last_seen_at"`
}
```

Add a field to `policyDTO` (after `DisabledAt`):

```go
type policyDTO struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	CreatedAt       int64             `json:"created_at"`
	UpdatedAt       int64             `json:"updated_at"`
	ClientFilters   clientFiltersDTO  `json:"client_filters"`
	ObjectFilters   []objectFilterDTO `json:"object_filters"`
	RPO             string            `json:"rpo"`
	BackupWindow    []string          `json:"backup_window"`
	Destination     string            `json:"destination"`
	StoragePolicyID string            `json:"storage_policy_id,omitempty"`
	Type            string            `json:"type"`
	Port            int32             `json:"port"`
	Config          string            `json:"config"`
	DisabledAt      int64             `json:"disabled_at,omitempty"`
	Checkins        []checkinDTO      `json:"checkins"`
}
```

Update `toPolicyDTO` to populate it (add alongside `objectFilters` construction):

```go
func toPolicyDTO(p *pb.Policy) policyDTO {
	objectFilters := make([]objectFilterDTO, len(p.GetObjectFilters()))
	for i, f := range p.GetObjectFilters() {
		objectFilters[i] = objectFilterDTO{ID: f.GetId(), Path: f.GetPath(), Include: f.GetInclude(), Exclude: f.GetExclude()}
	}
	checkins := make([]checkinDTO, len(p.GetCheckins()))
	for i, c := range p.GetCheckins() {
		checkins[i] = checkinDTO{Hostname: c.GetHostname(), LastSeenAt: c.GetLastSeenAt().AsTime().Unix()}
	}
	dto := policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters:   objectFilters,
		RPO:             p.GetRpo(),
		BackupWindow:    p.GetBackupWindow(),
		Destination:     p.GetDestination(),
		StoragePolicyID: p.GetStoragePolicyId(),
		Type:            p.GetType(),
		Port:            p.GetPort(),
		Config:          p.GetConfig(),
		Checkins:        checkins,
	}
	if p.GetDisabledAt() != nil {
		dto.DisabledAt = p.GetDisabledAt().AsTime().Unix()
	}
	return dto
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — full `api-server` suite, including the two new tests. `TestToPolicyDTO_ConvertsTimestampsToUnixSecondsAndClientFilters` and `TestToPolicyDTO_IncludesStorageFields` (pre-existing) must still pass unchanged since they don't set `Checkins` and now expect an empty (not nil-vs-empty-mismatched) slice — `make([]checkinDTO, 0)` from a `nil`/empty `p.GetCheckins()` marshals to `[]` in JSON either way, so no assertion changes needed there.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "$(cat <<'EOF'
feat(api-server): surface policy checkins in the REST policy DTO

GET /api/v1/policies and GET /api/v1/policies/{id} both already route
through ListPolicies internally, so both endpoints pick up checkins
with no handler changes beyond the DTO mapping.
EOF
)"
```

---

### Task 8: Documentation and changelog

**Files:**
- Modify: `docs/components/policy-server.md`
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Update `docs/components/policy-server.md`**

Replace the claim in the intro paragraph (currently: "`policy-server` is bootstrapped and certificate-managed exactly like any other node in the mesh (`client-manager add`, `agent` + `issuer` refresh) — it holds no database and calls no other service.") with:

```markdown
`policy-server` is bootstrapped and certificate-managed exactly like any other node in the mesh
(`client-manager add`, `agent` + `issuer` refresh) — it calls no other service, but now owns one
piece of local state: a SQLite database recording check-ins (see
[Check-in tracking](#check-in-tracking) below).
```

Add a new subsection (after "Disabling a policy without deleting it", before "## Configuration Keys"):

```markdown
### Check-in tracking

Every time `GetPolicies` hands a policy to a host, `policy-server` upserts a row —
`(policy, hostname, last_seen_at)` — into a local SQLite database at
`<var-dir>/policy-server.sqlite`. One row exists per `(policy, hostname)` pair: a host re-polling
the same policy overwrites its own row's timestamp rather than adding a new one, so the table always
holds each host's *most recent* check-in per policy, not a full history. This covers every policy
type `GetPolicies` returns (`"backup"` and `"storage"` alike). If the check-in write fails,
`GetPolicies` fails the whole call — check-in tracking is not best-effort telemetry the caller's
policies can silently proceed without.

`ListPolicies` attaches each policy's current check-in rows (host + last-seen timestamp) to the
response; `GetPolicies` never does, the same way it never echoes back `client_filters`.

A background routine ticks every fixed 1 minute and deletes any check-in row whose `last_seen_at` is
older than `CheckinRetentionSec` (config key, default `86400` = 24h). A host that stops polling a
policy — decommissioned, or no longer matched — simply ages out of that policy's check-in list once
its one row passes the retention window. See
[Design: Policy Check-in Tracking](../superpowers/specs/2026-08-03-policy-checkin-tracking-design.md).
```

Add to the `## Configuration Keys` list:

```markdown
- `CheckinRetentionSec` — how long a check-in row survives with no re-poll before the cleanup
  routine removes it *(default: 86400)*
```

Add a bullet to `## See Also`:

```markdown
- [Design: Policy Check-in Tracking](../superpowers/specs/2026-08-03-policy-checkin-tracking-design.md)
```

- [ ] **Step 2: Update `docs/protocols/policy-server.md`**

Add `PolicyCheckin` and the `checkins` field to the `## RPC` proto block, matching Task 3's actual proto changes (insert `message PolicyCheckin { ... }` after `ObjectFilter`, and `repeated PolicyCheckin checkins = 16;` as `Policy`'s last field, with the same comment used in the .proto file).

Add a bullet to `## Behavior` (after the `ListPoliciesRequest.type` bullet):

```markdown
- `Policy.checkins` is populated only by `ListPolicies` -- `GetPolicies`, `CreatePolicy`, and
  `UpdatePolicy` always leave it empty, the same way `GetPolicies`'s response never echoes back
  `client_filters`. Each entry is one host's most recent check-in for that policy (`hostname` +
  `last_seen_at`) -- `GetPolicies` upserts one such row per policy it returns to a caller, on every
  call. See [Design: Policy Check-in Tracking](../superpowers/specs/2026-08-03-policy-checkin-tracking-design.md).
```

Add a bullet to `## See Also`:

```markdown
- [Design: Policy Check-in Tracking](../superpowers/specs/2026-08-03-policy-checkin-tracking-design.md)
```

- [ ] **Step 3: Update `docs/api/rest-v1.md`**

Add `"checkins"` to the `GET /api/v1/policies` example JSON block, and a sentence documenting it:

```json
      "port": 0,
      "config": "",
      "checkins": [
        {"hostname": "web-01", "last_seen_at": 1752400500}
      ]
```

Add, after the `` `created_at`/`updated_at` are Unix seconds `` sentence:

```markdown
`checkins` lists every host that has received this policy from `GetPolicies`, each with its most
recent check-in time (Unix seconds) -- not a full history, one entry per host. Empty for a policy no
host has polled yet.
```

- [ ] **Step 4: Update `docs/ARCHITECTURE.md`**

In the `## Components` table, update the `policy-server` row's description (currently: "Serves backup policies filtered by a requesting client's hostname and attribute labels; no database, reads labels from the peer cert") to:

```markdown
| policy-server | Serves backup policies filtered by a requesting client's hostname and attribute labels, reading labels from the peer cert; tracks per-host check-ins in a local SQLite database | Implemented (`agent` fetches, caches, and now acts on its policies — deriving and running scheduled `brfs` backups via `policyclient`) |
```

In the `policy-server` paragraph (the one starting "`policy-server` is control plane by role..."), add a sentence at the end:

```markdown
It also now owns local state -- a SQLite database recording which hosts have checked in for which
policy and when, purged on a fixed 1-minute cleanup tick -- the first piece of persistent state this
component has held; it still calls no other service. See
[policy-server](components/policy-server.md#check-in-tracking).
```

- [ ] **Step 5: Add a `CHANGELOG.md` entry**

Add to the top of `CHANGELOG.md` (after the `# Changelog` header and its intro line, before the current first entry), dated today:

```markdown
## 2026-08-03 — policy-server: check-in tracking and cleanup

`policy-server` now records, in a local SQLite database, the most recent time each host received
each policy from `GetPolicies` -- one upserted row per `(policy, hostname)` pair, covering both
backup and storage policy types. A check-in write failure fails the whole `GetPolicies` call rather
than being silently dropped. `ListPolicies` (and therefore `api-server`'s `GET /api/v1/policies` /
`GET /api/v1/policies/{id}`) now returns each policy's current check-in list; `GetPolicies` itself
never echoes it back. A background routine, ticking every fixed 1 minute, purges check-ins older than
the new `CheckinRetentionSec` config key (default 24h), so a host that stops polling a policy ages out
of its check-in list on its own.
```

- [ ] **Step 6: Commit**

```bash
git add docs/components/policy-server.md docs/protocols/policy-server.md docs/api/rest-v1.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: document policy check-in tracking

Updates policy-server's component/protocol docs, the REST API
reference, the architecture overview, and the changelog for the
new SQLite-backed check-in store, ListPolicies.checkins, and the
1-minute cleanup routine.
EOF
)"
```

---

## Final verification

- [ ] **Run the full test suite**

Run: `cd src && go build ./... && go test ./... -v 2>&1 | tail -100`
Expected: PASS across every package, no build failures, no skipped/failed tests introduced by this plan.

- [ ] **Sanity-check the demo environment still boots** (optional, if you have it running): `policy-server`'s new SQLite file should appear at `<var-dir>/policy-server.sqlite` after the container starts and a node polls at least once.
