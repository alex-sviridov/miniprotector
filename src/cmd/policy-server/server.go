package main

import (
	"context"
	"log/slog"
	"sync"

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
	cache       *Cache
	policiesDir string
	logger      *slog.Logger

	// writeMu serializes CreatePolicy/UpdatePolicy/DeletePolicy against each
	// other. gRPC dispatches each unary RPC to its own goroutine, so without
	// this, two concurrent writes could race: one RPC's Reload can glob+parse
	// a stale snapshot of the directory before another RPC's write lands on
	// disk, then overwrite the cache with that stale snapshot after the other
	// RPC's own (fresher) Reload already ran -- silently reverting the other
	// write from the in-memory cache even though its file is correctly on
	// disk. Readers (GetPolicies/ListPolicies) only ever call Cache.Policies(),
	// never Reload, so they're unaffected and stay fully concurrent via
	// Cache's own sync.RWMutex.
	writeMu sync.Mutex
}

func NewPolicyServerServer(cache *Cache, policiesDir string, logger *slog.Logger) *policyServerServer {
	return &policyServerServer{cache: cache, policiesDir: policiesDir, logger: logger}
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
		objectFilters[i] = &pb.ObjectFilter{Id: f.ID, Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	return &pb.Policy{
		Id:            p.Metadata.ID,
		Name:          p.Metadata.Name,
		CreatedAt:     timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:     timestamppb.New(p.Metadata.UpdatedAt),
		ObjectFilters: objectFilters,
		Rpo:           p.RPO,
		BackupWindow:  p.BackupWindow,
		Destination:   p.Destination,
		Type:          p.Type,
	}
}

func toProtoClientFilters(cf ClientFilters) *pb.ClientFilters {
	return &pb.ClientFilters{Hostnames: cf.Hostnames, Labels: cf.Labels}
}

// toProtoPolicyAdmin is toProtoPolicy plus client_filters -- used by every
// RPC except GetPolicies (ListPolicies, CreatePolicy, UpdatePolicy), where
// an operator editing the full policy set needs to see and change
// client_filters. GetPolicies keeps using toProtoPolicy so a matched node
// never learns another node's targeting rules from a policy that already
// matched its own identity.
func toProtoPolicyAdmin(p Policy) *pb.Policy {
	pp := toProtoPolicy(p)
	pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	return pp
}

// ListPolicies returns every currently-loaded policy, unfiltered by any
// caller identity -- the admin surface api-server proxies for browsing and
// editing the full policy set. Unlike GetPolicies, it is never called by a
// mesh node itself.
func (s *policyServerServer) ListPolicies(ctx context.Context, _ *pb.ListPoliciesRequest) (*pb.ListPoliciesResponse, error) {
	policies := s.cache.Policies()
	out := make([]*pb.Policy, len(policies))
	for i, p := range policies {
		out[i] = toProtoPolicyAdmin(p)
	}
	s.logger.Info("ListPolicies", "count", len(out))
	return &pb.ListPoliciesResponse{Policies: out}, nil
}
