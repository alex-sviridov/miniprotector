package main

import (
	"encoding/json"
	"fmt"
	"path"
	"strconv"

	"github.com/google/uuid"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// BackupPolicy is the "backup" policy type: a set of object filters backed
// up on a schedule to a destination bwfs. Its on-disk JSON schema (beyond
// the shared metadata/client_filters PolicyBase already parses) is
// object_filters, rpo, backup_window, and destination.
type BackupPolicy struct {
	PolicyBase
	ObjectFilters []ObjectFilter `json:"object_filters"`
	// Duration string, e.g. "24h" (time.ParseDuration format).
	// policy-server never parses or evaluates this -- opaque pass-through
	// data.
	RPO string `json:"rpo"`
	// List of cron expressions (5-field). policy-server never parses or
	// evaluates these -- opaque pass-through data.
	BackupWindow []string `json:"backup_window"`
	Destination  string   `json:"destination"`
}

func parseBackupPolicyJSON(data []byte) (Policy, error) {
	var p BackupPolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the fields an operator can set on a backup policy,
// independent of where it came from (a file on disk or a Create/UpdatePolicy
// RPC request): the fields validateCommon checks, plus every object_filters
// include/exclude glob pattern must be syntactically valid (path.Match's
// syntax).
func (p *BackupPolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	for _, of := range p.ObjectFilters {
		for _, pattern := range of.Include {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("invalid include pattern %q: %w", pattern, err)
			}
		}
		for _, pattern := range of.Exclude {
			if _, err := path.Match(pattern, ""); err != nil {
				return fmt.Errorf("invalid exclude pattern %q: %w", pattern, err)
			}
		}
	}
	return nil
}

// setIdentity assigns PolicyBase's identity fields, then derives each
// ObjectFilter's ID from this policy's own id -- stable across reloads,
// changes only if the file is renamed or its object_filters are
// reordered/have entries inserted before an existing one.
func (p *BackupPolicy) setIdentity(sourcePath, policyType, id string) {
	p.PolicyBase.setIdentity(sourcePath, policyType, id)
	policyUUID := uuid.MustParse(id)
	for i := range p.ObjectFilters {
		p.ObjectFilters[i].ID = uuid.NewSHA1(policyUUID, []byte(strconv.Itoa(i))).String()
	}
}

// Clone deep-copies every reference-typed field so mutating the returned
// value never affects the cached original.
func (p *BackupPolicy) Clone() Policy {
	objectFilters := make([]ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = ObjectFilter{
			ID:      f.ID,
			Path:    f.Path,
			Include: append([]string(nil), f.Include...),
			Exclude: append([]string(nil), f.Exclude...),
		}
	}
	backupWindow := make([]string, len(p.BackupWindow))
	copy(backupWindow, p.BackupWindow)
	return &BackupPolicy{
		PolicyBase:    p.PolicyBase.clone(),
		ObjectFilters: objectFilters,
		RPO:           p.RPO,
		BackupWindow:  backupWindow,
		Destination:   p.Destination,
	}
}

// ToProto converts to the wire representation GetPolicies/ListPolicies/
// CreatePolicy/UpdatePolicy return. client_filters is only populated when
// includeClientFilters is true -- GetPolicies omits it so a matched node
// never learns another node's targeting rules from a policy that already
// matched its own identity; ListPolicies and the write RPCs include it for
// an operator editing the full policy set.
func (p *BackupPolicy) ToProto(includeClientFilters bool) *pb.Policy {
	objectFilters := make([]*pb.ObjectFilter, len(p.ObjectFilters))
	for i, f := range p.ObjectFilters {
		objectFilters[i] = &pb.ObjectFilter{Id: f.ID, Path: f.Path, Include: f.Include, Exclude: f.Exclude}
	}
	pp := &pb.Policy{
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
	if !p.Metadata.DisabledAt.IsZero() {
		pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
