package main

import (
	"context"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReorderRetentionPolicies rewrites every retention policy's priority to 1..n
// in the order req.Ids lists them -- the only way a retention policy's
// position ever changes (CreatePolicy appends, UpdatePolicy preserves it).
//
// req.Ids must be exactly the set of existing retention policy ids: a
// missing, unknown, duplicate or non-retention id rejects the whole request
// before anything is written, so a stale caller (a UI whose list predates
// another operator's create or delete) can never silently drop or reorder
// rules it didn't see. It shares writeMu with the other write RPCs, so a
// concurrent Create/Update/Delete can't interleave with the rewrite.
//
// A policy's file is only rewritten when its priority actually changes,
// and UpdatedAt is left alone: moving a rule is not editing it.
func (s *policyServerServer) ReorderRetentionPolicies(ctx context.Context, req *pb.ReorderRetentionPoliciesRequest) (*pb.ReorderRetentionPoliciesResponse, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	current := map[string]*RetentionPolicy{}
	for _, p := range s.cache.Policies() {
		if rp, ok := p.(*RetentionPolicy); ok {
			current[rp.Meta().ID] = rp
		}
	}

	ids := req.GetIds()
	if len(ids) != len(current) {
		return nil, status.Error(codes.InvalidArgument,
			fmt.Sprintf("ids must list every retention policy exactly once: got %d, have %d", len(ids), len(current)))
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("duplicate id %q", id))
		}
		seen[id] = true
		if _, ok := current[id]; !ok {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("%q is not an existing retention policy", id))
		}
	}

	var writeErr error
	for i, id := range ids {
		rp := current[id]
		want := i + 1
		if rp.Priority == want {
			continue
		}
		rp.Priority = want
		if err := atomicWriteJSON(rp.Path(), rp); err != nil {
			s.logger.Error("ReorderRetentionPolicies: write failed", "path", rp.Path(), "error", err)
			writeErr = err
			break
		}
	}
	// Reload even after a failed write so the cache reflects whatever did
	// reach disk; ListPolicies' (priority, id) ordering stays deterministic
	// for a partially applied reorder.
	reloadErr := s.cache.Reload(s.policiesDir, s.logger)
	if writeErr != nil {
		return nil, status.Error(codes.Internal, "failed to write reordered policy files")
	}
	if reloadErr != nil {
		s.logger.Error("ReorderRetentionPolicies: reload failed", "error", reloadErr)
		return nil, status.Error(codes.Internal, "failed to reload policies after reorder")
	}

	var out []*pb.Policy
	for _, p := range s.cache.Policies() {
		if p.Kind() == "retention" {
			out = append(out, p.ToProto(true))
		}
	}
	sortRetentionByPriority(out)
	s.logger.Info("ReorderRetentionPolicies", "count", len(out))
	return &pb.ReorderRetentionPoliciesResponse{Policies: out}, nil
}
