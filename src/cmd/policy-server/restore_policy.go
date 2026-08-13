package main

import (
	"encoding/json"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RestoreRule is one restore-cart selection rule -- {host, path, include,
// dest_path} mirroring web/src/utils/restoreRules.js's rule shape exactly,
// so the frontend can send its cart.rules through with no reshaping. Host
// == "" means host-agnostic (a folder rule that applies across every
// source host, matching restoreRules.js's `host: null` convention -- a
// JSON null decodes to Go's zero-value "" automatically); a non-empty Host
// scopes the rule to exactly that source. DestPath, if non-empty and
// different from Path, is the path to restore to instead of Path -- only
// meaningful when Include is true (see Validate). policy-server never
// resolves any of this against a real file listing or acts on DestPath --
// resolution happens at verify time, in rwfs, and DestPath is not consumed
// anywhere yet (no restore executor exists). See
// docs/superpowers/specs/2026-08-13-restore-destination-rename-design.md.
type RestoreRule struct {
	Host     string `json:"host"`
	Path     string `json:"path"`
	Include  bool   `json:"include"`
	DestPath string `json:"dest_path,omitempty"`
}

// RestorePolicy is the "restore" policy type: a one-shot directive telling
// a specific mesh node (via PolicyBase's ClientFilters, the same targeting
// mechanism BackupPolicy/StoragePolicy already use) to restore files from
// a source bwfs. Unlike BackupPolicy/StoragePolicy it has no recurring-
// schedule concept (no rpo/backup_window) -- it's meant to be picked up
// once by agent's restoreTasks (cmd/agent/restore.go), and is never
// updatable via UpdatePolicy (see buildPolicyForUpdate in write.go).
//
// StoragePolicyID reuses BackupPolicy's exact mechanism (references a
// "storage"-typed Policy.id; the dialable address is resolved live from its
// checkins, see server.go's attachDestination) rather than a raw
// source_store host:port baked in at creation time -- avoiding the
// staleness a pre-resolved address would have if the storage node's
// checked-in address changes before this one-shot policy is ever executed.
type RestorePolicy struct {
	PolicyBase
	StoragePolicyID string        `json:"storage_policy_id"`
	Rules           []RestoreRule `json:"rules"`
}

func parseRestorePolicyJSON(data []byte) (Policy, error) {
	var p RestorePolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the fields an operator can set on a restore policy,
// independent of where it came from (a file on disk or a CreatePolicy RPC
// request): the fields validateCommon checks (including client_filters,
// which is how a restore policy targets the node that executes it),
// storage_policy_id must be non-empty (existence against a live "storage"
// policy is checked separately in CreatePolicy, where a current cache is
// in scope -- the same split BackupPolicy.Validate already documents), and
// rules must contain at least one entry, each with a non-empty path.
func (p *RestorePolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if p.StoragePolicyID == "" {
		return fmt.Errorf("storage_policy_id is required")
	}
	if len(p.Rules) == 0 {
		return fmt.Errorf("rules must contain at least one entry")
	}
	for i, r := range p.Rules {
		if r.Path == "" {
			return fmt.Errorf("rules[%d]: path is required", i)
		}
		if r.DestPath != "" && r.DestPath != r.Path && !r.Include {
			return fmt.Errorf("rules[%d]: dest_path is only valid on an included rule", i)
		}
	}
	return nil
}

// Clone deep-copies Rules so mutating the returned value never affects the
// cached original.
func (p *RestorePolicy) Clone() Policy {
	rules := make([]RestoreRule, len(p.Rules))
	copy(rules, p.Rules)
	return &RestorePolicy{
		PolicyBase:      p.PolicyBase.clone(),
		StoragePolicyID: p.StoragePolicyID,
		Rules:           rules,
	}
}

// ToProto converts to the wire representation GetPolicies/ListPolicies/
// CreatePolicy return (never UpdatePolicy -- restore policies are not
// updatable). Destinations is intentionally left unset here -- the caller
// (server.go's attachDestination) resolves it live from StoragePolicyId's
// checkins, the same split BackupPolicy.ToProto already uses. client_filters
// is only populated when includeClientFilters is true.
func (p *RestorePolicy) ToProto(includeClientFilters bool) *pb.Policy {
	rules := make([]*pb.RestoreRule, len(p.Rules))
	for i, r := range p.Rules {
		rules[i] = &pb.RestoreRule{Host: r.Host, Path: r.Path, Include: r.Include, DestPath: r.DestPath}
	}
	pp := &pb.Policy{
		Id:              p.Metadata.ID,
		Name:            p.Metadata.Name,
		CreatedAt:       timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:       timestamppb.New(p.Metadata.UpdatedAt),
		Type:            p.Type,
		StoragePolicyId: p.StoragePolicyID,
		Rules:           rules,
	}
	if !p.Metadata.DisabledAt.IsZero() {
		pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
