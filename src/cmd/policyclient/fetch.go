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
	"os"
	"path/filepath"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/atomicfile"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// disabledAtFromProto converts a possibly-nil disabled_at field to
// time.Time, treating "field not set" as the zero time -- distinct from
// (*timestamppb.Timestamp).AsTime()'s own nil-safe behavior, which maps a
// nil Timestamp to the Unix epoch (1970), not Go's zero time.Time (year 1).
func disabledAtFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

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

// RestoreRule mirrors policy-server's RestoreRule -- {host, path, include}
// plus the optional restore timeframe, where an empty Host means the rule
// applies across every source host. Pure passthrough here; agent
// (cmd/agent/restore.go) is what interprets it, via rwfs verify
// --rules-stdin, and it decodes policies-cache.json by these exact JSON
// tags -- so they must stay identical to agent's own RestoreRule.
//
// NotBefore/NotAfter are Unix seconds bounding which backed-up version of
// the rule's path may satisfy it; 0 on either side means unbounded there.
// They are omitempty so an unbounded rule caches byte-identically to how
// it did before the timeframe feature existed.
type RestoreRule struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	NotBefore int64  `json:"not_before,omitempty"`
	NotAfter  int64  `json:"not_after,omitempty"`
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
	Destinations  []string       `json:"destinations"`
	// Storage-policy-only fields, zero/empty for a backup policy -- same
	// additive convention the proto itself uses. Passthrough here; agent
	// (cmd/agent/storage.go) is what interprets Config.
	Port   int32  `json:"port,omitempty"`
	Config string `json:"config,omitempty"`
	// "restore" policy only, zero/empty for every other type.
	Rules []RestoreRule `json:"rules,omitempty"`
	// Derived by policy-server from the subfolder the policy file was
	// loaded from (e.g. "backup"). Pure passthrough here -- policyclient
	// itself never branches on it; agent does (see
	// cmd/agent/backup.go's backupTasks and cmd/agent/storage.go's
	// storageTasks).
	Type       string    `json:"type"`
	DisabledAt time.Time `json:"disabled_at,omitempty"`
}

// bootstrapRefreshFailure does a best-effort read of agent-state.json's
// "bootstrap-refresh" entry -- the one piece of agent's local state this
// binary has any reason to look at, so agent-state.json's other entries
// (backup/storage/restore task history) are decoded into but otherwise
// ignored. A missing file, unparseable JSON, or an absent/healthy
// "bootstrap-refresh" key are all "nothing to report", the same fail-safe
// direction agent's own readCache already takes for this identical file
// (cmd/agent/cache.go). This must never block the GetPolicies call that
// follows it -- every error path here returns cleanly, never panics or
// propagates.
func bootstrapRefreshFailure(varDir string) (lastError string, lastAttemptAt int64) {
	data, err := os.ReadFile(filepath.Join(varDir, "agent-state.json"))
	if err != nil {
		return "", 0
	}
	var cache map[string]struct {
		LastAttemptAt *time.Time `json:"last_attempt_at"`
		LastError     string     `json:"last_error,omitempty"`
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return "", 0
	}
	entry, ok := cache["bootstrap-refresh"]
	if !ok || entry.LastError == "" {
		return "", 0
	}
	if entry.LastAttemptAt != nil {
		lastAttemptAt = entry.LastAttemptAt.Unix()
	}
	return entry.LastError, lastAttemptAt
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
// completely untouched. Before calling GetPolicies, it also does a
// best-effort local read of agent-state.json's "bootstrap-refresh" entry
// (via bootstrapRefreshFailure) so policy-server can surface a stuck
// bootstrap-certificate renewal without agent needing its own RPC --
// agent-state.json lives in the same directory as cachePath
// (filepath.Dir(cachePath)), since both are written under the same varDir.

func runFetch(ctx context.Context, client policyServiceClient, cachePath string, logger *slog.Logger) error {
	logger.Debug("fetching policies")
	lastErr, lastAt := bootstrapRefreshFailure(filepath.Dir(cachePath))
	resp, err := client.GetPolicies(ctx, &pb.GetPoliciesRequest{
		BootstrapRefreshLastError:     lastErr,
		BootstrapRefreshLastAttemptAt: lastAt,
	})
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
		rules := make([]RestoreRule, 0, len(p.GetRules()))
		for _, r := range p.GetRules() {
			rules = append(rules, RestoreRule{
				Host:      r.GetHost(),
				Path:      r.GetPath(),
				Include:   r.GetInclude(),
				NotBefore: r.GetNotBefore(),
				NotAfter:  r.GetNotAfter(),
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
			Destinations:  p.GetDestinations(),
			Port:          p.GetPort(),
			Config:        p.GetConfig(),
			Rules:         rules,
			Type:          p.GetType(),
			DisabledAt:    disabledAtFromProto(p.GetDisabledAt()),
		})
	}
	return out
}
