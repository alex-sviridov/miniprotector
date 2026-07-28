package main

import (
	"encoding/json"
	"fmt"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// StoragePolicy is the "storage" policy type: where a future storage server
// should run (hostname, port) and how it should be configured (config).
// policy-server never interprets config beyond checking it's well-formed
// JSON -- it's opaque pass-through data for whatever future component reads
// it.
type StoragePolicy struct {
	PolicyBase
	Hostname string          `json:"hostname"`
	Port     int             `json:"port"`
	Config   json.RawMessage `json:"config"`
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
// RPC request): the fields validateCommon checks, plus hostname must be
// non-empty, port must be a valid TCP port (1-65535), and config must be
// non-empty, well-formed JSON -- its contents are never interpreted
// further.
func (p *StoragePolicy) Validate() error {
	if err := validateCommon(p.PolicyBase); err != nil {
		return err
	}
	if p.Hostname == "" {
		return fmt.Errorf("hostname is required")
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
		Hostname:   p.Hostname,
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
		Hostname:  p.Hostname,
		Port:      int32(p.Port),
		Config:    string(p.Config),
	}
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
