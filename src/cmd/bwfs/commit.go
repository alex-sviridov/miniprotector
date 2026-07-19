package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sort"
	"strings"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/storage"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BackupCommit is brfs's final call for a job, after all of its streams
// have closed. bwfs independently recomputes the same hash from what it
// actually recorded in file_versions and only marks the job success if the
// two agree — the streams having closed is not, by itself, proof that
// everything brfs intended to send actually arrived.
func (server *backupServer) BackupCommit(ctx context.Context, req *pb.BackupCommitRequest) (*pb.BackupCommitResponse, error) {
	jobID, err := jobid.FromIncoming(ctx)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "job-id metadata required: %v", err)
	}

	sourceHost, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Unauthenticated, "resolve peer identity: %v", err)
	}

	job, err := server.store.GetBackupJob(jobID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "unknown job %s: %v", jobID, err)
	}
	if job.SourceHost != sourceHost {
		return nil, status.Errorf(codes.PermissionDenied, "job %s does not belong to host %s", jobID, sourceHost)
	}

	if job.Status != storage.JobStatusInProgress {
		// Already decided — by a prior commit call whose response was lost,
		// or by the stall watchdog racing ahead. Return the ground truth
		// instead of re-hashing or erroring, so retries are idempotent.
		server.logger.Info("BackupCommit for already-finalized job", "job_id", jobID, "event", "finish", "status", job.Status)
		return &pb.BackupCommitResponse{Success: job.Status == storage.JobStatusSuccess}, nil
	}

	objectIDs, err := server.store.FileVersionsForJob(jobID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list file versions for job: %v", err)
	}
	sort.Strings(objectIDs)
	computed := sha256.Sum256([]byte(strings.Join(objectIDs, "\n")))
	matched := bytes.Equal(computed[:], req.FileListHash)
	commitStatus := storage.JobStatusFailure
	if matched {
		commitStatus = storage.JobStatusSuccess
	}

	if _, err := server.store.FinalizeBackupJob(jobID, matched); err != nil {
		return nil, status.Errorf(codes.Internal, "finalize backup job: %v", err)
	}
	server.liveness.Complete(jobID)

	server.logger.Info("Backup job committed", "job_id", jobID, "event", "finish", "status", commitStatus, "matched", matched)
	return &pb.BackupCommitResponse{Success: matched}, nil
}
