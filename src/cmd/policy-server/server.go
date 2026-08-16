package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	checkinstore "github.com/alex-sviridov/miniprotector/storage/policyserver"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// policyServerServer implements PolicyService: the sole RPC any node calls
// to learn which backup policies target it. The caller's identity (hostname
// and attribute labels) is always derived from the verified mTLS peer
// certificate -- never a request field -- and matched against the current
// in-memory policy cache. No other service is consulted, though matching
// itself is recorded as a check-in in the local SQLite database.
type policyServerServer struct {
	pb.UnimplementedPolicyServiceServer
	cache       *Cache
	policiesDir string
	logger      *slog.Logger
	checkins    *checkinstore.Store

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

func NewPolicyServerServer(cache *Cache, policiesDir string, logger *slog.Logger, checkins *checkinstore.Store) *policyServerServer {
	return &policyServerServer{cache: cache, policiesDir: policiesDir, logger: logger, checkins: checkins}
}

// isDisabled reports whether m's DisabledAt has been set and has passed as
// of now. A zero DisabledAt means "never disabled".
func isDisabled(m Metadata, now time.Time) bool {
	return !m.DisabledAt.IsZero() && !m.DisabledAt.After(now)
}

func (s *policyServerServer) GetPolicies(ctx context.Context, req *pb.GetPoliciesRequest) (*pb.GetPoliciesResponse, error) {
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

	if err := s.checkins.RecordCertStatus(ctx, hostname, req.GetBootstrapRefreshLastError(),
		time.Unix(req.GetBootstrapRefreshLastAttemptAt(), 0)); err != nil {
		s.logger.Error("GetPolicies: failed to record cert status", "hostname", hostname, "job_id", jobID, "error", err)
		// non-fatal: unlike RecordCheckin below, a cert-status recording
		// failure must not prevent this node from getting its policies.
	}

	now := time.Now()
	var matched []*pb.Policy
	for _, p := range s.cache.Policies() {
		if isDisabled(p.Meta(), now) {
			continue
		}
		if !p.Matches(hostname, labels) {
			continue
		}
		pp := p.ToProto(false)
		attachDestination(ctx, pp, s.cache, s.checkins, s.logger)
		if err := s.checkins.RecordCheckin(ctx, pp.GetId(), hostname, now); err != nil {
			s.logger.Error("GetPolicies: failed to record check-in", "hostname", hostname, "job_id", jobID, "policy_id", pp.GetId(), "error", err)
			return nil, status.Error(codes.Internal, "failed to record check-in")
		}
		matched = append(matched, pp)
	}

	s.logger.Info("GetPolicies", "hostname", hostname, "job_id", jobID, "matched", len(matched))
	return &pb.GetPoliciesResponse{Policies: matched}, nil
}

// GetNodeCertStatus returns hostname's most recently recorded
// bootstrap-refresh status, as captured by every GetPolicies call. A
// hostname that has never called GetPolicies -- or has, and reported
// healthy -- both correctly produce an empty-LastError NodeCertStatus;
// see NodeCertStatus's own doc comment for the "not an error" contract.
//
// LastAttemptAt is left nil rather than filled in when nothing was
// recorded. CertStatusForHost returns a zero-value NodeCertStatus for an
// unknown host, and Go's zero time.Time is year 1, not the Unix epoch --
// so an unconditional timestamppb.New would hand back a valid, non-nil
// Timestamp of seconds=-62135596800, which api-server renders into its
// `omitempty` int64 as a very-much-present "last_attempt_at":
// -62135596800. Only a nil Timestamp actually produces the omitted field
// the REST contract promises (docs/api/rest-v1.md).
func (s *policyServerServer) GetNodeCertStatus(ctx context.Context, req *pb.GetNodeCertStatusRequest) (*pb.NodeCertStatus, error) {
	certStatus, _, err := s.checkins.CertStatusForHost(ctx, req.GetHostname())
	if err != nil {
		s.logger.Error("GetNodeCertStatus: store read failed", "hostname", req.GetHostname(), "error", err)
		return nil, status.Error(codes.Internal, "failed to read cert status")
	}
	out := &pb.NodeCertStatus{
		Hostname:  req.GetHostname(),
		LastError: certStatus.LastError,
	}
	if !certStatus.LastAttemptAt.IsZero() {
		out.LastAttemptAt = timestamppb.New(certStatus.LastAttemptAt)
	}
	return out, nil
}

func toProtoClientFilters(cf ClientFilters) *pb.ClientFilters {
	return &pb.ClientFilters{Hostnames: cf.Hostnames, Labels: cf.Labels}
}

// attachDestination resolves pp.Destinations for a "backup" or "restore"
// policy from its StoragePolicyId's checkin list, using cache's live state
// and checkins' live check-in records. Called right after ToProto at every
// RPC that returns a pb.Policy (GetPolicies, ListPolicies, CreatePolicy,
// UpdatePolicy). A dangling reference (unknown id, or an id that no longer
// names a storage policy), or a storage policy with no checkins yet, leaves
// pp.Destinations empty rather than erroring. A checkin lookup failure is
// logged and also leaves pp.Destinations empty rather than failing the RPC.
func attachDestination(ctx context.Context, pp *pb.Policy, cache *Cache, checkins *checkinstore.Store, logger *slog.Logger) {
	if (pp.GetType() != "backup" && pp.GetType() != "restore") || pp.GetStoragePolicyId() == "" {
		return
	}
	p, ok := cache.FindByID(pp.GetStoragePolicyId())
	if !ok || p.Kind() != "storage" {
		return
	}
	sp, ok := p.(*StoragePolicy)
	if !ok {
		return
	}
	records, err := checkins.CheckinsForPolicy(ctx, pp.GetStoragePolicyId())
	if err != nil {
		logger.Error("attachDestination: failed to load checkins", "storage_policy_id", pp.GetStoragePolicyId(), "error", err)
		return
	}
	for _, r := range records {
		pp.Destinations = append(pp.Destinations, fmt.Sprintf("%s:%d", r.Hostname, sp.Port))
	}
}

// attachCheckins populates pp.Checkins from store's per-host check-in
// records for pp's id. Called only by ListPolicies -- GetPolicies never
// echoes checkins back, the same way it never echoes client_filters. A
// lookup failure is logged and leaves pp.Checkins empty rather than
// failing the whole ListPolicies call -- the same "loud skip, don't block
// the rest" treatment this codebase already gives a single malformed
// policy file during Cache.Reload.
func attachCheckins(ctx context.Context, pp *pb.Policy, store *checkinstore.Store, logger *slog.Logger) {
	records, err := store.CheckinsForPolicy(ctx, pp.GetId())
	if err != nil {
		logger.Error("ListPolicies: failed to load checkins", "policy_id", pp.GetId(), "error", err)
		return
	}
	for _, r := range records {
		pp.Checkins = append(pp.Checkins, &pb.PolicyCheckin{
			Hostname:   r.Hostname,
			LastSeenAt: timestamppb.New(r.LastSeenAt),
		})
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
		attachDestination(ctx, pp, s.cache, s.checkins, s.logger)
		attachCheckins(ctx, pp, s.checkins, s.logger)
		out = append(out, pp)
	}
	s.logger.Info("ListPolicies", "type", req.GetType(), "count", len(out))
	return &pb.ListPoliciesResponse{Policies: out}, nil
}
