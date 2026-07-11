package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
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

	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, func() []Policy { return testPolicies }, 2)
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

	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr.run, func() []Policy { return testPolicies }, 2)
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
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, func() []Policy { return testPolicies }, 1)
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
		done <- run(ctx, testLogger(), cachePath, 5*time.Millisecond, blockingRunner, func() []Policy { return testPolicies }, 5)
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
