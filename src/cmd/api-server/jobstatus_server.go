// src/cmd/api-server/jobstatus_server.go
package main

import (
	"context"
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetPolicyJobStatus answers policy-server's restore-cleanup sweep: has
// job_id's event=finish line been observed? Reuses queryEvent/jobEventLine
// unchanged -- the only new behavior is the job_id-scoped selector and the
// policy_created_at-anchored, maxJobsWindow-clamped time window (see
// docs/superpowers/specs/2026-08-23-restore-policy-lifecycle-design.md).
func (s *server) GetPolicyJobStatus(ctx context.Context, req *pb.GetPolicyJobStatusRequest) (*pb.GetPolicyJobStatusResponse, error) {
	if req.GetJobId() == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id is required")
	}

	until := time.Now()
	since := req.GetPolicyCreatedAt().AsTime()
	if cutoff := until.Add(-maxJobsWindow); since.Before(cutoff) {
		since = cutoff
	}

	selector := fmt.Sprintf(`{binary=~"agent|policy-server"} | job_id="%s"`, req.GetJobId())
	finishes, _, err := queryEvent(ctx, s.loki, selector, "finish", since, until)
	if err != nil {
		return nil, status.Error(codes.Internal, "query loki: "+err.Error())
	}
	if len(finishes) == 0 {
		return &pb.GetPolicyJobStatusResponse{Finished: false}, nil
	}
	return &pb.GetPolicyJobStatusResponse{
		Finished:   true,
		FinishedAt: timestamppb.New(time.Unix(finishes[0].Timestamp, 0)),
	}, nil
}
