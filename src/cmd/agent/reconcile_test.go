package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testLoggerWithBuffer() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, nil)), &buf
}

type fakeRunner struct {
	mu    sync.Mutex
	calls int
	failN int // number of subsequent calls to fail before succeeding
}

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

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestIsDue_NeverRunIsDue(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	assert.True(t, isDue(p, PolicyState{}, time.Now()))
}

func TestIsDue_HealthyPolicyNotDueBeforeInterval(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	now := time.Now()
	last := now.Add(-1 * time.Minute)
	assert.False(t, isDue(p, PolicyState{LastSuccessAt: &last}, now))
}

func TestIsDue_HealthyPolicyDueAfterInterval(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	now := time.Now()
	last := now.Add(-6 * time.Minute)
	assert.True(t, isDue(p, PolicyState{LastSuccessAt: &last}, now))
}

func TestIsDue_FailingPolicyIgnoresIntervalUsesNextRetryAt(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	now := time.Now()
	last := now.Add(-1 * time.Minute)    // within Interval — would be "not due" if healthy
	retryAt := now.Add(-1 * time.Second) // but the retry threshold already passed
	state := PolicyState{LastSuccessAt: &last, ConsecutiveFailures: 1, NextRetryAt: &retryAt}
	assert.True(t, isDue(p, state, now))
}

func TestIsDue_FailingPolicyNotDueBeforeNextRetryAt(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	now := time.Now()
	retryAt := now.Add(1 * time.Minute)
	state := PolicyState{ConsecutiveFailures: 1, NextRetryAt: &retryAt}
	assert.False(t, isDue(p, state, now))
}

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

func TestBackoff_JitterWithinHalfToFullRange(t *testing.T) {
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Second, time.Minute
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	for failures := 1; failures <= 5; failures++ {
		exp := min(max(failures-1, 0), 8)
		full := backoffBase * time.Duration(1<<exp)
		if full > backoffMax {
			full = backoffMax
		}
		d := backoff(failures)
		assert.GreaterOrEqual(t, d, full/2)
		assert.LessOrEqual(t, d, full)
	}
}

func TestBackoff_CappedAtMax(t *testing.T) {
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Second, 30*time.Second
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	d := backoff(20) // huge failure count, must clamp to backoffMax
	assert.LessOrEqual(t, d, backoffMax)
}

func TestRun_ExecutesDuePolicyAndDoesNotRetriggerWithinInterval(t *testing.T) {
	testPolicies := []Policy{{ID: "test-policy", Binary: "true", Interval: time.Hour}}

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	fr := &fakeRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, 1, fr.callCount(), "a healthy 1-hour-interval policy must not re-trigger within the test window")

	cache, err := readCache(cachePath)
	require.NoError(t, err)
	state := cache["test-policy"]
	require.NotNil(t, state.LastSuccessAt)
	assert.Equal(t, 0, state.ConsecutiveFailures)
}

func TestRun_FailedExecutionRecordsFailureAndRetriesAfterBackoff(t *testing.T) {
	testPolicies := []Policy{{ID: "test-policy", Binary: "false", Interval: time.Hour}}

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 20*time.Millisecond, 50*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	fr := &fakeRunner{failN: 1} // fails once, then succeeds
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
	require.NoError(t, err)

	assert.GreaterOrEqual(t, fr.callCount(), 2, "must retry after the backoff window elapses")

	cache, err := readCache(cachePath)
	require.NoError(t, err)
	state := cache["test-policy"]
	assert.Equal(t, 0, state.ConsecutiveFailures, "resets to 0 after the eventual success")
	require.NotNil(t, state.LastSuccessAt)
}

// TestRealExec_ResolvesBinaryColocatedWithOwnExecutable proves realExec
// finds a sibling binary placed next to the test executable's own
// directory, even though its bare name is not on $PATH. This mirrors the
// repo's actual deployment layout (see deploy/control-plane/catalog), where
// certclient sits alongside its caller rather than being installed onto
// $PATH.
func TestRealExec_ResolvesBinaryColocatedWithOwnExecutable(t *testing.T) {
	exePath, err := os.Executable()
	require.NoError(t, err)

	// Name is unique per test run (via pid) so it can never collide with a
	// real binary that happens to be on $PATH.
	name := fmt.Sprintf("fake-colocated-binary-%d", os.Getpid())
	scriptPath := filepath.Join(filepath.Dir(exePath), name)

	script := "#!/bin/sh\nexit 0\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))
	defer os.Remove(scriptPath)

	err = realExec(context.Background(), name, nil)
	assert.NoError(t, err)
}

// TestRealExec_FallsBackToPathWhenNotColocated proves the pre-existing
// $PATH-based resolution still works for a bare name that isn't colocated
// next to the test binary's own directory — a regression guard for local
// and dev usage where certclient genuinely is on $PATH.
func TestRealExec_FallsBackToPathWhenNotColocated(t *testing.T) {
	err := realExec(context.Background(), "true", nil)
	assert.NoError(t, err)
}

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
		done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, blockingRunner, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
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
	entered := make(chan struct{}, 3)

	blockingRunner := func(ctx context.Context, binary string, args []string) error {
		mu.Lock()
		inFlight++
		if inFlight > maxObserved {
			maxObserved = inFlight
		}
		mu.Unlock()

		select {
		case entered <- struct{}{}:
		default:
		}
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
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, func() ([]Policy, bool) { return testPolicies, true }, 1, nil, nil, nil)
	}()

	<-entered
	time.Sleep(20 * time.Millisecond) // give a few more ticks a chance to (correctly) be refused a slot
	close(release)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, maxObserved, "concurrency cap of 1 must never be exceeded")
}

func TestRun_SamePolicyNotRedispatchedWhileStillInFlight(t *testing.T) {
	var mu sync.Mutex
	dispatchCount := 0
	release := make(chan struct{})
	entered := make(chan struct{}, 1)

	blockingRunner := func(ctx context.Context, binary string, args []string) error {
		mu.Lock()
		dispatchCount++
		mu.Unlock()
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return nil
	}

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	testPolicies := []Policy{{ID: "slow-backup", Binary: "slow", Interval: time.Hour, Background: true}}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, func() ([]Policy, bool) { return testPolicies, true }, 5, nil, nil, nil)
	}()

	<-entered
	time.Sleep(50 * time.Millisecond) // several more ticks pass while the one dispatch is still blocked on release
	close(release)
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, dispatchCount, "a still-running background policy must not be dispatched again on a later tick")
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
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
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
		func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
	require.NoError(t, err)

	cache, err := readCache(cachePath)
	require.NoError(t, err)
	assert.NotContains(t, cache, "orphaned")
	assert.Contains(t, cache, "current")
}

// TestRun_DisabledPolicyPrunedViaBackupTasks proves the "no dedicated
// disabled-handling code needed in reconcile.go" architectural claim holds
// under an actual end-to-end tick: it composes the real backupTasks (which
// already contributes zero tasks for a disabled policy) with the real
// run()/prune() path, rather than relying on the generic synthetic-policy
// prune tests above to stand in for this specific case.
func TestRun_DisabledPolicyPrunedViaBackupTasks(t *testing.T) {
	dir := t.TempDir()
	stateCachePath := filepath.Join(dir, "agent-state.json")
	policiesCachePath := writeCachedPolicies(t, dir, `[{
		"name": "one-shot",
		"type": "backup",
		"object_filters": [{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/data"}],
		"rpo": "5m",
		"backup_window": ["* * * * *"],
		"destination": "bwfs:8080",
		"disabled_at": "2020-01-01T00:00:00Z"
	}]`)
	taskID := "backup:one-shot:/data:aaaaaaaa"
	require.NoError(t, writeCache(stateCachePath, Cache{
		taskID: {ConsecutiveFailures: 0},
	}))

	conf := &config.Config{BackupWindowGraceSec: 3600}
	fr := &fakeRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), stateCachePath, 10*time.Millisecond, fr.run,
		func() ([]Policy, bool) { return backupTasks(policiesCachePath, conf) }, 2, nil, nil, nil)
	require.NoError(t, err)

	cache, err := readCache(stateCachePath)
	require.NoError(t, err)
	assert.NotContains(t, cache, taskID, "a task derived from a disabled policy must be pruned even though its cache entry pre-dates the policy going disabled")
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
		func() ([]Policy, bool) { return nil, false }, 2, nil, nil, nil)
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
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, policiesFunc, 2, nil, nil, nil)
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

func TestLogExecOutcome_SuccessLogsStartAndCompletionWithJobID(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	logExecStart(logger, p)
	logExecCompletion(logger, p, nil, 250*time.Millisecond)

	out := buf.String()
	assert.Contains(t, out, "policy execution started")
	assert.Contains(t, out, "policy execution completed")
	assert.Contains(t, out, "test-policy:123")
	assert.Contains(t, out, `"event":"start"`)
	assert.Contains(t, out, `"event":"finish"`)
	assert.Contains(t, out, `"status":"success"`)
}

func TestLogExecOutcome_FailureLogsStatusFailure(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	logExecCompletion(logger, p, errors.New("simulated failure"), time.Second)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, "failure", entry["status"])
	assert.Equal(t, "finish", entry["event"])
}

func TestLogExecOutcome_BackupPolicyOmitsEventAndStatus(t *testing.T) {
	// agent must never tag a scheduled backup dispatch with event/status --
	// brfs (start) and bwfs (finish) are the sole lifecycle sources for
	// kind=backup, otherwise GET /api/v1/jobs' unfiltered query would see
	// two competing event=start/event=finish lines for the same job_id.
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "backup:nightly:var-www:abcd1234", JobID: "backup:nightly:var-www:abcd1234:1752400000"}

	logExecStart(logger, p)
	logExecCompletion(logger, p, nil, 250*time.Millisecond)

	out := buf.String()
	assert.NotContains(t, out, `"event"`)
	assert.NotContains(t, out, `"status"`)
}

func TestLogExecOutcome_FailureLogsExitCodeWhenAvailable(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	err := exec.Command("false").Run()
	require.Error(t, err, "exec.Command(\"false\") must fail with a real *exec.ExitError")

	logExecCompletion(logger, p, err, time.Second)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	assert.Equal(t, float64(1), entry["exit_code"])
}

func TestLogExecOutcome_FailureWithoutExitErrorOmitsExitCode(t *testing.T) {
	logger, buf := testLoggerWithBuffer()
	p := Policy{ID: "test-policy", JobID: "test-policy:123"}

	logExecCompletion(logger, p, errors.New("simulated failure"), time.Second)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
	_, hasExitCode := entry["exit_code"]
	assert.False(t, hasExitCode, "a non-exec.ExitError failure must not fabricate an exit_code field")
}

func TestRun_LogsStartAndCompletionForEveryDispatchedExec(t *testing.T) {
	testPolicies := []Policy{{ID: "test-policy", Binary: "true", JobID: "test-policy:456", Interval: time.Hour}}

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	logger, buf := testLoggerWithBuffer()

	fr := &fakeRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, logger, cachePath, 10*time.Millisecond, fr.run, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
	require.NoError(t, err)

	out := buf.String()
	assert.Contains(t, out, "policy execution started")
	assert.Contains(t, out, "policy execution completed")
	assert.Contains(t, out, "test-policy:456")
}

func TestRun_CallsOnSuccessAfterASuccessfulExecOnly(t *testing.T) {
	testPolicies := []Policy{
		{ID: "ok-policy", Binary: "true", Interval: time.Hour},
		{ID: "fail-policy", Binary: "false", Interval: time.Hour},
	}

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 20*time.Millisecond, 50*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	var mu sync.Mutex
	var succeeded []string
	onSuccess := func(policyID string) {
		mu.Lock()
		defer mu.Unlock()
		succeeded = append(succeeded, policyID)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec, func() ([]Policy, bool) { return testPolicies, true }, 2, onSuccess, nil, nil)
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, succeeded, "ok-policy")
	assert.NotContains(t, succeeded, "fail-policy", "onSuccess must not fire for a failed exec")
}

func TestRun_NilOnSuccessIsSafe(t *testing.T) {
	testPolicies := []Policy{{ID: "test-policy", Binary: "true", Interval: time.Hour}}
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec, func() ([]Policy, bool) { return testPolicies, true }, 2, nil, nil, nil)
	assert.NoError(t, err, "run must not panic when onSuccess is nil")
}
