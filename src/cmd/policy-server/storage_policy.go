package main

import (
	"encoding/json"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// StoragePolicy is the "storage" policy type: how a future storage server
// should be configured (port, config). There is no Hostname field --
// targeting which node runs it is PolicyBase's ClientFilters, the same
// mechanism a BackupPolicy already uses, not a field specific to this type.
// policy-server never interprets config beyond checking it's well-formed
// JSON -- it's opaque pass-through data for whatever future component reads
// it. See docs/superpowers/specs/2026-07-28-agent-storage-supervision-design.md.
type StoragePolicy struct {
	PolicyBase
	Port   int             `json:"port"`
	Config json.RawMessage `json:"config"`
}

func parseStoragePolicyJSON(data []byte) (Policy, error) {
	var p StoragePolicy
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Validate checks the fields an operator can set on a storage policy,
// independent of where it came from (a file on disk or a Create/UpdatePolicy
// RPC request): the fields validateCommon checks (including client_filters,
// which is how a storage policy targets a node), plus port must be a valid
// TCP port (1-65535), and config must be non-empty, well-formed JSON -- its
// contents are never interpreted further.
func (p *StoragePolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", p.Port)
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
func (p *StoragePolicy) Clone() Policy {
	config := make(json.RawMessage, len(p.Config))
	copy(config, p.Config)
	return &StoragePolicy{
		PolicyBase: p.PolicyBase.clone(),
		Port:       p.Port,
		Config:     config,
	}
}

// ToProto converts to the wire representation GetPolicies/ListPolicies/
// CreatePolicy/UpdatePolicy return. client_filters is only populated when
// includeClientFilters is true, matching BackupPolicy.ToProto.
func (p *StoragePolicy) ToProto(includeClientFilters bool) *pb.Policy {
	pp := &pb.Policy{
		Id:        p.Metadata.ID,
		Name:      p.Metadata.Name,
		CreatedAt: timestamppb.New(p.Metadata.CreatedAt),
		UpdatedAt: timestamppb.New(p.Metadata.UpdatedAt),
		Type:      p.Type,
		Port:      int32(p.Port),
		Config:    string(p.Config),
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
