// supervisor.go: a generic long-running child-process supervisor, shared
// by storage.go's bwfs/catalogsync ensure-running tasks and vector.go's
// bundled Vector process. Both used to be independent, hand-written copies
// of this same spawn/wait/backoff/Stop lifecycle (storageSupervisor,
// vectorSupervisor) that had quietly drifted apart -- see
// docs/superpowers/specs/2026-08-28-agent-reliability-refactor-design.md
// for the two bugs that drift produced in the Vector copy, both fixed here
// by construction since there is now only one implementation for both
// callers.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// supervisorConfig configures one processSupervisor. Binary/Args/Logger/
// Backoff are required; every other field has a documented zero-value
// behavior so a caller only sets what it needs.
type supervisorConfig struct {
	Binary string
	Args   []string
	Logger *slog.Logger
	// Backoff governs the delay before respawning after an unexpected
	// exit. A zero value retries with no delay at all -- callers should
	// always set this explicitly (production code uses defaultBackoffPolicy).
	Backoff backoffPolicy

	// StabilityWindow, when non-zero, delays a successful start's
	// OnOutcome(nil) report until the process has stayed running this long
	// -- storage's crash-loop detection (see storageManager). Zero
	// (Vector's case) means OnOutcome(nil) fires as soon as the process is
	// spawned.
	StabilityWindow time.Duration

	// OnOutcome, when non-nil, is called with nil once a start is
	// considered successful (immediately if StabilityWindow is zero, or
	// after it elapses), and with a non-nil error on an unexpected exit.
	// Never called for a deliberate Stop(). nil (Vector's case) means this
	// supervisor's outcomes aren't tracked in agent-state.json at all.
	OnOutcome func(error)

	// Stdout/Stderr default to os.Stdout/os.Stderr when nil.
	Stdout, Stderr io.Writer
}

// processSupervisor owns the lifecycle of one supervised child process:
// spawn, wait, and on an unexpected exit, respawn after Backoff -- until
// Stop is called. Replaces the formerly-separate storageSupervisor
// (storage.go) and vectorSupervisor (vector.go).
type processSupervisor struct {
	cfg supervisorConfig

	mu           sync.Mutex
	cmd          *exec.Cmd
	shuttingDown bool
	restarting   bool // set by TriggerRestart; only vector.go's caller uses this

	// onSpawnForTest, when non-nil, is called once per spawn attempt --
	// test-only instrumentation, never set in production.
	onSpawnForTest func()

	// loopDone is closed when superviseLoop returns -- a real signal for
	// callers (and tests) to synchronize on instead of guessing at a sleep
	// duration.
	loopDone chan struct{}

	// stopCh is closed exactly once, by Stop(), so a superviseLoop sitting
	// in its backoff wait (no live process to signal -- the supervisor is
	// between crashes) notices Stop() immediately instead of only via ctx
	// (which Stop() doesn't touch) or waiting out the full backoff.
	stopCh   chan struct{}
	stopOnce sync.Once

	// waitCh, when non-nil, is the channel the current backoff wait (if
	// any) is parked on -- TriggerRestart closes it (under mu) to wake the
	// wait immediately, in addition to signalling s.cmd, closing the same
	// "loop is parked with no live process to signal" gap stopCh already
	// closes for Stop(). nil whenever superviseLoop isn't currently
	// sitting in the backoff select (a process is running, or the
	// supervisor hasn't reached its first wait yet); a fresh channel is
	// made each time a wait begins rather than reused, since a stale
	// buffered signal from an earlier TriggerRestart call (already
	// consumed via the direct process signal at the time) must never wake
	// a later, unrelated wait.
	waitCh chan struct{}
}

func newProcessSupervisor(cfg supervisorConfig) *processSupervisor {
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	return &processSupervisor{cfg: cfg, stopCh: make(chan struct{})}
}

// Start launches the supervise loop in its own goroutine and returns
// immediately; the loop itself runs until ctx is done, at which point the
// currently-running process (if any) is also signalled to exit.
func (s *processSupervisor) Start(ctx context.Context) {
	s.loopDone = make(chan struct{})
	go func() {
		defer close(s.loopDone)
		s.superviseLoop(ctx)
	}()
}

// TriggerRestart signals the currently-running process to exit and marks
// the next respawn as deliberate, so the supervise loop skips the
// crash-backoff delay for it. Only vector.go's caller uses this today (a
// fresh operating certificate landing); storage's tasks never call it.
// If the loop is currently sitting in a crash-backoff wait (no live
// process to signal -- see waitCh), that wait is woken immediately too,
// instead of only taking effect once the backoff naturally elapses.
func (s *processSupervisor) TriggerRestart() {
	s.mu.Lock()
	cmd := s.cmd
	s.restarting = true
	waitCh := s.waitCh
	s.waitCh = nil
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	if waitCh != nil {
		close(waitCh)
	}
}

// Stop signals the currently-running process to exit (SIGTERM) and tells
// the supervise loop not to respawn it -- takes effect immediately even if
// the loop is currently sitting out a crash-backoff wait between attempts.
func (s *processSupervisor) Stop() {
	s.mu.Lock()
	s.shuttingDown = true
	cmd := s.cmd
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stopCh) })
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (s *processSupervisor) superviseLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		err := s.spawnAndWait(ctx)

		s.mu.Lock()
		shuttingDown := s.shuttingDown
		deliberate := s.restarting
		s.restarting = false
		s.mu.Unlock()

		if shuttingDown || ctx.Err() != nil {
			return
		}
		if deliberate {
			failures = 0
			continue
		}

		failures++
		s.cfg.Logger.Error("supervised process exited unexpectedly, restarting with backoff", "binary", s.cfg.Binary, "failures", failures, "error", err)
		if s.cfg.OnOutcome != nil {
			s.cfg.OnOutcome(err)
		}

		s.mu.Lock()
		if s.restarting {
			// TriggerRestart landed in the window between the top-of-loop
			// clear above and here -- before this wait's waitCh even
			// existed for it to close, so it found waitCh == nil and could
			// only set the flag. Consume it here instead of walking into
			// the backoff select and parking for the full delay: without
			// this, the flag would still eventually be consumed (at the
			// top of the loop or in the select's own checks below), but
			// only after waiting out the entire backoff first.
			s.restarting = false
			shuttingDownNow := s.shuttingDown
			s.mu.Unlock()
			if shuttingDownNow {
				return
			}
			failures = 0
			continue
		}
		waitCh := make(chan struct{})
		s.waitCh = waitCh
		s.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-waitCh:
			// TriggerRestart woke this wait -- consume the deliberate flag
			// here (not at the top of the loop) so it applies to skipping
			// THIS backoff only, and never leaks into the outcome
			// classification of the generation we're about to respawn.
			// Re-check shuttingDown too: Stop() and TriggerRestart() can
			// race here, and select doesn't prefer stopCh over waitCh just
			// because both became ready -- Stop() must still win.
			s.mu.Lock()
			s.waitCh = nil
			s.restarting = false
			shuttingDownNow := s.shuttingDown
			s.mu.Unlock()
			if shuttingDownNow {
				return
			}
			failures = 0
			continue
		case <-time.After(s.cfg.Backoff.next(failures)):
		}

		s.mu.Lock()
		s.waitCh = nil
		if s.restarting {
			// TriggerRestart raced the timer and lost the select -- it set
			// the flag but its close(waitCh) either never happened (it
			// read waitCh == nil under this same lock, after this branch
			// already cleared it) or happened too late to be observed by
			// the select above. Consume it here instead of letting it
			// leak into the next generation's own outcome classification
			// at the top of the loop.
			s.restarting = false
			failures = 0
		}
		s.mu.Unlock()
	}
}

// spawnAndWait starts the process and blocks until it exits. A successful
// start's OnOutcome(nil) report is delayed until StabilityWindow has
// elapsed (StabilityWindow == 0 means "immediately") -- a process that
// crashes faster than that never reaches OnOutcome(nil), so a genuine
// crash loop's persisted failure count (reset to 0 by any nil outcome)
// climbs instead of bouncing back to "1 failure" on every restart attempt;
// the crash itself is still reported via superviseLoop's own
// OnOutcome(err) call for the exit.
//
// cmd.Start() runs under s.mu so s.cmd is updated atomically with the
// process actually starting, and the shuttingDown recheck immediately
// after covers the case where Stop() raced ahead of this spawn: it ran
// (and saw s.cmd == nil, so sent no signal) before cmd.Start() above
// completed. Without this check the process just-started would run
// forever unsignalled.
func (s *processSupervisor) spawnAndWait(ctx context.Context) error {
	cmd := exec.Command(s.cfg.Binary, s.cfg.Args...)
	cmd.Stdout = s.cfg.Stdout
	cmd.Stderr = s.cfg.Stderr

	s.mu.Lock()
	err := cmd.Start()
	shuttingDown := s.shuttingDown
	if err == nil {
		s.cmd = cmd
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start %s: %w", s.cfg.Binary, err)
	}
	if shuttingDown {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	if s.onSpawnForTest != nil {
		s.onSpawnForTest()
	}

	waitDone := make(chan struct{})
	defer close(waitDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case <-waitDone:
		}
	}()

	// Read StabilityWindow synchronously here, on the same goroutine that
	// will go on to close waitDone, rather than inside the goroutine below
	// -- avoids a race for a test that mutates cfg.StabilityWindow after
	// construction (none currently do, but this mirrors the original
	// storageSupervisor's own reasoning for this exact structure).
	stabilityWindow := s.cfg.StabilityWindow
	go func() {
		timer := time.NewTimer(stabilityWindow)
		defer timer.Stop()
		select {
		case <-timer.C:
			if s.cfg.OnOutcome != nil {
				s.cfg.OnOutcome(nil)
			}
		case <-waitDone:
			// Process exited before the stability window elapsed -- the
			// crash is reported by superviseLoop's own OnOutcome(err) call
			// instead, not here.
		}
	}()

	return cmd.Wait()
}
