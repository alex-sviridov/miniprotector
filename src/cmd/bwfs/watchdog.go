package main

import (
	"context"
	"time"
)

// watchStaleJobs periodically fails any backup job that has gone silent for
// longer than timeout — the bound on how long a crashed brfs or a dead
// connection can leave a job ambiguously in_progress. Soft-fail: the job is
// marked failed in the database the instant the timeout fires; the stream
// goroutines that were serving it are left to end on their own (the
// FailedPrecondition check in ProcessBackupStream's receive loop rejects any
// further message they might still deliver).
func watchStaleJobs(ctx context.Context, server *backupServer, timeout time.Duration) {
	pollInterval := timeout / 6
	if pollInterval < 5*time.Second {
		pollInterval = 5 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, jobID := range server.liveness.StaleJobs(timeout) {
				if _, err := server.store.FinalizeBackupJob(jobID, false); err != nil {
					server.logger.Error("failed to finalize stale job", "job_id", jobID, "error", err)
					continue
				}
				server.liveness.Complete(jobID)
				server.logger.Warn("backup job timed out and was marked failed", "job_id", jobID, "timeout", timeout)
			}
		}
	}
}
