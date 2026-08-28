package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeFakeScript(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-process.sh")
	require.NoError(t, os.WriteFile(script, []byte(content), 0o755))
	return script
}

const longRunningScript = "#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"

func TestProcessSupervisor_StartsAndStopsCleanlyOnContextCancel(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	var spawns int64
	sup := newProcessSupervisor(supervisorConfig{Binary: script, Logger: testLogger(), Backoff: defaultBackoffPolicy})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	sup.Start(ctx)

	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt64(&spawns))
	cancel()

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context cancellation")
	}
	assert.EqualValues(t, 1, atomic.LoadInt64(&spawns), "no respawn should happen once ctx is cancelled")
}

func TestProcessSupervisor_RestartsOnUnexpectedExitAndRecordsFailure(t *testing.T) {
	script := writeFakeScript(t, "#!/bin/sh\nexit 1\n")

	var spawns int64
	var mu sync.Mutex
	var outcomes []error
	sup := newProcessSupervisor(supervisorConfig{
		Binary:  script,
		Logger:  testLogger(),
		Backoff: backoffPolicy{Base: 10 * time.Millisecond, Max: 30 * time.Millisecond},
		OnOutcome: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			outcomes = append(outcomes, err)
		},
	})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Start(ctx)

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context timeout")
	}

	assert.GreaterOrEqual(t, atomic.LoadInt64(&spawns), int64(2), "a persistently crashing process must be respawned more than once")

	mu.Lock()
	defer mu.Unlock()
	var sawFailure bool
	for _, err := range outcomes {
		if err != nil {
			sawFailure = true
		}
	}
	assert.True(t, sawFailure, "at least one crash must be recorded as a failure")
}

func TestProcessSupervisor_ZeroStabilityWindowReportsSuccessImmediately(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	outcomes := make(chan error, 1)
	sup := newProcessSupervisor(supervisorConfig{
		Binary:    script,
		Logger:    testLogger(),
		Backoff:   defaultBackoffPolicy,
		OnOutcome: func(err error) { outcomes <- err },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	select {
	case err := <-outcomes:
		assert.NoError(t, err, "a zero StabilityWindow must report success as soon as the process spawns")
	case <-time.After(time.Second):
		t.Fatal("onOutcome was never called")
	}
	sup.Stop()
}

func TestProcessSupervisor_SuccessfulStartRecordsSuccessAfterStabilityWindow(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	outcomes := make(chan error, 1)
	sup := newProcessSupervisor(supervisorConfig{
		Binary:          script,
		Logger:          testLogger(),
		Backoff:         defaultBackoffPolicy,
		StabilityWindow: 20 * time.Millisecond,
		OnOutcome:       func(err error) { outcomes <- err },
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	select {
	case err := <-outcomes:
		t.Fatalf("onOutcome fired before the stability window elapsed: %v", err)
	case <-time.After(5 * time.Millisecond):
	}

	select {
	case err := <-outcomes:
		assert.NoError(t, err, "a start that stays up past the stability window must record success")
	case <-time.After(time.Second):
		t.Fatal("onOutcome was never called after the stability window elapsed")
	}
	sup.Stop()
}

func TestProcessSupervisor_CrashBeforeStabilityWindowNeverRecordsSuccess(t *testing.T) {
	// Exits almost immediately -- well before the 200ms stability window.
	script := writeFakeScript(t, "#!/bin/sh\nsleep 0.01\nexit 1\n")

	var mu sync.Mutex
	var outcomes []error
	sup := newProcessSupervisor(supervisorConfig{
		Binary:          script,
		Logger:          testLogger(),
		Backoff:         backoffPolicy{Base: 10 * time.Millisecond, Max: 30 * time.Millisecond},
		StabilityWindow: 200 * time.Millisecond,
		OnOutcome: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			outcomes = append(outcomes, err)
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Start(ctx)

	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context timeout")
	}

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, outcomes, "a persistently crashing process must record at least one outcome")
	for _, err := range outcomes {
		assert.Error(t, err, "a process that crashes before the stability window elapses must never be recorded as a success")
	}
}

func TestProcessSupervisor_DeliberateStopDoesNotRecordFailure(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	var mu sync.Mutex
	var outcomes []error
	sup := newProcessSupervisor(supervisorConfig{
		Binary:          script,
		Logger:          testLogger(),
		Backoff:         defaultBackoffPolicy,
		StabilityWindow: 20 * time.Millisecond,
		OnOutcome: func(err error) {
			mu.Lock()
			defer mu.Unlock()
			outcomes = append(outcomes, err)
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	time.Sleep(100 * time.Millisecond) // let it start and clear the stability window, recording one nil outcome

	sup.Stop()
	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after Stop()")
	}

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, outcomes, "the process should have stayed up past the stability window and recorded a success before Stop()")
	for _, err := range outcomes {
		assert.NoError(t, err, "a deliberate Stop() must never record a failure outcome")
	}
}

// TestProcessSupervisor_StopDuringBackoffWaitReturnsPromptly is a
// regression test for bug #1 found comparing storageSupervisor and
// vectorSupervisor: vectorSupervisor's Stop() didn't take effect until a
// pending crash-backoff wait elapsed (up to backoffMax, 10 minutes in
// production) because it had no dedicated stop signal for that wait --
// only storageSupervisor did (stopCh). processSupervisor has it
// unconditionally now, so both former callers get the fix.
func TestProcessSupervisor_StopDuringBackoffWaitReturnsPromptly(t *testing.T) {
	script := writeFakeScript(t, "#!/bin/sh\nexit 1\n")

	failed := make(chan struct{}, 1)
	sup := newProcessSupervisor(supervisorConfig{
		Binary:  script,
		Logger:  testLogger(),
		Backoff: backoffPolicy{Base: 10 * time.Second, Max: 10 * time.Second}, // large -- must not be waited out
		OnOutcome: func(err error) {
			if err != nil {
				select {
				case failed <- struct{}{}:
				default:
				}
			}
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)

	// Wait until the first crash has been recorded as a failure -- by then
	// superviseLoop has already passed its shuttingDown check for this
	// iteration and is heading into (or already sitting in) the 10s
	// backoff select, exactly the state this fix targets.
	select {
	case <-failed:
	case <-time.After(2 * time.Second):
		t.Fatal("first crash was never recorded as a failure")
	}

	start := time.Now()
	sup.Stop()

	select {
	case <-sup.loopDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Stop() during backoff wait did not stop the supervisor promptly")
	}
	assert.Less(t, time.Since(start), 500*time.Millisecond, "Stop() must interrupt the backoff wait, not wait out the full 10s backoff")
}

// TestProcessSupervisor_StopRacingSpawnDoesNotLeakProcess is a regression
// test for bug #2: vectorSupervisor was missing the post-cmd.Start()
// shuttingDown recheck storageSupervisor already had. Without it, a Stop()
// landing in the narrow window between another goroutine's cmd.Start()
// returning and s.cmd being assigned would see s.cmd == nil, signal
// nothing, and leave the just-spawned process running forever unsignalled.
// This test can't hit that exact nanosecond window deterministically, but
// it proves the mechanism that closes it: spawnAndWait's own recheck fires
// whenever shuttingDown is already true by the time cmd.Start() returns,
// regardless of exactly when Stop() ran relative to it.
func TestProcessSupervisor_StopRacingSpawnDoesNotLeakProcess(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	sup := newProcessSupervisor(supervisorConfig{Binary: script, Logger: testLogger(), Backoff: defaultBackoffPolicy})

	// Simulate Stop() having already run (shuttingDown = true, s.cmd still
	// nil -- exactly the state a real race would leave behind) before
	// spawnAndWait's own cmd.Start() call happens.
	sup.mu.Lock()
	sup.shuttingDown = true
	sup.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- sup.spawnAndWait(context.Background()) }()

	select {
	case <-done:
		// cmd.Wait() returned -- the process was signalled and exited,
		// proving the recheck caught the pre-set shuttingDown flag instead
		// of leaving the process running forever.
	case <-time.After(2 * time.Second):
		t.Fatal("spawnAndWait never returned -- the just-spawned process was left running unsignalled")
	}
}

func TestProcessSupervisor_TriggerRestartDoesNotApplyBackoff(t *testing.T) {
	script := writeFakeScript(t, longRunningScript)

	// A large backoff window -- if TriggerRestart incorrectly went through
	// the crash-backoff path, the respawn would not happen within this
	// test's short assertion window.
	var spawns int64
	sup := newProcessSupervisor(supervisorConfig{
		Binary:  script,
		Logger:  testLogger(),
		Backoff: backoffPolicy{Base: 10 * time.Second, Max: 10 * time.Second},
	})
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt64(&spawns))

	sup.TriggerRestart()
	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&spawns) >= 2
	}, time.Second, 20*time.Millisecond, "TriggerRestart must respawn promptly, not wait out the crash-backoff window")
}
