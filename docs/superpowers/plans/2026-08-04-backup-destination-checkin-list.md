# Backup Destination From Checkins Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `ClientFilters.Hostnames[0]` (a glob-matching pattern, not an address) as the source
of a backup policy's `destination` with the storage policy's live checkin list, exposed as an ordered
`Destinations []string` (freshest-checked-in-first), all the way through to `brfs`.

**Architecture:** `policy-server`'s `attachDestination` resolves a backup policy's `Destinations` by
looking up its `storage_policy_id`'s checkin records (already recorded by every `GetPolicies` call a
storage node makes against its own storage policy) and combining each checked-in hostname with the
storage policy's `Port`. The checkin store's `CheckinsForPolicy` query is reordered to return
freshest-first so this requires no extra sorting downstream. The field travels as a repeated string
end-to-end: proto → `policyclient`'s on-disk cache → `agent` (which execs `brfs` with `Destinations[0]`)
→ `api-server`'s REST DTO → the two Vue admin views that display it.

**Tech Stack:** Go (`policy-server`, `policyclient`, `agent`, `api-server`), protobuf/gRPC, GORM/SQLite
(`storage/policyserver`), Vue 3 (`web`).

## Global Constraints

- No backward compatibility: `destination` (singular) is removed outright, not kept alongside
  `destinations` — every consumer is inside this repo.
- Checkin order (freshest-first) is fixed once, in `CheckinsForPolicy`'s SQL query — nothing
  downstream re-sorts.
- `Destinations[0]` is the only entry `agent`/`brfs` reads today; retry logic over `Destinations[1:]`
  is explicitly out of scope for this plan.
- Design reference: `docs/superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md`.

---

### Task 1: Order checkins by recency, not hostname

**Files:**
- Modify: `src/storage/policyserver/store.go:39-43`
- Test: `src/storage/policyserver/store_test.go:64-74`

**Interfaces:**
- Consumes: nothing new — `(*Store).CheckinsForPolicy(policyID string) ([]CheckinRecord, error)`
  already exists.
- Produces: `CheckinsForPolicy` now returns records ordered by `LastSeenAt` descending (freshest
  first) instead of by `Hostname` ascending. Every later task that calls `CheckinsForPolicy` relies on
  this order.

- [ ] **Step 1: Rewrite the ordering test to expect recency order**

Replace `TestCheckinsForPolicy_OrderedByHostname` (`store_test.go:64-74`) with:

```go
func TestCheckinsForPolicy_OrderedByLastSeenAtDescending(t *testing.T) {
	store := newTestStore(t)
	older := time.Now().Add(-time.Hour).Truncate(time.Second)
	newer := time.Now().Truncate(time.Second)
	require.NoError(t, store.RecordCheckin("policy-1", "zebra", older))
	require.NoError(t, store.RecordCheckin("policy-1", "apple", newer))

	records, err := store.CheckinsForPolicy("policy-1")
	require.NoError(t, err)
	require.Len(t, records, 2)
	assert.Equal(t, "apple", records[0].Hostname, "the most recently checked-in host must come first")
	assert.Equal(t, "zebra", records[1].Hostname)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd /home/alex/miniprotector/src && go test ./storage/policyserver/... -run TestCheckinsForPolicy_OrderedByLastSeenAtDescending -v`
Expected: FAIL — `apple` (recorded second, newer) is currently returned second because the query still
orders by hostname (`"apple" < "zebra"` alphabetically would actually pass by accident here — to be
safe, confirm the failure is real by checking the assertion is exercising the query you're about to
change, not verify by output text alone. If it unexpectedly passes, swap the two hostnames for ones
where alphabetical and recency order disagree, e.g. `"zebra"` newer, `"apple"` older, and re-run.)

- [ ] **Step 3: Change the query order**

In `src/storage/policyserver/store.go`, change line 41:

```go
	err := s.db.Where("policy_id = ?", policyID).Order("hostname").Find(&out).Error
```

to:

```go
	err := s.db.Where("policy_id = ?", policyID).Order("last_seen_at DESC").Find(&out).Error
```

Also update the doc comment above it (`store.go:35-38`), which currently says "ordered by hostname":

```go
// CheckinsForPolicy returns every host that has checked in for policyID,
// ordered by LastSeenAt descending (freshest first) -- the single source of
// truth for checkin order; nothing downstream re-sorts. Each record already
// holds its most recent check-in time (see CheckinRecord). Returns an empty
// slice, not an error, for a policyID with no check-ins.
func (s *Store) CheckinsForPolicy(policyID string) ([]CheckinRecord, error) {
```

- [ ] **Step 4: Run the full package test suite to verify it passes and nothing else broke**

Run: `cd /home/alex/miniprotector/src && go test ./storage/policyserver/... -v`
Expected: PASS, all tests including `TestCheckinsForPolicy_OrderedByLastSeenAtDescending`,
`TestCheckinsForPolicy_ScopesByPolicyID`, `TestCheckinsForPolicy_UnknownPolicyReturnsEmpty`.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add src/storage/policyserver/store.go src/storage/policyserver/store_test.go
git commit -m "feat(policy-server): order checkins by recency instead of hostname"
```

---

### Task 2: Resolve backup destinations from storage-policy checkins

**Files:**
- Modify: `src/api/policyserver.proto`
- Regenerate: `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go` (via `make proto`)
- Modify: `src/cmd/policy-server/cache.go:128-145` (delete `ResolveDestination`)
- Modify: `src/cmd/policy-server/server.go` (rewrite `attachDestination`, both call sites, add `fmt` import)
- Modify: `src/cmd/policy-server/write.go:257,306` (both `attachDestination` call sites)
- Modify: `src/cmd/policy-server/backup_policy.go:29-31,115-118` (doc comments only)
- Test: `src/cmd/policy-server/cache_test.go:197-233` (delete `ResolveDestination` tests)
- Test: `src/cmd/policy-server/server_test.go` (rewrite destination tests, add new ones)
- Test: `src/cmd/policy-server/write_test.go:52-73,91,236` (seed a checkin, update assertions)
- Test: `src/cmd/policy-server/backup_policy_test.go:188-195` (rename, update assertion)

**Interfaces:**
- Consumes: `(*checkinstore.Store).CheckinsForPolicy(policyID string) ([]checkinstore.CheckinRecord, error)`
  from Task 1, now freshest-first.
- Produces: `pb.Policy.Destinations []string` (freshest-first `"host:port"` entries, replacing
  `pb.Policy.Destination string`). `attachDestination(pp *pb.Policy, cache *Cache, checkins *checkinstore.Store)`
  — every later task that reads a backup policy's destination reads `pp.GetDestinations()` /
  `p.GetDestinations()`, never `GetDestination()`.

- [ ] **Step 1: Change the proto — remove `destination`, add `destinations`**

In `src/api/policyserver.proto`, in the `Policy` message, replace line 71:

```proto
  // Derived, read-only. For a "backup" policy, resolved live from
  // storage_policy_id every time this message is produced -- never stored
  // or settable directly. Unset for a "storage" policy, as before.
  string destination = 7;
```

with:

```proto
  // reserved 7 was "destination" -- removed, replaced by the repeated
  // destinations field below. See
  // docs/superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md.
  reserved 7;
  reserved "destination";
```

Then add a new field after `checkins` (line 103, `repeated PolicyCheckin checkins = 16;`):

```proto
  // "backup" policy only. Derived, read-only: one "host:port" entry per
  // live checkin against storage_policy_id, freshest checkin first. Empty
  // if the storage policy has no checkins yet or storage_policy_id is
  // dangling. Unset for a "storage" policy, as before.
  repeated string destinations = 17;
```

`CreatePolicyRequest`/`UpdatePolicyRequest` need no change — both already `reserved` their
`destination` field from the prior `storage_policy_id` design and never had it writable.

- [ ] **Step 2: Regenerate the protobuf Go code**

Run: `cd /home/alex/miniprotector && make proto`
Expected: `Protobuf code generated in src/api/` with no errors. Verify the new field exists:
`grep -n "GetDestinations\|Destinations \[\]string" src/api/policyserver.pb.go` should show a
`GetDestinations()` method and a `Destinations []string` struct field; `grep -n "GetDestination\b"
src/api/policyserver.pb.go` should show nothing (the singular getter is gone).

- [ ] **Step 3: Delete `Cache.ResolveDestination` and its tests**

In `src/cmd/policy-server/cache.go`, delete lines 128-145 (the `ResolveDestination` doc comment and
function) entirely — nothing else in `cache.go` references it after this task.

In `src/cmd/policy-server/cache_test.go`, delete the three tests at lines 197-233:
`TestCache_ResolveDestination_ReturnsHostPortForKnownStoragePolicy`,
`TestCache_ResolveDestination_UnknownIDReturnsFalse`,
`TestCache_ResolveDestination_BackupPolicyIDReturnsFalse`. This coverage moves to `server_test.go` in
Step 5, against `attachDestination` instead — the resolution logic now lives there, not on `Cache`.

- [ ] **Step 4: Rewrite `attachDestination` in `server.go`**

Add `"fmt"` to `server.go`'s import block (`server.go:3-16`), alongside the existing `"context"`,
`"log/slog"`, `"sync"`, `"time"`.

Replace `attachDestination` (`server.go:99-113`) with:

```go
// attachDestination resolves pp.Destinations for a backup policy from its
// StoragePolicyId's checkin list, using cache's live state and checkins'
// live check-in records. Called right after ToProto at every RPC that
// returns a pb.Policy (GetPolicies, ListPolicies, CreatePolicy,
// UpdatePolicy). A dangling reference (unknown id, or an id that no longer
// names a storage policy), or a storage policy with no checkins yet, leaves
// pp.Destinations empty rather than erroring.
func attachDestination(pp *pb.Policy, cache *Cache, checkins *checkinstore.Store) {
	if pp.GetType() != "backup" || pp.GetStoragePolicyId() == "" {
		return
	}
	p, ok := cache.FindByID(pp.GetStoragePolicyId())
	if !ok || p.Kind() != "storage" {
		return
	}
	sp, ok := p.(*StoragePolicy)
	if !ok {
		return
	}
	records, err := checkins.CheckinsForPolicy(pp.GetStoragePolicyId())
	if err != nil {
		return
	}
	for _, r := range records {
		pp.Destinations = append(pp.Destinations, fmt.Sprintf("%s:%d", r.Hostname, sp.Port))
	}
}
```

Update both call sites in `server.go` to pass `s.checkins`:
- Line 83 (inside `GetPolicies`): `attachDestination(pp, s.cache)` → `attachDestination(pp, s.cache, s.checkins)`
- Line 150 (inside `ListPolicies`): `attachDestination(pp, s.cache)` → `attachDestination(pp, s.cache, s.checkins)`

In `src/cmd/policy-server/write.go`, update both call sites the same way:
- Line 257 (inside `CreatePolicy`): `attachDestination(pp, s.cache)` → `attachDestination(pp, s.cache, s.checkins)`
- Line 306 (inside `UpdatePolicy`): `attachDestination(pp, s.cache)` → `attachDestination(pp, s.cache, s.checkins)`

- [ ] **Step 5: Update doc comments in `backup_policy.go`**

Replace the `StoragePolicyID` field comment (`backup_policy.go:29-31`):

```go
	// References a "storage"-typed Policy.id. destinations is resolved from
	// its checkin list live by attachDestination (server.go) -- never
	// itself stored or set directly.
	StoragePolicyID string `json:"storage_policy_id"`
```

Replace the `ToProto` doc comment (`backup_policy.go:110-118`)'s last two sentences:

```go
// ToProto converts to the wire representation GetPolicies/ListPolicies/
// CreatePolicy/UpdatePolicy return. client_filters is only populated when
// includeClientFilters is true -- GetPolicies omits it so a matched node
// never learns another node's targeting rules from a policy that already
// matched its own identity; ListPolicies and the write RPCs include it for
// an operator editing the full policy set. Destinations is deliberately
// left unset here -- it's resolved from StoragePolicyID's checkin list by
// attachDestination (server.go), which every call site producing a
// pb.Policy invokes right after ToProto.
```

- [ ] **Step 6: Update `backup_policy_test.go`**

Rename and rewrite the test at `backup_policy_test.go:188-195` from
`TestBackupPolicy_ToProtoSetsStoragePolicyIdAndLeavesDestinationUnset` to:

```go
func TestBackupPolicy_ToProtoSetsStoragePolicyIdAndLeavesDestinationsUnset(t *testing.T) {
	// ...existing test body up to the assertion, unchanged...
	assert.Empty(t, pp.Destinations, "Destinations is resolved elsewhere (attachDestination in server.go via checkinstore), never set directly by ToProto")
}
```

(Keep everything in the test body above the final assertion exactly as it is today — only the func
name and the final `assert.Empty` line change.)

- [ ] **Step 7: Rewrite `server_test.go`'s destination tests**

In `TestGetPolicies_ResponseFieldsRoundTrip` (`server_test.go:162-204`), after
`srv := newTestServerWithPolicies(t, dir)` (line 181), seed a checkin before calling `GetPolicies`:

```go
	srv := newTestServerWithPolicies(t, dir)
	require.NoError(t, srv.checkins.RecordCheckin(storageID, "bwfs-east.internal", time.Now()))
```

and change the assertion (line 190) from:

```go
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination, "destination must resolve live from storage_policy_id")
```

to:

```go
	assert.Equal(t, []string{"bwfs-east.internal:8080"}, p.Destinations, "destinations must resolve live from storage_policy_id's checkins")
```

Rename `TestGetPolicies_DanglingStoragePolicyIdLeavesDestinationUnset` (`server_test.go:443-455`) to
`TestGetPolicies_DanglingStoragePolicyIdLeavesDestinationsEmpty` and change its final assertion from
`assert.Empty(t, resp.Policies[0].Destination)` to `assert.Empty(t, resp.Policies[0].Destinations)`.

Rename `TestListPolicies_ResolvesDestinationFromStoragePolicyId` (`server_test.go:457-479`) to
`TestListPolicies_ResolvesDestinationsFromStoragePolicyCheckins`, seed a checkin before calling
`ListPolicies` (mirroring the `GetPolicies` test above):

```go
	srv := newTestServerWithPolicies(t, dir)
	require.NoError(t, srv.checkins.RecordCheckin(storageID, "bwfs-east.internal", time.Now()))

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{Type: "backup"})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, []string{"bwfs-east.internal:8080"}, resp.Policies[0].Destinations)
```

Add two new tests directly after `TestListPolicies_ResolvesDestinationsFromStoragePolicyCheckins`:

```go
func TestGetPolicies_MultipleCheckinsAllAppearOrderedFreshestFirst(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east.json", `{
		"metadata": {"name": "east-storage"},
		"client_filters": {"hostnames": ["bwfs-*"]},
		"port": 8080,
		"config": {}
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	storageID := c.Policies()[0].Meta().ID

	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", fmt.Sprintf(`{
		"metadata": {"name": "nightly"},
		"storage_policy_id": %q
	}`, storageID))
	srv := newTestServerWithPolicies(t, dir)

	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	require.NoError(t, srv.checkins.RecordCheckin(storageID, "bwfs-1", older))
	require.NoError(t, srv.checkins.RecordCheckin(storageID, "bwfs-2", newer))

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, []string{"bwfs-2:8080", "bwfs-1:8080"}, resp.Policies[0].Destinations, "the most recently checked-in host must come first")
}

func TestGetPolicies_StoragePolicyWithNoCheckinsYieldsEmptyDestinations(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east.json", `{
		"metadata": {"name": "east-storage"},
		"client_filters": {"hostnames": ["bwfs-*"]},
		"port": 8080,
		"config": {}
	}`)
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	storageID := c.Policies()[0].Meta().ID

	writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", fmt.Sprintf(`{
		"metadata": {"name": "nightly"},
		"storage_policy_id": %q
	}`, storageID))
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Empty(t, resp.Policies[0].Destinations, "a storage policy nobody has checked in against yet must yield no destinations")
}
```

- [ ] **Step 8: Update `write_test.go`**

Update `createTestStoragePolicy` (`write_test.go:52-63`) to also seed a checkin for the storage policy
it just created, using the same `hostname` argument callers already pass — every existing caller wants
"a storage policy that actually resolves to a destination", which now requires a checkin, not just a
`client_filters.hostnames` entry:

```go
func createTestStoragePolicy(t *testing.T, srv *policyServerServer, hostname string, port int32) string {
	t.Helper()
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "storage-for-" + hostname,
		Type:          "storage",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{hostname}},
		Port:          port,
		Config:        "{}",
	})
	require.NoError(t, err)
	require.NoError(t, srv.checkins.RecordCheckin(resp.Id, hostname, time.Now()))
	return resp.Id
}
```

Update the two assertions that read the now-removed singular field:
- `write_test.go:91`: `assert.Equal(t, "bwfs:8080", resp.Destination, "destination must resolve from the referenced storage policy")`
  → `assert.Equal(t, []string{"bwfs:8080"}, resp.Destinations, "destinations must resolve from the referenced storage policy's checkins")`
- `write_test.go:236`: `assert.Equal(t, "bwfs:9090", resp.Destination)` → `assert.Equal(t, []string{"bwfs:9090"}, resp.Destinations)`

- [ ] **Step 9: Run the full `policy-server` test suite**

Run: `cd /home/alex/miniprotector/src && go build ./... && go test ./cmd/policy-server/... -v`
Expected: builds cleanly, all tests pass, including the two new tests from Step 7 and every renamed
one from Steps 6-8.

- [ ] **Step 10: Commit**

```bash
cd /home/alex/miniprotector
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go \
  src/cmd/policy-server/cache.go src/cmd/policy-server/cache_test.go \
  src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go \
  src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go \
  src/cmd/policy-server/backup_policy.go src/cmd/policy-server/backup_policy_test.go
git commit -m "feat(policy-server): resolve backup policy destinations from storage checkins"
```

---

### Task 3: Update `policyclient` to cache destinations as a list

**Files:**
- Modify: `src/cmd/policyclient/fetch.go:45-69,124-155`
- Test: `src/cmd/policyclient/fetch_test.go:37-83`

**Interfaces:**
- Consumes: `pb.Policy.GetDestinations() []string` (Task 2).
- Produces: `policyclient.CachedPolicy.Destinations []string` (`json:"destinations"`), written to
  `policies-cache.json`. Task 4 (`agent`) reads this exact JSON shape.

- [ ] **Step 1: Update the fixture and assertion in `fetch_test.go` first**

In `TestRunFetch_Success_WritesCacheFile` (`fetch_test.go:37-83`), change the fixture at line 56 from:

```go
				Destination:  "bwfs-east.internal:8080",
```

to:

```go
				Destinations: []string{"bwfs-east.internal:8080"},
```

and the assertion at line 81 from:

```go
	assert.Equal(t, "bwfs-east.internal:8080", got[0].Destination)
```

to:

```go
	assert.Equal(t, []string{"bwfs-east.internal:8080"}, got[0].Destinations)
```

- [ ] **Step 2: Run the test to verify it fails to compile**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/policyclient/... -run TestRunFetch_Success_WritesCacheFile -v`
Expected: FAIL to build — `pb.Policy` has no field `Destinations` used as a struct literal key
(`Destination` is what's currently accepted; the fixture now names a field that doesn't exist yet on
`CachedPolicy`, and `pb.Policy` itself already changed in Task 2, so `Destination:` in the fixture
would also no longer compile).

- [ ] **Step 3: Update `CachedPolicy` and `toCachedPolicies`**

In `src/cmd/policyclient/fetch.go`, change the `CachedPolicy` struct field at line 56:

```go
	Destination   string         `json:"destination"`
```

to:

```go
	Destinations  []string       `json:"destinations"`
```

and in `toCachedPolicies` (line 147):

```go
			Destination:   p.GetDestination(),
```

to:

```go
			Destinations:  p.GetDestinations(),
```

- [ ] **Step 4: Run the full `policyclient` test suite**

Run: `cd /home/alex/miniprotector/src && go build ./... && go test ./cmd/policyclient/... -v`
Expected: builds cleanly, all tests pass.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go
git commit -m "feat(policyclient): cache a backup policy's destinations as a list"
```

---

### Task 4: Update `agent` to exec `brfs` with the first cached destination

**Files:**
- Modify: `src/cmd/agent/backup.go:30-49,190-229`
- Test: `src/cmd/agent/backup_test.go` (fixtures at lines 108,133,155,182,212,244,260,276,293,301,360,380,408,427,330-336)

**Interfaces:**
- Consumes: `policyclient.CachedPolicy`'s on-disk JSON shape — `agent`'s `cachedPolicy` duplicates the
  subset it needs (documented existing pattern, `backup.go:30-33`), now including `destinations` as a
  JSON array instead of `destination` as a string (Task 3).
- Produces: `backupTasks` still returns `[]Policy` with a `--destination <value>` arg — behavior for a
  populated list is unchanged (same `"host:port"` string ends up in `Args`), only the source field
  name/shape changes, plus an empty list now produces an empty `--destination` arg exactly the way an
  empty/missing string did before.

- [ ] **Step 1: Update the JSON test fixtures first (bulk mechanical rename)**

Run this to convert every `"destination": "X"` fixture in the file to `"destinations": ["X"]`:

```bash
cd /home/alex/miniprotector
sed -i -E 's/"destination": "([^"]*)"/"destinations": ["\1"]/g' src/cmd/agent/backup_test.go
```

Verify it hit all 14 occurrences and none were missed or double-transformed:

```bash
grep -n '"destinations"' src/cmd/agent/backup_test.go | wc -l   # expect 14
grep -n '"destination":' src/cmd/agent/backup_test.go            # expect no output
```

- [ ] **Step 2: Update the one Go-struct-literal fixture**

In `TestBackupTasks_JobIDFieldMatchesArgsFlag` (`backup_test.go:330-336`), change:

```go
	cached := []cachedPolicy{{
		Name:          "web-policy",
		Type:          "backup",
		ObjectFilters: []ObjectFilter{{Path: "/srv/web"}},
		RPO:           "1h",
		BackupWindow:  []string{"* * * * *"},
		Destination:   "bwfs:9000",
	}}
```

to:

```go
	cached := []cachedPolicy{{
		Name:          "web-policy",
		Type:          "backup",
		ObjectFilters: []ObjectFilter{{Path: "/srv/web"}},
		RPO:           "1h",
		BackupWindow:  []string{"* * * * *"},
		Destinations:  []string{"bwfs:9000"},
	}}
```

- [ ] **Step 3: Run the test suite to verify it fails to compile**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/agent/... -v`
Expected: FAIL to build — `cachedPolicy` has no field `Destinations` yet, and the JSON fixtures'
`"destinations"` key doesn't map onto `cachedPolicy.Destination string`.

- [ ] **Step 4: Update `cachedPolicy` and `backupTasks`**

In `src/cmd/agent/backup.go`, change the `cachedPolicy` struct field at line 40:

```go
	Destination   string         `json:"destination"`
```

to:

```go
	Destinations  []string       `json:"destinations"`
```

In `backupTasks` (`backup.go:226-229`), change:

```go
		policyName, destination := p.Name, p.Destination
		for _, filter := range p.ObjectFilters {
			jobID := backupJobID(policyName, filter.Path, filter.ID, time.Now())
			args := []string{filter.Path, "--destination", destination, "--job-id", jobID}
```

to:

```go
		policyName := p.Name
		var destination string
		if len(p.Destinations) > 0 {
			destination = p.Destinations[0]
		}
		for _, filter := range p.ObjectFilters {
			jobID := backupJobID(policyName, filter.Path, filter.ID, time.Now())
			args := []string{filter.Path, "--destination", destination, "--job-id", jobID}
```

Update the doc comment above `backupTasks` (`backup.go:193-195`) — replace:

```go
// on a guess) is the fail-safe choice. A missing/invalid destination is
// not checked here: the task is still built, and simply fails at brfs
// exec time like any other exec failure (see reconcile.go).
```

with:

```go
// on a guess) is the fail-safe choice. A missing/invalid destination is
// not checked here: the task is still built with an empty --destination
// (Destinations[0] of an empty list), and simply fails at brfs exec time
// like any other exec failure (see reconcile.go). Only Destinations[0] is
// ever used -- retrying the rest of the list on failure is future work.
```

- [ ] **Step 5: Run the full `agent` test suite**

Run: `cd /home/alex/miniprotector/src && go build ./... && go test ./cmd/agent/... -v`
Expected: builds cleanly, all tests pass, including
`TestBackupTasks_TaskArgsIncludeIncludeExcludeFlagsWhenPresent` (still asserts `task.Args[2] ==
"bwfs:8080"`, now sourced from `Destinations[0]`).

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/agent/backup.go src/cmd/agent/backup_test.go
git commit -m "feat(agent): exec brfs with the first cached destination from a list"
```

---

### Task 5: Update `api-server`'s policy DTO to expose destinations

**Files:**
- Modify: `src/cmd/api-server/policies.go:29-71`
- Test: `src/cmd/api-server/policies_test.go:61-62,166-167,200-201`

**Interfaces:**
- Consumes: `pb.Policy.GetDestinations() []string` (Task 2).
- Produces: `policyDTO.Destinations []string` (`json:"destinations"`) in the `GET
  /api/v1/policies`/`GET /api/v1/policies/{id}`/`POST`/`PUT` JSON responses. Task 6 (web) reads this
  exact REST field name.

- [ ] **Step 1: Update the three test fixtures first**

In `src/cmd/api-server/policies_test.go`:
- Line 62: `Policies: []*pb.Policy{{Id: "p1", Name: "nightly", Destination: "bwfs:8080"}},` →
  `Policies: []*pb.Policy{{Id: "p1", Name: "nightly", Destinations: []string{"bwfs:8080"}}},`
- Line 167: `Destination:     "bwfs:8080",` → `Destinations:    []string{"bwfs:8080"},`
- Line 201: `fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1", Name: "nightly", Destination: "bwfs:8080"}}` →
  `fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1", Name: "nightly", Destinations: []string{"bwfs:8080"}}}`

- [ ] **Step 2: Run the test suite to verify it fails to compile**

Run: `cd /home/alex/miniprotector/src && go test ./cmd/api-server/... -v`
Expected: FAIL to build — `pb.Policy` (already changed in Task 2) has no field `Destination`.

- [ ] **Step 3: Update `policyDTO` and `toPolicyDTO`**

In `src/cmd/api-server/policies.go`, change the `policyDTO` struct field at line 39:

```go
	Destination     string            `json:"destination"`
```

to:

```go
	Destinations    []string          `json:"destinations"`
```

and in `toPolicyDTO` at line 69:

```go
		Destination:     p.GetDestination(),
```

to:

```go
		Destinations:    p.GetDestinations(),
```

- [ ] **Step 4: Run the full `api-server` test suite**

Run: `cd /home/alex/miniprotector/src && go build ./... && go test ./cmd/api-server/... -v`
Expected: builds cleanly, all tests pass.

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "feat(api-server): expose a backup policy's destinations as a list"
```

---

### Task 6: Update the Vue admin views to render a destinations list

**Files:**
- Modify: `web/src/views/BackupPolicyView.vue:36-48`
- Modify: `web/src/views/BackupPoliciesView.vue:56-97`
- Test: `web/src/views/BackupPolicyView.spec.js:37-56`
- Test: `web/src/views/BackupPoliciesView.spec.js:33-74`

**Interfaces:**
- Consumes: REST `GET /api/v1/policies`/`GET /api/v1/policies/{id}` response's `destinations: string[]`
  field (Task 5). The `policies` Pinia store passes API JSON through unmodified (`web/src/stores/policies.js`),
  so `policy.destinations` on any policy object read from the store is exactly this array.
- Produces: no new interfaces — this is the last consumer in the chain.

- [ ] **Step 1: Add a failing assertion to `BackupPolicyView.spec.js`**

In `renders the cached policy record` (`BackupPolicyView.spec.js:37-56`), change the fixture at line
44 from `destination: 'store:8080',` to `destinations: ['store:8080', 'store-2:9000'],`, and add an
assertion after the existing three `expect` calls (after line 55):

```js
    expect(wrapper.text()).toContain('store:8080, store-2:9000')
```

- [ ] **Step 2: Add a failing assertion to `BackupPoliciesView.spec.js`**

In `renders each policy with a link to its detail page` (`BackupPoliciesView.spec.js:33-42`), change
the fixture at line 35 from
`{ id: 'p1', name: 'nightly-db-backup', rpo: '1h', destination: 'store:8080' }` to
`{ id: 'p1', name: 'nightly-db-backup', rpo: '1h', destinations: ['store:8080'] }`, and add an
assertion after line 39:

```js
    expect(wrapper.text()).toContain('store:8080')
```

Also update the two other fixtures in the same file that use the old shape, for consistency (no new
assertions needed on these — they only exercise delete behavior):
- Line 57: `destination: 'store:8080'` → `destinations: ['store:8080']`
- Line 68: `destination: 'store:8080'` → `destinations: ['store:8080']`

- [ ] **Step 3: Run both spec files to verify the new assertions fail**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/views/BackupPolicyView.spec.js src/views/BackupPoliciesView.spec.js`
Expected: FAIL — `store:8080, store-2:9000` and the table's `store:8080` text aren't rendered yet,
since the components still read `policy.destination` (singular, now `undefined`).

- [ ] **Step 4: Update `BackupPolicyView.vue`**

Change line 40:

```js
    { key: 'destination', label: 'Destination', value: policy.value.destination },
```

to:

```js
    { key: 'destination', label: 'Destination', value: (policy.value.destinations || []).join(', ') || '—' },
```

(matching the exact pattern already used for `backupWindow`/`hostnames` two lines below it).

- [ ] **Step 5: Update `BackupPoliciesView.vue`**

Change the column definition at line 59:

```js
  { label: 'Destination', field: 'destination', sortable: true },
```

to:

```js
  { label: 'Destination', field: 'destinations', sortable: true },
```

Add a rendering branch in the `#table-row` template (after the `actions` branch, before the generic
`v-else`, around line 88-96):

```html
          <span v-else-if="column.field === 'destinations'">{{ (row.destinations || []).join(', ') || '—' }}</span>
          <span v-else>{{ formattedRow[column.field] }}</span>
```

(replacing the single existing `<span v-else>{{ formattedRow[column.field] }}</span>` with these two
lines.)

- [ ] **Step 6: Run both spec files to verify they pass, then the full web test suite**

Run: `cd /home/alex/miniprotector/web && npx vitest run src/views/BackupPolicyView.spec.js src/views/BackupPoliciesView.spec.js`
Expected: PASS.

Run: `cd /home/alex/miniprotector/web && npx vitest run`
Expected: PASS, no other spec broken by the column-field rename.

- [ ] **Step 7: Commit**

```bash
cd /home/alex/miniprotector
git add web/src/views/BackupPolicyView.vue web/src/views/BackupPoliciesView.vue \
  web/src/views/BackupPolicyView.spec.js web/src/views/BackupPoliciesView.spec.js
git commit -m "feat(web): render a backup policy's destinations list, not a single string"
```

---

### Task 7: Documentation and changelog

**Files:**
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/agent.md`
- Modify: `docs/api/rest-v1.md`
- Modify: `docs/components/api-server.md`
- Modify: `CHANGELOG.md`
- Modify: `backlog.md`

**Interfaces:** none — documentation only, no code.

- [ ] **Step 1: `docs/protocols/policy-server.md`**

In the proto listing, replace line 56:

```proto
  string destination = 7; // derived, read-only -- see below
```

with:

```proto
  reserved 7; reserved "destination"; // removed -- replaced by destinations, see below
```

and add, right after line 65 (`repeated PolicyCheckin checkins = 16; ...`):

```proto
  repeated string destinations = 17; // backup policy only, derived, read-only -- see below
```

Replace line 137:

```markdown
  `destination` is never itself a settable field on either type -- see below.
```

with:

```markdown
  `destinations` is never itself a settable field on either type -- see below.
```

Replace the paragraph at lines 156-167 (from "`rpo` and `backup_window` are opaque..." through "...a
known, currently accepted limitation (see `backlog.md`).") with:

```markdown
- `rpo` and `backup_window` are opaque, pass-through strings — `policy-server` never parses or
  evaluates either. `destinations` is derived, read-only: a `"backup"` policy instead carries
  `storage_policy_id`, a required reference to a `"storage"`-typed policy's `id`.
  `GetPolicies`/`ListPolicies`/`CreatePolicy`/`UpdatePolicy` all resolve it live, on every response,
  from that storage policy's checkin records (see [Design: backup destination from checkin
  list](../superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md)) — one
  `"host:port"` entry per host that has checked in against the storage policy, combined with its
  `port`, ordered freshest-checked-in-first. This is a real, client-confirmed list of storage
  servers, not a static `client_filters.hostnames` guess: a storage policy targeted purely by labels
  (no `client_filters.hostnames`) now still resolves correctly once any matching node checks in,
  closing the gap the previous hostname-pattern-based resolution had. `destinations` is empty if the
  reference doesn't resolve (an id that doesn't exist, or no longer names a storage policy) or if the
  referenced storage policy has no checkins yet (a brand-new one, or one every check-in for has aged
  past `CheckinRetentionSec`).
```

- [ ] **Step 2: `docs/components/policy-server.md`**

Replace lines 66-70 (from "A `\"backup\"` policy describes..." through "...updates every backup
policy linked to it with no re-save needed.") with:

```markdown
A `"backup"` policy describes what to back up and, via `storage_policy_id`, where: `object_filters`,
`rpo`, `backup_window`, and a required reference to a `"storage"`-typed policy's `id`. Its
`destinations` (one `"host:port"` entry per storage server, freshest-checked-in-first) is never
itself stored or settable — it's computed live from the referenced storage policy's checkin records
every time `policy-server` returns the policy, so a storage node checking in under a new hostname (or
simply staying alive) keeps every backup policy linked to it current with no re-save needed. See
[Design: backup destination from checkin list](../superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md).
```

Replace lines 91-92 (from "`destination`, unlike every other backup-policy field..." through "...the
storage policy `storage_policy_id` names.") with:

```markdown
`destinations`, unlike every other backup-policy field, is never read from the on-disk JSON: it's
computed at read time from the checkin records against the storage policy `storage_policy_id` names,
ordered freshest-checked-in-first (`storage/policyserver`'s `CheckinsForPolicy` query, not re-sorted
downstream).
```

- [ ] **Step 3: `docs/components/agent.md`**

Replace line 81-82:

```markdown
When due, `agent` execs `brfs <path> --destination <destination> --job-id
backup:<policy>:<slug(path)>:<short-filter-id>:<timestamp>`, appending `--include <patterns>`
```

with:

```markdown
When due, `agent` execs `brfs <path> --destination <destinations[0]> --job-id
backup:<policy>:<slug(path)>:<short-filter-id>:<timestamp>`, appending `--include <patterns>`
```

Replace lines 96-99:

```markdown
A policy with an unparseable `rpo`, or no valid `backup_window` entry at all, contributes no tasks.
A missing or invalid `destination` is not checked in advance — the task is still created, and its
`brfs` exec simply fails (recorded as an ordinary failure with backoff), the same as any other exec
failure.
```

with:

```markdown
A policy with an unparseable `rpo`, or no valid `backup_window` entry at all, contributes no tasks.
A missing or invalid `destinations[0]` is not checked in advance — the task is still created, and its
`brfs` exec simply fails (recorded as an ordinary failure with backoff), the same as any other exec
failure. Only `destinations[0]` is ever used; retrying the rest of the list is not implemented.
```

- [ ] **Step 4: `docs/api/rest-v1.md`**

Replace line 169:

```json
      "destination": "bwfs-east.internal:8080",
```

with:

```json
      "destinations": ["bwfs-east.internal:8080"],
```

Replace line 210 (from "The response's `destination` is always derived..." through "...something this
body sets directly."):

```markdown
`201` with the created policy (including its server-assigned `id` and each object filter's `id`) on
success. `400` if `name` is empty or slugifies to nothing (no alphanumeric characters), any
`include`/`exclude`/hostname entry isn't a syntactically valid glob pattern, `storage_policy_id` is
empty, or `storage_policy_id` doesn't name an existing storage policy — no file is written when
validation fails. The response's `destinations` is always derived live from `storage_policy_id`'s
checkin records, never something this body sets directly.
```

- [ ] **Step 5: `docs/components/api-server.md`**

Replace lines 40-41:

```markdown
`rpo`/`storage_policy_id`/`object_filters`). A `"backup"` policy's `destination` in the response DTO
is always derived by `policy-server` from its `storage_policy_id` — it's never itself part of the
```

with:

```markdown
`rpo`/`storage_policy_id`/`object_filters`). A `"backup"` policy's `destinations` in the response DTO
is always derived by `policy-server`, live from its `storage_policy_id`'s checkin records — it's
never itself part of the
```

- [ ] **Step 6: `CHANGELOG.md`**

Add a new entry at the top (after the `# Changelog` header and its intro line, before the existing
`## 2026-08-04 — web: navigation shell...` entry):

```markdown
## 2026-08-04 — policy-server: resolve backup destinations from storage checkins

A backup policy's destination was resolved from `client_filters.hostnames[0]` — a glob-matching
pattern meant for targeting, not an address — silently breaking for any storage policy with a
wildcard or more than one matching host. It's now resolved from the storage policy's live checkin
list instead: one `host:port` entry per host that has actually checked in against it, ordered
freshest-first. `Policy.destination` (a single string) is replaced outright by `Policy.destinations`
(a list) across the wire, `policyclient`'s on-disk cache, `agent` (which uses the first entry when
execing `brfs`), `api-server`'s REST responses, and the web admin views — a breaking change with no
compatibility shim, since every consumer is inside this repo.
```

- [ ] **Step 7: `backlog.md`**

In the "Policy check-in / resolvable-storage-server list" section (lines 5-24), replace the whole
section with a short resolved note (keep the "Frontend angle" and "Related gap" subsections below it
untouched):

```markdown
## Policy check-in / resolvable-storage-server list

Resolved 2026-08-04: `policy-server` now resolves a backup policy's `destinations` from its storage
policy's live checkin records instead of `client_filters.hostnames[0]`, closing the gap described
below. See
[Design: backup destination from checkin list](docs/superpowers/specs/2026-08-04-backup-destination-checkin-list-design.md).
```

- [ ] **Step 8: Commit**

```bash
cd /home/alex/miniprotector
git add docs/protocols/policy-server.md docs/components/policy-server.md docs/components/agent.md \
  docs/api/rest-v1.md docs/components/api-server.md CHANGELOG.md backlog.md
git commit -m "docs: document backup destinations resolved from storage checkins"
```
