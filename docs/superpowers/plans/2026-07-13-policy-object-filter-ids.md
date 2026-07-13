# Policy/Object-Filter Deterministic IDs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every policy and every object filter a `policy-server`-computed, deterministic UUID, and use the object-filter ID to make `agent`'s backup task/job IDs collision-proof even when two object filters share a `path`.

**Architecture:** `policy-server` derives a policy's ID from its filename and each object filter's ID from `(policy ID, index)` via UUID v5 (`uuid.NewSHA1`) at parse time — no persistence, recomputed identically on every reload. Both IDs flow purely additively through the proto, `policyclient`'s cache, and (the object-filter ID only) into `agent`'s task/job ID strings as a short suffix, alongside the existing human-readable policy-name/path segments.

**Tech Stack:** Go, `github.com/google/uuid` (already a project dependency via `common/jobid`), protobuf/gRPC, `testify`.

## Global Constraints

- IDs are computed by `policy-server` only, deterministically, from `(filename)` for a policy and `(policy ID, object-filter index)` for an object filter — recomputed fresh every reload, never persisted to the policy JSON files or anywhere else.
- The on-disk policy JSON schema itself does not change — IDs are compute-only (`json:"-"` in `policy-server`'s in-memory types).
- `Metadata.Name` and `ObjectFilter.Path` are unchanged and remain the human-facing labels; IDs are purely additive fields, not replacements.
- `agent`'s new task-ID/job-ID format is `backup:<policy-name>:<path>[:...]:<short-filter-id>` (task ID) and `backup:<policy-name>:<slug(path)>:<short-filter-id>:<timestamp>` (job ID) — the short suffix is the object filter's UUID with dashes stripped, first 8 characters.
- No migration or fallback lookup for `agent-state.json`'s task-ID format change — accepted one-time history reset on upgrade, consistent with this project's existing precedent for internal/pre-release state formats.
- No new `list-policies` column or CLI surface for the raw IDs.

---

## Task 1: `policy-server` computes deterministic policy/object-filter IDs

**Files:**
- Modify: `src/api/policyserver.proto`
- Regenerate: `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go` (via `make proto`)
- Modify: `src/cmd/policy-server/policy.go`
- Modify: `src/cmd/policy-server/policy_test.go`
- Modify: `src/cmd/policy-server/cache.go`
- Modify: `src/cmd/policy-server/cache_test.go`
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/server_test.go`

**Interfaces:**
- Produces: `Metadata.ID string` and `ObjectFilter.ID string` on `cmd/policy-server`'s in-memory `Policy` type (both `json:"-"`, computed in `parsePolicyFile`), and `pb.Policy.GetId() string` / `pb.ObjectFilter.GetId() string` on the wire — both consumed by Task 2 (`policyclient`).

- [ ] **Step 1: Add `id` to the proto and regenerate**

In `src/api/policyserver.proto`, change:
```proto
message ObjectFilter {
  string path = 1;
  repeated string include = 2;
  repeated string exclude = 3;
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
}
```
to:
```proto
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
}
```

Run: `make proto`
Expected: `Protobuf code generated in src/api/` printed, no errors. Confirm with
`grep -n "GetId" src/api/policyserver.pb.go` — both `*ObjectFilter` and `*Policy` should now have
a `GetId()` method.

- [ ] **Step 2: Write the failing tests**

In `src/cmd/policy-server/policy_test.go`, replace `TestParsePolicyFile_ValidPolicyParsesAllFields` with:
```go
func TestParsePolicyFile_ValidPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-10T00:00:00Z"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	assert.Equal(t, "nightly-web-backup", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.Metadata.CreatedAt)
	assert.Equal(t, []string{"web-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, p.ClientFilters.Labels)
	require.Len(t, p.ObjectFilters, 1)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, []string{"*.html", "*.css"}, p.ObjectFilters[0].Include)
	assert.Equal(t, []string{"*.tmp"}, p.ObjectFilters[0].Exclude)
	assert.NotEmpty(t, p.ObjectFilters[0].ID)
	assert.Equal(t, "24h", p.RPO)
	assert.Equal(t, []string{"0 2 * * *", "0 20 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
}
```

Add four new tests, right after it:
```go
func TestParsePolicyFile_ComputesDeterministicPolicyID(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup"},
		"object_filters": [{"path": "/var/www"}]
	}`)

	p1, err := parsePolicyFile(path)
	require.NoError(t, err)
	p2, err := parsePolicyFile(path)
	require.NoError(t, err)

	assert.NotEmpty(t, p1.Metadata.ID)
	assert.Equal(t, p1.Metadata.ID, p2.Metadata.ID, "same filename must yield the same policy ID every parse")
}

func TestParsePolicyFile_DifferentFilenamesYieldDifferentPolicyIDs(t *testing.T) {
	dir := t.TempDir()
	pathA := writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "same-name"}}`)
	pathB := writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "same-name"}}`)

	pa, err := parsePolicyFile(pathA)
	require.NoError(t, err)
	pb, err := parsePolicyFile(pathB)
	require.NoError(t, err)

	assert.NotEqual(t, pa.Metadata.ID, pb.Metadata.ID, "identical metadata.name in different files must not collide")
}

func TestParsePolicyFile_ObjectFiltersAtDifferentIndicesGetDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "multi.json", `{
		"metadata": {"name": "multi"},
		"object_filters": [{"path": "/a"}, {"path": "/b"}]
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEmpty(t, p.ObjectFilters[0].ID)
	assert.NotEmpty(t, p.ObjectFilters[1].ID)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID)
}

func TestParsePolicyFile_ObjectFiltersWithIdenticalPathGetDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "duplicate-path.json", `{
		"metadata": {"name": "duplicate-path"},
		"object_filters": [
			{"path": "/var/www", "include": ["*.html"]},
			{"path": "/var/www", "exclude": ["*.log"]}
		]
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID, "two object filters sharing a path must still get distinct IDs")
}
```

In `src/cmd/policy-server/cache_test.go`, add one line to the end of `TestCache_PoliciesReturnsSnapshotCopy` (after its last existing `assert.Equal` line):
```go
	assert.NotEmpty(t, got2[0].ObjectFilters[0].ID, "ObjectFilter.ID must survive the snapshot copy")
```

In `src/cmd/policy-server/server_test.go`, add four lines to the end of `TestGetPolicies_ResponseFieldsRoundTrip` (after its last existing `assert.Equal` line):
```go
	assert.NotEmpty(t, p.Id)
	assert.NotEmpty(t, p.ObjectFilters[0].Id)
	assert.NotEmpty(t, p.ObjectFilters[1].Id)
	assert.NotEqual(t, p.ObjectFilters[0].Id, p.ObjectFilters[1].Id)
```

- [ ] **Step 3: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: FAIL — compile errors (`Metadata`/`ObjectFilter` have no field `ID`) and/or assertion
failures (`p.Id`/`p.ObjectFilters[0].Id` always empty, since nothing populates them yet).

- [ ] **Step 4: Implement ID computation in `policy.go`**

In `src/cmd/policy-server/policy.go`, change the import block:
```go
import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
)
```

Add a package-level namespace constant, right after the imports:
```go
// policyIDNamespace scopes this project's deterministic policy/object-filter
// IDs into their own UUID namespace (RFC 4122 §4.3) -- an arbitrary fixed
// UUID whose only job is separating this ID-space from unrelated uuid.New
// uses elsewhere in the codebase (e.g. common/jobid's random job-ids).
var policyIDNamespace = uuid.MustParse("6f1c3a2e-8b4d-4e11-9a7c-2d5f8e0b1c34")
```

Add `ID` fields to `Metadata` and `ObjectFilter`:
```go
type Metadata struct {
	ID        string    `json:"-"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
```
```go
type ObjectFilter struct {
	ID      string   `json:"-"`
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}
```

In `parsePolicyFile`, insert ID computation right after the `metadata.name is required` check and
before the hostname-pattern validation loop:
```go
	if p.Metadata.Name == "" {
		return Policy{}, fmt.Errorf("%s: metadata.name is required", filePath)
	}

	policyUUID := uuid.NewSHA1(policyIDNamespace, []byte(filepath.Base(filePath)))
	p.Metadata.ID = policyUUID.String()
	for i := range p.ObjectFilters {
		p.ObjectFilters[i].ID = uuid.NewSHA1(policyUUID, []byte(strconv.Itoa(i))).String()
	}

	for _, pattern := range p.ClientFilters.Hostnames {
```
(the rest of the function — the hostname and include/exclude validation loops, and the final
`return p, nil` — stays exactly as it is today.)

- [ ] **Step 5: Implement the deep-copy and proto conversion**

In `src/cmd/policy-server/cache.go`, change:
```go
		for j, f := range p.ObjectFilters {
			out[i].ObjectFilters[j] = ObjectFilter{
				Path:    f.Path,
				Include: append([]string(nil), f.Include...),
				Exclude: append([]string(nil), f.Exclude...),
			}
		}
```
to:
```go
		for j, f := range p.ObjectFilters {
			out[i].ObjectFilters[j] = ObjectFilter{
				ID:      f.ID,
				Path:    f.Path,
				Include: append([]string(nil), f.Include...),
				Exclude: append([]string(nil), f.Exclude...),
			}
		}
```
(`Policy.ID` needs no change here — it lives on `Metadata`, already copied by whole-struct value
via `Metadata: p.Metadata` a few lines above.)

In `src/cmd/policy-server/server.go`, change `toProtoPolicy`:
```go
func toProtoPolicy(p Policy) *pb.Policy {
	objectFilters := make([]*pb.ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = &pb.ObjectFilter{Id: f.ID, Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	return &pb.Policy{
		Id:            p.Metadata.ID,
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
		Destination:   p.Destination,
	}
}
```

- [ ] **Step 6: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: PASS — all tests green.

- [ ] **Step 7: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go \
        src/cmd/policy-server/policy.go src/cmd/policy-server/policy_test.go \
        src/cmd/policy-server/cache.go src/cmd/policy-server/cache_test.go \
        src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go
git commit -m "feat(policy-server): compute deterministic policy/object-filter IDs"
```

---

## Task 2: `policyclient` carries the new IDs through the cache

**Files:**
- Modify: `src/cmd/policyclient/fetch.go`
- Modify: `src/cmd/policyclient/fetch_test.go`

**Interfaces:**
- Consumes: `pb.Policy.GetId()` / `pb.ObjectFilter.GetId() string` (Task 1).
- Produces: `CachedPolicy.ID string` and `ObjectFilter.ID string` on `cmd/policyclient`'s on-disk
  cache types — the `ObjectFilter.ID` is what Task 3 (`agent`) reads.

- [ ] **Step 1: Write the failing test**

In `src/cmd/policyclient/fetch_test.go`, replace `TestRunFetch_Success_WritesCacheFile` with:
```go
func TestRunFetch_Success_WritesCacheFile(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "nested", "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Id:        "policy-uuid-123",
				Name:      "daily-db-backup",
				CreatedAt: created,
				UpdatedAt: updated,
				ObjectFilters: []*pb.ObjectFilter{
					{Id: "filter-uuid-1", Path: "/var/lib/postgres", Include: []string{"*.sql"}},
					{Id: "filter-uuid-2", Path: "/etc/postgres", Exclude: []string{"*.bak"}},
				},
				Rpo:          "24h",
				BackupWindow: []string{"0 2 * * *"},
				Destination:  "bwfs-east.internal:8080",
			},
		},
	}}

	err := runFetch(context.Background(), fake, cachePath, fetchTestLogger())
	require.NoError(t, err)

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)

	var got []CachedPolicy
	require.NoError(t, json.Unmarshal(data, &got))
	require.Len(t, got, 1)
	assert.Equal(t, "policy-uuid-123", got[0].ID)
	assert.Equal(t, "daily-db-backup", got[0].Name)
	assert.True(t, created.AsTime().Equal(got[0].CreatedAt))
	assert.True(t, updated.AsTime().Equal(got[0].UpdatedAt))
	assert.Equal(t, []ObjectFilter{
		{ID: "filter-uuid-1", Path: "/var/lib/postgres", Include: []string{"*.sql"}},
		{ID: "filter-uuid-2", Path: "/etc/postgres", Exclude: []string{"*.bak"}},
	}, got[0].ObjectFilters)
	assert.Equal(t, "24h", got[0].RPO)
	assert.Equal(t, []string{"0 2 * * *"}, got[0].BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", got[0].Destination)
}
```

- [ ] **Step 2: Run the test, confirm it fails to build**

Run: `cd src && go test ./cmd/policyclient/... -run TestRunFetch_Success -v`
Expected: FAIL — compile error, `ObjectFilter`/`CachedPolicy` have no field `ID`, and `pb.Policy`
literal has no field `Id` (this second one should already compile fine since Task 1 regenerated
the proto — if it fails here, Task 1 wasn't merged first).

- [ ] **Step 3: Implement the new fields and conversion in `fetch.go`**

In `src/cmd/policyclient/fetch.go`, change the `ObjectFilter` and `CachedPolicy` types:
```go
// ObjectFilter is the on-disk representation of one policy-server
// ObjectFilter: a backup root path plus its optional include/exclude glob
// patterns and its policy-server-computed ID, carried through verbatim
// from the RPC response.
type ObjectFilter struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// CachedPolicy is the on-disk representation of one policy-server Policy --
// the same fields the GetPolicies RPC response already defines, converted
// directly from the protobuf message.
type CachedPolicy struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}
```

Then update `toCachedPolicies`:
```go
func toCachedPolicies(policies []*pb.Policy) []CachedPolicy {
	out := make([]CachedPolicy, 0, len(policies))
	for _, p := range policies {
		filters := make([]ObjectFilter, 0, len(p.GetObjectFilters()))
		for _, of := range p.GetObjectFilters() {
			filters = append(filters, ObjectFilter{
				ID:      of.GetId(),
				Path:    of.GetPath(),
				Include: of.GetInclude(),
				Exclude: of.GetExclude(),
			})
		}
		out = append(out, CachedPolicy{
			ID:            p.GetId(),
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
		})
	}
	return out
}
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/policyclient/... -v`
Expected: PASS — all tests green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go
git commit -m "feat(policyclient): carry policy/object-filter IDs through the cache"
```

---

## Task 3: `agent` uses the object-filter ID to make task/job IDs collision-proof

**Files:**
- Modify: `src/cmd/agent/backup.go`
- Modify: `src/cmd/agent/backup_test.go`

**Interfaces:**
- Consumes: the on-disk `policies-cache.json` shape's `object_filters[].id` field (Task 2) — via
  `agent`'s own duplicated `ObjectFilter` struct (agent never reads `CachedPolicy.ID`/`Policy.ID`;
  it only needs the per-filter ID, so its "subset of the schema" mirror stays as narrow as it is
  today).
- Produces: `backupTaskID(policyName, path, filterID string) string`,
  `backupJobID(policyName, path, filterID string, now time.Time) string`, and a new
  `shortID(id string) string` helper.

- [ ] **Step 1: Write the failing tests**

In `src/cmd/agent/backup_test.go`, add these tests (anywhere in the file, e.g. right after
`writeCachedPolicies`):
```go
func TestShortID_TruncatesToEightHexCharsAfterStrippingDashes(t *testing.T) {
	assert.Equal(t, "aaaaaaaa", shortID("aaaaaaaa-1111-1111-1111-111111111111"))
}

func TestShortID_EmptyInputReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", shortID(""))
}

func TestShortID_ShorterThanEightCharsReturnedUnchanged(t *testing.T) {
	assert.Equal(t, "abcd", shortID("ab-cd"))
}
```

Add this test, proving the actual bug fix, near the other `backupTasks` tests:
```go
func TestBackupTasks_ObjectFiltersSharingPathGetDistinctTaskIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "web-policy",
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/var/www", "include": ["*.html"]},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/var/www", "exclude": ["*.log"]}
		],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 2)
	assert.NotEqual(t, tasks[0].ID, tasks[1].ID, "two object filters sharing a path must get distinct task IDs")
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "backup:web-policy:/var/www:aaaaaaaa")
	assert.Contains(t, ids, "backup:web-policy:/var/www:bbbbbbbb")
}
```

Update `TestBackupTasks_OnePolicyWithTwoPathsYieldsTwoTasksWithStableDistinctIDs` (give the two
object filters explicit IDs and update the expected strings to include the new suffix):
```go
func TestBackupTasks_OnePolicyWithTwoPathsYieldsTwoTasksWithStableDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/var/lib/postgres"},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/etc/postgres"}
		],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 2)
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "backup:daily-db-backup:/var/lib/postgres:aaaaaaaa")
	assert.Contains(t, ids, "backup:daily-db-backup:/etc/postgres:bbbbbbbb")
	assert.NotEqual(t, tasks[0].ID, tasks[1].ID)
}
```

Update `TestBackupTasks_PerPathIndependence` the same way:
```go
func TestBackupTasks_PerPathIndependence(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/a"},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/b"}
		],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	require.True(t, ok)
	require.Len(t, tasks, 2)

	windowOpenTime := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC)
	recent := windowOpenTime.Add(-10 * time.Minute)

	var taskA, taskB Policy
	for _, task := range tasks {
		if task.ID == "backup:p:/a:aaaaaaaa" {
			taskA = task
		} else {
			taskB = task
		}
	}
	// /a recently succeeded (not due); /b never ran (due) -- proves one
	// path's state has no effect on its sibling's due-check.
	assert.False(t, taskA.Due(PolicyState{LastSuccessAt: &recent}, windowOpenTime))
	assert.True(t, taskB.Due(PolicyState{}, windowOpenTime))
}
```

- [ ] **Step 2: Run the tests, confirm they fail**

Run: `cd src && go test ./cmd/agent/... -run 'TestShortID|TestBackupTasks' -v`
Expected: FAIL — compile error (`undefined: shortID`) and/or the two updated exact-ID assertions
failing (current IDs have no `:<suffix>` segment).

- [ ] **Step 3: Implement `shortID` and thread the filter ID through**

In `src/cmd/agent/backup.go`, add the `ID` field to `ObjectFilter`:
```go
// ObjectFilter mirrors the subset of policyclient's on-disk ObjectFilter
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type ObjectFilter struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}
```

Replace `backupTaskID` and `backupJobID`:
```go
// shortID returns id (a UUID) with its dashes stripped, truncated to 8
// hex characters -- git-short-hash-style, just long enough to disambiguate
// in practice without making task/job IDs unreadable. Safe for any input
// length: shorter-than-8 (including empty) is returned unchanged rather
// than panicking on a slice out of range.
func shortID(id string) string {
	stripped := strings.ReplaceAll(id, "-", "")
	if len(stripped) > 8 {
		return stripped[:8]
	}
	return stripped
}

// backupTaskID is the stable identifier for one object filter's
// PolicyState entry in agent-state.json -- stable across ticks, so its
// backoff/success history persists as long as the filter keeps appearing
// in policies-cache.json. filterID's short suffix guarantees uniqueness
// even when two object filters in the same policy share a path (e.g. one
// with include, one with exclude, both scoped to the same root) -- policy
// name and path stay in the string for readability, but the suffix is
// what actually disambiguates.
func backupTaskID(policyName, path, filterID string) string {
	return fmt.Sprintf("backup:%s:%s:%s", policyName, path, shortID(filterID))
}

// backupJobID is the --job-id passed to brfs for one run -- unlike
// backupTaskID, it includes a timestamp so every run gets a distinct ID,
// and it slugs the path so bwfs's job records stay easy to grep.
func backupJobID(policyName, path, filterID string, now time.Time) string {
	return fmt.Sprintf("backup:%s:%s:%s:%d", policyName, slug(path), shortID(filterID), now.Unix())
}
```

In `backupTasks`, update the calls to both functions:
```go
		policyName, destination := p.Name, p.Destination
		for _, filter := range p.ObjectFilters {
			jobID := backupJobID(policyName, filter.Path, filter.ID, time.Now())
			args := []string{filter.Path, "--destination", destination, "--job-id", jobID}
			if len(filter.Include) > 0 {
				args = append(args, "--include", strings.Join(filter.Include, ","))
			}
			if len(filter.Exclude) > 0 {
				args = append(args, "--exclude", strings.Join(filter.Exclude, ","))
			}
			tasks = append(tasks, Policy{
				ID:         backupTaskID(policyName, filter.Path, filter.ID),
				Binary:     "brfs",
				JobID:      jobID,
				Args:       args,
				Background: true,
				Due: func(s PolicyState, now time.Time) bool {
					return windowOpen(schedules, now, grace) && rpoElapsed(s, now, rpo)
				},
				NextRun: func(s PolicyState, now time.Time) time.Time {
					return nextWindow(schedules, now)
				},
			})
		}
```

- [ ] **Step 4: Run the tests, confirm they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS — all tests green, including
`TestBackupTasks_ObjectFiltersSharingPathGetDistinctTaskIDs`, and the pre-existing
`TestBackupTasks_TaskArgsMatchBrfsShape`, `TestBackupTasks_JobIDFieldMatchesArgsFlag`,
`TestRun_BackupTaskFromRealCacheFileExecutesBrfsWithExpectedArgs` (in `integration_test.go`), which
all use fixtures without an `id` field and rely on `shortID("")` degrading gracefully to an empty
suffix rather than breaking their existing prefix-based (`assert.Contains`) or length-based
assertions.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/backup.go src/cmd/agent/backup_test.go
git commit -m "feat(agent): use the object-filter ID to make task/job IDs collision-proof"
```

---

## Task 4: Documentation and changelog

**Files:**
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/policyclient.md`
- Modify: `docs/components/agent.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none — documentation only, no code.

- [ ] **Step 1: Update `docs/protocols/policy-server.md`**

Change the `ObjectFilter` and `Policy` message blocks:
```proto
message ObjectFilter {
  string path = 1;
  repeated string include = 2;
  repeated string exclude = 3;
}

message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
}
```
to:
```proto
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
}
```

Add a bullet to the `## Behavior` section, right after the `client_filters` bullet:
```markdown
- Both `Policy.id` and each `ObjectFilter.id` are computed by `policy-server` itself --
  deterministically, from the policy file's name (and each object filter's position within it) --
  never read from or written to the on-disk policy JSON. They exist so two policies, or two object
  filters within one policy, can never be confused with each other downstream even when their
  human-facing `name`/`path` happen to collide.
```

- [ ] **Step 2: Update `docs/components/policy-server.md`**

Change:
```markdown
Each `$MP_CONFIG_PATH/policies/*.json` file is one policy: `metadata` (`name` plus operator-set
`created_at`/`updated_at`), `client_filters` (`hostnames` glob list, `labels` map), `object_filters`
(a list of `{"path": "...", "include": [...], "exclude": [...]}` entries — `include`/`exclude` are
optional glob-pattern lists, validated as syntactically-valid patterns at load time but otherwise
opaque to `policy-server`; see [Filesystem Backup Flow](../process/filesystem-backup.md) for how
`brfs` applies them), `rpo` (a duration string, e.g. `"24h"`), `backup_window`
(a list of cron expressions, e.g. `["0 2 * * *", "0 20 * * *"]`), and `destination` (a `host:port`
string, the target `bwfs` for this policy's backups).
```
to:
```markdown
Each `$MP_CONFIG_PATH/policies/*.json` file is one policy: `metadata` (`name` plus operator-set
`created_at`/`updated_at`), `client_filters` (`hostnames` glob list, `labels` map), `object_filters`
(a list of `{"path": "...", "include": [...], "exclude": [...]}` entries — `include`/`exclude` are
optional glob-pattern lists, validated as syntactically-valid patterns at load time but otherwise
opaque to `policy-server`; see [Filesystem Backup Flow](../process/filesystem-backup.md) for how
`brfs` applies them), `rpo` (a duration string, e.g. `"24h"`), `backup_window`
(a list of cron expressions, e.g. `["0 2 * * *", "0 20 * * *"]`), and `destination` (a `host:port`
string, the target `bwfs` for this policy's backups). `policy-server` also computes (never reads)
a deterministic ID for the policy itself and for each object filter, derived from the file's name
and each filter's position — stable across reloads, and changes only if the file is renamed or its
`object_filters` are reordered/have entries inserted before an existing one.
```

- [ ] **Step 3: Update `docs/components/policyclient.md`**

Change:
```json
[
  {
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": [
      {"path": "/var/lib/postgres", "include": ["*.sql"]},
      {"path": "/etc/postgres"}
    ],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"],
    "destination": "bwfs-east.internal:8080"
  }
]
```
to:
```json
[
  {
    "id": "b1f2c3d4-...",
    "name": "daily-db-backup",
    "created_at": "2026-07-01T00:00:00Z",
    "updated_at": "2026-07-05T00:00:00Z",
    "object_filters": [
      {"id": "a9e8d7c6-...", "path": "/var/lib/postgres", "include": ["*.sql"]},
      {"id": "f0e1d2c3-...", "path": "/etc/postgres"}
    ],
    "rpo": "24h",
    "backup_window": ["0 2 * * *"],
    "destination": "bwfs-east.internal:8080"
  }
]
```

- [ ] **Step 4: Update `docs/components/agent.md`**

Change:
```markdown
Every reconcile tick, `agent` re-reads `policies-cache.json` fresh (so it notices `policy-update`
refreshing the cache without needing a restart) and derives one backup task per
`(policy, object_filters path)` pair. Each task is tracked independently in `agent-state.json`
(ID: `backup:<policy-name>:<path>`) — one path's failures and backoff never affect any other path,
including a sibling path in the same policy.
```
to:
```markdown
Every reconcile tick, `agent` re-reads `policies-cache.json` fresh (so it notices `policy-update`
refreshing the cache without needing a restart) and derives one backup task per
`(policy, object_filters path)` pair. Each task is tracked independently in `agent-state.json`
(ID: `backup:<policy-name>:<path>:<short-filter-id>`, where `<short-filter-id>` is the first 8
characters of that object filter's `policy-server`-computed ID with dashes stripped) — one path's
failures and backoff never affect any other path, including a sibling path in the same policy. The
suffix exists so two object filters sharing the same `path` within one policy (e.g. one filtering
with `include`, one with `exclude`, both scoped to the same root) still get distinct task-tracking
entries instead of silently sharing one.
```

Change:
```markdown
When due, `agent` execs `brfs <path> --destination <destination> --job-id
backup:<policy>:<slug(path)>:<timestamp>`, appending `--include <patterns>` and/or `--exclude
<patterns>` (comma-joined) only when the object filter that produced this task actually carries
them — the explicit job-id lets an operator correlate a `bwfs` job record back to the policy and
path that produced it.
```
to:
```markdown
When due, `agent` execs `brfs <path> --destination <destination> --job-id
backup:<policy>:<slug(path)>:<short-filter-id>:<timestamp>`, appending `--include <patterns>`
and/or `--exclude <patterns>` (comma-joined) only when the object filter that produced this task
actually carries them — the explicit job-id lets an operator correlate a `bwfs` job record back to
the exact policy and object filter that produced it, even when two filters share a path.
```

- [ ] **Step 5: Add a `CHANGELOG.md` entry**

Add this heading and paragraph at the top of the file, right after the `most recent first` line:
```markdown
## 2026-07-13 — Deterministic IDs for policies and object filters

`policy-server` now computes a deterministic ID for every policy and every object filter within
it, derived from the policy file's name and each filter's position — never read from or written to
the policy JSON files themselves. `agent` uses the object-filter ID to make its backup task/job IDs
collision-proof: two object filters sharing a `path` within one policy (e.g. one with `include`,
one with `exclude`, both scoped to the same root) previously shared one `agent-state.json` entry
and one in-flight-run slot; each now gets its own. Upgrading resets every existing backup task's
history once (last-success/backoff tracking), since the task-ID format changed — a one-time,
low-cost consequence of fixing the underlying collision.
```

- [ ] **Step 6: Verify nothing broke**

Run: `cd src && go build ./... && go vet ./...`
Expected: both succeed (the pre-existing, unrelated `go vet` warning in `cmd/brfs/filesstream.go`
is out of scope for this change and may still appear).

- [ ] **Step 7: Commit**

```bash
git add docs/protocols/policy-server.md docs/components/policy-server.md \
        docs/components/policyclient.md docs/components/agent.md CHANGELOG.md
git commit -m "docs: document deterministic policy/object-filter IDs"
```
