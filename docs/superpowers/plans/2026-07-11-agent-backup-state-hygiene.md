# Agent Backup State Hygiene Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prune orphaned `agent-state.json` entries safely (gated on a confirmed-good `policies-cache.json` read) and surface the last failure's actual error message in `agent list-policies`, closing both non-goals left open by `docs/superpowers/specs/2026-07-10-agent-backup-execution-design.md`.

**Architecture:** `readCachedPolicies`/`backupTasks` gain a second `ok` return value distinguishing "confirmed empty/valid" from "read failed" — `run`'s `policiesFunc` parameter and `main.go`'s combinator thread that same signal through, and a new `reconcileState.prune` method deletes cache entries absent from a confirmed-good tick's policy list before that tick's due-checks run. Separately, `PolicyState` gains a `LastError` field, set/cleared in the single existing `recordOutcome` write path and rendered as a new truncated `ERROR` column in `list-policies`.

**Tech Stack:** Go (existing `src/cmd/agent` package), `testify` for assertions, no new dependencies.

## Global Constraints

- Design doc: `docs/superpowers/specs/2026-07-11-agent-backup-state-hygiene-design.md` — follow its Non-Goals exactly (no bwfs/catalog-backed recovery, no structured error taxonomy, no `inFlight` map changes).
- Test command: `cd src && go test ./cmd/agent/... -v` for this package; `cd src && go test ./...` before considering the plan done (per `Makefile`'s `test` target).
- Per `.claude/CLAUDE.md`'s feature-change rule: update `docs/components/agent.md` and add a `CHANGELOG.md` entry before this is considered mergeable — both included as part of Task 5 below, not deferred.
- No new gRPC/proto surface, no new `local.conf` keys — this plan touches only `src/cmd/agent`'s internal state handling and its own CLI output.

---

## Task 1: `ok` signal on `readCachedPolicies`/`backupTasks`

**Files:**
- Modify: `src/cmd/agent/backup.go:31-45` (`readCachedPolicies`) and `backup.go:129-172` (`backupTasks`)
- Test: `src/cmd/agent/backup_test.go` (full rewrite — nearly every existing test calls `backupTasks`)

**Interfaces:**
- Produces: `readCachedPolicies(policiesCachePath string) (policies []cachedPolicy, ok bool)` — `ok` is `true` iff the file was present and parsed as valid JSON (regardless of whether the resulting slice is empty), `false` on missing file or unmarshal error.
- Produces: `backupTasks(policiesCachePath string, conf *config.Config) (tasks []Policy, ok bool)` — `ok` passes through `readCachedPolicies`'s `ok` unchanged; when `false`, `tasks` is always `nil`.
- Consumed by: Task 2 (`run`'s `policiesFunc`, `main.go`'s combinator), Task 2's update to `integration_test.go`.

- [ ] **Step 1: Update `backup_test.go` for the new two-value signature and add `ok`-specific tests**

Replace the full file contents of `src/cmd/agent/backup_test.go` with:

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

func TestReadCachedPolicies_MissingFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	policies, ok := readCachedPolicies(filepath.Join(dir, "does-not-exist.json"))
	assert.False(t, ok)
	assert.Empty(t, policies)
}

func TestReadCachedPolicies_CorruptFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	policies, ok := readCachedPolicies(path)
	assert.False(t, ok)
	assert.Empty(t, policies)
}

func TestReadCachedPolicies_ValidEmptyListReturnsOkTrue(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[]`)
	policies, ok := readCachedPolicies(path)
	assert.True(t, ok)
	assert.Empty(t, policies)
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
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
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
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
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
	tasks, ok := backupTasks(path, conf)
	require.True(t, ok)
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
	tasks, ok := backupTasks(path, conf)
	require.True(t, ok)
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
	tasks, ok := backupTasks(path, conf)
	assert.True(t, ok, "the file itself was still validly read")
	assert.Empty(t, tasks)
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
	tasks, ok := backupTasks(path, conf)
	assert.True(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_MissingCacheFileReturnsOkFalseWithNoTasks(t *testing.T) {
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(filepath.Join(t.TempDir(), "does-not-exist.json"), conf)
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_CorruptCacheFileReturnsOkFalseWithNoTasks(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_RemovedPolicyStopsBeingDerived(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	conf := &config.Config{BackupWindowGraceSec: 3600}

	require.NoError(t, os.WriteFile(cachePath, []byte(`[{
		"name": "p", "object_filters": ["/data"], "rpo": "1h",
		"backup_window": ["0 2 * * *"], "destination": "bwfs:8080"
	}]`), 0o644))
	tasks, ok := backupTasks(cachePath, conf)
	require.True(t, ok)
	require.Len(t, tasks, 1)

	require.NoError(t, os.WriteFile(cachePath, []byte(`[]`), 0o644))
	tasks, ok = backupTasks(cachePath, conf)
	assert.True(t, ok, "an empty-but-valid file is still a confirmed-good read")
	assert.Empty(t, tasks)
}
```

- [ ] **Step 2: Run the test package to confirm it fails to compile against the old signature**

Run: `cd src && go test ./cmd/agent/... -run TestBackupTasks -v`
Expected: build failure — `backup.go` still declares `backupTasks`/`readCachedPolicies` with a single return value, so the test file's two-value `tasks, ok := backupTasks(...)` calls don't compile. The compiler error should reference `backup.go`'s current single-value `func backupTasks(...) []Policy` declaration.

- [ ] **Step 3: Update `backup.go`'s `readCachedPolicies` and `backupTasks`**

Replace `readCachedPolicies` (currently `backup.go:31-45`) with:

```go
// readCachedPolicies reads policiesCachePath, returning ok=false if the
// file is missing or unparseable -- distinct from a confirmed-good read
// that happens to list zero policies (ok=true, nil slice). Callers that
// prune state derived from this list (see reconcile.go's prune) rely on
// this distinction: a transient read failure must never be mistaken for
// "every policy was removed."
func readCachedPolicies(policiesCachePath string) ([]cachedPolicy, bool) {
	data, err := os.ReadFile(policiesCachePath)
	if err != nil {
		return nil, false
	}
	var policies []cachedPolicy
	if err := json.Unmarshal(data, &policies); err != nil {
		return nil, false
	}
	return policies, true
}
```

Replace `backupTasks` (currently `backup.go:129-172`) with:

```go
// backupTasks derives one Policy per (cached policy, object_filters path)
// pair from policiesCachePath, valid at the instant it's called. Callers
// that need to notice policies-cache.json changing over time (agent
// serve's reconcile loop) must call this fresh every tick rather than
// caching its result once.
//
// The second return value is ok=false whenever the underlying read
// failed (see readCachedPolicies) -- callers must treat that as "this
// tick's view is untrustworthy," never as "there are zero tasks."
//
// A policy with an unparseable rpo, or with no valid backup_window
// schedule at all, contributes no tasks -- there is no sound due-check
// that could be built for it, so skipping entirely (rather than running
// on a guess) is the fail-safe choice. A missing/invalid destination is
// not checked here: the task is still built, and simply fails at brfs
// exec time like any other exec failure (see reconcile.go).
func backupTasks(policiesCachePath string, conf *config.Config) ([]Policy, bool) {
	grace := time.Duration(conf.BackupWindowGraceSec) * time.Second

	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []Policy
	for _, p := range cachedPolicies {
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
	return tasks, true
}
```

Every other function in `backup.go` (`parseSchedules`, `windowOpen`, `nextWindow`, `slug`, `backupTaskID`, `backupJobID`) is unchanged.

- [ ] **Step 4: Run the test package to confirm it now passes**

Run: `cd src && go test ./cmd/agent/... -run 'TestBackupTasks|TestReadCachedPolicies|TestWindowOpen|TestRpoElapsed' -v`
Expected: all `PASS`. (Note: the rest of the package, e.g. `reconcile_test.go` and `main.go`, will still fail to *build* until Task 2 updates their call sites — that's expected and resolved next task, not a regression in this one.)

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/backup.go src/cmd/agent/backup_test.go
git commit -m "feat(agent): distinguish confirmed-empty from failed policy-cache reads"
```

---

## Task 2: Wire the `ok` signal through `run`, add pruning

**Files:**
- Modify: `src/cmd/agent/reconcile.go:90-221` (`reconcileState`, `run`)
- Modify: `src/cmd/agent/main.go:47-53,80-84` (`policiesFunc` combinator, `list-policies` call site)
- Modify: `src/cmd/agent/reconcile_test.go` (every `func() []Policy {...}` call site, plus new prune tests)
- Modify: `src/cmd/agent/integration_test.go:50` (`policiesFunc` call site)

**Interfaces:**
- Consumes: `backupTasks(policiesCachePath string, conf *config.Config) ([]Policy, bool)` from Task 1.
- Produces: `run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int) error` — signature change from `func() []Policy` to `func() ([]Policy, bool)`.
- Produces: `(rs *reconcileState) prune(currentIDs map[string]struct{})` — deletes any `rs.cache` entry whose key isn't in `currentIDs`, persists if anything changed.
- Consumed by: Task 3 modifies `recordOutcome` in the same file; no other task depends on `prune`'s signature.

- [ ] **Step 1: Update `reconcile_test.go`'s existing call sites and add prune tests**

In `src/cmd/agent/reconcile_test.go`, change every occurrence of:

```go
func() []Policy { return testPolicies }
```

to:

```go
func() ([]Policy, bool) { return testPolicies, true }
```

This applies to `TestRun_ExecutesDuePolicyAndDoesNotRetriggerWithinInterval`, `TestRun_FailedExecutionRecordsFailureAndRetriesAfterBackoff`, `TestRun_BackgroundPolicyDoesNotBlockSyncPolicyInSameTick`, `TestRun_ConcurrencyCapLimitsSimultaneousBackgroundExecs`, `TestRun_SamePolicyNotRedispatchedWhileStillInFlight`, and `TestRun_BackgroundExecReceivesCancelledContextOnShutdown` — six call sites total, each currently reading `func() []Policy { return testPolicies }` inline as the fifth argument to `run(...)`.

Then append these new tests to the end of the file:

```go
func TestPrune_RemovesEntryNotInCurrentIDs(t *testing.T) {
	dir := t.TempDir()
	rs := &reconcileState{
		cachePath: filepath.Join(dir, "agent-state.json"),
		cache: Cache{
			"backup:p:/a": {ConsecutiveFailures: 1},
			"backup:p:/b": {ConsecutiveFailures: 0},
		},
		logger: testLogger(),
	}

	rs.prune(map[string]struct{}{"backup:p:/a": {}})

	assert.Contains(t, rs.cache, "backup:p:/a")
	assert.NotContains(t, rs.cache, "backup:p:/b")

	onDisk, err := readCache(rs.cachePath)
	require.NoError(t, err)
	assert.NotContains(t, onDisk, "backup:p:/b", "prune must persist the removal")
}

func TestPrune_NoWriteWhenNothingRemoved(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	rs := &reconcileState{
		cachePath: cachePath,
		cache:     Cache{"backup:p:/a": {ConsecutiveFailures: 1}},
		logger:    testLogger(),
	}

	rs.prune(map[string]struct{}{"backup:p:/a": {}})

	_, err := os.Stat(cachePath)
	assert.True(t, os.IsNotExist(err), "prune must not write the cache file when nothing changed")
}

func TestRun_PrunesOrphanedEntryOnConfirmedGoodTick(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	require.NoError(t, writeCache(cachePath, Cache{
		"orphaned": {ConsecutiveFailures: 2},
	}))

	testPolicies := []Policy{{ID: "current", Binary: "true", Interval: time.Hour}}
	fr := &fakeRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run,
		func() ([]Policy, bool) { return testPolicies, true }, 2)
	require.NoError(t, err)

	cache, err := readCache(cachePath)
	require.NoError(t, err)
	assert.NotContains(t, cache, "orphaned")
	assert.Contains(t, cache, "current")
}

func TestRun_SkipsPruneWhenPoliciesFuncReportsNotOk(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	require.NoError(t, writeCache(cachePath, Cache{
		"stale": {ConsecutiveFailures: 2},
	}))

	fr := &fakeRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	// ok=false every tick, mirroring a persistently unreadable
	// policies-cache.json -- "stale" must survive untouched.
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run,
		func() ([]Policy, bool) { return nil, false }, 2)
	require.NoError(t, err)

	cache, err := readCache(cachePath)
	require.NoError(t, err)
	assert.Contains(t, cache, "stale", "a not-ok tick must never prune")
}

// TestRun_PruneRaceResurrectedEntryPrunedAgainNextTick exercises the
// "accepted race" documented in the design: a task's cache entry, pruned
// while that task's own previous-tick run is still in flight, is
// resurrected by recordOutcome once the run completes (it writes
// unconditionally by ID) -- and, if the task is genuinely gone, gets
// pruned again on the next confirmed-good tick. Uses the same fake
// blocking-runner pattern as TestRun_ConcurrencyCapLimitsSimultaneousBackgroundExecs.
func TestRun_PruneRaceResurrectedEntryPrunedAgainNextTick(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	old := time.Now().Add(-2 * time.Hour)
	require.NoError(t, writeCache(cachePath, Cache{
		"slow-backup": {LastSuccessAt: &old},
	}))

	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	blockingRunner := func(ctx context.Context, binary string, args []string) error {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil
	}

	var mu sync.Mutex
	removed := false
	policiesFunc := func() ([]Policy, bool) {
		mu.Lock()
		defer mu.Unlock()
		if removed {
			return nil, true
		}
		return []Policy{{ID: "slow-backup", Binary: "slow", Interval: time.Hour, Background: true}}, true
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, policiesFunc, 2)
	}()

	<-entered // dispatched while "slow-backup" was still present in the policy list

	mu.Lock()
	removed = true
	mu.Unlock()

	time.Sleep(20 * time.Millisecond) // let several ticks prune the now-absent entry while the run is still in flight
	close(release)                    // let the in-flight run complete; recordOutcome resurrects the entry
	time.Sleep(20 * time.Millisecond) // let at least one more tick prune it again
	cancel()
	<-done

	cache, err := readCache(cachePath)
	require.NoError(t, err)
	assert.NotContains(t, cache, "slow-backup", "resurrected entry must be pruned again on the next confirmed-good tick")
}
```

This uses `os` (already imported by other test files in the package? check: `reconcile_test.go` already imports `"os"` at line 9 — reuse it) and `filepath`, `time`, `context` — all already imported in this file.

- [ ] **Step 2: Update `integration_test.go`'s call site**

In `src/cmd/agent/integration_test.go:50`, change:

```go
	policiesFunc := func() []Policy { return backupTasks(policiesCachePath, conf) }
```

to:

```go
	policiesFunc := func() ([]Policy, bool) { return backupTasks(policiesCachePath, conf) }
```

(`backupTasks` already returns `([]Policy, bool)` as of Task 1, so this is a direct pass-through — no further change needed in this file.)

- [ ] **Step 3: Run the test package to confirm it fails to compile against the old `run` signature**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: build failure — `reconcile.go` still declares `run`'s `policiesFunc` parameter as `func() []Policy`, so every updated test call site (now passing `func() ([]Policy, bool)`) fails to type-check. The error should reference `reconcile.go`'s current `run` declaration.

- [ ] **Step 4: Add `prune` and update `run` in `reconcile.go`**

Add this method directly below `recordOutcome` (before the `run` function) in `src/cmd/agent/reconcile.go`:

```go
// prune removes any cache entry whose ID isn't present in currentIDs --
// called once per reconcile tick, only when that tick's policy list came
// from a confirmed-good read (run passes ok from policiesFunc), so a
// transient unreadable policies-cache.json can never be mistaken for
// "every backup task was removed" and wipe live backoff/RPO history for
// tasks that are still current.
func (rs *reconcileState) prune(currentIDs map[string]struct{}) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	changed := false
	for id := range rs.cache {
		if _, ok := currentIDs[id]; !ok {
			delete(rs.cache, id)
			changed = true
		}
	}
	if !changed {
		return
	}
	if err := writeCache(rs.cachePath, rs.cache); err != nil {
		rs.logger.Error("failed to persist cache after prune", "error", err)
	}
}
```

Replace the `run` function's signature and its tick loop's opening (the `for ctx.Err() == nil { ... }` block) with:

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policiesFunc func() ([]Policy, bool), maxConcurrentBackgroundJobs int) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}
	rs := &reconcileState{cachePath: cachePath, cache: cache, logger: logger}

	sem := make(chan struct{}, maxConcurrentBackgroundJobs)
	var wg sync.WaitGroup

	for ctx.Err() == nil {
		now := time.Now()
		policyList, ok := policiesFunc()
		if ok {
			currentIDs := make(map[string]struct{}, len(policyList))
			for _, p := range policyList {
				currentIDs[p.ID] = struct{}{}
			}
			rs.prune(currentIDs)
		}

		for _, p := range policyList {
			state := rs.get(p.ID)
			if !isDue(p, state, now) {
				continue
			}

			if p.Background {
				if !rs.tryMarkInFlight(p.ID) {
					continue // still running from a previous tick; stays due, skip this tick
				}
				select {
				case sem <- struct{}{}:
				default:
					rs.clearInFlight(p.ID)
					continue // no free slot this tick; stays due, retried next tick
				}
				wg.Add(1)
				go func(p Policy) {
					defer wg.Done()
					defer func() { <-sem }()
					defer rs.clearInFlight(p.ID)
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
```

(Only the two lines introducing `policyList, ok := policiesFunc()` and the `if ok { ... prune ... }` block are new; the per-policy dispatch loop body is copied unchanged from the existing implementation.)

- [ ] **Step 5: Update `main.go`'s `policiesFunc` combinator and its two call sites**

In `src/cmd/agent/main.go:47-53`, replace:

```go
	// policiesFunc combines the three static policies with the dynamic
	// backup tasks derived from policies-cache.json -- called fresh every
	// reconcile tick (not resolved once here) so agent serve notices
	// policy-update's cache changing over time without needing a restart.
	policiesFunc := func() []Policy {
		return append(policies(conf), backupTasks(policiesCachePath, conf)...)
	}
```

with:

```go
	// policiesFunc combines the three static policies with the dynamic
	// backup tasks derived from policies-cache.json -- called fresh every
	// reconcile tick (not resolved once here) so agent serve notices
	// policy-update's cache changing over time without needing a restart.
	// ok is false whenever backupTasks's own read of policies-cache.json
	// failed this tick -- see reconcile.go's prune, which must not treat a
	// failed read as "every backup task was removed."
	policiesFunc := func() ([]Policy, bool) {
		tasks, ok := backupTasks(policiesCachePath, conf)
		return append(policies(conf), tasks...), ok
	}
```

In the same file, `case "list-policies":` (`main.go:80-84`), replace:

```go
	case "list-policies":
		if err := renderPolicies(os.Stdout, cachePath, time.Now(), policiesFunc()); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
```

with:

```go
	case "list-policies":
		allPolicies, _ := policiesFunc()
		if err := renderPolicies(os.Stdout, cachePath, time.Now(), allPolicies); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
```

(`list-policies` is a read-only display of whatever `agent serve` last recorded — see `renderPolicies`'s own doc comment — so it deliberately ignores `ok` here; it always shows every currently-known policy/task by ID, ok or not, exactly as before this change.)

- [ ] **Step 6: Run the full package test suite to confirm everything passes**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: all tests `PASS`, including the new `TestPrune_*`, `TestRun_Prunes*`, `TestRun_SkipsPrune*`, and `TestRun_PruneRace*` tests from Step 1.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/agent/reconcile.go src/cmd/agent/main.go src/cmd/agent/reconcile_test.go src/cmd/agent/integration_test.go
git commit -m "feat(agent): prune orphaned backup-task state on confirmed-good cache reads"
```

---

## Task 3: `LastError` tracking

**Files:**
- Modify: `src/cmd/agent/cache.go:14-22` (`PolicyState`)
- Modify: `src/cmd/agent/reconcile.go` (`recordOutcome`)
- Modify: `src/cmd/agent/reconcile_test.go` (new tests)

**Interfaces:**
- Produces: `PolicyState.LastError string` (JSON tag `last_error,omitempty`) — set to the failing error's `.Error()` string on failure, cleared to `""` on success.
- Consumed by: Task 4 (`list.go`'s `renderPolicies`).

- [ ] **Step 1: Write failing tests for `recordOutcome`'s `LastError` handling**

Append to `src/cmd/agent/reconcile_test.go`:

```go
func TestRecordOutcome_SetsLastErrorOnFailure(t *testing.T) {
	dir := t.TempDir()
	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}

	rs.recordOutcome("p", errors.New("boom"), time.Now())

	assert.Equal(t, "boom", rs.cache["p"].LastError)
}

func TestRecordOutcome_ClearsLastErrorOnSuccess(t *testing.T) {
	dir := t.TempDir()
	rs := &reconcileState{
		cachePath: filepath.Join(dir, "agent-state.json"),
		cache:     Cache{"p": {LastError: "boom"}},
		logger:    testLogger(),
	}

	rs.recordOutcome("p", nil, time.Now())

	assert.Empty(t, rs.cache["p"].LastError)
}

func TestRecordOutcome_LastErrorReflectsMostRecentFailure(t *testing.T) {
	dir := t.TempDir()
	rs := &reconcileState{cachePath: filepath.Join(dir, "agent-state.json"), cache: Cache{}, logger: testLogger()}

	rs.recordOutcome("p", errors.New("first failure"), time.Now())
	rs.recordOutcome("p", errors.New("second failure"), time.Now())

	assert.Equal(t, "second failure", rs.cache["p"].LastError)
}
```

(`errors`, `time`, `filepath`, `testing` are all already imported at the top of `reconcile_test.go`.)

- [ ] **Step 2: Run the new tests to confirm they fail**

Run: `cd src && go test ./cmd/agent/... -run TestRecordOutcome_ -v`
Expected: FAIL — `rs.cache["p"].LastError` is a compile error today (`PolicyState` has no `LastError` field yet).

- [ ] **Step 3: Add `LastError` to `PolicyState` and wire it into `recordOutcome`**

In `src/cmd/agent/cache.go`, replace the `PolicyState` struct (`cache.go:14-22`):

```go
type PolicyState struct {
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
	LastError           string     `json:"last_error,omitempty"`
}
```

In `src/cmd/agent/reconcile.go`, replace `recordOutcome`'s body:

```go
func (rs *reconcileState) recordOutcome(id string, attemptErr error, attemptTime time.Time) {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	state := rs.cache[id]
	state.LastAttemptAt = &attemptTime
	if attemptErr == nil {
		state.LastSuccessAt = &attemptTime
		state.ConsecutiveFailures = 0
		state.NextRetryAt = nil
		state.LastError = ""
	} else {
		state.ConsecutiveFailures++
		retryAt := attemptTime.Add(backoff(state.ConsecutiveFailures))
		state.NextRetryAt = &retryAt
		state.LastError = attemptErr.Error()
		rs.logger.Error("policy execution failed", "policy", id, "error", attemptErr)
	}
	rs.cache[id] = state

	if err := writeCache(rs.cachePath, rs.cache); err != nil {
		rs.logger.Error("failed to persist cache", "error", err)
	}
}
```

- [ ] **Step 4: Run the tests to confirm they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: all `PASS`, including the three new `TestRecordOutcome_*` tests and every pre-existing test in the package.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/cache.go src/cmd/agent/reconcile.go src/cmd/agent/reconcile_test.go
git commit -m "feat(agent): track the last failure's error message per policy/task"
```

---

## Task 4: Surface `LastError` in `list-policies`

**Files:**
- Modify: `src/cmd/agent/list.go`
- Modify: `src/cmd/agent/list_test.go`

**Interfaces:**
- Consumes: `PolicyState.LastError` from Task 3.
- Produces: `formatError(s string) string` — `"-"` for empty, the string unchanged if `<= maxErrorColumnWidth` characters, else truncated with a trailing `…`.

- [ ] **Step 1: Write failing tests**

Append to `src/cmd/agent/list_test.go` (and add `"strings"` to its import block, alongside the existing `"bytes"`, `"path/filepath"`, `"testing"`, `"time"`):

```go
func TestFormatError_EmptyReturnsDash(t *testing.T) {
	assert.Equal(t, "-", formatError(""))
}

func TestFormatError_ShortReturnsUnchanged(t *testing.T) {
	assert.Equal(t, "boom", formatError("boom"))
}

func TestFormatError_LongIsTruncatedWithEllipsis(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := formatError(long)
	// maxErrorColumnWidth is a rune-count cap, not a byte-count cap -- "…"
	// is one rune but three UTF-8 bytes, so the check must count runes.
	assert.LessOrEqual(t, len([]rune(got)), maxErrorColumnWidth)
	assert.True(t, strings.HasSuffix(got, "…"))
}

func TestRenderPolicies_FailingPolicyShowsLastError(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{{ID: "operating-refresh", Binary: "certclient", Interval: 15 * time.Minute}}

	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"operating-refresh": {LastAttemptAt: &now, ConsecutiveFailures: 1, LastError: "connection refused"},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, testPolicies))

	assert.Contains(t, buf.String(), "connection refused")
}

func TestRenderPolicies_LongLastErrorIsTruncatedInTable(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{{ID: "p", Binary: "b", Interval: time.Minute}}

	longErr := strings.Repeat("x", 200)
	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"p": {LastAttemptAt: &now, ConsecutiveFailures: 1, LastError: longErr},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, testPolicies))

	out := buf.String()
	assert.NotContains(t, out, longErr, "the full 200-char error must not appear verbatim in table output")
}
```

- [ ] **Step 2: Run the new tests to confirm they fail**

Run: `cd src && go test ./cmd/agent/... -run 'TestFormatError|TestRenderPolicies_.*LastError' -v`
Expected: FAIL to compile — `formatError` and `maxErrorColumnWidth` don't exist yet in `list.go`.

- [ ] **Step 3: Add `formatError` and wire it into `renderPolicies`**

In `src/cmd/agent/list.go`, add this near the top of the file, alongside the other `format*` helpers:

```go
// maxErrorColumnWidth caps how many runes of LastError renderPolicies
// shows in its table -- a rune cap, not a byte cap, since the "…"
// truncation marker is one rune but three UTF-8 bytes, and error strings
// aren't guaranteed to be ASCII. The full, untruncated message is always
// available by reading agent-state.json directly.
const maxErrorColumnWidth = 60

func formatError(s string) string {
	if s == "" {
		return "-"
	}
	runes := []rune(s)
	if len(runes) <= maxErrorColumnWidth {
		return s
	}
	return string(runes[:maxErrorColumnWidth-1]) + "…"
}
```

Replace `renderPolicies`'s header line and per-row `Fprintf`:

```go
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "POLICY\tSTATE\tLAST SUCCESS\tLAST ATTEMPT\tFAILURES\tERROR\tNEXT RUN")
	for _, p := range policies {
		s := cache[p.ID]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			p.ID,
			health(s),
			formatTime(s.LastSuccessAt),
			formatTime(s.LastAttemptAt),
			s.ConsecutiveFailures,
			formatError(s.LastError),
			formatNextRun(estimatedNextRun(p, s, now), now),
		)
	}
	return tw.Flush()
```

- [ ] **Step 4: Run the full package test suite to confirm everything passes**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: all `PASS`. Existing tests (`TestRenderPolicies_MissingCacheShowsNeverRunAndDueNow`, `TestRenderPolicies_HealthyPolicyShowsOkAndNotNeverRun`, `TestRenderPolicies_FailingPolicyShowsRetryingWithCount`) use `assert.Contains`/`assert.NotContains` on substrings unaffected by the new column, so they continue to pass unmodified.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/agent/list.go src/cmd/agent/list_test.go
git commit -m "feat(agent): show last failure's error in list-policies output"
```

---

## Task 5: Documentation, changelog, and full-suite verification

**Files:**
- Modify: `docs/components/agent.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing new — this task only documents Tasks 1-4's already-implemented, already-tested behavior.

- [ ] **Step 1: Update `docs/components/agent.md`**

Replace the paragraph at `docs/components/agent.md:82-85` (currently reading "A policy with an unparseable `rpo`..."):

```markdown
A policy with an unparseable `rpo`, or no valid `backup_window` entry at all, contributes no tasks.
A missing or invalid `destination` is not checked in advance — the task is still created, and its
`brfs` exec simply fails (recorded as an ordinary failure with backoff), the same as any other exec
failure.

A backup task's `agent-state.json` entry is removed automatically once its `(policy, path)` pair no
longer appears in `policies-cache.json` — checked every reconcile tick, but only acted on when that
tick's read of the cache file succeeded; a momentarily missing or corrupt cache file never triggers
pruning, so a transient read glitch can never be mistaken for "every policy was removed" and wipe a
live task's backoff/RPO history.
```

Replace the paragraph at `docs/components/agent.md:87-89` (currently reading "`agent list-policies` shows backup tasks..."):

```markdown
`agent list-policies` shows backup tasks as additional rows (`backup:<policy>:<path>`) alongside
the three static policies; a task's "NEXT RUN" reflects its next `backup_window` occurrence rather
than a fixed interval. Each row's `ERROR` column shows the most recent failure's message (truncated
to 60 characters, `-` if there isn't one), cleared automatically on that policy/task's next success.
```

Also update the example table at `docs/components/agent.md:46-50` to include the new column:

```markdown
```
POLICY              STATE               LAST SUCCESS         LAST ATTEMPT         FAILURES  ERROR  NEXT RUN
bootstrap-refresh    ok                  2026-07-03 14:32:10  2026-07-03 14:32:10  0         -      2026-07-04 14:32:10
operating-refresh    ok                  2026-07-05 09:10:00  2026-07-05 09:10:00  0         -      2026-07-05 09:25:00
```
```

- [ ] **Step 2: Add a `CHANGELOG.md` entry**

Insert this new entry at the very top of `CHANGELOG.md`, above the existing `## 2026-07-11 — Agent acts on cached backup policies` entry (most-recent-first ordering; same-day multiple entries already occur elsewhere in this file, e.g. 2026-07-10):

```markdown
## 2026-07-11 — Agent backup state hygiene: pruning and last-error tracking

`agent-state.json` entries for backup tasks whose policy or path has been removed from
`policies-cache.json` are now pruned automatically, gated on a confirmed-good cache read so a
transient unreadable or corrupt cache file can never be mistaken for "everything was removed" and
wipe live backoff/RPO history. `PolicyState` also gains `LastError`, the most recent failure's
message, cleared on the next success and surfaced as a new `ERROR` column in `agent list-policies`.

```

- [ ] **Step 3: Run the full repository test suite**

Run: `cd src && go test ./...`
Expected: all packages `ok`, no failures — this confirms Tasks 1-4's changes to `src/cmd/agent` haven't broken any other package (none should import `cmd/agent`, since Go forbids importing another command's `main` package, but this is the standing verification step per `Makefile`'s own `test` target).

- [ ] **Step 4: Commit**

```bash
git add docs/components/agent.md CHANGELOG.md
git commit -m "docs: document agent backup state pruning and last-error tracking"
```
