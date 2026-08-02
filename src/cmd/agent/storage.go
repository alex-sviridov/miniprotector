// storage.go derives agent's ensure-running tasks from cached "storage"-type
// policies -- one task to keep a bwfs server running, and one independent
// task to keep a catalogsync process running against the same root, with no
// coordination between the two (see docs/superpowers/specs/
// 2026-07-31-agent-catalogsync-supervision-design.md for why that's safe:
// catalogsync's read-only sqlite open fails cleanly, not corruptingly, if it
// ever starts before bwfs has created the database, and just gets
// crash-restarted like any other transient exec failure). Like backupTasks
// (backup.go), it relies on policy-server's server-side scoping:
// ClientFilters.Matches applies in GetPolicies before a policy reaches
// policies-cache.json, so anything with Type == "storage" in the cache is
// already scoped to this node.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// storageTask is one long-running process this node should be running,
// derived from a cached "storage" policy -- either the bwfs server itself,
// or the catalogsync process replicating its catalog, treated as two
// independent entries with no relationship to each other beyond sharing an
// ID prefix.
type storageTask struct {
	ID     string
	Binary string
	Args   []string
}

// storageTaskID is the stable identifier for one storage policy's bwfs task
// in agent-state.json -- mirrors backup.go's "backup:" prefix convention.
// Like backupTaskID, this assumes policy names are effectively unique
// (the same pre-existing assumption backup tasks already make; not solved
// fresh here).
func storageTaskID(policyName string) string {
	return fmt.Sprintf("storage:%s", policyName)
}

// catalogsyncTaskID mirrors storageTaskID's "storage:<name>" convention with
// a suffix, so the two tasks derived from one storage policy are
// related-but-distinct IDs in agent-state.json / list-policies -- prune and
// storageManager.reconcile treat them as two ordinary, independent entries.
func catalogsyncTaskID(policyName string) string {
	return storageTaskID(policyName) + ":catalogsync"
}

// storageConfig is the subset of a storage policy's opaque config this
// agent understands -- today, exactly one backend.
type storageConfig struct {
	Backend string `json:"backend"`
	Root    string `json:"root"`
}

// storageTasks derives two ensure-running tasks per cached "storage" policy
// -- one for bwfs, one for catalogsync -- valid at the instant it's called;
// callers that need to notice policies-cache.json changing over time
// (agent serve's reconcile loop) must call this fresh every tick rather than
// caching its result once.
//
// ok=false mirrors backupTasks's contract: it means this tick's read of
// policiesCachePath failed, and callers must never treat that as "there are
// zero storage tasks."
//
// A policy whose config doesn't parse as a filesystem-backend JSON object,
// or whose root is empty, is skipped entirely (contributing neither task)
// with a logged error -- the same fail-safe "skip, don't block the rest"
// direction backupTasks already uses for an unparseable rpo or missing
// backup_window.
func storageTasks(policiesCachePath string, logger *slog.Logger, bwfsBinary, catalogsyncBinary string) ([]storageTask, bool) {
	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []storageTask
	for _, p := range cachedPolicies {
		if p.Type != "storage" {
			continue
		}
		if p.disabled(time.Now()) {
			continue
		}
		var cfg storageConfig
		if err := json.Unmarshal([]byte(p.Config), &cfg); err != nil || cfg.Backend != "filesystem" || cfg.Root == "" {
			logger.Error("storage policy has unsupported or unparseable config, skipping", "policy", p.Name)
			continue
		}
		tasks = append(tasks,
			storageTask{
				ID:     storageTaskID(p.Name),
				Binary: bwfsBinary,
				Args:   []string{cfg.Root, "server", "--port", strconv.Itoa(int(p.Port))},
			},
			storageTask{
				ID:     catalogsyncTaskID(p.Name),
				Binary: catalogsyncBinary,
				Args:   []string{cfg.Root},
			},
		)
	}
	return tasks, true
}

// storageStabilityWindow is how long a spawned supervised process must stay
// running before its start is reported as a success. A process that crashes
// faster than this never resets the persisted failure count, so a
// genuine crash loop's failure count climbs instead of bouncing back to
// "1 failure" on every restart attempt. A var (not const) so tests can
// shrink it instead of waiting out a real multi-second window.
var storageStabilityWindow = 3 * time.Second

// storageSupervisor owns the lifecycle of one supervised process (bwfs
// server or catalogsync): a long-running child, not a due/execute/complete
// Policy, so it gets its own small supervise loop -- modeled directly on
// vector.go's vectorSupervisor. Two differences: no TriggerRestart (unlike
// Vector, both bwfs and catalogsync already hot-reload their mTLS identity
// cert on every handshake (bwfs via mtls.LoadServerCredentials's
// GetCertificate, catalogsync via mtls.LoadClientCredentials's
// GetClientCertificate), so a cert-rotation-triggered restart would only add
// disruption with no benefit), and an onOutcome callback so the supervised
// process's state reaches agent-state.json via reconcileState.recordOutcome
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

	// onOutcome is called with nil once a successful process start has
	// stayed running past storageStabilityWindow (this supervisor's notion
	// of "success" -- a server isn't expected to exit on its own), and with
	// a non-nil error only when the process exits unexpectedly. Never
	// called for a deliberate Stop().
	onOutcome func(err error)

	// loopDone is closed when superviseLoop returns, giving callers (and
	// tests) a real signal to synchronize on instead of guessing at a
	// sleep duration.
	loopDone chan struct{}

	// stopCh is closed exactly once, by Stop(), so a superviseLoop sitting
	// in its backoff wait (no live process to signal -- the supervisor is
	// between crashes) notices Stop() immediately instead of only via ctx
	// (which Stop() doesn't touch) or waiting out the full backoff.
	stopCh   chan struct{}
	stopOnce sync.Once
}

func newStorageSupervisor(binary string, args []string, logger *slog.Logger, onOutcome func(err error)) *storageSupervisor {
	return &storageSupervisor{binary: binary, args: args, logger: logger, onOutcome: onOutcome, stopCh: make(chan struct{})}
}

// Start launches the supervise loop in its own goroutine and returns
// immediately; the loop itself runs until ctx is done, at which point the
// currently-running process (if any) is also signalled to exit.
func (s *storageSupervisor) Start(ctx context.Context) {
	s.loopDone = make(chan struct{})
	go func() {
		defer close(s.loopDone)
		s.superviseLoop(ctx)
	}()
}

// Stop signals the currently-running process to exit (SIGTERM -- a
// graceful drain once bwfs's own signal.NotifyContext fix lands, see Task 11)
// and tells the supervise loop not to respawn it.
func (s *storageSupervisor) Stop() {
	s.mu.Lock()
	s.shuttingDown = true
	cmd := s.cmd
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stopCh) })
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
		s.logger.Error("supervised process exited unexpectedly, restarting with backoff", "binary", s.binary, "failures", failures, "error", err)
		if s.onOutcome != nil {
			s.onOutcome(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-time.After(backoff(failures)):
		}
	}
}

// spawnAndWait starts the process and blocks until it exits, calling onOutcome(nil)
// only once the process has stayed running past storageStabilityWindow --
// not immediately on a successful start. A process that crashes faster than
// that window never reaches onOutcome(nil), so a genuine crash loop's
// persisted failure count (reset to 0 by any nil outcome, see
// reconcileState.recordOutcome) climbs instead of bouncing back to "1
// failure" on every restart attempt; the crash itself is still reported via
// superviseLoop's own onOutcome(err) call for the exit. If ctx is cancelled
// while the process is still running, it is sent SIGTERM -- see
// vectorSupervisor.spawnAndWait in vector.go for the detailed reasoning
// behind starting cmd.Start() under the mutex and handling ctx cancellation
// this way; identical here.
func (s *storageSupervisor) spawnAndWait(ctx context.Context) error {
	cmd := exec.Command(s.binary, s.args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	s.mu.Lock()
	err := cmd.Start()
	shuttingDown := s.shuttingDown
	if err == nil {
		s.cmd = cmd
	}
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("start %s: %w", s.binary, err)
	}
	if shuttingDown {
		// Stop() raced ahead of this spawn: it ran (and saw s.cmd == nil,
		// so sent no signal) before cmd.Start() above completed. Without
		// this check the process just-started would run forever unsignalled
		// -- superviseLoop only rechecks shuttingDown after spawnAndWait
		// returns, which blocks on cmd.Wait() until the process exits.
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

	// Read storageStabilityWindow synchronously here, on the same goroutine
	// that will go on to close waitDone (via cmd.Wait() below returning and
	// the deferred close above), rather than inside the goroutine below --
	// tests shrink this var and restore it only after synchronizing on
	// loopDone, so a read inside the goroutine (which isn't guaranteed to
	// have run by the time spawnAndWait returns) would race with that
	// restore.
	stabilityWindow := storageStabilityWindow
	go func() {
		timer := time.NewTimer(stabilityWindow)
		defer timer.Stop()
		select {
		case <-timer.C:
			if s.onOutcome != nil {
				s.onOutcome(nil)
			}
		case <-waitDone:
			// Process exited before the stability window elapsed -- the
			// crash is reported by superviseLoop's own onOutcome(err) call
			// instead, not here.
		}
	}()

	return cmd.Wait()
}

// storageManager holds one storageSupervisor per current storage task,
// keyed by task ID, and reconciles that set against agent's latest read of
// policies-cache.json every tick (see reconcile.go's run(), which calls
// reconcile once per loop iteration). It has no knowledge of what any given
// task actually runs -- bwfs, catalogsync, or anything else -- it only ever
// sees (ID, Binary, Args) tuples and supervises whatever it's handed.
type storageManager struct {
	logger *slog.Logger

	mu          sync.Mutex
	supervisors map[string]*storageSupervisor
	args        map[string][]string // last-started args, to detect a changed task
}

func newStorageManager(logger *slog.Logger) *storageManager {
	return &storageManager{
		logger:      logger,
		supervisors: map[string]*storageSupervisor{},
		args:        map[string][]string{},
	}
}

// reconcile starts a supervisor for every newly-appeared task, stops and
// removes one for every task that disappeared or whose Args changed
// (port/path edited on the same policy -- the old process is stopped, a
// fresh one started with the new args), and leaves an unchanged task's
// supervisor running untouched. rs is the same reconcileState run()'s own
// loop already uses -- recordOutcome is mutex-guarded internally, so this
// is safe to call from storageSupervisor's own background goroutines
// concurrently with run()'s main loop, exactly like backup-task goroutines
// already do.
func (m *storageManager) reconcile(ctx context.Context, rs *reconcileState, tasks []storageTask) {
	m.mu.Lock()
	defer m.mu.Unlock()

	wanted := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		wanted[t.ID] = t.Args
	}

	for id, sup := range m.supervisors {
		newArgs, stillWanted := wanted[id]
		if !stillWanted || !slices.Equal(newArgs, m.args[id]) {
			sup.Stop()
			delete(m.supervisors, id)
			delete(m.args, id)
		}
	}

	for _, t := range tasks {
		if _, exists := m.supervisors[t.ID]; exists {
			continue
		}
		id := t.ID
		sup := newStorageSupervisor(t.Binary, t.Args, m.logger, func(err error) {
			rs.recordOutcome(id, err, time.Now())
		})
		sup.Start(ctx)
		m.supervisors[t.ID] = sup
		m.args[t.ID] = t.Args
	}
}

// StopAll stops every currently-supervised process -- called on agent
// shutdown so none are orphaned.
func (m *storageManager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, sup := range m.supervisors {
		sup.Stop()
	}
}
