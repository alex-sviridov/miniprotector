// storage.go derives agent's "ensure this bwfs server is running" tasks
// from cached "storage"-type policies. Like backupTasks (backup.go), it
// relies on policy-server's server-side scoping: ClientFilters.Matches
// applies in GetPolicies before a policy reaches policies-cache.json,
// so anything with Type == "storage" in the cache is already scoped to
// this node. A later task adds process supervision on top of this task
// derivation. See docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// storageTask is one bwfs server this node should be running, derived from
// a cached "storage" policy.
type storageTask struct {
	ID   string
	Args []string
}

// storageTaskID is the stable identifier for one storage policy's task in
// agent-state.json -- mirrors backup.go's "backup:" prefix convention.
// Like backupTaskID, this assumes policy names are effectively unique
// (the same pre-existing assumption backup tasks already make; not solved
// fresh here).
func storageTaskID(policyName string) string {
	return fmt.Sprintf("storage:%s", policyName)
}

// storageConfig is the subset of a storage policy's opaque config this
// agent understands -- today, exactly one backend.
type storageConfig struct {
	Backend string `json:"backend"`
	Root    string `json:"root"`
}

// storageTasks derives one ensure-running task per cached "storage" policy,
// valid at the instant it's called -- callers that need to notice
// policies-cache.json changing over time (agent serve's reconcile loop)
// must call this fresh every tick rather than caching its result once.
//
// ok=false mirrors backupTasks's contract: it means this tick's read of
// policiesCachePath failed, and callers must never treat that as "there are
// zero storage tasks."
//
// A policy whose config doesn't parse as a filesystem-backend JSON object,
// or whose root is empty, is skipped with a logged error -- the same
// fail-safe "skip, don't block the rest" direction backupTasks already uses
// for an unparseable rpo or missing backup_window.
func storageTasks(policiesCachePath string, logger *slog.Logger) ([]storageTask, bool) {
	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []storageTask
	for _, p := range cachedPolicies {
		if p.Type != "storage" {
			continue
		}
		var cfg storageConfig
		if err := json.Unmarshal([]byte(p.Config), &cfg); err != nil || cfg.Backend != "filesystem" || cfg.Root == "" {
			logger.Error("storage policy has unsupported or unparseable config, skipping", "policy", p.Name)
			continue
		}
		tasks = append(tasks, storageTask{
			ID:   storageTaskID(p.Name),
			Args: []string{cfg.Root, "server", "--port", strconv.Itoa(int(p.Port))},
		})
	}
	return tasks, true
}

// storageSupervisor owns the lifecycle of one supervised bwfs server
// process: a long-running child, not a due/execute/complete Policy, so it
// gets its own small supervise loop -- modeled directly on vector.go's
// vectorSupervisor. Two differences: no TriggerRestart (bwfs already
// hot-reloads its identity cert per-handshake via mtls.LoadServerCredentials,
// unlike Vector, so a cert-rotation-triggered restart would only add
// disruption with no benefit), and an onOutcome callback so a supervised
// bwfs's state reaches agent-state.json via reconcileState.recordOutcome
// (see storageManager in this same file).
type storageSupervisor struct {
	binary string
	args   []string
	logger *slog.Logger

	mu           sync.Mutex
	cmd          *exec.Cmd
	shuttingDown bool

	// onSpawnForTest, when non-nil, is called once per spawn attempt --
	// test-only instrumentation, never set in production.
	onSpawnForTest func()

	// onOutcome is called with nil immediately after every successful
	// process start (this supervisor's notion of "success" -- a server
	// isn't expected to exit on its own), and with a non-nil error only
	// when the process exits unexpectedly. Never called for a deliberate
	// Stop().
	onOutcome func(err error)

	// loopDone is closed when superviseLoop returns, giving callers (and
	// tests) a real signal to synchronize on instead of guessing at a
	// sleep duration.
	loopDone chan struct{}
}

func newStorageSupervisor(binary string, args []string, logger *slog.Logger, onOutcome func(err error)) *storageSupervisor {
	return &storageSupervisor{binary: binary, args: args, logger: logger, onOutcome: onOutcome}
}

// Start launches the supervise loop in its own goroutine and returns
// immediately; the loop itself runs until ctx is done, at which point the
// currently-running bwfs process (if any) is also signalled to exit.
func (s *storageSupervisor) Start(ctx context.Context) {
	s.loopDone = make(chan struct{})
	go func() {
		defer close(s.loopDone)
		s.superviseLoop(ctx)
	}()
}

// Stop signals the currently-running bwfs process to exit (SIGTERM -- a
// graceful drain once bwfs's own signal.NotifyContext fix lands, see Task 11)
// and tells the supervise loop not to respawn it.
func (s *storageSupervisor) Stop() {
	s.mu.Lock()
	s.shuttingDown = true
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
}

func (s *storageSupervisor) superviseLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		err := s.spawnAndWait(ctx)

		s.mu.Lock()
		shuttingDown := s.shuttingDown
		s.mu.Unlock()

		if shuttingDown || ctx.Err() != nil {
			return
		}

		failures++
		s.logger.Error("bwfs exited unexpectedly, restarting with backoff", "failures", failures, "error", err)
		if s.onOutcome != nil {
			s.onOutcome(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff(failures)):
		}
	}
}

// spawnAndWait starts bwfs and blocks until it exits, calling onOutcome(nil)
// immediately on a successful start. If ctx is cancelled while bwfs is still
// running, it is sent SIGTERM -- see vectorSupervisor.spawnAndWait in
// vector.go for the detailed reasoning behind starting cmd.Start() under
// the mutex and handling ctx cancellation this way; identical here.
func (s *storageSupervisor) spawnAndWait(ctx context.Context) error {
	cmd := exec.Command(s.binary, s.args...)

	s.mu.Lock()
	err := cmd.Start()
	if err == nil {
		s.cmd = cmd
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start bwfs: %w", err)
	}
	if s.onSpawnForTest != nil {
		s.onSpawnForTest()
	}
	if s.onOutcome != nil {
		s.onOutcome(nil)
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

	return cmd.Wait()
}
