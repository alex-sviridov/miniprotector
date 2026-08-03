package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/mtls"
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

// isDisabled reports whether m's DisabledAt has been set and has passed as
// of now. A zero DisabledAt means "never disabled".
func isDisabled(m Metadata, now time.Time) bool {
	return !m.DisabledAt.IsZero() && !m.DisabledAt.After(now)
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
		if isDisabled(p.Meta(), time.Now()) {
			continue
		}
		if !p.Matches(hostname, labels) {
			continue
		}
		pp := p.ToProto(false)
		attachDestination(pp, s.cache)
		matched = append(matched, pp)
	}

	s.logger.Info("GetPolicies", "hostname", hostname, "job_id", jobID, "matched", len(matched))
	return &pb.GetPoliciesResponse{Policies: matched}, nil
}

func toProtoClientFilters(cf ClientFilters) *pb.ClientFilters {
	return &pb.ClientFilters{Hostnames: cf.Hostnames, Labels: cf.Labels}
}

// attachDestination resolves pp.Destination for a backup policy from its
// StoragePolicyId, using cache's live state. Called right after ToProto at
// every RPC that returns a pb.Policy (GetPolicies, ListPolicies,
// CreatePolicy, UpdatePolicy). A dangling reference (unknown id, or an id
// that no longer names a storage policy -- only reachable by hand-editing
// policy files outside the write RPCs, since DeletePolicy blocks the
// alternative) leaves pp.Destination unset rather than erroring.
func attachDestination(pp *pb.Policy, cache *Cache) {
	if pp.GetType() != "backup" || pp.GetStoragePolicyId() == "" {
		return
	}
	if dest, ok := cache.ResolveDestination(pp.GetStoragePolicyId()); ok {
		pp.Destination = dest
	}
}

// ListPolicies returns every currently-loaded policy, unfiltered by any
// caller identity -- the admin surface api-server proxies for browsing and
// editing the full policy set. Unlike GetPolicies, it is never called by a
// mesh node itself. If req.Type is set, only policies whose Kind() matches
// are returned; empty Type returns every type, unchanged from before this
// filter existed.
func (s *policyServerServer) ListPolicies(ctx context.Context, req *pb.ListPoliciesRequest) (*pb.ListPoliciesResponse, error) {
	policies := s.cache.Policies()
	var out []*pb.Policy
	for _, p := range policies {
		if req.GetType() != "" && p.Kind() != req.GetType() {
			continue
		}
		pp := p.ToProto(true)
		attachDestination(pp, s.cache)
		out = append(out, pp)
	}
	s.logger.Info("ListPolicies", "type", req.GetType(), "count", len(out))
	return &pb.ListPoliciesResponse{Policies: out}, nil
}
