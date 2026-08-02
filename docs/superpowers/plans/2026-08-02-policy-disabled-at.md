# Generic `disabled_at` Policy Field Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a generic, type-agnostic `disabled_at` timestamp to `policy-server`'s `Policy` schema so any policy (backup or storage) can stop being served/acted on after a point in time, with no "adhoc" concept anywhere in `policy-server` or `agent` — the primitive a future one-time/ad hoc backup capability will build on.

**Architecture:** `disabled_at` flows end-to-end as an ordinary additive field, the same way `type`/`port`/`config` were added before it: proto → `policy-server`'s `Metadata` → `GetPolicies`'s live filter → `policyclient`'s on-disk cache → `agent`'s `backupTasks`/`storageTasks` skip check. No new RPC, no new policy type, no changes to `agent`'s due-computation, pruning, or scheduling logic beyond one skip condition per task-deriving function.

**Tech Stack:** Go, gRPC/protobuf (`protoc --go_out`), `testify` (`assert`/`require`).

## Global Constraints

- `disabled_at` is a `google.protobuf.Timestamp` *message* field (not a string like `rpo`), on `Policy`, `CreatePolicyRequest`, and `UpdatePolicyRequest` — a message field lets "unset" be a nil pointer on the wire, distinct from an explicit zero timestamp.
- A nil `*timestamppb.Timestamp` must convert to Go's zero `time.Time{}` (year 1), **not** the Unix epoch (1970) that `(*Timestamp).AsTime()` produces on a nil receiver. Every proto→domain conversion site needs an explicit `disabledAtFromProto` helper instead of calling `.AsTime()` directly. This helper is duplicated per-package (`policy-server`, `policyclient`) — matches this codebase's existing convention of duplicating small schema-adjacent helpers rather than sharing across command packages.
- `GetPolicies` excludes an already-disabled policy, checked against `time.Now()` at request time (not cached at `Reload`). `ListPolicies` is unaffected — it keeps returning every policy regardless of `disabled_at`.
- `agent`'s `backupTasks`/`storageTasks` skip a disabled cached policy, evaluated fresh every reconcile tick (both functions already run fresh each tick, so this falls out naturally). No changes to `reconcile.go`'s `prune()`/`run()` — a disabled policy simply stops appearing in a tick's task-ID set, and the existing prune mechanism already removes anything no longer in that set.
- `UpdatePolicy` stays full-replace for `disabled_at`, same as every other editable field — no merge/patch semantics.
- No validation rejects a `disabled_at` already in the past at create/update time.
- No automatic cleanup of on-disk policy files whose `disabled_at` has passed.
- Spec: `docs/superpowers/specs/2026-08-02-policy-disabled-at-design.md`.

---

### Task 1: `disabled_at` on the wire (proto)

**Files:**
- Modify: `src/api/policyserver.proto`
- Generated (do not hand-edit): `src/api/policyserver.pb.go`

**Interfaces:**
- Produces: `pb.Policy.GetDisabledAt() *timestamppb.Timestamp`, `pb.CreatePolicyRequest.GetDisabledAt() *timestamppb.Timestamp`, `pb.UpdatePolicyRequest.GetDisabledAt() *timestamppb.Timestamp` — consumed by Tasks 2, 3, 4.

- [ ] **Step 1: Add the field to `Policy`**

In `src/api/policyserver.proto`, inside `message Policy { ... }`, after the `config = 13;` field (the last one before the closing brace):

```proto
  // Zero/unset means never disabled. Once this time passes, GetPolicies
  // stops returning the policy (checked live, not cached); ListPolicies is
  // unaffected. Generic across every policy type -- policy-server attaches
  // no meaning to *why* a policy is disabled.
  google.protobuf.Timestamp disabled_at = 14;
```

- [ ] **Step 2: Add the field to `CreatePolicyRequest`**

Inside `message CreatePolicyRequest { ... }`, after `string config = 10;`:

```proto
  google.protobuf.Timestamp disabled_at = 11;
```

- [ ] **Step 3: Add the field to `UpdatePolicyRequest`**

Inside `message UpdatePolicyRequest { ... }`, after `string config = 10;`:

```proto
  google.protobuf.Timestamp disabled_at = 11;
```

- [ ] **Step 4: Regenerate the Go bindings**

Run: `make proto`
Expected: completes with `Protobuf code generated in src/api/` and no errors. `git diff src/api/policyserver.pb.go` should show new `DisabledAt` fields/getters on `Policy`, `CreatePolicyRequest`, `UpdatePolicyRequest`.

- [ ] **Step 5: Verify the whole module still builds**

Run: `cd src && go build ./...`
Expected: exits 0. This is the task's test — a pure schema/codegen change has no unit test of its own; the deliverable is "the new field exists and everything downstream still compiles," which later tasks build on.

- [ ] **Step 6: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go
git commit -m "feat(api): add disabled_at to Policy/CreatePolicyRequest/UpdatePolicyRequest"
```

---

### Task 2: `policy-server` domain layer — `Metadata.DisabledAt` and proto conversion

**Files:**
- Modify: `src/cmd/policy-server/policy.go` (`Metadata` struct)
- Modify: `src/cmd/policy-server/write.go` (`buildPolicyForCreate`, `buildPolicyForUpdate`, new `disabledAtFromProto` helper)
- Modify: `src/cmd/policy-server/backup_policy.go` (`BackupPolicy.ToProto`)
- Modify: `src/cmd/policy-server/storage_policy.go` (`StoragePolicy.ToProto`)
- Test: `src/cmd/policy-server/write_test.go`

**Interfaces:**
- Consumes: `pb.Policy.GetDisabledAt()`, `pb.CreatePolicyRequest.GetDisabledAt()`, `pb.UpdatePolicyRequest.GetDisabledAt()` (Task 1).
- Produces: `Metadata.DisabledAt time.Time`, `disabledAtFromProto(ts *timestamppb.Timestamp) time.Time` — consumed by Task 3 (`server.go`'s `GetPolicies` filter).

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/write_test.go`:

```go
func TestCreatePolicy_DisabledAtRoundTrips(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	disabledAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:        "one-shot",
		Type:        "backup",
		Destination: "bwfs:8080",
		DisabledAt:  timestamppb.New(disabledAt),
	})

	require.NoError(t, err)
	require.NotNil(t, resp.DisabledAt)
	assert.Equal(t, disabledAt, resp.DisabledAt.AsTime())
}

func TestCreatePolicy_NoDisabledAtLeavesItUnset(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:        "ordinary",
		Type:        "backup",
		Destination: "bwfs:8080",
	})

	require.NoError(t, err)
	assert.Nil(t, resp.DisabledAt, "an omitted disabled_at must stay unset, not become the Unix epoch")
}

func TestCreatePolicy_PastDisabledAtAcceptedWithoutError(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:        "already-expired",
		Type:        "backup",
		Destination: "bwfs:8080",
		DisabledAt:  timestamppb.New(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)),
	})

	require.NoError(t, err)
	require.NotNil(t, resp.DisabledAt)
}

func TestUpdatePolicy_DisabledAtRoundTrips(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)
	created, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:        "will-be-disabled",
		Type:        "backup",
		Destination: "bwfs:8080",
	})
	require.NoError(t, err)

	disabledAt := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	updated, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:          created.Id,
		Name:        created.Name,
		Destination: "bwfs:8080",
		DisabledAt:  timestamppb.New(disabledAt),
	})

	require.NoError(t, err)
	require.NotNil(t, updated.DisabledAt)
	assert.Equal(t, disabledAt, updated.DisabledAt.AsTime())
}
```

Add `"google.golang.org/protobuf/types/known/timestamppb"` to that file's import block if not already present (`fetch_test.go` in `policyclient` already imports it the same way, for reference).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run 'DisabledAt' -v`
Expected: FAIL — `unknown field DisabledAt in struct literal of type pb.CreatePolicyRequest` (or similar), since Task 1 only added the proto field; nothing reads or writes it yet in this package's domain logic. If Task 1 wasn't completed first, this step instead fails to compile — same "not implemented yet" signal either way.

- [ ] **Step 3: Add `DisabledAt` to `Metadata`**

In `src/cmd/policy-server/policy.go`:

```go
type Metadata struct {
	ID         string    `json:"-"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	DisabledAt time.Time `json:"disabled_at,omitempty"`
}
```

(`PolicyBase.clone()` needs no change — it already copies `Metadata` by value, which now includes `DisabledAt`.)

- [ ] **Step 4: Add the nil-safe conversion helper and wire it into `buildPolicyForCreate`/`buildPolicyForUpdate`**

In `src/cmd/policy-server/write.go`, add near the top (alongside `fromProtoClientFilters`):

```go
// disabledAtFromProto converts a possibly-nil disabled_at field to
// time.Time, treating "field not set" as the zero time -- distinct from
// (*timestamppb.Timestamp).AsTime()'s own nil-safe behavior, which maps a
// nil Timestamp to the Unix epoch (1970), not Go's zero time.Time (year 1).
func disabledAtFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}
```

Add `"google.golang.org/protobuf/types/known/timestamppb"` to `write.go`'s imports.

Update `buildPolicyForCreate`'s `Metadata{...}` literal:

```go
func buildPolicyForCreate(req *pb.CreatePolicyRequest, now time.Time) (Policy, error) {
	base := PolicyBase{
		Metadata: Metadata{
			Name:       req.GetName(),
			CreatedAt:  now,
			UpdatedAt:  now,
			DisabledAt: disabledAtFromProto(req.GetDisabledAt()),
		},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
	}
	return buildPolicy(req.GetType(), base, req)
}
```

Update `buildPolicyForUpdate`'s `Metadata{...}` literal the same way:

```go
func buildPolicyForUpdate(req *pb.UpdatePolicyRequest, kind string, existingMeta Metadata, now time.Time) (Policy, error) {
	if kind != "backup" && kind != "storage" {
		return nil, fmt.Errorf("existing policy has unknown type %q", kind)
	}
	base := PolicyBase{
		Metadata: Metadata{
			Name:       req.GetName(),
			CreatedAt:  existingMeta.CreatedAt,
			UpdatedAt:  now,
			DisabledAt: disabledAtFromProto(req.GetDisabledAt()),
		},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
	}
	return buildPolicy(kind, base, req)
}
```

Note `buildPolicyForUpdate` does **not** preserve `existingMeta.DisabledAt` the way it preserves `existingMeta.CreatedAt` — `disabled_at` is operator-settable like `Name`/`ClientFilters` (full-replace), not system-computed like `CreatedAt`. An `UpdatePolicy` call that omits `disabled_at` clears it.

- [ ] **Step 5: Emit `DisabledAt` in both policy types' `ToProto`**

In `src/cmd/policy-server/backup_policy.go`, inside `BackupPolicy.ToProto`, after building `pp`:

```go
	pp := &pb.Policy{
		Id:            p.Metadata.ID,
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
		Destination:   p.Destination,
		Type:          p.Type,
	}
	if !p.Metadata.DisabledAt.IsZero() {
		pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
```

In `src/cmd/policy-server/storage_policy.go`, inside `StoragePolicy.ToProto`, the same way:

```go
	pp := &pb.Policy{
		Id:        p.Metadata.ID,
		Name:      p.Metadata.Name,
		CreatedAt: timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt: timestamppb.New(p.Metadata.UpdatedAt),
		Type:      p.Type,
		Port:      int32(p.Port),
		Config:    string(p.Config),
	}
	if !p.Metadata.DisabledAt.IsZero() {
		pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
```

Only setting the field when non-zero keeps "never disabled" a nil field on the wire, not an explicit epoch timestamp.

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run 'DisabledAt' -v`
Expected: PASS, all four new tests.

- [ ] **Step 7: Run the full `policy-server` package test suite**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS — confirms nothing existing broke (e.g. `TestCreatePolicy_WritesFileAndReturnsPolicyWithID`, which doesn't set `DisabledAt` at all, must still pass with `resp.DisabledAt == nil`).

- [ ] **Step 8: Commit**

```bash
git add src/cmd/policy-server/policy.go src/cmd/policy-server/write.go src/cmd/policy-server/backup_policy.go src/cmd/policy-server/storage_policy.go src/cmd/policy-server/write_test.go
git commit -m "feat(policy-server): carry disabled_at through Create/UpdatePolicy and ToProto"
```

---

### Task 3: `GetPolicies` excludes disabled policies

**Files:**
- Modify: `src/cmd/policy-server/server.go`
- Test: `src/cmd/policy-server/server_test.go`

**Interfaces:**
- Consumes: `Metadata.DisabledAt` (Task 2).
- Produces: `isDisabled(m Metadata, now time.Time) bool` — package-private, no other task depends on it.

- [ ] **Step 1: Write the failing tests**

Add to `src/cmd/policy-server/server_test.go`:

```go
func TestGetPolicies_ExcludesPolicyPastItsDisabledAt(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "expired.json", `{
		"metadata": {"name": "expired-policy", "disabled_at": "2020-01-01T00:00:00Z"}
	}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "active.json", `{
		"metadata": {"name": "active-policy"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})

	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "active-policy", resp.Policies[0].Name)
}

func TestGetPolicies_IncludesPolicyWithFutureDisabledAt(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "not-yet.json", `{
		"metadata": {"name": "not-yet-disabled", "disabled_at": "2099-01-01T00:00:00Z"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})

	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	assert.Equal(t, "not-yet-disabled", resp.Policies[0].Name)
}

func TestListPolicies_IncludesDisabledPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "expired.json", `{
		"metadata": {"name": "expired-policy", "disabled_at": "2020-01-01T00:00:00Z"}
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.ListPolicies(context.Background(), &pb.ListPoliciesRequest{})

	require.NoError(t, err)
	require.Len(t, resp.Policies, 1, "ListPolicies is the admin visibility surface -- it must still show a disabled policy")
	assert.NotNil(t, resp.Policies[0].DisabledAt)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestGetPolicies_Excludes|TestGetPolicies_IncludesPolicyWithFutureDisabledAt|TestListPolicies_IncludesDisabledPolicies' -v`
Expected: `TestGetPolicies_ExcludesPolicyPastItsDisabledAt` FAILs (`resp.Policies` has length 2, not 1 — nothing filters yet). The other two already pass by construction (no filtering exists yet to break them) — that's fine, they're regression guards for the behavior Step 3 is about to add.

- [ ] **Step 3: Add the filter**

In `src/cmd/policy-server/server.go`, add above `GetPolicies`:

```go
// isDisabled reports whether m's DisabledAt has been set and has passed as
// of now. A zero DisabledAt means "never disabled".
func isDisabled(m Metadata, now time.Time) bool {
	return !m.DisabledAt.IsZero() && !m.DisabledAt.After(now)
}
```

Add `"time"` to `server.go`'s imports if not already present.

In `GetPolicies`, change the matching loop:

```go
	var matched []*pb.Policy
	for _, p := range s.cache.Policies() {
		if isDisabled(p.Meta(), time.Now()) {
			continue
		}
		if !p.Matches(hostname, labels) {
			continue
		}
		matched = append(matched, p.ToProto(false))
	}
```

`ListPolicies` is left untouched — no filter added there.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/policy-server/... -run 'TestGetPolicies_Excludes|TestGetPolicies_IncludesPolicyWithFutureDisabledAt|TestListPolicies_IncludesDisabledPolicies' -v`
Expected: PASS, all three.

- [ ] **Step 5: Run the full `policy-server` package test suite**

Run: `cd src && go test ./cmd/policy-server/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policy-server/server.go src/cmd/policy-server/server_test.go
git commit -m "feat(policy-server): GetPolicies excludes policies past their disabled_at"
```

---

### Task 4: `policyclient` caches `disabled_at`

**Files:**
- Modify: `src/cmd/policyclient/fetch.go`
- Test: `src/cmd/policyclient/fetch_test.go`

**Interfaces:**
- Consumes: `pb.Policy.GetDisabledAt()` (Task 1).
- Produces: `CachedPolicy.DisabledAt time.Time`, written into `policies-cache.json` as `"disabled_at"` — consumed by Task 5 (`agent`'s `cachedPolicy`).

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/policyclient/fetch_test.go`:

```go
func TestRunFetch_DisabledAtRoundTrips(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")

	disabledAt := timestamppb.New(time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{Id: "id-1", Name: "one-shot", Type: "backup", DisabledAt: disabledAt},
			{Id: "id-2", Name: "ordinary", Type: "backup"},
		},
	}}

	require.NoError(t, runFetch(context.Background(), fake, cachePath, fetchTestLogger()))

	data, err := os.ReadFile(cachePath)
	require.NoError(t, err)
	var cached []CachedPolicy
	require.NoError(t, json.Unmarshal(data, &cached))
	require.Len(t, cached, 2)
	assert.Equal(t, disabledAt.AsTime(), cached[0].DisabledAt)
	assert.True(t, cached[1].DisabledAt.IsZero(), "an unset disabled_at must cache as the zero time, not the Unix epoch")
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/policyclient/... -run TestRunFetch_DisabledAtRoundTrips -v`
Expected: FAIL to compile (`CachedPolicy` has no field `DisabledAt`).

- [ ] **Step 3: Add `DisabledAt` to `CachedPolicy` and populate it**

In `src/cmd/policyclient/fetch.go`, add the nil-safe helper (same reasoning as Task 2's — this package can't import `cmd/policy-server`, so it's duplicated):

```go
// disabledAtFromProto converts a possibly-nil disabled_at field to
// time.Time, treating "field not set" as the zero time -- distinct from
// (*timestamppb.Timestamp).AsTime()'s own nil-safe behavior, which maps a
// nil Timestamp to the Unix epoch (1970), not Go's zero time.Time (year 1).
func disabledAtFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}
```

Add `"google.golang.org/protobuf/types/known/timestamppb"` to `fetch.go`'s imports.

Update `CachedPolicy`:

```go
type CachedPolicy struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	Port          int32          `json:"port,omitempty"`
	Config        string         `json:"config,omitempty"`
	Type          string         `json:"type"`
	DisabledAt    time.Time      `json:"disabled_at,omitempty"`
}
```

Update `toCachedPolicies`:

```go
		out = append(out, CachedPolicy{
			ID:            p.GetId(),
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
			Port:          p.GetPort(),
			Config:        p.GetConfig(),
			Type:          p.GetType(),
			DisabledAt:    disabledAtFromProto(p.GetDisabledAt()),
		})
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd src && go test ./cmd/policyclient/... -run TestRunFetch_DisabledAtRoundTrips -v`
Expected: PASS.

- [ ] **Step 5: Run the full `policyclient` package test suite**

Run: `cd src && go test ./cmd/policyclient/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go
git commit -m "feat(policyclient): carry disabled_at through to policies-cache.json"
```

---

### Task 5: `agent` skips a disabled backup policy

**Files:**
- Modify: `src/cmd/agent/backup.go`
- Test: `src/cmd/agent/backup_test.go`

**Interfaces:**
- Consumes: `"disabled_at"` field in `policies-cache.json` (Task 4's on-disk shape).
- Produces: `cachedPolicy.DisabledAt time.Time` — consumed by Task 6 (`storage.go`'s `storageTasks`, which already reads the same `cachedPolicy` struct via `readCachedPolicies`).

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/agent/backup_test.go`:

```go
func TestBackupTasks_DisabledAtInPastSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "one-shot",
		"type": "backup",
		"object_filters": [{"path": "/data"}],
		"rpo": "5m",
		"backup_window": ["* * * * *"],
		"destination": "bwfs:8080",
		"disabled_at": "2020-01-01T00:00:00Z"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}

	tasks, ok := backupTasks(path, conf)

	assert.True(t, ok, "the file itself was still validly read")
	assert.Empty(t, tasks)
}

func TestBackupTasks_FutureDisabledAtDoesNotSkip(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "still-active",
		"type": "backup",
		"object_filters": [{"path": "/data"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080",
		"disabled_at": "2099-01-01T00:00:00Z"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}

	tasks, ok := backupTasks(path, conf)

	assert.True(t, ok)
	assert.Len(t, tasks, 1)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd src && go test ./cmd/agent/... -run 'TestBackupTasks_DisabledAtInPastSkipsPolicyEntirely|TestBackupTasks_FutureDisabledAtDoesNotSkip' -v`
Expected: `TestBackupTasks_DisabledAtInPastSkipsPolicyEntirely` FAILs (`tasks` has 1 entry, not empty) — the JSON field is silently ignored by `json.Unmarshal` since `cachedPolicy` doesn't declare it yet. `TestBackupTasks_FutureDisabledAtDoesNotSkip` already passes by construction; it's a regression guard for the next step.

- [ ] **Step 3: Add `DisabledAt` to `cachedPolicy` and skip in `backupTasks`**

In `src/cmd/agent/backup.go`, update `cachedPolicy`:

```go
type cachedPolicy struct {
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	Port          int32          `json:"port,omitempty"`
	Config        string         `json:"config,omitempty"`
	DisabledAt    time.Time      `json:"disabled_at,omitempty"`
}
```

In `backupTasks`, add the skip immediately after the existing `p.Type != "backup"` check:

```go
	for _, p := range cachedPolicies {
		if p.Type != "backup" {
			continue
		}
		if !p.DisabledAt.IsZero() && !p.DisabledAt.After(time.Now()) {
			continue
		}
		rpo, err := time.ParseDuration(p.RPO)
		...
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -run 'TestBackupTasks_DisabledAtInPastSkipsPolicyEntirely|TestBackupTasks_FutureDisabledAtDoesNotSkip' -v`
Expected: PASS, both.

- [ ] **Step 5: Run the full `agent` package test suite**

Run: `cd src && go test ./cmd/agent/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/backup.go src/cmd/agent/backup_test.go
git commit -m "feat(agent): skip backup tasks for policies past their disabled_at"
```

---

### Task 6: `agent` skips a disabled storage policy

**Files:**
- Modify: `src/cmd/agent/storage.go`
- Test: `src/cmd/agent/storage_test.go`

**Interfaces:**
- Consumes: `cachedPolicy.DisabledAt` (Task 5 — `storage.go` already calls the same `readCachedPolicies`/`cachedPolicy` defined in `backup.go`).
- Produces: nothing new for later tasks — this is the last functional task.

- [ ] **Step 1: Write the failing test**

Add to `src/cmd/agent/storage_test.go`:

```go
func TestStorageTasks_SkipsDisabledPolicy(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}",
		"disabled_at": "2020-01-01T00:00:00Z"
	}]`)

	tasks, ok := storageTasks(path, testLogger(), "bwfs-bin", "catalogsync-bin")

	assert.True(t, ok, "the file itself was still validly read")
	assert.Empty(t, tasks)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd src && go test ./cmd/agent/... -run TestStorageTasks_SkipsDisabledPolicy -v`
Expected: FAIL — `tasks` has 1 entry, not empty (Task 5 only added the skip inside `backupTasks`; `storageTasks` is a separate loop that hasn't been touched yet).

- [ ] **Step 3: Add the skip to `storageTasks`**

In `src/cmd/agent/storage.go`, inside `storageTasks`'s loop over `cachedPolicies`, add the same skip immediately after its existing `p.Type != "storage"` check:

```go
	for _, p := range cachedPolicies {
		if p.Type != "storage" {
			continue
		}
		if !p.DisabledAt.IsZero() && !p.DisabledAt.After(time.Now()) {
			continue
		}
		var cfg storageConfig
		...
```

(No struct change needed here — `cachedPolicy.DisabledAt` was already added in Task 5, and both functions share the same type.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd src && go test ./cmd/agent/... -run TestStorageTasks_SkipsDisabledPolicy -v`
Expected: PASS.

- [ ] **Step 5: Run the full `agent` package test suite**

Run: `cd src && go test ./cmd/agent/...`
Expected: PASS. This also exercises the existing generic prune tests (`TestPrune_RemovesEntryNotInCurrentIDs`, `TestRun_PrunesOrphanedEntryOnConfirmedGoodTick`) unchanged — they already prove that anything absent from a tick's task-ID set gets pruned from `agent-state.json`, which is exactly what now happens to a disabled policy's task automatically, with no dedicated pruning code of its own.

- [ ] **Step 6: Run the full module build and test suite**

Run: `cd src && go build ./... && go test ./...`
Expected: both succeed — confirms Tasks 1-6 compose cleanly end to end.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/agent/storage.go src/cmd/agent/storage_test.go
git commit -m "feat(agent): skip storage-policy supervision for policies past their disabled_at"
```

---

### Task 7: Documentation

**Files:**
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `docs/components/policyclient.md`
- Modify: `docs/components/agent.md`
- Modify: `CHANGELOG.md`

**Interfaces:** none — documentation only, no code.

- [ ] **Step 1: Update `docs/protocols/policy-server.md`**

Add `disabled_at` to the `Policy`, `CreatePolicyRequest`, and `UpdatePolicyRequest` message listings in the `## RPC` section (mirroring `src/api/policyserver.proto` exactly, including the `reserved 11`/`reserved 8` lines already there):

```proto
message Policy {
  ...
  int32 port = 12;
  string config = 13;
  google.protobuf.Timestamp disabled_at = 14;
}
```

```proto
message CreatePolicyRequest {
  ...
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
}
```

```proto
message UpdatePolicyRequest {
  ...
  int32 port = 9;
  string config = 10;
  google.protobuf.Timestamp disabled_at = 11;
}
```

In the `## Behavior` section, add a new bullet after the existing `port`/`config` bullet:

```markdown
- `disabled_at` is generic across every policy type -- unset (zero/nil) means never disabled. Once it
  passes, `GetPolicies` stops returning that policy to any node, checked live against the current
  time on every call (not cached at load/reload time) -- no `.changed`-touch or restart needed for a
  policy to disappear once its `disabled_at` arrives. `ListPolicies` is unaffected: it keeps returning
  every policy regardless of `disabled_at`, since it's the full-visibility admin surface `api-server`
  proxies. `UpdatePolicy` treats `disabled_at` as full-replace, the same as every other editable
  field -- an update that omits it clears it, it is not preserved automatically the way `created_at`
  is. There is no validation rejecting a `disabled_at` already in the past; a policy created or
  updated that way is simply already inert.
```

- [ ] **Step 2: Update `docs/components/policy-server.md`**

Add a new paragraph after the existing "### Policy files and hot reload" section (before "## Configuration Keys"):

```markdown
### Disabling a policy without deleting it

Every policy, of any type, can carry a `disabled_at` timestamp -- unset by default, meaning "never
disabled." Once that time passes, `GetPolicies` stops returning the policy to any matching node
(checked live against the current time on every call); `ListPolicies` keeps showing it, disabled or
not, since it's the admin/`api-server` visibility surface. `policy-server` attaches no meaning to
*why* a policy is disabled -- it's a generic primitive, not an "adhoc" or "temporary" policy concept
of its own. A one-time backup, for instance, is planned to be nothing more than an ordinary `"backup"`
policy with an unusually permissive `backup_window` and a near-future `disabled_at`, composed by a
future `api-server` convenience endpoint -- neither `policy-server` nor `agent` need to know that
composition happened. See
[Design: generic disabled_at policy field](../superpowers/specs/2026-08-02-policy-disabled-at-design.md).
```

- [ ] **Step 3: Update `docs/components/policyclient.md`**

In the "## Behavior" section, after the paragraph describing `port`/`config` passthrough, add:

```markdown
`disabled_at` is likewise carried through verbatim to `policies-cache.json` for every policy type --
`policyclient` itself never interprets it; `agent` is what acts on it (see
[agent](./agent.md#policy-driven-backup-execution)).
```

- [ ] **Step 4: Update `docs/components/agent.md`**

In the "## Policy-driven backup execution" section, add one sentence after the existing paragraph about a policy with an unparseable `rpo`/no valid `backup_window`:

```markdown
A policy whose `disabled_at` has passed also contributes no tasks -- checked fresh every reconcile
tick against the current time, so a policy that becomes disabled between two ticks stops being acted
on at the very next one, without waiting for `policy-update` to refresh the cache. Its existing
`agent-state.json` entry is removed the same way a deleted policy's already is: it simply stops
appearing in that tick's task list, which the existing pruning in `reconcile.go` already handles.
```

In the "## Storage-policy supervision" section, add a parallel sentence after the paragraph describing an unsupported/missing `backend`:

```markdown
A storage policy whose `disabled_at` has passed is skipped the same way, contributing neither the
`bwfs` nor the `catalogsync` ensure-running task -- an already-running pair is stopped via the same
path used when the policy is edited or deleted outright.
```

- [ ] **Step 5: Add a `CHANGELOG.md` entry**

`CHANGELOG.md` starts with a `# Changelog` title, an intro line, then dated `## YYYY-MM-DD — <short summary>` entries, most recent first (confirmed from its current top entry, `## 2026-07-31 — agent: supervise catalogsync alongside bwfs; demo drops its process-sequencing shell script`). Insert a new entry directly below the intro line, above that `2026-07-31` entry:

```markdown
## 2026-08-02 — policy-server: generic disabled_at field on every policy type

Policies of any type (`"backup"` or `"storage"`) can now carry a `disabled_at` timestamp. Once it
passes, `policy-server`'s `GetPolicies` stops serving that policy and `agent` stops acting on it
(deriving no backup task, supervising no `bwfs`/`catalogsync` process) -- checked live, no restart or
manual reload needed. `ListPolicies` still shows a disabled policy for admin visibility. This is a
generic primitive with no "adhoc" concept baked in anywhere; it's the foundation a future one-time/ad
hoc backup capability (an ordinary backup policy with a near-future `disabled_at`, composed by a
planned `api-server` convenience endpoint) will build on.
```

- [ ] **Step 6: Commit**

```bash
git add docs/protocols/policy-server.md docs/components/policy-server.md docs/components/policyclient.md docs/components/agent.md CHANGELOG.md
git commit -m "docs: document the generic disabled_at policy field"
```
