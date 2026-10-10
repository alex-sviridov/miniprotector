package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
)

// runningServiceBackoff is the crash-restart backoff for running services: a
// dead listener or control-plane service takes its host's whole gRPC surface
// down, so the first restart is about a second away (policy-derived tasks use
// the 30s defaultBackoffPolicy, which suits a flapping backup job), and a crash
// loop is still bounded at 30s between attempts.
var runningServiceBackoff = backoffPolicy{Base: time.Second, Max: 30 * time.Second}

// newRunningServicesManager returns the storageManager that supervises
// running_service processes, separate from the policy-derived one.
func newRunningServicesManager(logger *slog.Logger) *storageManager {
	mgr := newStorageManager(logger)
	mgr.backoff = runningServiceBackoff
	return mgr
}

// runningServiceTasks turns `running_service=` config lines into supervised
// tasks. IDs are "service:<binary>"; the binary is resolved next to agent.
func runningServiceTasks(services []config.RunningService) []storageTask {
	tasks := make([]storageTask, 0, len(services))
	for _, s := range services {
		tasks = append(tasks, storageTask{
			ID:     "service:" + s.Binary,
			Binary: resolveExecPath(s.Binary),
			Args:   s.Args,
		})
	}
	return tasks
}

// superviseRunningServices starts tasks under mgr once the operating
// certificate (certsDir/client.crt) exists, polling every poll, and returns
// right after starting them (or when ctx is cancelled first). Static services
// are deliberately not driven by run()'s per-tick storage reconcile, which is
// skipped whenever the policy cache is unreadable -- a fresh policy-server must
// start before any policy can be fetched from it.
func superviseRunningServices(ctx context.Context, logger *slog.Logger, mgr *storageManager, certsDir string, tasks []storageTask, poll time.Duration) {
	if len(tasks) == 0 {
		return
	}
	crt := filepath.Join(certsDir, "client.crt")
	logged := false
	for ctx.Err() == nil {
		if _, err := os.Stat(crt); err == nil {
			mgr.reconcile(ctx, nil, tasks)
			logger.Info("running services started", "count", len(tasks))
			return
		}
		if !logged {
			logger.Info("waiting for the operating certificate before starting running services", "path", crt, "count", len(tasks))
			logged = true
		}
		if !sleepOrDone(ctx, poll) {
			return
		}
	}
}
