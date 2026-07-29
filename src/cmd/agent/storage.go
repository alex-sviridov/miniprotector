// storage.go derives agent's "ensure this bwfs server is running" tasks
// from cached "storage"-type policies -- see storage_supervisor.go... no
// wait, kept in this same file: storageSupervisor/storageManager (Tasks 4-5
// of docs/superpowers/plans/2026-07-28-agent-storage-supervision.md) also
// live here. Unlike backupTasks (backup.go), there is no per-node targeting
// check: policy-server's GetPolicies already applied ClientFilters.Matches
// before a policy ever reached policies-cache.json, so anything with
// Type == "storage" in the cache is already scoped to this node. See
// docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
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
