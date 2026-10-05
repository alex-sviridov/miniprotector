package main

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// retentionBackupTypes is the set of backup types a retention rule may
// target. Only "filesystem" exists today; adding a workload type is adding
// its name here (and teaching agent to match it).
var retentionBackupTypes = map[string]bool{"filesystem": true}

// RetentionPolicy is the "retention" policy type: exactly one retention
// rule -- how long file versions under Path are kept -- plus the usual
// client_filters deciding which nodes it applies to. Rules are evaluated in
// ascending Priority and the first match wins; see
// docs/superpowers/specs/2026-10-05-retention-policies-design.md.
//
// Priority is server-managed: CreatePolicy appends, UpdatePolicy preserves
// it, and only ReorderRetentionPolicies changes it.
type RetentionPolicy struct {
	PolicyBase
	BackupType  string   `json:"backup_type"`
	PathPrefix  string   `json:"path"` // not "Path": PolicyBase.Path() is the on-disk source path
	Include     []string `json:"include,omitempty"`
	KeepSeconds int64    `json:"keep_seconds"`
	Priority    int      `json:"priority"`
}

func parseRetentionPolicyJSON(data []byte) (Policy, error) {
	var p RetentionPolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the fields an operator can set on a retention policy,
// independent of where it came from (a file on disk or a Create/UpdatePolicy
// request): the common fields, a known backup_type, a clean absolute
// slash-separated path without "..", basename-only include globs (a "/" has
// no sound meaning once a rule's prefix can sit above or below a job's
// root), a non-negative keep_seconds (0 = never expire), and priority >= 1.
func (p *RetentionPolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if !retentionBackupTypes[p.BackupType] {
		return fmt.Errorf("backup_type %q is not supported", p.BackupType)
	}
	if !strings.HasPrefix(p.PathPrefix, "/") {
		return fmt.Errorf("path must be absolute, got %q", p.PathPrefix)
	}
	for _, seg := range strings.Split(p.PathPrefix, "/") {
		if seg == ".." {
			return fmt.Errorf("path must not contain '..': %q", p.PathPrefix)
		}
	}
	if p.PathPrefix != path.Clean(p.PathPrefix) {
		return fmt.Errorf("path must be a clean path without trailing or repeated slashes, got %q", p.PathPrefix)
	}
	for _, pattern := range p.Include {
		if pattern == "" {
			return fmt.Errorf("include patterns must not be empty")
		}
		if strings.Contains(pattern, "/") {
			return fmt.Errorf("include pattern %q must not contain '/': patterns match file names only", pattern)
		}
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid include pattern %q: %w", pattern, err)
		}
	}
	if p.KeepSeconds < 0 {
		return fmt.Errorf("keep_seconds must not be negative, got %d", p.KeepSeconds)
	}
	if p.Priority < 1 {
		return fmt.Errorf("priority must be at least 1, got %d", p.Priority)
	}
	return nil
}

// Clone deep-copies every reference-typed field so mutating the returned
// value never affects the cached original.
func (p *RetentionPolicy) Clone() Policy {
	include := make([]string, len(p.Include))
	copy(include, p.Include)
	if len(include) == 0 {
		include = nil
	}
	return &RetentionPolicy{
		PolicyBase:  p.PolicyBase.clone(),
		BackupType:  p.BackupType,
		PathPrefix:  p.PathPrefix,
		Include:     include,
		KeepSeconds: p.KeepSeconds,
		Priority:    p.Priority,
	}
}

// ToProto converts to the wire representation; client_filters is only
// populated when includeClientFilters is true, matching every other type.
func (p *RetentionPolicy) ToProto(includeClientFilters bool) *pb.Policy {
	pp := &pb.Policy{
		Id:        p.Metadata.ID,
		Name:      p.Metadata.Name,
		CreatedAt: timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt: timestamppb.New(p.Metadata.UpdatedAt),
		Type:      p.Type,
		Retention: &pb.RetentionRule{
			BackupType:  p.BackupType,
			Path:        p.PathPrefix,
			Include:     p.Include,
			KeepSeconds: p.KeepSeconds,
			Priority:    int32(p.Priority),
		},
	}
	if !p.Metadata.DisabledAt.IsZero() {
		pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
