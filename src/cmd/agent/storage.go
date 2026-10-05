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
	"slices"
	"strconv"
	"sync"
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
// A policy whose config doesn't parse as a filesystem-backend JSON object,
// or whose root is empty, is skipped entirely (contributing neither task)
// with a logged error -- the same fail-safe "skip, don't block the rest"
// direction backupTasks already uses for an unparseable rpo or missing
// backup_window.
func storageTasks(cachedPolicies []cachedPolicy, logger *slog.Logger, bwfsBinary, catalogsyncBinary string) []storageTask {
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
				Args:   []string{cfg.Root, "server", "--port", strconv.Itoa(int(p.Port)), "--policy-id", p.ID},
			},
			storageTask{
				ID:     catalogsyncTaskID(p.Name),
				Binary: catalogsyncBinary,
				Args:   []string{cfg.Root},
			},
		)
	}
	return tasks
}

// defaultStorageStabilityWindow is production's StabilityWindow for every
// storage-policy-derived processSupervisor -- see supervisor.go. A var
// (not const) only so it reads naturally next to storageManager's own
// backoff/stabilityWindow fields below; never mutated outside a test's own
// storageManager instance.
var defaultStorageStabilityWindow = 3 * time.Second

// storageManager holds one processSupervisor per current storage task,
// keyed by task ID, and reconciles that set against agent's latest read of
// policies-cache.json every tick (see reconcile.go's run(), which calls
// reconcile once per loop iteration). It has no knowledge of what any
// given task actually runs -- bwfs, catalogsync, or anything else -- it
// only ever sees (ID, Binary, Args) tuples and supervises whatever it's
// handed.
type storageManager struct {
	logger *slog.Logger

	// backoff/stabilityWindow configure every processSupervisor this
	// manager creates -- defaulted to production values by
	// newStorageManager, overridable directly (same package) by a test
	// that needs faster timing, replacing the old package-level
	// backoffBase/backoffMax/storageStabilityWindow var-mutation pattern.
	backoff         backoffPolicy
	stabilityWindow time.Duration

	mu          sync.Mutex
	supervisors map[string]*processSupervisor
	args        map[string][]string // last-started args, to detect a changed task
}

func newStorageManager(logger *slog.Logger) *storageManager {
	return &storageManager{
		logger:          logger,
		backoff:         defaultBackoffPolicy,
		stabilityWindow: defaultStorageStabilityWindow,
		supervisors:     map[string]*processSupervisor{},
		args:            map[string][]string{},
	}
}

// reconcile starts a supervisor for every newly-appeared task, stops and
// removes one for every task that disappeared or whose Args changed
// (port/path edited on the same policy -- the old process is stopped, a
// fresh one started with the new args), and leaves an unchanged task's
// supervisor running untouched. rs is the same reconcileState run()'s own
// loop already uses -- recordOutcome is mutex-guarded internally, so this
// is safe to call from a processSupervisor's own background goroutines
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
		sup := newProcessSupervisor(supervisorConfig{
			Binary:          t.Binary,
			Args:            t.Args,
			Logger:          m.logger,
			Backoff:         m.backoff,
			StabilityWindow: m.stabilityWindow,
			OnOutcome:       func(err error) { rs.recordOutcome(id, err, time.Now()) },
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
