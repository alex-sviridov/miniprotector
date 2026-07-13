package main

import (
	"context"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// policyServerServer implements PolicyService: the sole RPC any node calls
// to learn which backup policies target it. The caller's identity (hostname
// and attribute labels) is always derived from the verified mTLS peer
// certificate -- never a request field -- and matched against the current
// in-memory policy cache. No database, no other service is consulted.
type policyServerServer struct {
	pb.UnimplementedPolicyServiceServer
	cache  *Cache
	logger *slog.Logger
}

func NewPolicyServerServer(cache *Cache, logger *slog.Logger) *policyServerServer {
	return &policyServerServer{cache: cache, logger: logger}
}

func (s *policyServerServer) GetPolicies(ctx context.Context, _ *pb.GetPoliciesRequest) (*pb.GetPoliciesResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not determine peer identity", "error", err)
		return nil, err
	}

	jobID, err := jobid.FromIncoming(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: job-id metadata required", "hostname", hostname, "error", err)
		return nil, err
	}

	labels, err := mtls.PeerAttributes(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not read peer attributes", "hostname", hostname, "job_id", jobID, "error", err)
		return nil, err
	}

	var matched []*pb.Policy
	for _, p := range s.cache.Policies() {
		if !p.Matches(hostname, labels) {
			continue
		}
		matched = append(matched, toProtoPolicy(p))
	}

	s.logger.Info("GetPolicies", "hostname", hostname, "job_id", jobID, "matched", len(matched))
	return &pb.GetPoliciesResponse{Policies: matched}, nil
}

func toProtoPolicy(p Policy) *pb.Policy {
	objectFilters := make([]*pb.ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = &pb.ObjectFilter{Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	return &pb.Policy{
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
		Destination:   p.Destination,
	}
}
