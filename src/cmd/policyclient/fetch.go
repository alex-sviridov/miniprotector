// fetch.go implements policyclient fetch: pulling the current policy list
// from policy-server and atomically caching it locally via
// common/atomicfile. On any failure the existing cache file is left
// completely untouched -- policyclient never clears or partially
// overwrites a previously-good cache.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/atomicfile"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"google.golang.org/grpc"
)

// ObjectFilter is the on-disk representation of one policy-server
// ObjectFilter: a backup root path plus its optional include/exclude glob
// patterns and its policy-server-computed ID, carried through verbatim
// from the RPC response.
type ObjectFilter struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// CachedPolicy is the on-disk representation of one policy-server Policy --
// the same fields the GetPolicies RPC response already defines, converted
// directly from the protobuf message.
type CachedPolicy struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
}

// policyServiceClient is the subset of pb.PolicyServiceClient runFetch
// needs -- satisfied directly by the real generated client, and by a fake
// in tests, mirroring certclient's issuerClient pattern.
type policyServiceClient interface {
	GetPolicies(ctx context.Context, in *pb.GetPoliciesRequest, opts ...grpc.CallOption) (*pb.GetPoliciesResponse, error)
}

// fetchAndCache is the real, network-dialing entry point main.go calls: it
// authenticates to policy-server with this node's operating credential
// (the default connection.Connect identity -- required, since policy-server
// matches policies against attribute labels embedded only in the operating
// certificate) and delegates to runFetch. jobID rides the RPC as outgoing
// job-id metadata, so policy-server's own log for this exact fetch is
// correlatable back to this process's local log.
func fetchAndCache(certsDir, host string, port, timeoutSec int, cachePath, jobID string, logger *slog.Logger) error {
	conn, err := connection.Connect(host, port, timeoutSec, certsDir)
	if err != nil {
		return fmt.Errorf("connect to policy-server: %w", err)
	}
	defer conn.Close()

	client := pb.NewPolicyServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	ctx = jobid.Outgoing(ctx, jobID)

	return runFetch(ctx, client, cachePath, logger)
}

// runFetch is the testable core: given an already-connected
// policyServiceClient, fetch the current policy list and atomically write
// it to cachePath via common/atomicfile. On any failure, cachePath is left
// completely untouched.
func runFetch(ctx context.Context, client policyServiceClient, cachePath string, logger *slog.Logger) error {
	logger.Debug("fetching policies")
	resp, err := client.GetPolicies(ctx, &pb.GetPoliciesRequest{})
	if err != nil {
		return fmt.Errorf("get policies: %w", err)
	}

	cached := toCachedPolicies(resp.GetPolicies())
	data, err := json.MarshalIndent(cached, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal policies: %w", err)
	}

	if err := atomicfile.Write(cachePath, data); err != nil {
		return fmt.Errorf("write policy cache: %w", err)
	}
	logger.Info("policy cache updated", "count", len(cached))
	return nil
}

// toCachedPolicies converts the RPC response's policies to their on-disk
// representation. Always returns a non-nil slice (even when policies is
// empty) so the cache file holds a JSON array, never null.
func toCachedPolicies(policies []*pb.Policy) []CachedPolicy {
	out := make([]CachedPolicy, 0, len(policies))
	for _, p := range policies {
		filters := make([]ObjectFilter, 0, len(p.GetObjectFilters()))
		for _, of := range p.GetObjectFilters() {
			filters = append(filters, ObjectFilter{
				ID:      of.GetId(),
				Path:    of.GetPath(),
				Include: of.GetInclude(),
				Exclude: of.GetExclude(),
			})
		}
		out = append(out, CachedPolicy{
			ID:            p.GetId(),
			Name:          p.GetName(),
			CreatedAt:     p.GetCreatedAt().AsTime(),
			UpdatedAt:     p.GetUpdatedAt().AsTime(),
			ObjectFilters: filters,
			RPO:           p.GetRpo(),
			BackupWindow:  p.GetBackupWindow(),
			Destination:   p.GetDestination(),
		})
	}
	return out
}
