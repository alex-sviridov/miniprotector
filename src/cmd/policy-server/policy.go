// policy-server's on-disk policy schema: one JSON file per policy under
// $MP_CONFIG_PATH/policies/. See
// docs/superpowers/specs/2026-07-10-policy-server-design.md.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// policyIDNamespace scopes this project's deterministic policy/object-filter
// IDs into their own UUID namespace (RFC 4122 §4.3) -- an arbitrary fixed
// UUID whose only job is separating this ID-space from unrelated uuid.New
// uses elsewhere in the codebase (e.g. common/jobid's random job-ids).
var policyIDNamespace = uuid.MustParse("6f1c3a2e-8b4d-4e11-9a7c-2d5f8e0b1c34")

type Metadata struct {
	ID        string    `json:"-"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ClientFilters struct {
	Hostnames []string          `json:"hostnames"`
	Labels    map[string]string `json:"labels"`
}

type ObjectFilter struct {
	ID      string   `json:"-"`
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type Policy struct {
	Metadata      Metadata       `json:"metadata"`
	ClientFilters ClientFilters  `json:"client_filters"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destination   string         `json:"destination"`
	SourcePath    string         `json:"-"`
}

// validatePolicy checks the fields an operator can set on a policy,
// independent of where it came from (a file on disk, via parsePolicyFile,
// or a CreatePolicy/UpdatePolicy RPC request): metadata.name must be
// non-empty, and every client_filters.hostnames/object_filters include/
// exclude glob pattern must be syntactically valid (path.Match's syntax).
func validatePolicy(p Policy) error {
	if p.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	for _, pattern := range p.ClientFilters.Hostnames {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid hostname pattern %q: %w", pattern, err)
		}
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

// parsePolicyFile reads and validates a single policy JSON file -- see
// validatePolicy for the validation rules applied.
func parsePolicyFile(filePath string) (Policy, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Policy{}, fmt.Errorf("read %s: %w", filePath, err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if err := validatePolicy(p); err != nil {
		return Policy{}, fmt.Errorf("%s: %w", filePath, err)
	}

	policyUUID := uuid.NewSHA1(policyIDNamespace, []byte(filepath.Base(filePath)))
	p.Metadata.ID = policyUUID.String()
	p.SourcePath = filePath
	for i := range p.ObjectFilters {
		p.ObjectFilters[i].ID = uuid.NewSHA1(policyUUID, []byte(strconv.Itoa(i))).String()
	}

	return p, nil
}
