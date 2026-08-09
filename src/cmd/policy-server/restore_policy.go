package main

import (
	"encoding/json"
	"fmt"
	"net"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RestorePolicy is the "restore" policy type: a one-shot directive telling a
// specific mesh node (via PolicyBase's ClientFilters, the same targeting
// mechanism BackupPolicy/StoragePolicy already use) to restore files from a
// source bwfs. Unlike BackupPolicy/StoragePolicy it has no recurring-
// schedule concept (no rpo/backup_window) -- it's meant to be picked up
// once by a future agent-side consumer (not yet built), and is never
// updatable via UpdatePolicy (see buildPolicyForUpdate in write.go). It
// reuses Config, the same field StoragePolicy already carries, for its
// restore spec rather than introducing a second opaque-JSON field -- same
// load-time-well-formed-only semantics, contents interpreted by neither
// type. See docs/superpowers/specs/2026-08-09-restore-policy-type-design.md.
type RestorePolicy struct {
	PolicyBase
	// host:port of the source bwfs to restore from.
	SourceStore string `json:"source_store"`
	// Opaque JSON text describing what to restore (file list etc.) -- format
	// left for a future design. policy-server never interprets it beyond
	// checking well-formedness, the same way StoragePolicy.Config is opaque.
	Config json.RawMessage `json:"config"`
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
// source_store must be a non-empty, syntactically valid "host:port", and
// config must be non-empty, well-formed JSON -- its contents are never
// interpreted further.
func (p *RestorePolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if _, _, err := net.SplitHostPort(p.SourceStore); err != nil {
		return fmt.Errorf("source_store must be a valid host:port: %w", err)
	}
	if len(p.Config) == 0 {
		return fmt.Errorf("config is required")
	}
	if !json.Valid(p.Config) {
		return fmt.Errorf("config must be well-formed JSON")
	}
	return nil
}

// Clone deep-copies every reference-typed field so mutating the returned
// value never affects the cached original.
func (p *RestorePolicy) Clone() Policy {
	config := make(json.RawMessage, len(p.Config))
	copy(config, p.Config)
	return &RestorePolicy{
		PolicyBase:  p.PolicyBase.clone(),
		SourceStore: p.SourceStore,
		Config:      config,
	}
}

// ToProto converts to the wire representation GetPolicies/ListPolicies/
// CreatePolicy return (never UpdatePolicy -- restore policies are not
// updatable). client_filters is only populated when includeClientFilters is
// true, matching BackupPolicy.ToProto/StoragePolicy.ToProto.
func (p *RestorePolicy) ToProto(includeClientFilters bool) *pb.Policy {
	pp := &pb.Policy{
		Id:          p.Metadata.ID,
		Name:        p.Metadata.Name,
		CreatedAt:   timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt:   timestamppb.New(p.Metadata.UpdatedAt),
		Type:        p.Type,
		SourceStore: p.SourceStore,
		Config:      string(p.Config),
	}
	if !p.Metadata.DisabledAt.IsZero() {
		pp.DisabledAt = timestamppb.New(p.Metadata.DisabledAt)
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
