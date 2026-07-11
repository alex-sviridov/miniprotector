# Agent Backup Execution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the loop from "operator authors a backup policy" to "a real `brfs` backup actually runs" — `agent` gains the ability to derive scheduled backup tasks from its cached policies and execute them.

**Architecture:** `policy-server`'s `Policy` schema gains a `destination` field (host:port), threaded through `policyclient`'s on-disk cache unchanged in shape otherwise. `agent`'s reconcile loop is generalized so a `Policy`'s due-check and execution mode are pluggable instead of hardcoded to a simple interval run synchronously; a new `backupTasks()` function derives one such dynamic `Policy` per `(cached policy, object_filters path)` pair, due when a `backup_window` cron slot is open **and** the path's `rpo` has elapsed, executed as `brfs <path> --destination <dest> --job-id ...` in a background goroutine bounded by a concurrency cap, so a slow backup never delays credential refresh.

**Tech Stack:** Go 1.26, gRPC/protobuf (existing), `github.com/robfig/cron/v3` (new), `testify` for tests.

## Global Constraints

- Every new/changed `local.conf` key follows the existing `common/config` pattern exactly: struct field → default in `ParseConfig`'s init block → `strconv.Atoi` switch case → two tests (default + explicit parse), mirroring `OperatingCertFetchIntervalSec`.
- No behavior change for `agent`'s three existing policies (`bootstrap-refresh`, `operating-refresh`, `policy-update`) — all existing tests in `cmd/agent` must keep passing with only mechanical signature updates (new required parameters), never a logic change to their outcomes.
- Per `.claude/CLAUDE.md`: any `.proto` change requires updating the matching `docs/protocols/` file before commit; any feature/behavior change requires updating the relevant `docs/components/` file(s) and `README.md`/`docs/ARCHITECTURE.md` if topology/data-flow is affected; a `CHANGELOG.md` entry is required before merging to `main`.
- Design reference: `docs/superpowers/specs/2026-07-10-agent-backup-execution-design.md`.
- **Deviation from the design doc:** the design doc names the new concurrency-cap config key
  `MaxConcurrentBackupJobsInt`. `src/common/config/config.go`'s actual `Config` struct has no
  `*Int`-suffix precedent anywhere (plain ints are unsuffixed, e.g. `DefaultStreams`,
  `CatalogSyncBatchSize`; only durations get `*Sec`) — this plan uses `MaxConcurrentBackupJobs`
  instead, to match the codebase's real convention rather than the design doc's inference about it.

---

### Task 1: Add `destination` to the policy schema (proto + policy-server)

**Files:**
- Modify: `src/api/policyserver.proto`
- Modify: `src/cmd/policy-server/policy.go`
- Modify: `src/cmd/policy-server/server.go`
- Modify: `src/cmd/policy-server/policy_test.go`
- Modify: `src/cmd/policy-server/server_test.go`
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`

**Interfaces:**
- Produces: `Policy.Destination string` (on-disk JSON field `destination`, `cmd/policy-server/policy.go`), `pb.Policy.Destination` (proto field 7, via `p.GetDestination()`), both consumed by Task 2.

- [ ] **Step 1: Add the field to the proto and regenerate**

Edit `src/api/policyserver.proto`, changing the `Policy` message from:

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
}
```

to:

```proto
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

Then regenerate:

```bash
make proto
```

Expected: `src/api/policyserver.pb.go` is rewritten; `git diff --stat src/api/policyserver.pb.go` shows changes.

- [ ] **Step 2: Add `Destination` to the on-disk `Policy` struct**

Edit `src/cmd/policy-server/policy.go`, changing:

```go
type Policy struct {
	Metadata      Metadata       `json:"metadata"`
	ClientFilters ClientFilters  `json:"client_filters"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
}
```

to:

```go
type Policy struct {
	Metadata      Metadata       `json:"metadata"`
	ClientFilters ClientFilters  `json:"client_filters"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}
```

- [ ] **Step 3: Thread it through the proto conversion**

Edit `src/cmd/policy-server/server.go`'s `toProtoPolicy`, changing:

```go
	return &pb.Policy{
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
	}
```

to:

```go
	return &pb.Policy{
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
		Destination:   p.Destination,
	}
```

- [ ] **Step 4: Update the two existing tests that assert full field round-trips**

In `src/cmd/policy-server/policy_test.go`, `TestParsePolicyFile_ValidPolicyParsesAllFields` — add `"destination"` to the JSON fixture and an assertion:

```go
func TestParsePolicyFile_ValidPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-10T00:00:00Z"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	assert.Equal(t, "nightly-web-backup", p.Metadata.Name)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.Metadata.CreatedAt)
	assert.Equal(t, []string{"web-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, p.ClientFilters.Labels)
	assert.Equal(t, []ObjectFilter{{Path: "/var/www"}}, p.ObjectFilters)
	assert.Equal(t, "24h", p.RPO)
	assert.Equal(t, []string{"0 2 * * *", "0 20 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
}
```

In `src/cmd/policy-server/server_test.go`, `TestGetPolicies_ResponseFieldsRoundTrip` — add `"destination"` to the JSON fixture and an assertion:

```go
func TestGetPolicies_ResponseFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "full.json", `{
		"metadata": {"name": "full-policy", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-11T00:00:00Z"},
		"object_filters": [{"path": "/var/www"}, {"path": "/etc"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)
	srv := newTestServerWithPolicies(t, dir)

	resp, err := srv.GetPolicies(fakeAuthContext(t, "any", nil), &pb.GetPoliciesRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Policies, 1)
	p := resp.Policies[0]
	assert.Equal(t, "full-policy", p.Name)
	assert.Equal(t, "24h", p.Rpo)
	assert.Equal(t, []string{"0 2 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
	require.Len(t, p.ObjectFilters, 2)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.CreatedAt.AsTime())
	assert.Equal(t, time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC), p.UpdatedAt.AsTime())
}
```

- [ ] **Step 5: Run the policy-server tests**

```bash
cd src && go test ./cmd/policy-server/...
```

Expected: `ok`, all tests pass.

- [ ] **Step 6: Update documentation**

In `docs/protocols/policy-server.md`, update the `Policy` message block to match Step 1's proto exactly (add `string destination = 7;`), and add one sentence to the "Behavior" section: `destination` is likewise opaque, pass-through data — `policy-server` never validates or connects to it.

In `docs/components/policy-server.md`, in the "Policy files and hot reload" paragraph, add `destination` (a `host:port` string, the target `bwfs` for this policy's backups) to the list of fields a policy file has, alongside `metadata`/`client_filters`/`object_filters`/`rpo`/`backup_window`.

- [ ] **Step 7: Commit**

```bash
git add src/api/policyserver.proto src/api/policyserver.pb.go src/cmd/policy-server/policy.go src/cmd/policy-server/server.go src/cmd/policy-server/policy_test.go src/cmd/policy-server/server_test.go docs/protocols/policy-server.md docs/components/policy-server.md
git commit -m "feat(policy-server): add destination field to the policy schema"
```

---

### Task 2: Thread `destination` through policyclient's cache

**Files:**
- Modify: `src/cmd/policyclient/fetch.go`
- Modify: `src/cmd/policyclient/fetch_test.go`
- Modify: `docs/components/policyclient.md`

**Interfaces:**
- Consumes: `pb.Policy.Destination` (Task 1).
- Produces: `CachedPolicy.Destination string` (JSON field `destination` in `policies-cache.json`), consumed by Task 7's `cachedPolicy` struct (a separate, package-local mirror — see Task 7).

- [ ] **Step 1: Add the field to `CachedPolicy` and its conversion**

Edit `src/cmd/policyclient/fetch.go`, changing:

```go
type CachedPolicy struct {
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ObjectFilters []string  `json:"object_filters"`
	RPO           string    `json:"rpo"`
	BackupWindow  []string  `json:"backup_window"`
}
```

to:

```go
type CachedPolicy struct {
	Name          string    `json:"name"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	ObjectFilters []string  `json:"object_filters"`
	RPO           string    `json:"rpo"`
	BackupWindow  []string  `json:"backup_window"`
	Destination   string    `json:"destination"`
}
```

And in `toCachedPolicies`, changing:

```go
		out = append(out, CachedPolicy{
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
		})
```

to:

```go
		out = append(out, CachedPolicy{
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
		})
```

- [ ] **Step 2: Update the existing round-trip test**

In `src/cmd/policyclient/fetch_test.go`, `TestRunFetch_Success_WritesCacheFile` — add `Destination` to the fake response and an assertion:

```go
func TestRunFetch_Success_WritesCacheFile(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "nested", "policies-cache.json")

	created := timestamppb.New(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC))
	updated := timestamppb.New(time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC))
	fake := &fakePolicyServiceClient{resp: &pb.GetPoliciesResponse{
		Policies: []*pb.Policy{
			{
				Name:          "daily-db-backup",
				CreatedAt:     created,
				UpdatedAt:     updated,
				ObjectFilters: []*pb.ObjectFilter{{Path: "/var/lib/postgres"}, {Path: "/etc/postgres"}},
				Rpo:           "24h",
				BackupWindow:  []string{"0 2 * * *"},
				Destination:   "bwfs-east.internal:8080",
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
	assert.Equal(t, "daily-db-backup", got[0].Name)
	assert.True(t, created.AsTime().Equal(got[0].CreatedAt))
	assert.True(t, updated.AsTime().Equal(got[0].UpdatedAt))
	assert.Equal(t, []string{"/var/lib/postgres", "/etc/postgres"}, got[0].ObjectFilters)
	assert.Equal(t, "24h", got[0].RPO)
	assert.Equal(t, []string{"0 2 * * *"}, got[0].BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", got[0].Destination)
}
```

- [ ] **Step 3: Run the policyclient tests**

```bash
cd src && go test ./cmd/policyclient/...
```

Expected: `ok`, all tests pass.

- [ ] **Step 4: Update documentation**

In `docs/components/policyclient.md`, update the example `policies-cache.json` block to include `"destination": "bwfs-east.internal:8080"` as the last field, matching the design doc's example.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/policyclient/fetch.go src/cmd/policyclient/fetch_test.go docs/components/policyclient.md
git commit -m "feat(policyclient): cache the destination field"
```

---

### Task 3: Add `BackupWindowGraceSec` and `MaxConcurrentBackupJobs` config keys

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.BackupWindowGraceSec int` (default `3600`), `Config.MaxConcurrentBackupJobs int` (default `2`), consumed by Task 7 (`BackupWindowGraceSec`) and Task 8 (`MaxConcurrentBackupJobs`).

- [ ] **Step 1: Write the failing tests**

Add to `src/common/config/config_test.go`:

```go
func TestParseConfig_BackupWindowGraceSecDefaultsTo3600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 3600, conf.BackupWindowGraceSec)
}

func TestParseConfig_BackupWindowGraceSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nBackupWindowGraceSec=600\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 600, conf.BackupWindowGraceSec)
}

func TestParseConfig_MaxConcurrentBackupJobsDefaultsTo2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 2, conf.MaxConcurrentBackupJobs)
}

func TestParseConfig_MaxConcurrentBackupJobsParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nMaxConcurrentBackupJobs=5\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 5, conf.MaxConcurrentBackupJobs)
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
cd src && go test ./common/config/... -run TestParseConfig_BackupWindowGraceSec -v
cd src && go test ./common/config/... -run TestParseConfig_MaxConcurrentBackupJobs -v
```

Expected: FAIL — `conf.BackupWindowGraceSec`/`conf.MaxConcurrentBackupJobs` undefined (compile error), since the fields don't exist yet.

- [ ] **Step 3: Add the fields, defaults, and parsing cases**

In `src/common/config/config.go`, add to the `Config` struct (after `PolicyFetchIntervalSec`):

```go
	PolicyServerHost                 string
	PolicyServerPort                 int
	PolicyFetchIntervalSec           int
	BackupWindowGraceSec             int
	MaxConcurrentBackupJobs          int
}
```

Add to `ParseConfig`'s default-init block:

```go
		PolicyServerPort:                 9300,
		PolicyFetchIntervalSec:           900,
		BackupWindowGraceSec:             3600,
		MaxConcurrentBackupJobs:          2,
	}
```

Add two new `case`s to the `switch key` block, right after the existing `"PolicyFetchIntervalSec"` case:

```go
		case "BackupWindowGraceSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid BackupWindowGraceSec value at line %d: %s", lineNum, value)
			}
			config.BackupWindowGraceSec = number
			foundFields["BackupWindowGraceSec"] = true
		case "MaxConcurrentBackupJobs":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid MaxConcurrentBackupJobs value at line %d: %s", lineNum, value)
			}
			config.MaxConcurrentBackupJobs = number
			foundFields["MaxConcurrentBackupJobs"] = true
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd src && go test ./common/config/... -v
```

Expected: `PASS` for all `TestParseConfig_*` tests, including the four new ones.

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add BackupWindowGraceSec and MaxConcurrentBackupJobs"
```

---

### Task 4: Add the `robfig/cron/v3` dependency

**Files:**
- Modify: `src/go.mod`
- Modify: `src/go.sum`

**Interfaces:**
- Produces: `github.com/robfig/cron/v3` importable as `cron`, consumed by Task 7.

- [ ] **Step 1: Fetch the dependency**

```bash
cd src && go get github.com/robfig/cron/v3
```

Expected: `go.mod` gains a `require github.com/robfig/cron/v3 vX.Y.Z` line; `go.sum` gains matching entries.

- [ ] **Step 2: Tidy and verify the build still compiles**

```bash
cd src && go mod tidy && go build ./...
```

Expected: no errors; `go.mod`/`go.sum` unchanged by `tidy` beyond what `go get` already added (or trivially reformatted).

- [ ] **Step 3: Commit**

```bash
git add src/go.mod src/go.sum
git commit -m "chore: add github.com/robfig/cron/v3 dependency"
```

---

### Task 5: Make `agent`'s `Policy` due-check and next-run display pluggable

**Files:**
- Modify: `src/cmd/agent/policy.go`
- Modify: `src/cmd/agent/reconcile.go`
- Modify: `src/cmd/agent/list.go`
- Modify: `src/cmd/agent/reconcile_test.go`
- Modify: `src/cmd/agent/list_test.go`

**Interfaces:**
- Produces: `Policy.Due func(PolicyState, time.Time) bool`, `Policy.NextRun func(PolicyState, time.Time) time.Time`, `Policy.Background bool` (all new, zero-valued by default) — consumed by Task 6 (`Background`) and Task 7 (`Due`, `NextRun`).
- Consumes: nothing new; this task only generalizes existing `isDue`/`estimatedNextRun`.

This task changes zero observable behavior for the three existing policies — every pre-existing test in `reconcile_test.go`/`list_test.go` must pass with either no changes, or only a mechanical signature update (a new required argument), never a changed expected value.

- [ ] **Step 1: Write the failing tests for the new pluggable behavior**

Add to `src/cmd/agent/reconcile_test.go`:

```go
func TestIsDue_HealthyPolicyDefersToCustomDueFunc(t *testing.T) {
	always := Policy{Due: func(PolicyState, time.Time) bool { return true }}
	assert.True(t, isDue(always, PolicyState{}, time.Now()))

	never := Policy{Due: func(PolicyState, time.Time) bool { return false }}
	assert.False(t, isDue(never, PolicyState{}, time.Now()))
}

func TestIsDue_FailingPolicyIgnoresCustomDueFunc(t *testing.T) {
	// Even with Due present, a currently-failing policy is still governed
	// by NextRetryAt, not Due -- Due is only consulted on the healthy path.
	p := Policy{Due: func(PolicyState, time.Time) bool { return false }}
	now := time.Now()
	retryAt := now.Add(-1 * time.Second)
	state := PolicyState{ConsecutiveFailures: 1, NextRetryAt: &retryAt}
	assert.True(t, isDue(p, state, now))
}
```

Add to `src/cmd/agent/list_test.go`:

```go
func TestEstimatedNextRun_NotDueDefersToCustomNextRunFunc(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 2, 0, 0, 0, time.UTC)
	p := Policy{
		Due:     func(PolicyState, time.Time) bool { return false },
		NextRun: func(PolicyState, time.Time) time.Time { return fixed },
	}
	got := estimatedNextRun(p, PolicyState{}, time.Now())
	assert.Equal(t, fixed, got)
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
cd src && go test ./cmd/agent/... -run 'TestIsDue_HealthyPolicyDefersToCustomDueFunc|TestIsDue_FailingPolicyIgnoresCustomDueFunc|TestEstimatedNextRun_NotDueDefersToCustomNextRunFunc' -v
```

Expected: FAIL — compile error, `Policy.Due`/`Policy.NextRun` undefined.

- [ ] **Step 3: Add the new `Policy` fields**

Edit `src/cmd/agent/policy.go`, changing the `Policy` struct and its doc comment from:

```go
// Policy is a single reconcilable unit: run Binary with Args whenever more
// than Interval has elapsed since the last successful run.
type Policy struct {
	ID       string
	Binary   string
	Args     []string
	Interval time.Duration
}
```

to:

```go
// Policy is a single reconcilable unit. By default (Due == nil), it's due
// once more than Interval has elapsed since the last successful run --
// agent's original behavior, unchanged for bootstrap-refresh,
// operating-refresh, and policy-update. A non-nil Due overrides that
// check entirely (see backup.go's backupTasks, whose window+RPO due-check
// doesn't fit a bare interval). NextRun is the equivalent override for
// list-policies' display only (see list.go's estimatedNextRun).
// Background, when true, makes run() execute this policy in a goroutine
// instead of synchronously in the reconcile loop (see reconcile.go).
type Policy struct {
	ID         string
	Binary     string
	Args       []string
	Interval   time.Duration
	Due        func(PolicyState, time.Time) bool
	NextRun    func(PolicyState, time.Time) time.Time
	Background bool
}
```

- [ ] **Step 4: Make `isDue` consult `Policy.Due`**

Edit `src/cmd/agent/reconcile.go`, changing `isDue` from:

```go
func isDue(p Policy, s PolicyState, now time.Time) bool {
	if s.ConsecutiveFailures == 0 {
		if s.LastSuccessAt == nil {
			return true // never succeeded, run immediately
		}
		return !now.Before(s.LastSuccessAt.Add(p.Interval))
	}
	return s.NextRetryAt == nil || !now.Before(*s.NextRetryAt)
}
```

to:

```go
func isDue(p Policy, s PolicyState, now time.Time) bool {
	if s.ConsecutiveFailures == 0 {
		if p.Due != nil {
			return p.Due(s, now)
		}
		if s.LastSuccessAt == nil {
			return true // never succeeded, run immediately
		}
		return !now.Before(s.LastSuccessAt.Add(p.Interval))
	}
	return s.NextRetryAt == nil || !now.Before(*s.NextRetryAt)
}
```

- [ ] **Step 5: Make `estimatedNextRun` consult `Policy.NextRun`, delegating to `isDue`**

Edit `src/cmd/agent/list.go`, changing `estimatedNextRun` from:

```go
// estimatedNextRun mirrors isDue's own comparisons exactly (see
// reconcile.go) so this display can never disagree with what the daemon
// would actually do. Returns the zero time.Time for "due now".
func estimatedNextRun(p Policy, s PolicyState) time.Time {
	if s.ConsecutiveFailures == 0 {
		if s.LastSuccessAt == nil {
			return time.Time{}
		}
		return s.LastSuccessAt.Add(p.Interval)
	}
	if s.NextRetryAt == nil {
		return time.Time{}
	}
	return *s.NextRetryAt
}
```

to:

```go
// estimatedNextRun calls isDue directly (rather than re-deriving its
// logic) so this display can never disagree with what the daemon would
// actually do. Returns the zero time.Time for "due now".
func estimatedNextRun(p Policy, s PolicyState, now time.Time) time.Time {
	if isDue(p, s, now) {
		return time.Time{}
	}
	if s.ConsecutiveFailures == 0 {
		if p.NextRun != nil {
			return p.NextRun(s, now)
		}
		return s.LastSuccessAt.Add(p.Interval)
	}
	return *s.NextRetryAt
}
```

Update its one call site, in `renderPolicies` (same file), changing:

```go
			formatNextRun(estimatedNextRun(p, s), now),
```

to:

```go
			formatNextRun(estimatedNextRun(p, s, now), now),
```

- [ ] **Step 6: Update the three existing `estimatedNextRun` tests for the new signature**

In `src/cmd/agent/list_test.go`, replace the three tests:

```go
func TestEstimatedNextRun_NeverRunReturnsZeroValue(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	got := estimatedNextRun(p, PolicyState{}, time.Now())
	assert.True(t, got.IsZero())
}

func TestEstimatedNextRun_HealthyUsesLastSuccessPlusInterval(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	last := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := last.Add(1 * time.Minute) // within Interval, so not due yet
	got := estimatedNextRun(p, PolicyState{LastSuccessAt: &last}, now)
	assert.Equal(t, last.Add(5*time.Minute), got)
}

func TestEstimatedNextRun_FailingUsesStoredNextRetryAt(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	retryAt := time.Date(2026, 7, 3, 12, 5, 0, 0, time.UTC)
	now := retryAt.Add(-1 * time.Minute) // before the retry threshold, so not due yet
	got := estimatedNextRun(p, PolicyState{ConsecutiveFailures: 2, NextRetryAt: &retryAt}, now)
	assert.Equal(t, retryAt, got)
}
```

- [ ] **Step 7: Run all agent tests**

```bash
cd src && go test ./cmd/agent/... -v
```

Expected: `PASS` for every test, including the three new ones from Step 1 and the updated ones from Step 6.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/agent/policy.go src/cmd/agent/reconcile.go src/cmd/agent/list.go src/cmd/agent/reconcile_test.go src/cmd/agent/list_test.go
git commit -m "refactor(agent): make Policy due-check and next-run display pluggable"
```

---

### Task 6: Background, context-cancellable, concurrency-bounded policy execution

**Files:**
- Modify: `src/cmd/agent/reconcile.go`
- Modify: `src/cmd/agent/reconcile_test.go`
- Modify: `src/cmd/agent/main.go`

**Interfaces:**
- Consumes: `Policy.Background` (Task 5).
- Produces: `runner` type becomes `func(ctx context.Context, binary string, args []string) error`; `run()`'s signature becomes `run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() []Policy, maxConcurrentBackgroundJobs int) error` — both consumed by Task 7's tests and Task 8's `main.go` wiring.

- [ ] **Step 1: Write the failing tests for the new execution behavior**

Add to `src/cmd/agent/reconcile_test.go` (needs `"sync"`, already imported):

```go
func TestRun_BackgroundPolicyDoesNotBlockSyncPolicyInSameTick(t *testing.T) {
	release := make(chan struct{})
	blockingRunner := func(ctx context.Context, binary string, args []string) error {
		if binary == "slow-backup" {
			<-release
		}
		return nil
	}

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{
		{ID: "slow-backup", Binary: "slow-backup", Interval: time.Hour, Background: true},
		{ID: "fast-sync", Binary: "fast-sync", Interval: time.Hour},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, blockingRunner, func() []Policy { return testPolicies }, 2)
	}()

	require.Eventually(t, func() bool {
		cache, err := readCache(cachePath)
		return err == nil && cache["fast-sync"].LastSuccessAt != nil
	}, time.Second, 5*time.Millisecond, "fast-sync must complete without waiting for slow-backup")

	close(release)
	cancel()
	<-done
}

func TestRun_ConcurrencyCapLimitsSimultaneousBackgroundExecs(t *testing.T) {
	var mu sync.Mutex
	inFlight, maxObserved := 0, 0
	release := make(chan struct{})

	blockingRunner := func(ctx context.Context, binary string, args []string) error {
		mu.Lock()
		inFlight++
		if inFlight > maxObserved {
			maxObserved = inFlight
		}
		mu.Unlock()

		<-release

		mu.Lock()
		inFlight--
		mu.Unlock()
		return nil
	}

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{
		{ID: "backup-1", Binary: "b1", Interval: time.Hour, Background: true},
		{ID: "backup-2", Binary: "b2", Interval: time.Hour, Background: true},
		{ID: "backup-3", Binary: "b3", Interval: time.Hour, Background: true},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, func() []Policy { return testPolicies }, 1)
	}()

	time.Sleep(50 * time.Millisecond) // let several ticks pass, all contending for the single slot
	close(release)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, maxObserved, "concurrency cap of 1 must never be exceeded")
}

func TestRun_BackgroundExecReceivesCancelledContextOnShutdown(t *testing.T) {
	ctxErrCh := make(chan error, 1)
	blockingRunner := func(ctx context.Context, binary string, args []string) error {
		<-ctx.Done()
		ctxErrCh <- ctx.Err()
		return ctx.Err()
	}

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	testPolicies := []Policy{{ID: "backup-1", Binary: "b1", Interval: time.Hour, Background: true}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, func() []Policy { return testPolicies }, 2)
	}()

	time.Sleep(20 * time.Millisecond) // let the background goroutine launch and block on ctx.Done()
	cancel()

	select {
	case err := <-ctxErrCh:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("background exec never observed context cancellation")
	}
	<-done
}

func TestRealExec_ContextCancellationKillsProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	start := time.Now()

	done := make(chan error, 1)
	go func() {
		done <- realExec(ctx, "sleep", []string{"5"})
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("realExec did not terminate promptly after context cancellation")
	}
	assert.Less(t, time.Since(start), 2*time.Second, "sleep 5 should have been killed well before its natural completion")
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
cd src && go test ./cmd/agent/... -run 'TestRun_BackgroundPolicyDoesNotBlockSyncPolicyInSameTick|TestRun_ConcurrencyCapLimitsSimultaneousBackgroundExecs|TestRun_BackgroundExecReceivesCancelledContextOnShutdown|TestRealExec_ContextCancellationKillsProcess' -v
```

Expected: FAIL — compile errors (`run`/`realExec` called with the wrong number/type of arguments, since `runner`'s signature and `run()`'s signature haven't changed yet).

- [ ] **Step 3: Update `runner`, `realExec`, `isDue`'s callers stay same, and rewrite `run()`**

Replace the full contents of `src/cmd/agent/reconcile.go` with:

```go
package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// backoffBase and backoffMax are vars (not consts) so tests can shrink them
// temporarily instead of waiting out real multi-minute backoff windows.
var (
	backoffBase = 30 * time.Second
	backoffMax  = 10 * time.Minute
)

// runner executes a policy's binary under ctx; production code uses
// realExec, tests substitute a fake so they don't actually invoke
// certclient/policyclient/brfs. ctx is honored via exec.CommandContext so
// a cancelled context (agent shutdown) terminates an in-flight process
// rather than orphaning it.
type runner func(ctx context.Context, binary string, args []string) error

// realExec runs binary with args under ctx. If binary is a bare name (no
// path separator), it is first resolved relative to this agent's own
// executable directory — the same "colocated sibling binary" layout used
// elsewhere in this repo (see deploy/control-plane/catalog's
// entrypoint.sh, which execs ./certclient from the same directory as its
// own binary, and common/config.ResolveBaseDir/ResolveVarDir, which
// resolve relative to os.Executable() the same way). This matters because
// Go's os/exec only resolves a bare name via $PATH, never via the working
// or executable directory, and nothing in that deployment layout puts
// certclient/brfs on $PATH. If no colocated file is found, binary is
// passed through unchanged so exec.Command falls back to its normal $PATH
// lookup — this keeps local/dev usage, where these binaries genuinely are
// on $PATH, working exactly as before.
func realExec(ctx context.Context, binary string, args []string) error {
	path := binary
	if !strings.Contains(binary, string(filepath.Separator)) {
		if exePath, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(exePath), binary)
			if _, err := os.Stat(candidate); err == nil {
				path = candidate
			}
		}
	}
	return exec.CommandContext(ctx, path, args...).Run()
}

// isDue reports whether p should run now, given its last recorded state.
// A currently-failing policy (ConsecutiveFailures > 0) is due once
// NextRetryAt has passed, regardless of Due/Interval — decoupled from
// either, so a persistent failure doesn't get retried on every tick, and
// doesn't wait a full Interval/window cycle either. A healthy policy
// defers to p.Due if set, or else to the original
// Interval-since-last-success comparison.
func isDue(p Policy, s PolicyState, now time.Time) bool {
	if s.ConsecutiveFailures == 0 {
		if p.Due != nil {
			return p.Due(s, now)
		}
		if s.LastSuccessAt == nil {
			return true // never succeeded, run immediately
		}
		return !now.Before(s.LastSuccessAt.Add(p.Interval))
	}
	return s.NextRetryAt == nil || !now.Before(*s.NextRetryAt)
}

// backoff returns a jittered retry delay for the given number of
// consecutive failures. It must be called exactly once per failure and the
// result stored (see reconcileState.recordOutcome, PolicyState.NextRetryAt)
// rather than recomputed on every isDue check — recomputing it would
// redraw the jitter each time and make the due-ness threshold unstable.
func backoff(failures int) time.Duration {
	exp := min(max(failures-1, 0), 8)
	d := backoffBase * time.Duration(1<<exp)
	if d > backoffMax {
		d = backoffMax
	}
	// half jitter: never near-zero, still spreads retries across a fleet
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// reconcileState bundles the persisted Cache with the mutex guarding it.
// Before background policies existed, run() only ever touched the cache
// from its own single goroutine; a Policy with Background == true now
// updates it from its own goroutine too, concurrently with the main loop
// and with every other background goroutine, so every read/write goes
// through here.
type reconcileState struct {
	mu        sync.Mutex
	cachePath string
	cache     Cache
	logger    *slog.Logger
}

func (rs *reconcileState) get(id string) PolicyState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.cache[id]
}

// recordOutcome updates and immediately persists id's state given the
// outcome of one attempt at attemptTime. It's the single place both the
// synchronous and background-goroutine paths in run() update PolicyState,
// so their behavior — and what ends up on disk — can never diverge.
func (rs *reconcileState) recordOutcome(id string, attemptErr error, attemptTime time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	state := rs.cache[id]
	state.LastAttemptAt = &attemptTime
	if attemptErr == nil {
		state.LastSuccessAt = &attemptTime
		state.ConsecutiveFailures = 0
		state.NextRetryAt = nil
	} else {
		state.ConsecutiveFailures++
		retryAt := attemptTime.Add(backoff(state.ConsecutiveFailures))
		state.NextRetryAt = &retryAt
		rs.logger.Error("policy execution failed", "policy", id, "error", attemptErr)
	}
	rs.cache[id] = state

	if err := writeCache(rs.cachePath, rs.cache); err != nil {
		rs.logger.Error("failed to persist cache", "error", err)
	}
}

// run polls policiesFunc() every reconcileInterval, executing and
// recording the outcome of any policy isDue reports as due. A due policy
// with Background == false runs synchronously, exactly as before this
// type existed. A due policy with Background == true is launched in its
// own goroutine, bounded by maxConcurrentBackgroundJobs simultaneous
// in-flight jobs — a due background policy that can't acquire a slot this
// tick is simply left due and reconsidered next tick, never queued. run()
// returns once ctx is cancelled, after every in-flight background
// goroutine it launched has finished (each one's execute call receives
// the same ctx, so a context-respecting runner like realExec terminates
// rather than being orphaned).
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() []Policy, maxConcurrentBackgroundJobs int) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}
	rs := &reconcileState{cachePath: cachePath, cache: cache, logger: logger}

	sem := make(chan struct{}, maxConcurrentBackgroundJobs)
	var wg sync.WaitGroup

	for ctx.Err() == nil {
		now := time.Now()
		for _, p := range policiesFunc() {
			state := rs.get(p.ID)
			if !isDue(p, state, now) {
				continue
			}

			if p.Background {
				select {
				case sem <- struct{}{}:
				default:
					continue // no free slot this tick; stays due, retried next tick
				}
				wg.Add(1)
				go func(p Policy) {
					defer wg.Done()
					defer func() { <-sem }()
					attemptErr := execute(ctx, p.Binary, p.Args)
					rs.recordOutcome(p.ID, attemptErr, time.Now())
				}(p)
				continue
			}

			attemptErr := execute(ctx, p.Binary, p.Args)
			rs.recordOutcome(p.ID, attemptErr, now)
		}

		if !sleepOrDone(ctx, reconcileInterval) {
			break
		}
	}

	wg.Wait()
	return nil
}

// sleepOrDone sleeps for d, or returns false immediately if ctx is
// cancelled first.
func sleepOrDone(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
```

- [ ] **Step 4: Update existing tests for the new `runner`/`run()` signatures**

In `src/cmd/agent/reconcile_test.go`, change `fakeRunner.run`:

```go
func (f *fakeRunner) run(ctx context.Context, binary string, args []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failN > 0 {
		f.failN--
		return errors.New("simulated failure")
	}
	return nil
}
```

Change the two `TestRealExec_*` tests' calls:

```go
	err = realExec(context.Background(), name, nil)
	assert.NoError(t, err)
}
```

```go
func TestRealExec_FallsBackToPathWhenNotColocated(t *testing.T) {
	err := realExec(context.Background(), "true", nil)
	assert.NoError(t, err)
}
```

Change `TestRun_ExecutesDuePolicyAndDoesNotRetriggerWithinInterval` and `TestRun_FailedExecutionRecordsFailureAndRetriesAfterBackoff`'s calls to `run`, e.g.:

```go
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() []Policy { return testPolicies }, 2)
	require.NoError(t, err)
```

(same pattern — wrap `testPolicies` in a `func() []Policy { return testPolicies }` closure and add a trailing `2` — for both tests).

- [ ] **Step 5: Update `main.go`'s call to `run`**

In `src/cmd/agent/main.go`, change:

```go
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, pols); err != nil {
```

to:

```go
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, func() []Policy { return pols }, conf.MaxConcurrentBackupJobs); err != nil {
```

(This is a temporary intermediate step — Task 8 replaces `pols`/this closure with one that also derives backup tasks. This step's only job is to keep `main.go` compiling against the new `run()` signature.)

- [ ] **Step 6: Run all agent tests**

```bash
cd src && go test ./cmd/agent/... -v
```

Expected: `PASS` for every test. `TestRun_ConcurrencyCapLimitsSimultaneousBackgroundExecs` and `TestRun_BackgroundPolicyDoesNotBlockSyncPolicyInSameTick` may each take up to ~1s (bounded by their own timeouts) — this is expected, not a hang.

- [ ] **Step 7: Run with the race detector**

```bash
cd src && go test ./cmd/agent/... -race
```

Expected: `ok`, no `DATA RACE` reports — this is the concrete check that `reconcileState`'s mutex actually covers every concurrent access.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go src/cmd/agent/main.go
git commit -m "feat(agent): support backgrounded, concurrency-bounded, context-cancellable policies"
```

---

### Task 7: Derive backup tasks from `policies-cache.json`

**Files:**
- Create: `src/cmd/agent/backup.go`
- Create: `src/cmd/agent/backup_test.go`

**Interfaces:**
- Consumes: `Config.BackupWindowGraceSec` (Task 3), `Policy.Due`/`Policy.NextRun`/`Policy.Background` (Task 5), `github.com/robfig/cron/v3` (Task 4).
- Produces: `backupTasks(policiesCachePath string, conf *config.Config) []Policy`, consumed by Task 8.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/agent/backup_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCachedPolicies(t *testing.T, dir, json string) string {
	t.Helper()
	path := filepath.Join(dir, "policies-cache.json")
	require.NoError(t, os.WriteFile(path, []byte(json), 0o644))
	return path
}

func TestWindowOpen_TriggerJustInsideGraceReportsOpen(t *testing.T) {
	sched, err := cron.ParseStandard("0 2 * * *") // fires 02:00 daily
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 2, 30, 0, 0, time.UTC) // 30 min after trigger
	assert.True(t, windowOpen([]cron.Schedule{sched}, now, time.Hour))
}

func TestWindowOpen_TriggerJustOutsideGraceReportsClosed(t *testing.T) {
	sched, err := cron.ParseStandard("0 2 * * *")
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 3, 30, 0, 0, time.UTC) // 90 min after trigger
	assert.False(t, windowOpen([]cron.Schedule{sched}, now, time.Hour))
}

func TestWindowOpen_OneOfMultipleSchedulesRecentlyTriggeredStillOpen(t *testing.T) {
	morning, err := cron.ParseStandard("0 2 * * *")
	require.NoError(t, err)
	evening, err := cron.ParseStandard("0 20 * * *")
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC) // just after the morning slot only
	assert.True(t, windowOpen([]cron.Schedule{morning, evening}, now, time.Hour))
}

func TestRpoElapsed_NeverSucceededIsElapsed(t *testing.T) {
	assert.True(t, rpoElapsed(PolicyState{}, time.Now(), time.Hour))
}

func TestRpoElapsed_RecentSuccessIsNotElapsed(t *testing.T) {
	now := time.Now()
	last := now.Add(-10 * time.Minute)
	assert.False(t, rpoElapsed(PolicyState{LastSuccessAt: &last}, now, time.Hour))
}

func TestRpoElapsed_OldSuccessIsElapsed(t *testing.T) {
	now := time.Now()
	last := now.Add(-2 * time.Hour)
	assert.True(t, rpoElapsed(PolicyState{LastSuccessAt: &last}, now, time.Hour))
}

func TestBackupTasks_OnePolicyWithTwoPathsYieldsTwoTasksWithStableDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"object_filters": ["/var/lib/postgres", "/etc/postgres"],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks := backupTasks(path, conf)

	require.Len(t, tasks, 2)
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "backup:daily-db-backup:/var/lib/postgres")
	assert.Contains(t, ids, "backup:daily-db-backup:/etc/postgres")
	assert.NotEqual(t, tasks[0].ID, tasks[1].ID)
}

func TestBackupTasks_TaskArgsMatchBrfsShape(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"object_filters": ["/var/lib/postgres"],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks := backupTasks(path, conf)

	require.Len(t, tasks, 1)
	task := tasks[0]
	assert.Equal(t, "brfs", task.Binary)
	require.Len(t, task.Args, 5)
	assert.Equal(t, "/var/lib/postgres", task.Args[0])
	assert.Equal(t, "--destination", task.Args[1])
	assert.Equal(t, "bwfs-east:8080", task.Args[2])
	assert.Equal(t, "--job-id", task.Args[3])
	assert.Contains(t, task.Args[4], "backup:daily-db-backup:var-lib-postgres:")
	assert.True(t, task.Background)
}

func TestBackupTasks_DueRequiresBothWindowOpenAndRpoElapsed(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": ["/data"],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks := backupTasks(path, conf)
	require.Len(t, tasks, 1)
	task := tasks[0]

	windowOpenTime := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC)
	windowClosedTime := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	recent := windowOpenTime.Add(-10 * time.Minute)
	old := windowOpenTime.Add(-2 * time.Hour)

	assert.False(t, task.Due(PolicyState{LastSuccessAt: &recent}, windowOpenTime), "window open but RPO not elapsed: not due")
	assert.False(t, task.Due(PolicyState{LastSuccessAt: &old}, windowClosedTime), "RPO elapsed but window closed: not due")
	assert.True(t, task.Due(PolicyState{LastSuccessAt: &old}, windowOpenTime), "both true: due")
	assert.True(t, task.Due(PolicyState{}, windowOpenTime), "never run and window open: due")
}

func TestBackupTasks_PerPathIndependence(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": ["/a", "/b"],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks := backupTasks(path, conf)
	require.Len(t, tasks, 2)

	windowOpenTime := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC)
	recent := windowOpenTime.Add(-10 * time.Minute)

	var taskA, taskB Policy
	for _, task := range tasks {
		if task.ID == "backup:p:/a" {
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

func TestBackupTasks_UnparseableRpoSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": ["/data"],
		"rpo": "not-a-duration",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	assert.Empty(t, backupTasks(path, conf))
}

func TestBackupTasks_NoValidBackupWindowSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": ["/data"],
		"rpo": "1h",
		"backup_window": ["not a cron expression"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	assert.Empty(t, backupTasks(path, conf))
}

func TestBackupTasks_MissingCacheFileYieldsNoTasks(t *testing.T) {
	conf := &config.Config{BackupWindowGraceSec: 3600}
	assert.Empty(t, backupTasks(filepath.Join(t.TempDir(), "does-not-exist.json"), conf))
}

func TestBackupTasks_RemovedPolicyStopsBeingDerived(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	conf := &config.Config{BackupWindowGraceSec: 3600}

	require.NoError(t, os.WriteFile(cachePath, []byte(`[{
		"name": "p", "object_filters": ["/data"], "rpo": "1h",
		"backup_window": ["0 2 * * *"], "destination": "bwfs:8080"
	}]`), 0o644))
	require.Len(t, backupTasks(cachePath, conf), 1)

	require.NoError(t, os.WriteFile(cachePath, []byte(`[]`), 0o644))
	assert.Empty(t, backupTasks(cachePath, conf))
}
```

- [ ] **Step 2: Run to verify they fail**

```bash
cd src && go test ./cmd/agent/... -run 'TestWindowOpen|TestRpoElapsed|TestBackupTasks' -v
```

Expected: FAIL — compile error, `backupTasks`/`windowOpen`/`rpoElapsed` undefined.

- [ ] **Step 3: Implement `backup.go`**

Create `src/cmd/agent/backup.go`:

```go
// backup.go derives agent's dynamic "backup task" policies from
// policies-cache.json (written by policyclient's policy-update job) --
// one task per (cached policy, object_filters path) pair, due when a
// backup_window cron slot is open and that path's rpo has elapsed. See
// docs/superpowers/specs/2026-07-10-agent-backup-execution-design.md.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/robfig/cron/v3"
)

// cachedPolicy mirrors the subset of policyclient's on-disk CachedPolicy
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type cachedPolicy struct {
	Name          string   `json:"name"`
	ObjectFilters []string `json:"object_filters"`
	RPO           string   `json:"rpo"`
	BackupWindow  []string `json:"backup_window"`
	Destination   string   `json:"destination"`
}

// readCachedPolicies reads policiesCachePath, returning nil (never an
// error) if the file is missing or unparseable -- the same fail-safe
// direction used throughout this codebase: on any doubt, assume there is
// nothing to do yet.
func readCachedPolicies(policiesCachePath string) []cachedPolicy {
	data, err := os.ReadFile(policiesCachePath)
	if err != nil {
		return nil
	}
	var policies []cachedPolicy
	if err := json.Unmarshal(data, &policies); err != nil {
		return nil
	}
	return policies
}

// parseSchedules parses each cron expression independently -- one
// malformed entry is dropped, not treated as invalidating the rest of the
// list, mirroring policy-server's own "skip the bad file, keep the good
// ones" direction (cmd/policy-server/cache.go's Reload).
func parseSchedules(exprs []string) []cron.Schedule {
	var out []cron.Schedule
	for _, expr := range exprs {
		sched, err := cron.ParseStandard(expr)
		if err != nil {
			continue
		}
		out = append(out, sched)
	}
	return out
}

// windowOpen reports whether any schedule fired within the last grace
// window ending at now -- i.e., a trigger occurred and the window hasn't
// closed yet. schedule.Next(t) returns the first activation strictly
// after t, so checking it against now-grace catches any trigger from that
// point forward, up to and including now.
func windowOpen(schedules []cron.Schedule, now time.Time, grace time.Duration) bool {
	threshold := now.Add(-grace)
	for _, s := range schedules {
		if !s.Next(threshold).After(now) {
			return true
		}
	}
	return false
}

// nextWindow returns the soonest upcoming trigger across all schedules,
// strictly after now. Only meaningful when the task is not currently due
// -- see list.go's estimatedNextRun, which checks isDue first.
func nextWindow(schedules []cron.Schedule, now time.Time) time.Time {
	var next time.Time
	for _, s := range schedules {
		t := s.Next(now)
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next
}

// rpoElapsed reports whether the path's last successful backup is older
// than rpo, or never happened at all.
func rpoElapsed(s PolicyState, now time.Time, rpo time.Duration) bool {
	if s.LastSuccessAt == nil {
		return true
	}
	return now.Sub(*s.LastSuccessAt) > rpo
}

// slug makes path safe to embed in a job-id: strips leading/trailing "/"
// and replaces the rest with "-". Cosmetic only -- job-id is opaque
// metadata to both brfs and bwfs, it never needs to round-trip back to a
// literal path.
func slug(path string) string {
	s := strings.Trim(path, "/")
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		return "root"
	}
	return s
}

// backupTaskID is the stable identifier for one (policy, path) pair's
// PolicyState entry in agent-state.json -- stable across ticks, so its
// backoff/success history persists as long as the pair keeps appearing in
// policies-cache.json.
func backupTaskID(policyName, path string) string {
	return fmt.Sprintf("backup:%s:%s", policyName, path)
}

// backupJobID is the --job-id passed to brfs for one run -- unlike
// backupTaskID, it includes a timestamp so every run gets a distinct ID,
// and it slugs the path so bwfs's job records stay easy to grep.
func backupJobID(policyName, path string, now time.Time) string {
	return fmt.Sprintf("backup:%s:%s:%d", policyName, slug(path), now.Unix())
}

// backupTasks derives one Policy per (cached policy, object_filters path)
// pair from policiesCachePath, valid at the instant it's called. Callers
// that need to notice policies-cache.json changing over time (agent
// serve's reconcile loop) must call this fresh every tick rather than
// caching its result once.
//
// A policy with an unparseable rpo, or with no valid backup_window
// schedule at all, contributes no tasks -- there is no sound due-check
// that could be built for it, so skipping entirely (rather than running
// on a guess) is the fail-safe choice. A missing/invalid destination is
// not checked here: the task is still built, and simply fails at brfs
// exec time like any other exec failure (see reconcile.go).
func backupTasks(policiesCachePath string, conf *config.Config) []Policy {
	grace := time.Duration(conf.BackupWindowGraceSec) * time.Second

	var tasks []Policy
	for _, p := range readCachedPolicies(policiesCachePath) {
		rpo, err := time.ParseDuration(p.RPO)
		if err != nil {
			continue
		}
		schedules := parseSchedules(p.BackupWindow)
		if len(schedules) == 0 {
			continue
		}

		policyName, destination := p.Name, p.Destination
		for _, path := range p.ObjectFilters {
			tasks = append(tasks, Policy{
				ID:         backupTaskID(policyName, path),
				Binary:     "brfs",
				Args:       []string{path, "--destination", destination, "--job-id", backupJobID(policyName, path, time.Now())},
				Background: true,
				Due: func(s PolicyState, now time.Time) bool {
					return windowOpen(schedules, now, grace) && rpoElapsed(s, now, rpo)
				},
				NextRun: func(s PolicyState, now time.Time) time.Time {
					return nextWindow(schedules, now)
				},
			})
		}
	}
	return tasks
}
```

- [ ] **Step 4: Run to verify they pass**

```bash
cd src && go test ./cmd/agent/... -v
```

Expected: `PASS` for every test in the package, including all new `backup_test.go` tests.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/backup.go src/cmd/agent/backup_test.go
git commit -m "feat(agent): derive backup tasks from policies-cache.json"
```

---

### Task 8: Wire backup tasks into `agent serve` and `agent list-policies`

**Files:**
- Modify: `src/cmd/agent/main.go`
- Create: `src/cmd/agent/integration_test.go`

**Interfaces:**
- Consumes: `backupTasks` (Task 7), `run()`'s final signature (Task 6).

- [ ] **Step 1: Write the failing end-to-end test**

Create `src/cmd/agent/integration_test.go`:

```go
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRun_BackupTaskFromRealCacheFileExecutesBrfsWithExpectedArgs proves
// the full pipeline end to end within the agent package: a real
// policies-cache.json on disk, read by the real backupTasks(), scheduled
// by the real isDue/run(), resulting in a (fake-executed) brfs invocation
// with the expected path/destination/job-id shape. This is deliberately
// not a Docker-based e2e test -- no existing harness stands up
// policy-server/bwfs together yet (see docs/superpowers/specs/
// 2026-07-10-agent-backup-execution-design.md's Testing section for the
// original proposal and this deviation's rationale) -- everything below
// the process-exec boundary (brfs -> bwfs) is already covered by
// src/e2e's existing Docker-based tests.
func TestRun_BackupTaskFromRealCacheFileExecutesBrfsWithExpectedArgs(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	policiesCachePath := filepath.Join(dir, "policies-cache.json")

	cacheJSON := `[{
		"name": "daily-db-backup",
		"object_filters": ["/var/lib/postgres"],
		"rpo": "1h",
		"backup_window": ["* * * * *"],
		"destination": "bwfs-east.internal:8080"
	}]`
	require.NoError(t, os.WriteFile(policiesCachePath, []byte(cacheJSON), 0o644))

	conf := &config.Config{BackupWindowGraceSec: 3600}

	var capturedBinary string
	var capturedArgs []string
	fr := func(ctx context.Context, binary string, args []string) error {
		capturedBinary = binary
		capturedArgs = args
		return nil
	}

	policiesFunc := func() []Policy { return backupTasks(policiesCachePath, conf) }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr, policiesFunc, 2)
	require.NoError(t, err)

	assert.Equal(t, "brfs", capturedBinary)
	require.Len(t, capturedArgs, 5)
	assert.Equal(t, "/var/lib/postgres", capturedArgs[0])
	assert.Equal(t, "--destination", capturedArgs[1])
	assert.Equal(t, "bwfs-east.internal:8080", capturedArgs[2])
	assert.Equal(t, "--job-id", capturedArgs[3])
	assert.Contains(t, capturedArgs[4], "backup:daily-db-backup:var-lib-postgres:")
}
```

- [ ] **Step 2: Run to verify it fails**

```bash
cd src && go test ./cmd/agent/... -run TestRun_BackupTaskFromRealCacheFileExecutesBrfsWithExpectedArgs -v
```

Expected: FAIL — `capturedBinary` is empty (`""`, not `"brfs"`), since `backupTasks`' output isn't wired into anything failing yet; actually, since `backupTasks` already exists (Task 7) and this test calls it directly via `policiesFunc`, this test should actually be exercising already-complete plumbing. Confirm by running it: if it unexpectedly passes already, that's fine — it means Task 7 already made this true, and this step is a no-op confirmation rather than a red step. Either outcome is acceptable; the important thing is Step 4 verifies main.go itself now uses the same composition.

- [ ] **Step 3: Wire `backupTasks` into `main.go`**

Edit `src/cmd/agent/main.go`, changing:

```go
	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Var directory resolution failed: %v\n", err)
		os.Exit(1)
	}
	cachePath := filepath.Join(varDir, "agent-state.json")

	pols := policies(conf)

	switch arguments.Action {
	case "serve":
		if err := os.MkdirAll(varDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create var directory %s: %v\n", varDir, err)
			os.Exit(1)
		}

		ctx := context.WithValue(context.Background(), "appName", appName)
		ctx = context.WithValue(ctx, config.ContextKey, conf)
		ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
		ctx = context.WithValue(ctx, "quietMode", false)

		logger, logfile := logging.NewLogger(ctx)
		defer logfile.Close()

		reconcileInterval := time.Duration(conf.ReconcileIntervalSec) * time.Second
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath)
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, func() []Policy { return pols }, conf.MaxConcurrentBackupJobs); err != nil {
			logger.Error("agent exited with error", "error", err)
			os.Exit(1)
		}

	case "list-policies":
		if err := renderPolicies(os.Stdout, cachePath, time.Now(), pols); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
	}
```

to:

```go
	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Var directory resolution failed: %v\n", err)
		os.Exit(1)
	}
	cachePath := filepath.Join(varDir, "agent-state.json")
	policiesCachePath := filepath.Join(varDir, "policies-cache.json")

	// policiesFunc combines the three static policies with the dynamic
	// backup tasks derived from policies-cache.json -- called fresh every
	// reconcile tick (not resolved once here) so agent serve notices
	// policy-update's cache changing over time without needing a restart.
	policiesFunc := func() []Policy {
		return append(policies(conf), backupTasks(policiesCachePath, conf)...)
	}

	switch arguments.Action {
	case "serve":
		if err := os.MkdirAll(varDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create var directory %s: %v\n", varDir, err)
			os.Exit(1)
		}

		ctx := context.WithValue(context.Background(), "appName", appName)
		ctx = context.WithValue(ctx, config.ContextKey, conf)
		ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
		ctx = context.WithValue(ctx, "quietMode", false)

		logger, logfile := logging.NewLogger(ctx)
		defer logfile.Close()

		reconcileInterval := time.Duration(conf.ReconcileIntervalSec) * time.Second
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath)
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs); err != nil {
			logger.Error("agent exited with error", "error", err)
			os.Exit(1)
		}

	case "list-policies":
		if err := renderPolicies(os.Stdout, cachePath, time.Now(), policiesFunc()); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
	}
```

- [ ] **Step 4: Run the full agent test suite and build**

```bash
cd src && go build ./... && go test ./cmd/agent/... -v -race
```

Expected: build succeeds; `PASS` for every test, no data races.

- [ ] **Step 5: Manual smoke check of `list-policies` output shape**

```bash
cd src && go run ./cmd/agent list-policies
```

Expected: fails gracefully with a configuration error (no `local.conf` in this ad-hoc invocation) — this step only confirms `main.go` still compiles and runs far enough to hit real config resolution, not a full functional check (that needs a configured node, out of scope for this plan).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/agent/main.go src/cmd/agent/integration_test.go
git commit -m "feat(agent): wire policy-driven backup tasks into serve and list-policies"
```

---

### Task 9: Documentation and changelog

**Files:**
- Modify: `docs/components/agent.md`
- Modify: `docs/components/brfs.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

**Interfaces:** None (docs only).

- [ ] **Step 1: Rewrite the relevant parts of `docs/components/agent.md`**

Replace the introductory paragraph:

```markdown
Node-level agent that reconciles local state against a small, config-driven set of policies.
It runs three embedded policies — `bootstrap-refresh`, `operating-refresh`, and `policy-update` —
the first two keep this node's two-tier mTLS credential (see [Security Model](../SECURITY.md))
fresh via `certclient`; the third fetches this node's applicable backup policies from
`policy-server` (see [policy-server](./policy-server.md)) into a local cache via `policyclient`.
Nothing yet acts on that cache — no policy-driven scheduling is wired into `agent`'s reconcile
loop. That integration remains separate, later work.
```

with:

```markdown
Node-level agent that reconciles local state against a small, config-driven set of policies.
It runs three embedded, statically-configured policies — `bootstrap-refresh`, `operating-refresh`,
and `policy-update` — the first two keep this node's two-tier mTLS credential (see
[Security Model](../SECURITY.md)) fresh via `certclient`; the third fetches this node's applicable
backup policies from `policy-server` (see [policy-server](./policy-server.md)) into a local cache
via `policyclient`. On top of those three, `agent` also derives a dynamic **backup task** for every
`(cached policy, object_filters path)` pair in that cache, and executes `brfs` for each one on its
own schedule — see "Policy-driven backup execution" below.
```

Update the `agent`'s three-policies table's surrounding text and add a new section right after the existing policy table (before "## Configuration Keys"):

```markdown
## Policy-driven backup execution

Every reconcile tick, `agent` re-reads `policies-cache.json` fresh (so it notices `policy-update`
refreshing the cache without needing a restart) and derives one backup task per
`(policy, object_filters path)` pair. Each task is tracked independently in `agent-state.json`
(ID: `backup:<policy-name>:<path>`) — one path's failures and backoff never affect any other path,
including a sibling path in the same policy.

A backup task is due when **both**:
- a `backup_window` cron slot is currently open (a trigger fired within the last
  `BackupWindowGraceSec`, and hasn't closed yet), **and**
- the path's last successful backup is older than the policy's `rpo` (or it has never succeeded).

When due, `agent` execs `brfs <path> --destination <destination> --job-id
backup:<policy>:<slug(path)>:<timestamp>` — the explicit job-id lets an operator correlate a
`bwfs` job record back to the policy and path that produced it. Unlike the three static policies,
backup task execs run in a background goroutine rather than the synchronous reconcile loop, so a
long-running backup never delays `bootstrap-refresh`/`operating-refresh`. Concurrency is bounded by
`MaxConcurrentBackupJobs`; a due task that can't acquire a slot this tick simply stays due and is
retried next tick. On `agent serve` shutdown (`SIGTERM`), in-flight backup execs are terminated
cleanly rather than orphaned — the resulting `bwfs` job simply never completes, the same outcome
already assigned to a crashed `brfs`.

A policy with an unparseable `rpo`, or no valid `backup_window` entry at all, contributes no tasks.
A missing or invalid `destination` is not checked in advance — the task is still created, and its
`brfs` exec simply fails (recorded as an ordinary failure with backoff), the same as any other exec
failure.

`agent list-policies` shows backup tasks as additional rows (`backup:<policy>:<path>`) alongside
the three static policies; a task's "NEXT RUN" reflects its next `backup_window` occurrence rather
than a fixed interval.
```

Update the config keys table, adding two rows after `PolicyFetchIntervalSec`:

```markdown
| `BackupWindowGraceSec` | 3600 (1 hour) | How long after a `backup_window` cron trigger a backup task's window stays "open" |
| `MaxConcurrentBackupJobs` | 2 | Upper bound on simultaneously in-flight `brfs` execs launched by backup tasks |
```

Add a cross-reference in "See Also":

```markdown
- [brfs](./brfs.md) — the binary backup tasks exec
```

- [ ] **Step 2: Update `docs/components/brfs.md`**

In the "Examples" section (or immediately after the `--job-id` paragraph), add:

```markdown
`agent`'s policy-driven backup tasks (see [agent](./agent.md#policy-driven-backup-execution)) use
the job-id convention `backup:<policy-name>:<slug-of-path>:<unix-timestamp>` — useful when grepping
`bwfs`'s job history for which policy produced a given run.
```

- [ ] **Step 3: Update `README.md`**

Change the `agent` bullet from:

```markdown
- **[agent](docs/components/agent.md)** - Node agent — reconciles local state against embedded policies (credential renewal via `certclient`, policy fetch via `policyclient`)
```

to:

```markdown
- **[agent](docs/components/agent.md)** - Node agent — reconciles local state against embedded policies (credential renewal via `certclient`, policy fetch via `policyclient`, and policy-driven backup execution via `brfs`)
```

- [ ] **Step 4: Update `docs/ARCHITECTURE.md`**

Change the `agent` row of the component table from:

```markdown
| agent | Node Agent — reconciles local state against embedded policies | Implemented (three policies: bootstrap credential renewal, operating-certificate refresh via `issuer`, and policy fetch via `policyclient`) |
```

to:

```markdown
| agent | Node Agent — reconciles local state against embedded policies | Implemented (bootstrap credential renewal, operating-certificate refresh via `issuer`, policy fetch via `policyclient`, and policy-driven backup execution via `brfs`) |
```

Change the `policy-server` row from:

```markdown
| policy-server | Serves backup policies filtered by a requesting client's hostname and attribute labels; no database, reads labels from the peer cert | Implemented (`agent` now fetches and caches its policies via `policyclient`; nothing yet acts on the cache — that remains separate, later work) |
```

to:

```markdown
| policy-server | Serves backup policies filtered by a requesting client's hostname and attribute labels; no database, reads labels from the peer cert | Implemented (`agent` fetches, caches, and now acts on its policies — deriving and running scheduled `brfs` backups via `policyclient`) |
```

In the prose paragraph beginning "`policy-server` is control plane by role...", change the final clause from:

```markdown
though nothing yet acts on that cache, turning
it into anything that actually runs a backup (`agent` or `brfs` consuming it) is separate, later
work.
```

to:

```markdown
and `agent` now acts on that cache directly, deriving and running scheduled `brfs` backups from
it — see [agent](components/agent.md#policy-driven-backup-execution).
```

In the prose paragraph beginning "`agent` is a node-level process...", change the final clause from:

```markdown
`policy-update` (`policyclient fetch`, every 15 minutes by default) fetches this node's
applicable backup policies from `policy-server` into a local cache. Each policy's outcome is
tracked in the same local cache (`agent list-policies` inspects it). It has no network role of its
own; all network behavior is `certclient`'s and `policyclient`'s, unchanged. See
[agent](components/agent.md).
```

to:

```markdown
`policy-update` (`policyclient fetch`, every 15 minutes by default) fetches this node's
applicable backup policies from `policy-server` into a local cache. `agent` also derives a dynamic
backup task per cached policy's object path and executes `brfs` for each one on a schedule gated by
that policy's `backup_window` and `rpo` — see [agent](components/agent.md#policy-driven-backup-execution).
Each policy's (and backup task's) outcome is tracked in the same local cache (`agent list-policies`
inspects it). See [agent](components/agent.md).
```

- [ ] **Step 5: Add a `CHANGELOG.md` entry**

Add to the top of `CHANGELOG.md`, above the existing `## 2026-07-10 — Agent policy-update job` entry:

```markdown
## 2026-07-11 — Agent acts on cached backup policies

`agent` closes the loop left open by the `policy-update` job: it now derives one backup task per
`(cached policy, object_filters path)` pair and runs `brfs` for each, scheduled by that policy's
`backup_window` (cron) and `rpo` (staleness) — both must hold, a slot must be open and the path
must actually be stale. `policy-server`'s policy schema gains a `destination` field (the target
`bwfs`), threaded through `policyclient`'s cache unchanged in shape otherwise. Backup execs run in
background goroutines bounded by a new `MaxConcurrentBackupJobs` config key, so a long backup never
delays credential refresh; a `SIGTERM` cleanly terminates in-flight backups rather than orphaning
them.
```

- [ ] **Step 6: Commit**

```bash
git add docs/components/agent.md docs/components/brfs.md README.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document policy-driven backup execution"
```

---

### Task 10: Final verification

**Files:** None (verification only).

- [ ] **Step 1: Full build**

```bash
cd src && go build ./...
```

Expected: no errors.

- [ ] **Step 2: Full test suite with race detector**

```bash
cd src && go test ./... -race
```

Expected: `ok` for every package, no failures, no data races.

- [ ] **Step 3: Lint**

```bash
make lint
```

Expected: no `go vet` issues.

- [ ] **Step 4: Review the full diff against `main`**

```bash
git log --oneline main..HEAD
git diff main..HEAD --stat
```

Expected: one commit per task (10 commits), touching exactly the files listed in each task above — no stray or unintended changes.
