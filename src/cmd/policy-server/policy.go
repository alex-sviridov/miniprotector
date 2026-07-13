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
}

// parsePolicyFile reads and validates a single policy JSON file. A policy
// must have a non-empty metadata.name, and every client_filters.hostnames
// entry must be a syntactically valid glob pattern (path.Match's syntax) --
// both are treated as load errors, causing the caller to skip this file
// rather than serve a policy no client could ever legitimately match.
func parsePolicyFile(filePath string) (Policy, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return Policy{}, fmt.Errorf("read %s: %w", filePath, err)
	}
	var p Policy
	if err := json.Unmarshal(data, &p); err != nil {
		return Policy{}, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if p.Metadata.Name == "" {
		return Policy{}, fmt.Errorf("%s: metadata.name is required", filePath)
	}

	policyUUID := uuid.NewSHA1(policyIDNamespace, []byte(filepath.Base(filePath)))
	p.Metadata.ID = policyUUID.String()
	for i := range p.ObjectFilters {
		p.ObjectFilters[i].ID = uuid.NewSHA1(policyUUID, []byte(strconv.Itoa(i))).String()
	}

	for _, pattern := range p.ClientFilters.Hostnames {
		if _, err := path.Match(pattern, ""); err != nil {
			return Policy{}, fmt.Errorf("%s: invalid hostname pattern %q: %w", filePath, pattern, err)
		}
	}
	for _, of := range p.ObjectFilters {
		for _, pattern := range of.Include {
			if _, err := path.Match(pattern, ""); err != nil {
				return Policy{}, fmt.Errorf("%s: invalid include pattern %q: %w", filePath, pattern, err)
			}
		}
		for _, pattern := range of.Exclude {
			if _, err := path.Match(pattern, ""); err != nil {
				return Policy{}, fmt.Errorf("%s: invalid exclude pattern %q: %w", filePath, pattern, err)
			}
		}
	}
	return p, nil
}
