# Policy Management API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `policy-server` full CRUD over its own policy files via new gRPC RPCs, and expose that through `api-server` as a REST surface, so a browser (or any REST client) can list, create, edit, and delete backup policies without hand-editing JSON files on `policy-server`'s host.

**Architecture:** `policy-server` gains `ListPolicies` (unfiltered, admin-facing — distinct from the existing identity-scoped `GetPolicies`), `CreatePolicy`, `UpdatePolicy`, and `DeletePolicy`. Every write validates in-memory first (reusing `parsePolicyFile`'s existing validation, extracted into `validatePolicy`), writes atomically (temp file + rename), then synchronously reloads its own in-memory cache before responding — no persistence layer beyond the existing flat JSON files, no new persistent actor. `api-server` adds a thin 1:1 REST proxy in front of the four new RPCs, following its existing `/clients` and `/catalog` conventions exactly (bearer-token auth, `{"data": [...]}` envelopes, the existing `writeGRPCError` status mapping).

**Tech Stack:** Go, gRPC/protobuf (`google.golang.org/grpc`), `github.com/google/uuid` (already a `policy-server` dependency), `testify`.

## Global Constraints

- IDs are addressed by `id` (the existing deterministic UUID) for `Update`/`Delete` — never by `name` or `path`.
- No optimistic-concurrency check on `Update`/`Delete` — last-write-wins.
- `GetPolicies`'s response keeps omitting `client_filters` — only `ListPolicies`/`CreatePolicy`/`UpdatePolicy` populate it. This is a privacy invariant (a mesh node must never learn another node's targeting rules), not an oversight — every task touching this must preserve it.
- Writes never touch the `.changed` sentinel file — they call `Cache.Reload` directly, in-process, before responding.
- `Update` overwrites the same on-disk file (filename unchanged) so a policy's `id` stays stable across content edits.
- No new `common/config` keys — `PolicyServerHost`/`PolicyServerPort` already exist (`src/common/config/config.go:108-109`) and are already set in both `demo/local.conf` and `deploy/control-plane/api-server/local.conf` (`policy_server_host=policy-server`).
- Proto messages needing an "empty" response follow this codebase's existing convention of a locally-defined empty message (see `catalog.proto`'s `SyncResponse {}`) — not `google.protobuf.Empty`.

---

### Task 1: `policyserver.proto` — add the write/list RPC surface

**Files:**
- Modify: `src/api/policyserver.proto`
- Regenerate: `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go` (via `make proto`)

**Interfaces:**
- Produces: `pb.PolicyServiceClient`/`pb.PolicyServiceServer` gain `ListPolicies`, `CreatePolicy`, `UpdatePolicy`, `DeletePolicy`; `pb.Policy` gains `GetClientFilters() *pb.ClientFilters`; new types `pb.ClientFilters`, `pb.ListPoliciesRequest`, `pb.ListPoliciesResponse`, `pb.CreatePolicyRequest`, `pb.UpdatePolicyRequest`, `pb.DeletePolicyRequest`, `pb.DeletePolicyResponse` — consumed by Task 2 (`policy-server` handlers) and Task 7 (`api-server` client interface).

- [ ] **Step 1: Replace `src/api/policyserver.proto`**

```proto
syntax = "proto3";

package policyserverservice;

option go_package = "./proto";

import "google/protobuf/timestamp.proto";

// PolicyService is policy-server's RPC surface. GetPolicies answers "which
// policies apply to me?" for a mesh node, derived entirely from its verified
// mTLS identity. ListPolicies/CreatePolicy/UpdatePolicy/DeletePolicy are the
// admin surface api-server proxies for browsing and editing the full policy
// set -- unlike GetPolicies, these are never called by a mesh node itself.
service PolicyService {
  rpc GetPolicies(GetPoliciesRequest) returns (GetPoliciesResponse);
  rpc ListPolicies(ListPoliciesRequest) returns (ListPoliciesResponse);
  rpc CreatePolicy(CreatePolicyRequest) returns (Policy);
  rpc UpdatePolicy(UpdatePolicyRequest) returns (Policy);
  rpc DeletePolicy(DeletePolicyRequest) returns (DeletePolicyResponse);
}

message GetPoliciesRequest {}

message GetPoliciesResponse {
  repeated Policy policies = 1;
}

message ListPoliciesRequest {}

message ListPoliciesResponse {
  repeated Policy policies = 1;
}

message ClientFilters {
  repeated string hostnames = 1;
  map<string, string> labels = 2;
}

message ObjectFilter {
  string path = 1;
  repeated string include = 2;
  repeated string exclude = 3;
  // policy-server-computed, deterministic (see Policy.id). Not present in
  // the on-disk policy JSON schema.
  string id = 4;
}

message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  // Duration string, e.g. "24h" (time.ParseDuration format). policy-server
  // never parses or evaluates this -- opaque pass-through data.
  string rpo = 5;
  // List of cron expressions (5-field). policy-server never parses or
  // evaluates these -- opaque pass-through data.
  repeated string backup_window = 6;
  string destination = 7;
  // policy-server-computed, deterministic from the policy file's name --
  // stable across reloads, changes only if the file is renamed. Not
  // present in the on-disk policy JSON schema.
  string id = 8;
  // Only ever populated by ListPolicies/CreatePolicy/UpdatePolicy -- omitted
  // by GetPolicies so a node never learns another node's targeting rules
  // from a policy that already matched its own identity.
  ClientFilters client_filters = 9;
}

message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  // Any id set on an entry here is ignored -- object filter IDs are always
  // server-computed from their position in this list.
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  string destination = 6;
}

message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  // Full replacement of object_filters, not a patch -- reordering or
  // inserting entries changes the affected filters' ids.
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
}

message DeletePolicyRequest {
  string id = 1;
}

message DeletePolicyResponse {}
```

- [ ] **Step 2: Regenerate and verify**

Run: `make proto`
Expected: `Protobuf code generated in src/api/` printed, no errors.

Run: `grep -n "func.*GetClientFilters\|ListPolicies\|CreatePolicy\|UpdatePolicy\|DeletePolicy" src/api/policyserver.pb.go | head -20`
Expected: non-empty output showing the new generated getters/types.

- [ ] **Step 3: Confirm the whole tree still builds**

Run: `cd src && go build ./...`
Expected: succeeds (nothing consumes the new RPCs yet, so this only proves the regenerated proto package itself compiles).

- [ ] **Step 4: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go
git commit -m "feat(api): add ListPolicies/Create/Update/DeletePolicy to PolicyService"
```

---

### Task 2: `policy-server` — prep: shared validation, `SourcePath` tracking, cache lookups, `policiesDir` on the server

**Files:**
- Modify: `src/cmd/policy-server/policy.go`
- Modify: `src/cmd/policy-server/policy_test.go`
- Modify: `src/cmd/policy-server/cache.go`
- Modify: `src/cmd/policy-server/cache_test.go`
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/server_test.go`
- Modify: `src/cmd/policy-server/main.go`

**Interfaces:**
- Consumes: Task 1's regenerated proto package (no new types used yet, just confirms it still builds alongside these changes).
- Produces: `validatePolicy(p Policy) error`, `Policy.SourcePath string`, `Cache.FindByID(id string) (Policy, bool)`, `Cache.FindBySourcePath(path string) (Policy, bool)`, `NewPolicyServerServer(cache *Cache, policiesDir string, logger *slog.Logger) *policyServerServer` (signature change) — all consumed by Task 3 (`ListPolicies`) and Tasks 4-6 (the write RPCs).

- [ ] **Step 1: Write the failing tests**

In `src/cmd/policy-server/policy_test.go`, add at the end of the file:

```go
func TestValidatePolicy_ValidPolicyReturnsNil(t *testing.T) {
	p := Policy{
		Metadata:      Metadata{Name: "ok"},
		ClientFilters: ClientFilters{Hostnames: []string{"web-*"}},
		ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"*.sql"}, Exclude: []string{"*.tmp"}}},
	}
	assert.NoError(t, validatePolicy(p))
}

func TestValidatePolicy_MissingNameFails(t *testing.T) {
	assert.Error(t, validatePolicy(Policy{}))
}

func TestValidatePolicy_InvalidHostnamePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ClientFilters: ClientFilters{Hostnames: []string{"["}}}
	assert.Error(t, validatePolicy(p))
}

func TestValidatePolicy_InvalidIncludePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"["}}}}
	assert.Error(t, validatePolicy(p))
}

func TestValidatePolicy_InvalidExcludePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ObjectFilters: []ObjectFilter{{Path: "/data", Exclude: []string{"["}}}}
	assert.Error(t, validatePolicy(p))
}
```

Add one assertion to the end of the existing `TestParsePolicyFile_ValidPolicyParsesAllFields` (after its last `assert.Equal` line):

```go
	assert.Equal(t, path, p.SourcePath)
```

In `src/cmd/policy-server/cache_test.go`, add at the end of the file:

```go
func TestCache_FindByIDReturnsMatchingPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	want := c.Policies()[0]
	got, ok := c.FindByID(want.Metadata.ID)
	require.True(t, ok)
	assert.Equal(t, "policy-a", got.Metadata.Name)
	assert.Equal(t, filepath.Join(dir, "a.json"), got.SourcePath)
}

func TestCache_FindByIDUnknownIDReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.FindByID("does-not-exist")
	assert.False(t, ok)
}

func TestCache_FindBySourcePathReturnsMatchingPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got, ok := c.FindBySourcePath(filepath.Join(dir, "a.json"))
	require.True(t, ok)
	assert.Equal(t, "policy-a", got.Metadata.Name)
}

func TestCache_FindBySourcePathUnknownPathReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.FindBySourcePath("/does/not/exist.json")
	assert.False(t, ok)
}
```

In `src/cmd/policy-server/server_test.go`, change `newTestServerWithPolicies`:

```go
func newTestServerWithPolicies(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, dir, testLogger())
}
```
(only the `NewPolicyServerServer` call changes — `dir` is now passed as the second argument.)

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: FAIL — compile errors (`validatePolicy` undefined, `Policy.SourcePath` undefined, `Cache.FindByID`/`FindBySourcePath` undefined, `NewPolicyServerServer` called with wrong arg count).

- [ ] **Step 3: Implement in `policy.go`**

Add `SourcePath` to the `Policy` struct:

```go
type Policy struct {
	Metadata      Metadata       `json:"metadata"`
	ClientFilters ClientFilters  `json:"client_filters"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	SourcePath    string         `json:"-"`
}
```

Replace `parsePolicyFile` with a version that extracts validation into `validatePolicy` and sets `SourcePath`:

```go
// validatePolicy checks the fields an operator can set on a policy,
// independent of where it came from (a file on disk, via parsePolicyFile,
// or a CreatePolicy/UpdatePolicy RPC request): metadata.name must be
// non-empty, and every client_filters.hostnames/object_filters include/
// exclude glob pattern must be syntactically valid (path.Match's syntax).
func validatePolicy(p Policy) error {
	if p.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	for _, pattern := range p.ClientFilters.Hostnames {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid hostname pattern %q: %w", pattern, err)
		}
	}
	for _, of := range p.ObjectFilters {
		for _, pattern := range of.Include {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("invalid include pattern %q: %w", pattern, err)
			}
		}
		for _, pattern := range of.Exclude {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
			}
		}
	}
	return nil
}

// parsePolicyFile reads and validates a single policy JSON file -- see
// validatePolicy for the validation rules applied.
func parsePolicyFile(filePath string) (Policy, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Policy{}, fmt.Errorf("read %s: %w", filePath, err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if err := validatePolicy(p); err != nil {
		return Policy{}, fmt.Errorf("%s: %w", filePath, err)
	}

	policyUUID := uuid.NewSHA1(policyIDNamespace, []byte(filepath.Base(filePath)))
	p.Metadata.ID = policyUUID.String()
	p.SourcePath = filePath
	for i := range p.ObjectFilters {
		p.ObjectFilters[i].ID = uuid.NewSHA1(policyUUID, []byte(strconv.Itoa(i))).String()
	}

	return p, nil
}
```

(The import block is unchanged — `validatePolicy` uses only `fmt` and `path`, both already imported.)

- [ ] **Step 4: Implement in `cache.go`**

In the `Policies()` deep-copy loop, add `SourcePath` to the reconstructed `Policy` literal:

```go
		out[i] = Policy{
			Metadata: p.Metadata, // plain types: string, time.Time, time.Time
			ClientFilters: ClientFilters{
				Hostnames: make([]string, len(p.ClientFilters.Hostnames)),
				Labels:    make(map[string]string, len(p.ClientFilters.Labels)),
			},
			ObjectFilters: make([]ObjectFilter, len(p.ObjectFilters)),
			RPO:           p.RPO, // plain string
			BackupWindow:  make([]string, len(p.BackupWindow)),
			Destination:   p.Destination, // plain string
			SourcePath:    p.SourcePath,  // plain string
		}
```

Add two new methods at the end of `cache.go`:

```go
// FindByID returns the currently-loaded policy with the given Metadata.ID.
// Used by UpdatePolicy/DeletePolicy, which address a policy by its
// caller-facing ID rather than its on-disk filename.
func (c *Cache) FindByID(id string) (Policy, bool) {
	for _, p := range c.Policies() {
		if p.Metadata.ID == id {
			return p, true
		}
	}
	return Policy{}, false
}

// FindBySourcePath returns the currently-loaded policy parsed from exactly
// this file path. Used by CreatePolicy to look up the policy it just wrote,
// once Reload has re-parsed it and computed its ID.
func (c *Cache) FindBySourcePath(path string) (Policy, bool) {
	for _, p := range c.Policies() {
		if p.SourcePath == path {
			return p, true
		}
	}
	return Policy{}, false
}
```

- [ ] **Step 5: Implement in `server.go` and `main.go`**

In `src/cmd/policy-server/server.go`, change the struct and constructor:

```go
type policyServerServer struct {
	pb.UnimplementedPolicyServiceServer
	cache       *Cache
	policiesDir string
	logger      *slog.Logger
}

func NewPolicyServerServer(cache *Cache, policiesDir string, logger *slog.Logger) *policyServerServer {
	return &policyServerServer{cache: cache, policiesDir: policiesDir, logger: logger}
}
```

In `src/cmd/policy-server/main.go`, update the call site:

```go
	srv := NewPolicyServerServer(cache, policiesDir, logger)
```
(replaces the existing `srv := NewPolicyServerServer(cache, logger)` line — `policiesDir` is already in scope from the earlier `config.ResolvePoliciesDir()` call.)

- [ ] **Step 6: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS — all tests green, including the new `TestValidatePolicy_*`, `TestCache_FindByID*`, `TestCache_FindBySourcePath*` tests.

Run: `cd src && go build ./...`
Expected: succeeds.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/policy-server/policy.go src/cmd/policy-server/policy_test.go \
        src/cmd/policy-server/cache.go src/cmd/policy-server/cache_test.go \
        src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go \
        src/cmd/policy-server/main.go
git commit -m "refactor(policy-server): extract validatePolicy, track SourcePath, add cache lookups by ID"
```

---

### Task 3: `policy-server` — implement `ListPolicies`

**Files:**
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/server_test.go`

**Interfaces:**
- Consumes: Task 1's `pb.ListPoliciesRequest`/`pb.ListPoliciesResponse`/`pb.ClientFilters`; Task 2's `Cache.Policies()` (unchanged).
- Produces: `toProtoPolicyAdmin(p Policy) *pb.Policy`, `(*policyServerServer) ListPolicies(...)` — `toProtoPolicyAdmin` is reused by Tasks 4-6 (`CreatePolicy`/`UpdatePolicy` responses).

- [ ] **Step 1: Write the failing tests**

In `src/cmd/policy-server/server_test.go`, add at the end of the file:

```go
func TestListPolicies_ReturnsAllPoliciesRegardlessOfIdentity(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	writePolicyFile(t, dir, "db.json", `{
		"metadata": {"name": "db-policy"},
		"client_filters": {"labels": {"role": "db"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	assert.Len(t, resp.Policies, 2)
}

func TestListPolicies_IncludesClientFilters(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, []string{"web-*"}, resp.Policies[0].ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, resp.Policies[0].ClientFilters.Labels)
}

func TestGetPolicies_StillOmitsClientFilters(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "web.json", `{
		"metadata": {"name": "web-policy"},
		"client_filters": {"hostnames": ["web-*"]}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "web-01", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Nil(t, resp.Policies[0].ClientFilters)
}
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestListPolicies|TestGetPolicies_StillOmits' -v`
Expected: FAIL — compile error (`srv.ListPolicies` undefined).

- [ ] **Step 3: Implement in `server.go`**

Add, right after `toProtoPolicy`:

```go
func toProtoClientFilters(cf ClientFilters) *pb.ClientFilters {
	return &pb.ClientFilters{Hostnames: cf.Hostnames, Labels: cf.Labels}
}

// toProtoPolicyAdmin is toProtoPolicy plus client_filters -- used by every
// RPC except GetPolicies (ListPolicies, CreatePolicy, UpdatePolicy), where
// an operator editing the full policy set needs to see and change
// client_filters. GetPolicies keeps using toProtoPolicy so a matched node
// never learns another node's targeting rules from a policy that already
// matched its own identity.
func toProtoPolicyAdmin(p Policy) *pb.Policy {
	pp := toProtoPolicy(p)
	pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	return pp
}

// ListPolicies returns every currently-loaded policy, unfiltered by any
// caller identity -- the admin surface api-server proxies for browsing and
// editing the full policy set. Unlike GetPolicies, it is never called by a
// mesh node itself.
func (s *policyServerServer) ListPolicies(ctx context.Context, _ *pb.ListPoliciesRequest) (*pb.ListPoliciesResponse, error) {
	policies := s.cache.Policies()
	out := make([]*pb.Policy, len(policies))
	for i, p := range policies {
		out[i] = toProtoPolicyAdmin(p)
	}
	s.logger.Info("ListPolicies", "count", len(out))
	return &pb.ListPoliciesResponse{Policies: out}, nil
}
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go
git commit -m "feat(policy-server): add ListPolicies, an unfiltered admin view of every policy"
```

---

### Task 4: `policy-server` — implement `CreatePolicy`

**Files:**
- Create: `src/cmd/policy-server/write.go`
- Create: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: Task 1's `pb.CreatePolicyRequest`; Task 2's `validatePolicy`, `Cache.Reload`, `Cache.FindBySourcePath`, `policyServerServer.policiesDir`; Task 3's `toProtoPolicyAdmin`.
- Produces: `slugify(name string) string`, `uniqueFilename(dir, slug string) (string, error)`, `atomicWriteJSON(path string, v any) error`, `fromProtoClientFilters`, `fromProtoObjectFilters`, `(*policyServerServer) CreatePolicy(...)` — `atomicWriteJSON`/`fromProtoClientFilters`/`fromProtoObjectFilters` are reused by Task 5 (`UpdatePolicy`).

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/policy-server/write_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func TestSlugify(t *testing.T) {
	assert.Equal(t, "nightly-db-backup", slugify("Nightly DB Backup!"))
	assert.Equal(t, "a-b-c", slugify("  a__b--c  "))
	assert.Equal(t, "", slugify("!!!"))
}

func TestUniqueFilename_ReturnsBaseWhenFree(t *testing.T) {
	dir := t.TempDir()
	got, err := uniqueFilename(dir, "nightly-db-backup")
	require.NoError(t, err)
	assert.Equal(t, "nightly-db-backup.json", got)
}

func TestUniqueFilename_AppendsSuffixOnCollision(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly-db-backup.json", `{}`)
	got, err := uniqueFilename(dir, "nightly-db-backup")
	require.NoError(t, err)
	assert.Equal(t, "nightly-db-backup-2.json", got)
}

func TestUniqueFilename_SkipsMultipleCollisions(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "x.json", `{}`)
	writePolicyFile(t, dir, "x-2.json", `{}`)
	got, err := uniqueFilename(dir, "x")
	require.NoError(t, err)
	assert.Equal(t, "x-3.json", got)
}

func newTestWriteServer(t *testing.T, dir string) *policyServerServer {
	t.Helper()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	return NewPolicyServerServer(c, dir, testLogger())
}

func TestCreatePolicy_WritesFileAndReturnsPolicyWithID(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "Nightly DB Backup",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/var/lib/postgres"}},
		Rpo:           "24h",
		BackupWindow:  []string{"0 2 * * *"},
		Destination:   "bwfs:8080",
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "Nightly DB Backup", resp.Name)
	require.Len(t, resp.ObjectFilters, 1)
	assert.NotEmpty(t, resp.ObjectFilters[0].Id)

	data, err := os.ReadFile(filepath.Join(dir, "nightly-db-backup.json"))
	require.NoError(t, err)
	var onDisk map[string]any
	require.NoError(t, json.Unmarshal(data, &onDisk))
	assert.Equal(t, "Nightly DB Backup", onDisk["metadata"].(map[string]any)["name"])
}

func TestCreatePolicy_SecondCallWithSameNameGetsDistinctFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	req := &pb.CreatePolicyRequest{Name: "dup", Destination: "bwfs:8080"}
	first, err := srv.CreatePolicy(context.Background(), req)
	require.NoError(t, err)
	second, err := srv.CreatePolicy(context.Background(), req)
	require.NoError(t, err)

	assert.NotEqual(t, first.Id, second.Id)
	_, err = os.Stat(filepath.Join(dir, "dup.json"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "dup-2.json"))
	require.NoError(t, err)
}

func TestCreatePolicy_MissingNameReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_InvalidGlobPatternReturnsInvalidArgumentAndWritesNoFile(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "broken",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/data", Include: []string{"["}}},
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no file should be written when validation fails")
}

func TestCreatePolicy_ClientFiltersRoundTrip(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "web",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
	})

	require.NoError(t, err)
	require.NotNil(t, resp.ClientFilters)
	assert.Equal(t, []string{"web-*"}, resp.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, resp.ClientFilters.Labels)
}
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestSlugify|TestUniqueFilename|TestCreatePolicy' -v`
Expected: FAIL — compile errors (`slugify`, `uniqueFilename`, `srv.CreatePolicy` undefined).

- [ ] **Step 3: Create `write.go`**

```go
// The write RPCs (CreatePolicy, UpdatePolicy, DeletePolicy): policy-server
// is the sole writer of its own policy files, so a write RPC validates the
// proposed content, atomically writes/removes the file, then synchronously
// reloads its own in-memory cache before responding -- the caller only ever
// sees a state the cache has already picked up. See
// docs/superpowers/specs/2026-07-18-policy-management-api-design.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lowercases name and collapses every run of non-alphanumeric
// characters into a single "-", trimming any leading/trailing "-" --
// "Nightly DB Backup!" -> "nightly-db-backup".
func slugify(name string) string {
	slug := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(slug, "-")
}

// uniqueFilename returns a filename in dir based on slug that doesn't
// already exist: "<slug>.json" if free, otherwise "<slug>-2.json",
// "<slug>-3.json", etc.
func uniqueFilename(dir, slug string) (string, error) {
	candidate := slug + ".json"
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("check %s: %w", filepath.Join(dir, candidate), err)
		}
		candidate = fmt.Sprintf("%s-%d.json", slug, i)
	}
}

// atomicWriteJSON marshals v and writes it to path via a temp file in the
// same directory followed by os.Rename, so a concurrent Cache.Reload (or an
// operator's own read) never observes a half-written file. The temp file's
// create/rename does generate fsnotify events, but watchForReload filters
// every event down to the exact ".changed" path, so this produces no
// spurious reload.
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

func fromProtoClientFilters(cf *pb.ClientFilters) ClientFilters {
	return ClientFilters{Hostnames: cf.GetHostnames(), Labels: cf.GetLabels()}
}

func fromProtoObjectFilters(filters []*pb.ObjectFilter) []ObjectFilter {
	out := make([]ObjectFilter, len(filters))
	for i, f := range filters {
		out[i] = ObjectFilter{Path: f.GetPath(), Include: f.GetInclude(), Exclude: f.GetExclude()}
	}
	return out
}

// CreatePolicy validates req, allocates a filename from a slug of the
// policy's name (appending "-2", "-3", ... on collision), and atomically
// writes the new policy file before reloading the cache. The filename it
// picks is permanent for that policy's lifetime -- it's what the policy's
// id derives from.
func (s *policyServerServer) CreatePolicy(ctx context.Context, req *pb.CreatePolicyRequest) (*pb.Policy, error) {
	now := time.Now().UTC()
	p := Policy{
		Metadata:      Metadata{Name: req.GetName(), CreatedAt: now, UpdatedAt: now},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
		ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
		RPO:           req.GetRpo(),
		BackupWindow:  req.GetBackupWindow(),
		Destination:   req.GetDestination(),
	}
	if err := validatePolicy(p); err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	slug := slugify(p.Metadata.Name)
	if slug == "" {
		return nil, status.Error(codes.InvalidArgument, "name must contain at least one alphanumeric character")
	}
	filename, err := uniqueFilename(s.policiesDir, slug)
	if err != nil {
		s.logger.Error("CreatePolicy: filename allocation failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to allocate a policy filename")
	}
	filePath := filepath.Join(s.policiesDir, filename)

	if err := atomicWriteJSON(filePath, p); err != nil {
		s.logger.Error("CreatePolicy: write failed", "path", filePath, "error", err)
		return nil, status.Error(codes.Internal, "failed to write policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("CreatePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after write")
	}

	created, ok := s.cache.FindBySourcePath(filePath)
	if !ok {
		return nil, status.Error(codes.Internal, "policy not found in cache after create")
	}
	s.logger.Info("CreatePolicy", "id", created.Metadata.ID, "name", created.Metadata.Name, "path", filePath)
	return toProtoPolicyAdmin(created), nil
}
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go
git commit -m "feat(policy-server): add CreatePolicy, validating and atomically writing a new policy file"
```

---

### Task 5: `policy-server` — implement `UpdatePolicy`

**Files:**
- Modify: `src/cmd/policy-server/write.go`
- Modify: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: Task 1's `pb.UpdatePolicyRequest`; Task 4's `atomicWriteJSON`, `fromProtoClientFilters`, `fromProtoObjectFilters`; Task 2's `Cache.FindByID`, `Cache.FindBySourcePath`.
- Produces: `(*policyServerServer) UpdatePolicy(...)`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/write_test.go`:

```go
func TestUpdatePolicy_OverwritesFileKeepsIDAndCreatedAt(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly", "created_at": "2026-07-01T00:00:00Z", "updated_at": "2026-07-01T00:00:00Z"},
		"object_filters": [{"path": "/old"}],
		"destination": "bwfs:8080"
	}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	resp, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:            original.Metadata.ID,
		Name:          "nightly-renamed",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/new"}},
		Destination:   "bwfs:9090",
	})

	require.NoError(t, err)
	assert.Equal(t, original.Metadata.ID, resp.Id, "id must stay stable across an update")
	assert.Equal(t, "nightly-renamed", resp.Name)
	assert.Equal(t, "bwfs:9090", resp.Destination)
	assert.Equal(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), resp.CreatedAt.AsTime())
	assert.NotEqual(t, time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), resp.UpdatedAt.AsTime())
}

func TestUpdatePolicy_UnknownIDReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{Id: "does-not-exist", Name: "x"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestUpdatePolicy_InvalidInputReturnsInvalidArgumentLeavesFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	before, err := os.ReadFile(filepath.Join(dir, "nightly.json"))
	require.NoError(t, err)

	_, err = srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{Id: original.Metadata.ID, Name: ""})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
	after, err := os.ReadFile(filepath.Join(dir, "nightly.json"))
	require.NoError(t, err)
	assert.Equal(t, before, after, "file must be unchanged when validation fails")
}
```

Add `"time"` to the import block of `write_test.go` (it currently imports `context`, `encoding/json`, `os`, `path/filepath`, `testing`, plus `testify`/`grpc` packages and `pb`).

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestUpdatePolicy -v`
Expected: FAIL — compile error (`srv.UpdatePolicy` undefined).

- [ ] **Step 3: Implement in `write.go`**

Add after `CreatePolicy`:

```go
// UpdatePolicy fully replaces an existing policy's editable fields,
// identified by id. The on-disk filename -- and therefore the policy's id,
// which derives from it -- never changes; only the file's content does.
// CreatedAt is preserved from the existing record; UpdatedAt is set to now.
func (s *policyServerServer) UpdatePolicy(ctx context.Context, req *pb.UpdatePolicyRequest) (*pb.Policy, error) {
	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}

	p := Policy{
		Metadata:      Metadata{Name: req.GetName(), CreatedAt: existing.Metadata.CreatedAt, UpdatedAt: time.Now().UTC()},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
		ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
		RPO:           req.GetRpo(),
		BackupWindow:  req.GetBackupWindow(),
		Destination:   req.GetDestination(),
	}
	if err := validatePolicy(p); err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := atomicWriteJSON(existing.SourcePath, p); err != nil {
		s.logger.Error("UpdatePolicy: write failed", "path", existing.SourcePath, "error", err)
		return nil, status.Error(codes.Internal, "failed to write policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("UpdatePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after write")
	}

	updated, ok := s.cache.FindBySourcePath(existing.SourcePath)
	if !ok {
		return nil, status.Error(codes.Internal, "policy not found in cache after update")
	}
	s.logger.Info("UpdatePolicy", "id", updated.Metadata.ID, "name", updated.Metadata.Name, "path", existing.SourcePath)
	return toProtoPolicyAdmin(updated), nil
}
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go
git commit -m "feat(policy-server): add UpdatePolicy, replacing a policy's content by id"
```

---

### Task 6: `policy-server` — implement `DeletePolicy`

**Files:**
- Modify: `src/cmd/policy-server/write.go`
- Modify: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: Task 1's `pb.DeletePolicyRequest`/`pb.DeletePolicyResponse`; Task 2's `Cache.FindByID`.
- Produces: `(*policyServerServer) DeletePolicy(...)`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/write_test.go`:

```go
func TestDeletePolicy_RemovesFileAndReloads(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: original.Metadata.ID})

	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "nightly.json"))
	assert.True(t, os.IsNotExist(err))
	assert.Empty(t, srv.cache.Policies())
}

func TestDeletePolicy_UnknownIDReturnsNotFound(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: "does-not-exist"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.NotFound, st.Code())
}

func TestDeletePolicy_LeavesOtherPoliciesIntact(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "policy-b"}}`)
	srv := newTestWriteServer(t, dir)
	var target Policy
	for _, p := range srv.cache.Policies() {
		if p.Metadata.Name == "policy-a" {
			target = p
		}
	}

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: target.Metadata.ID})

	require.NoError(t, err)
	remaining := srv.cache.Policies()
	require.Len(t, remaining, 1)
	assert.Equal(t, "policy-b", remaining[0].Metadata.Name)
}
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/policy-server/... -run TestDeletePolicy -v`
Expected: FAIL — compile error (`srv.DeletePolicy` undefined).

- [ ] **Step 3: Implement in `write.go`**

Add after `UpdatePolicy`:

```go
// DeletePolicy removes the policy file backing id and reloads the cache.
func (s *policyServerServer) DeletePolicy(ctx context.Context, req *pb.DeletePolicyRequest) (*pb.DeletePolicyResponse, error) {
	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}

	if err := os.Remove(existing.SourcePath); err != nil {
		s.logger.Error("DeletePolicy: remove failed", "path", existing.SourcePath, "error", err)
		return nil, status.Error(codes.Internal, "failed to remove policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("DeletePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after delete")
	}

	s.logger.Info("DeletePolicy", "id", req.GetId(), "path", existing.SourcePath)
	return &pb.DeletePolicyResponse{}, nil
}
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS — all tests green.

Run: `cd src && go build ./... && go vet ./...`
Expected: both succeed (the pre-existing, unrelated `go vet` warning in `cmd/brfs/filesstream.go` is out of scope and may still appear).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go
git commit -m "feat(policy-server): add DeletePolicy, removing a policy file by id"
```

---

### Task 7: `api-server` — wire a `policyServiceClient` into `server.go` and `main.go`

**Files:**
- Modify: `src/cmd/api-server/server.go`
- Modify: `src/cmd/api-server/main.go`
- Modify: `src/cmd/api-server/clients_test.go`
- Modify: `src/cmd/api-server/catalog_test.go`

**Interfaces:**
- Consumes: Task 1's `pb.PolicyServiceClient` (specifically `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy`).
- Produces: `policyServiceClient` interface, `server.policy` field, `newServer(cm clientManagerClient, catalog catalogQueryClient, policy policyServiceClient, logger *slog.Logger) *server` (signature change) — consumed by Task 8-11 (the policy handlers) and every existing test that constructs a `server`.

This task has no new externally-observable behavior of its own (no new route is registered yet) — it's the plumbing Task 8 needs. Its "test" is that the whole existing api-server suite still compiles and passes after every `newServer` call site is updated.

- [ ] **Step 1: Update `server.go`**

Add the new interface, right after `catalogQueryClient`:

```go
// policyServiceClient is the subset of pb.PolicyServiceClient the policies
// handlers (Tasks 8-11) need -- api-server never calls GetPolicies, the
// identity-scoped RPC mesh nodes use.
type policyServiceClient interface {
	ListPolicies(ctx context.Context, in *pb.ListPoliciesRequest, opts ...grpc.CallOption) (*pb.ListPoliciesResponse, error)
	CreatePolicy(ctx context.Context, in *pb.CreatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error)
	UpdatePolicy(ctx context.Context, in *pb.UpdatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error)
	DeletePolicy(ctx context.Context, in *pb.DeletePolicyRequest, opts ...grpc.CallOption) (*pb.DeletePolicyResponse, error)
}
```

Change the `server` struct and `newServer`:

```go
type server struct {
	clientManager clientManagerClient
	catalog       catalogQueryClient
	policy        policyServiceClient
	logger        *slog.Logger
}

func newServer(cm clientManagerClient, catalog catalogQueryClient, policy policyServiceClient, logger *slog.Logger) *server {
	return &server{clientManager: cm, catalog: catalog, policy: policy, logger: logger}
}
```

- [ ] **Step 2: Update `main.go`**

Change the block that dials backends and constructs `srv`:

```go
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

	policyConn, err := connection.Connect(conf.PolicyServerHost, conf.PolicyServerPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to policy-server failed", "error", err)
		os.Exit(1)
	}
	defer policyConn.Close()

	srv := newServer(pb.NewClientManagerServiceClient(cmConn), pb.NewCatalogServiceClient(catalogConn), pb.NewPolicyServiceClient(policyConn), logger)
```

- [ ] **Step 3: Update existing test call sites**

In `src/cmd/api-server/clients_test.go`, change all four `newServer(fake, nil, testLogger())` calls to `newServer(fake, nil, nil, testLogger())`.

In `src/cmd/api-server/catalog_test.go`, change all four `newServer(nil, fake, testLogger())` calls to `newServer(nil, fake, nil, testLogger())`.

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — all existing tests green (nothing new to test yet).

Run: `cd src && go build ./...`
Expected: succeeds.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/api-server/server.go src/cmd/api-server/main.go \
        src/cmd/api-server/clients_test.go src/cmd/api-server/catalog_test.go
git commit -m "feat(api-server): wire an outbound connection and client interface for policy-server"
```

---

### Task 8: `api-server` — `GET /api/v1/policies` and `GET /api/v1/policies/{id}`

**Files:**
- Create: `src/cmd/api-server/policies.go`
- Create: `src/cmd/api-server/policies_test.go`
- Modify: `src/cmd/api-server/server.go`

**Interfaces:**
- Consumes: Task 7's `policyServiceClient`, `server.policy`.
- Produces: `policyDTO`, `clientFiltersDTO`, `objectFilterDTO`, `toPolicyDTO(*pb.Policy) policyDTO`, `fakePolicyServiceClient` (test double, reused by Tasks 9-11), `(*server) handleListPolicies`, `(*server) handleGetPolicy`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/api-server/policies_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type fakePolicyServiceClient struct {
	listResp *pb.ListPoliciesResponse
	listErr  error

	createResp    *pb.Policy
	createErr     error
	lastCreateReq *pb.CreatePolicyRequest

	updateResp    *pb.Policy
	updateErr     error
	lastUpdateReq *pb.UpdatePolicyRequest

	deleteResp    *pb.DeletePolicyResponse
	deleteErr     error
	lastDeleteReq *pb.DeletePolicyRequest
}

func (f *fakePolicyServiceClient) ListPolicies(ctx context.Context, in *pb.ListPoliciesRequest, opts ...grpc.CallOption) (*pb.ListPoliciesResponse, error) {
	return f.listResp, f.listErr
}

func (f *fakePolicyServiceClient) CreatePolicy(ctx context.Context, in *pb.CreatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error) {
	f.lastCreateReq = in
	return f.createResp, f.createErr
}

func (f *fakePolicyServiceClient) UpdatePolicy(ctx context.Context, in *pb.UpdatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error) {
	f.lastUpdateReq = in
	return f.updateResp, f.updateErr
}

func (f *fakePolicyServiceClient) DeletePolicy(ctx context.Context, in *pb.DeletePolicyRequest, opts ...grpc.CallOption) (*pb.DeletePolicyResponse, error) {
	f.lastDeleteReq = in
	return f.deleteResp, f.deleteErr
}

func TestHandleListPolicies_ReturnsDataEnvelope(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{
		Policies: []*pb.Policy{{Id: "p1", Name: "nightly", Destination: "bwfs:8080"}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	assert.Equal(t, "nightly", data[0].(map[string]any)["name"])
}

func TestHandleListPolicies_BackendErrorTranslated(t *testing.T) {
	fake := &fakePolicyServiceClient{listErr: status.Error(codes.Unavailable, "down")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandleGetPolicy_ReturnsMatchingPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{
		Policies: []*pb.Policy{
			{Id: "p1", Name: "nightly"},
			{Id: "p2", Name: "weekly"},
		},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies/p2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "weekly", body["name"])
}

func TestHandleGetPolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies/ghost", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestToPolicyDTO_ConvertsTimestampsToUnixSecondsAndClientFilters(t *testing.T) {
	p := &pb.Policy{
		Id:            "p1",
		Name:          "nightly",
		CreatedAt:     timestamppb.New(time.Unix(1752400000, 0)),
		UpdatedAt:     timestamppb.New(time.Unix(1752400010, 0)),
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
		ObjectFilters: []*pb.ObjectFilter{{Id: "f1", Path: "/data", Include: []string{"*.sql"}}},
		Rpo:           "24h",
		BackupWindow:  []string{"0 2 * * *"},
		Destination:   "bwfs:8080",
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, int64(1752400000), dto.CreatedAt)
	assert.Equal(t, int64(1752400010), dto.UpdatedAt)
	assert.Equal(t, []string{"web-*"}, dto.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, dto.ClientFilters.Labels)
	require.Len(t, dto.ObjectFilters, 1)
	assert.Equal(t, "f1", dto.ObjectFilters[0].ID)
	assert.Equal(t, "/data", dto.ObjectFilters[0].Path)
}
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/api-server/... -run 'TestHandleListPolicies|TestHandleGetPolicy|TestToPolicyDTO' -v`
Expected: FAIL — compile errors (`toPolicyDTO`, `handleListPolicies`, `handleGetPolicy` undefined; routes not registered).

- [ ] **Step 3: Create `policies.go`**

```go
package main

import (
	"fmt"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type clientFiltersDTO struct {
	Hostnames []string          `json:"hostnames"`
	Labels    map[string]string `json:"labels"`
}

type objectFilterDTO struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type policyDTO struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	CreatedAt     int64             `json:"created_at"`
	UpdatedAt     int64             `json:"updated_at"`
	ClientFilters clientFiltersDTO  `json:"client_filters"`
	ObjectFilters []objectFilterDTO `json:"object_filters"`
	RPO           string            `json:"rpo"`
	BackupWindow  []string          `json:"backup_window"`
	Destination   string            `json:"destination"`
}

func toPolicyDTO(p *pb.Policy) policyDTO {
	objectFilters := make([]objectFilterDTO, len(p.GetObjectFilters()))
	for i, f := range p.GetObjectFilters() {
		objectFilters[i] = objectFilterDTO{ID: f.GetId(), Path: f.GetPath(), Include: f.GetInclude(), Exclude: f.GetExclude()}
	}
	return policyDTO{
		ID:        p.GetId(),
		Name:      p.GetName(),
		CreatedAt: p.GetCreatedAt().AsTime().Unix(),
		UpdatedAt: p.GetUpdatedAt().AsTime().Unix(),
		ClientFilters: clientFiltersDTO{
			Hostnames: p.GetClientFilters().GetHostnames(),
			Labels:    p.GetClientFilters().GetLabels(),
		},
		ObjectFilters: objectFilters,
		RPO:           p.GetRpo(),
		BackupWindow:  p.GetBackupWindow(),
		Destination:   p.GetDestination(),
	}
}

func (s *server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	resp, err := s.policy.ListPolicies(r.Context(), &pb.ListPoliciesRequest{})
	if err != nil {
		s.logger.Error("handleListPolicies: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	policies := make([]policyDTO, len(resp.GetPolicies()))
	for i, p := range resp.GetPolicies() {
		policies[i] = toPolicyDTO(p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": policies})
}

func (s *server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	resp, err := s.policy.ListPolicies(r.Context(), &pb.ListPoliciesRequest{})
	if err != nil {
		s.logger.Error("handleGetPolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	for _, p := range resp.GetPolicies() {
		if p.GetId() == id {
			writeJSON(w, http.StatusOK, toPolicyDTO(p))
			return
		}
	}
	writeJSONError(w, http.StatusNotFound, fmt.Sprintf("policy %q not found", id))
}
```

- [ ] **Step 4: Register the routes**

In `src/cmd/api-server/server.go`'s `registerRoutes`, add two lines:

```go
	mux.HandleFunc("GET /api/v1/policies", s.handleListPolicies)
	mux.HandleFunc("GET /api/v1/policies/{id}", s.handleGetPolicy)
```

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — all tests green.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go src/cmd/api-server/server.go
git commit -m "feat(api-server): add GET /api/v1/policies and GET /api/v1/policies/{id}"
```

---

### Task 9: `api-server` — `POST /api/v1/policies`

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/policies_test.go`
- Modify: `src/cmd/api-server/server.go`

**Interfaces:**
- Consumes: Task 8's `policyDTO`, `clientFiltersDTO`, `fakePolicyServiceClient`.
- Produces: `objectFilterInput`, `policyInput`, `decodePolicyInput`, `toProtoClientFiltersInput`, `toProtoObjectFiltersInput` (reused by Task 10's `UpdatePolicy` handler), `(*server) handleCreatePolicy`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/policies_test.go` (add `"strings"` to the import block):

```go
func TestHandleCreatePolicy_ReturnsCreatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1", Name: "nightly", Destination: "bwfs:8080"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "nightly", fake.lastCreateReq.GetName())
	assert.Equal(t, "bwfs:8080", fake.lastCreateReq.GetDestination())
}

func TestHandleCreatePolicy_PassesClientAndObjectFiltersThrough(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web",
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www", "include": ["*.html"]}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, []string{"web-*"}, fake.lastCreateReq.GetClientFilters().GetHostnames())
	require.Len(t, fake.lastCreateReq.GetObjectFilters(), 1)
	assert.Equal(t, "/var/www", fake.lastCreateReq.GetObjectFilters()[0].GetPath())
}

func TestHandleCreatePolicy_MalformedJSONReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastCreateReq, "backend must not be called on malformed input")
}

func TestHandleCreatePolicy_BackendValidationErrorReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{createErr: status.Error(codes.InvalidArgument, "metadata.name is required")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleCreatePolicy -v`
Expected: FAIL — compile error (`handleCreatePolicy` undefined; route not registered).

- [ ] **Step 3: Implement in `policies.go`**

Add `"encoding/json"` to the import block. Add, at the end of the file:

```go
type objectFilterInput struct {
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type policyInput struct {
	Name          string              `json:"name"`
	ClientFilters clientFiltersDTO    `json:"client_filters"`
	ObjectFilters []objectFilterInput `json:"object_filters"`
	RPO           string              `json:"rpo"`
	BackupWindow  []string            `json:"backup_window"`
	Destination   string              `json:"destination"`
}

func decodePolicyInput(r *http.Request) (policyInput, error) {
	var in policyInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return policyInput{}, err
	}
	return in, nil
}

func toProtoClientFiltersInput(cf clientFiltersDTO) *pb.ClientFilters {
	return &pb.ClientFilters{Hostnames: cf.Hostnames, Labels: cf.Labels}
}

func toProtoObjectFiltersInput(filters []objectFilterInput) []*pb.ObjectFilter {
	out := make([]*pb.ObjectFilter, len(filters))
	for i, f := range filters {
		out[i] = &pb.ObjectFilter{Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	return out
}

func (s *server) handleCreatePolicy(w http.ResponseWriter, r *http.Request) {
	in, err := decodePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters: toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:           in.RPO,
		BackupWindow:  in.BackupWindow,
		Destination:   in.Destination,
	})
	if err != nil {
		s.logger.Error("handleCreatePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPolicyDTO(resp))
}
```

- [ ] **Step 4: Register the route**

In `src/cmd/api-server/server.go`'s `registerRoutes`, add:

```go
	mux.HandleFunc("POST /api/v1/policies", s.handleCreatePolicy)
```

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — all tests green.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go src/cmd/api-server/server.go
git commit -m "feat(api-server): add POST /api/v1/policies"
```

---

### Task 10: `api-server` — `PUT /api/v1/policies/{id}`

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/policies_test.go`
- Modify: `src/cmd/api-server/server.go`

**Interfaces:**
- Consumes: Task 9's `decodePolicyInput`, `toProtoClientFiltersInput`, `toProtoObjectFiltersInput`.
- Produces: `(*server) handleUpdatePolicy`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/policies_test.go`:

```go
func TestHandleUpdatePolicy_ReturnsUpdatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{Id: "p1", Name: "nightly-renamed"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly-renamed", "destination": "bwfs:9090"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/p1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	assert.Equal(t, "p1", fake.lastUpdateReq.GetId())
	assert.Equal(t, "nightly-renamed", fake.lastUpdateReq.GetName())
	assert.Equal(t, "bwfs:9090", fake.lastUpdateReq.GetDestination())
}

func TestHandleUpdatePolicy_MalformedJSONReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/p1", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastUpdateReq)
}

func TestHandleUpdatePolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{updateErr: status.Error(codes.NotFound, "policy \"ghost\" not found")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/ghost", strings.NewReader(`{"name": "x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleUpdatePolicy -v`
Expected: FAIL — compile error (`handleUpdatePolicy` undefined; route not registered).

- [ ] **Step 3: Implement in `policies.go`**

Add, after `handleCreatePolicy`:

```go
func (s *server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, err := decodePolicyInput(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.policy.UpdatePolicy(r.Context(), &pb.UpdatePolicyRequest{
		Id:            id,
		Name:          in.Name,
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters: toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:           in.RPO,
		BackupWindow:  in.BackupWindow,
		Destination:   in.Destination,
	})
	if err != nil {
		s.logger.Error("handleUpdatePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(resp))
}
```

- [ ] **Step 4: Register the route**

In `src/cmd/api-server/server.go`'s `registerRoutes`, add:

```go
	mux.HandleFunc("PUT /api/v1/policies/{id}", s.handleUpdatePolicy)
```

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — all tests green.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go src/cmd/api-server/server.go
git commit -m "feat(api-server): add PUT /api/v1/policies/{id}"
```

---

### Task 11: `api-server` — `DELETE /api/v1/policies/{id}`

**Files:**
- Modify: `src/cmd/api-server/policies.go`
- Modify: `src/cmd/api-server/policies_test.go`
- Modify: `src/cmd/api-server/server.go`

**Interfaces:**
- Consumes: Task 8's `fakePolicyServiceClient`.
- Produces: `(*server) handleDeletePolicy`.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/api-server/policies_test.go`:

```go
func TestHandleDeletePolicy_ReturnsNoContent(t *testing.T) {
	fake := &fakePolicyServiceClient{deleteResp: &pb.DeletePolicyResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/policies/p1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, fake.lastDeleteReq)
	assert.Equal(t, "p1", fake.lastDeleteReq.GetId())
}

func TestHandleDeletePolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{deleteErr: status.Error(codes.NotFound, "policy \"p1\" not found")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/policies/p1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/api-server/... -run TestHandleDeletePolicy -v`
Expected: FAIL — compile error (`handleDeletePolicy` undefined; route not registered).

- [ ] **Step 3: Implement in `policies.go`**

Add, after `handleUpdatePolicy`:

```go
func (s *server) handleDeletePolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	_, err := s.policy.DeletePolicy(r.Context(), &pb.DeletePolicyRequest{Id: id})
	if err != nil {
		s.logger.Error("handleDeletePolicy: backend call failed", "error", err)
		writeGRPCError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

- [ ] **Step 4: Register the route**

In `src/cmd/api-server/server.go`'s `registerRoutes`, add:

```go
	mux.HandleFunc("DELETE /api/v1/policies/{id}", s.handleDeletePolicy)
```

- [ ] **Step 5: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/api-server/... -v`
Expected: PASS — all tests green.

Run: `cd src && go build ./... && go vet ./...`
Expected: both succeed (the pre-existing, unrelated `go vet` warning in `cmd/brfs/filesstream.go` is out of scope).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go src/cmd/api-server/server.go
git commit -m "feat(api-server): add DELETE /api/v1/policies/{id}"
```

---

### Task 12: Deploy — start `api-server` after `policy-server`

**Files:**
- Modify: `demo/docker-compose.yml`
- Modify: `deploy/control-plane/docker-compose.yml`

**Interfaces:** none — compose configuration only, no code.

`connection.Connect` (used by Task 7's `main.go` change) blocks until the target is reachable or `ConnectionTimeOutSec` elapses; `api-server`'s existing `depends_on` already lists `clientmanager-api`/`catalog` for the same reason. `policy-server` needs the same treatment now that `api-server` dials it too.

- [ ] **Step 1: Update `demo/docker-compose.yml`**

Change the `api-server` service's `depends_on` block:

```yaml
    depends_on:
      - ca
      - issuer
      - clientmanager-api
      - catalog
```
to:
```yaml
    depends_on:
      - ca
      - issuer
      - clientmanager-api
      - catalog
      - policy-server
```

- [ ] **Step 2: Update `deploy/control-plane/docker-compose.yml`**

Change the `api-server` service's `depends_on` block:

```yaml
    depends_on:
      - step-ca
      - issuer
      - clientmanager-api
      - catalog
```
to:
```yaml
    depends_on:
      - step-ca
      - issuer
      - clientmanager-api
      - catalog
      - policy-server
```

- [ ] **Step 3: Verify the demo stack starts and the new endpoints answer**

Run: `cd demo && docker compose up -d --build && sleep 20`
Expected: all services report healthy/running (`docker compose ps`).

Run:
```bash
curl -s -H "Authorization: Bearer dev-placeholder-token-change-me" http://localhost:8090/api/v1/policies | head -c 500
```
Expected: `200` with a `{"data": [...]}` body listing the demo's seeded policies (`audit-logs`, `webserver-backup`, `database-backup`).

Run: `cd demo && docker compose down`

- [ ] **Step 4: Commit**

```bash
git add demo/docker-compose.yml deploy/control-plane/docker-compose.yml
git commit -m "fix(deploy): start api-server after policy-server, which it now dials"
```

---

### Task 13: Documentation and changelog

**Files:**
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/api-server.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none — documentation only, no code.

- [ ] **Step 1: Update `docs/protocols/policy-server.md`**

Replace the entire `## RPC` proto block with:

```proto
service PolicyService {
  rpc GetPolicies(GetPoliciesRequest) returns (GetPoliciesResponse);
  rpc ListPolicies(ListPoliciesRequest) returns (ListPoliciesResponse);
  rpc CreatePolicy(CreatePolicyRequest) returns (Policy);
  rpc UpdatePolicy(UpdatePolicyRequest) returns (Policy);
  rpc DeletePolicy(DeletePolicyRequest) returns (DeletePolicyResponse);
}

message GetPoliciesRequest {}

message GetPoliciesResponse {
  repeated Policy policies = 1;
}

message ListPoliciesRequest {}

message ListPoliciesResponse {
  repeated Policy policies = 1;
}

message ClientFilters {
  repeated string hostnames = 1;
  map<string, string> labels = 2;
}

message ObjectFilter {
  string path = 1;
  repeated string include = 2;
  repeated string exclude = 3;
  string id = 4;
}

message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string id = 8;
  ClientFilters client_filters = 9;
}

message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  string destination = 6;
}

message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
}

message DeletePolicyRequest {
  string id = 1;
}

message DeletePolicyResponse {}
```

Add a bullet to the end of the `## Behavior` section:

```markdown
- `ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy` are the admin surface `api-server`
  proxies for browsing and editing the full policy set — never called by a mesh node. Unlike
  `GetPolicies`, `ListPolicies`'s response (and `Create`/`UpdatePolicy`'s echoed-back result)
  includes `client_filters`. `Create`/`UpdatePolicy` validate the same way `parsePolicyFile` does
  (non-empty `metadata.name`, syntactically valid glob patterns) before writing anything; a write
  that fails validation returns `INVALID_ARGUMENT` and touches no file. `Update`/`Delete` address a
  policy by its `id`; `Update` keeps the on-disk filename (and therefore the `id`) unchanged,
  overwriting only the file's content. Every write reloads `policy-server`'s own in-memory cache
  synchronously before responding, bypassing the `.changed` sentinel entirely — that remains solely
  the mechanism for an operator's own manual, possibly multi-file, batch edits.
```

- [ ] **Step 2: Update `docs/components/policy-server.md`**

Change the opening line:

```markdown
Serves backup policies — static, operator-authored JSON files under `$MP_CONFIG_PATH/policies/` —
filtered to exactly the policies whose `client_filters` match a requesting client's verified
hostname and certificate-embedded attribute labels. See
[Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md).
```
to:
```markdown
Serves backup policies — JSON files under `$MP_CONFIG_PATH/policies/`, one per policy — filtered to
exactly the policies whose `client_filters` match a requesting client's verified hostname and
certificate-embedded attribute labels. Also exposes an admin write API
(`ListPolicies`/`CreatePolicy`/`UpdatePolicy`/`DeletePolicy`) that `api-server` proxies as REST, so
policies no longer have to be hand-edited on this host. See
[Design: Policy Server](../superpowers/specs/2026-07-10-policy-server-design.md) and
[Design: Policy Management API](../superpowers/specs/2026-07-18-policy-management-api-design.md).
```

Add a paragraph at the end of the `### Policy files and hot reload` section:

```markdown
Writes made through `CreatePolicy`/`UpdatePolicy`/`DeletePolicy` bypass this sentinel-and-fsnotify
path entirely: each validates its input, atomically writes (or removes) the affected file, then
calls the same `Reload` directly, in-process, before the RPC responds. An operator hand-editing
files on disk and the write RPCs can coexist — both funnel through the same `Reload`/validation
logic — but there's no locking between them beyond the atomic-rename write itself.
```

- [ ] **Step 3: Update `docs/components/api-server.md`**

Change the opening line:

```markdown
Unified, read-only REST API in front of the control plane's client and catalog data — for browsers
and admin tools that don't hold a mesh mTLS client certificate. **Control-plane component.**
```
to:
```markdown
Unified REST API in front of the control plane's client, catalog, and policy data — for browsers
and admin tools that don't hold a mesh mTLS client certificate. Client and catalog access are
read-only; policies additionally support create/update/delete. **Control-plane component.**
```

Change the `## Authentication` paragraph's parenthetical:

```markdown
Every request must present `Authorization: Bearer <token>`, checked against the single
config-supplied token; missing or mismatched returns `401`. This is the only auth layer today — no
RBAC, no per-user identity (see
[Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md)). Any node holding a
valid mesh operating credential can still call `clientmanager-api`/`catalog`'s RPCs directly,
bypassing this token — an accepted continuation of this project's existing "any operating-tier cert
may call any RPC it can reach" convention, not a new gap.
```
to:
```markdown
Every request must present `Authorization: Bearer <token>`, checked against the single
config-supplied token; missing or mismatched returns `401`. This is the only auth layer today — no
RBAC, no per-user identity, including for the policy write endpoints (see
[Design: api-server](../superpowers/specs/2026-07-14-api-server-design.md) and
[Design: Policy Management API](../superpowers/specs/2026-07-18-policy-management-api-design.md)).
Any node holding a valid mesh operating credential can still call
`clientmanager-api`/`catalog`/`policy-server`'s RPCs directly, bypassing this token — an accepted
continuation of this project's existing "any operating-tier cert may call any RPC it can reach"
convention, not a new gap.
```

Change `## Configuration Keys`:

```markdown
## Configuration Keys

- `api_server_port` — port the REST listener binds to *(default: 8090)*
- `api_server_token` — bearer token required on every REST request
- `clientmanager_api_host` / `clientmanager_api_port` — where to dial `clientmanager-api`
- `catalog_host` / `catalog_port` — where to dial `catalog`
```
to:
```markdown
## Configuration Keys

- `api_server_port` — port the REST listener binds to *(default: 8090)*
- `api_server_token` — bearer token required on every REST request
- `clientmanager_api_host` / `clientmanager_api_port` — where to dial `clientmanager-api`
- `catalog_host` / `catalog_port` — where to dial `catalog`
- `policy_server_host` / `policy_server_port` — where to dial `policy-server` *(default port:
  9300)*
```

- [ ] **Step 4: Update `docs/api/rest-v1.md`**

Change the opening line:

```markdown
`api-server`'s REST surface: read-only, v1, no RBAC. See [api-server](../components/api-server.md)
for auth and deployment.
```
to:
```markdown
`api-server`'s REST surface: v1, no RBAC. Client and catalog endpoints are read-only; policy
endpoints support create/update/delete. See [api-server](../components/api-server.md) for auth and
deployment.
```

Add a new section after `## GET /api/v1/catalog` and before `## See Also`:

```markdown
## `GET /api/v1/policies`

Returns every policy, unfiltered by any client identity (unlike `policy-server`'s own `GetPolicies`
RPC, which every mesh node calls and which is scoped to its own matching policies). Not paginated.

```json
{
  "data": [
    {
      "id": "b1f2c3d4-...",
      "name": "nightly-web-backup",
      "created_at": 1752400000,
      "updated_at": 1752400010,
      "client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
      "object_filters": [
        {"id": "a9e8d7c6-...", "path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}
      ],
      "rpo": "24h",
      "backup_window": ["0 2 * * *", "0 20 * * *"],
      "destination": "bwfs-east.internal:8080"
    }
  ]
}
```

`created_at`/`updated_at` are Unix seconds, matching every other timestamp field in this API.

## `GET /api/v1/policies/{id}`

Returns one policy (same shape as one entry above). `404` if `id` doesn't match any policy.

## `POST /api/v1/policies`

Creates a new policy. Body:

```json
{
  "name": "nightly-web-backup",
  "client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
  "object_filters": [{"path": "/var/www", "include": ["*.html"], "exclude": ["*.tmp"]}],
  "rpo": "24h",
  "backup_window": ["0 2 * * *"],
  "destination": "bwfs-east.internal:8080"
}
```

`201` with the created policy (including its server-assigned `id` and each object filter's `id`) on
success. `400` if `name` is empty, or any `include`/`exclude`/hostname entry isn't a syntactically
valid glob pattern — no file is written when validation fails.

## `PUT /api/v1/policies/{id}`

Replaces an existing policy's editable fields — same body shape as `POST`, full replacement rather
than a partial patch. `200` with the updated policy; the `id` and `created_at` never change.
Reordering or inserting `object_filters` entries changes the affected filters' `id`s. `400` on the
same validation failures as `POST` (the existing file is left untouched). `404` if `id` doesn't
match any policy.

## `DELETE /api/v1/policies/{id}`

Deletes a policy. `204` on success, `404` if `id` doesn't match any policy.
```

- [ ] **Step 5: Add a `CHANGELOG.md` entry**

Add this heading and paragraph at the top of the file, right after the `most recent first` line:

```markdown
## 2026-07-18 — policy-server: an admin write API for policies, proxied through api-server

`policy-server` gains `ListPolicies` (an unfiltered admin view, distinct from the existing
identity-scoped `GetPolicies`) and `CreatePolicy`/`UpdatePolicy`/`DeletePolicy` — each validates its
input the same way `parsePolicyFile` already does, atomically writes or removes the policy file, and
synchronously reloads its own in-memory cache before responding. `api-server` proxies all five as
`GET/POST/PUT/DELETE /api/v1/policies[/{id}]`, so backup policies can be listed and edited from a
browser instead of hand-editing JSON files on `policy-server`'s host. Policies remain flat files on
disk — no new database, no new persistent actor.
```

- [ ] **Step 6: Verify nothing broke**

Run: `cd src && go build ./... && go vet ./...`
Expected: both succeed.

Run: `cd src && go test ./cmd/policy-server/... ./cmd/api-server/... -v`
Expected: PASS — every test from Tasks 1-11 still green.

- [ ] **Step 7: Commit**

```bash
git add docs/protocols/policy-server.md docs/components/policy-server.md \
        docs/components/api-server.md docs/api/rest-v1.md CHANGELOG.md
git commit -m "docs: document the policy management API"
```
