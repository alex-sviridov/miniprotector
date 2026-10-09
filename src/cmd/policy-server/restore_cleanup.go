// restore_cleanup.go runs policy-server's own background sweep for
// restore-type policies: on a fixed tick, ask api-server whether each
// cached restore policy's one-shot job has finished, and delete the
// policy once it has, past a grace period. Mirrors checkin.go's
// runCheckinCleanup shape. See
// docs/superpowers/specs/2026-08-23-restore-policy-lifecycle-design.md.
package main

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// runRestoreCleanup runs sweepRestorePolicies every interval until ctx is
// done.
func (s *policyServerServer) runRestoreCleanup(ctx context.Context, jobStatus pb.JobStatusServiceClient, interval, gracePeriod time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepRestorePolicies(ctx, jobStatus, gracePeriod, logger)
		}
	}
}

// sweepRestorePolicies checks every cached "restore"-type policy against
// api-server's job status and deletes the ones whose job finished more
// than gracePeriod ago. A query failure for one policy is logged and
// skipped -- retried on the next tick, never fatal to the sweep as a
// whole (same best-effort direction as DeletePolicy's own check-in
// cleanup, write.go).
//
// A policy with no job_id predates this feature (it was written to disk
// before RestorePolicy carried a JobID) and can never resolve through
// GetPolicyJobStatus -- api-server rejects an empty job_id outright. Such
// policies are skipped before the RPC is attempted, logged once per tick
// at Info level rather than Error: this is an expected steady state for a
// pre-migration policy, not an operational failure, and there's no
// per-tick "log once" state in this sweep loop to suppress the repeat
// line, so Info is the tradeoff that avoids paging anyone on it.
func (s *policyServerServer) sweepRestorePolicies(ctx context.Context, jobStatus pb.JobStatusServiceClient, gracePeriod time.Duration, logger *slog.Logger) {
	for _, p := range s.cache.Policies() {
		rp, ok := p.(*RestorePolicy)
		if !ok {
			continue
		}

		if rp.JobID == "" {
			logger.Info("restore policy has no job_id (predates this feature), skipping cleanup sweep -- delete manually if no longer needed", "policy", rp.Meta().ID)
			continue
		}

		resp, err := jobStatus.GetPolicyJobStatus(ctx, &pb.GetPolicyJobStatusRequest{
			JobId:           rp.JobID,
			PolicyCreatedAt: timestamppb.New(rp.Meta().CreatedAt),
		})
		if err != nil {
			logger.Error("restore cleanup: GetPolicyJobStatus failed", "policy", rp.Meta().ID, "job_id", rp.JobID, "error", err)
			continue
		}
		if !resp.GetFinished() {
			continue
		}
		if time.Since(resp.GetFinishedAt().AsTime()) < gracePeriod {
			continue
		}

		if _, err := s.DeletePolicy(ctx, &pb.DeletePolicyRequest{Id: rp.Meta().ID}); err != nil {
			logger.Error("restore cleanup: DeletePolicy failed", "policy", rp.Meta().ID, "job_id", rp.JobID, "error", err)
			continue
		}
		logger.Info("restore policy deleted as job found", "policy", rp.Meta().ID, "job_id", rp.JobID, "event", "deleted")
	}
}
