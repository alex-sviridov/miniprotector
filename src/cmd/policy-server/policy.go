// policy-server's on-disk policy schema: one JSON file per policy under
// $MP_CONFIG_PATH/policies/<type>/ (e.g. policies/backup/, policies/storage/).
// Each policy type is a concrete Go type implementing the Policy interface;
// see backup_policy.go and storage_policy.go. See docs/superpowers/specs/
// 2026-07-10-policy-server-design.md, 2026-07-20-policy-type-subfolders-design.md,
// and 2026-07-28-storage-policy-type-design.md.
package main

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// policyIDNamespace scopes this project's deterministic policy/object-filter
// IDs into their own UUID namespace (RFC 4122 §4.3) -- an arbitrary fixed
// UUID whose only job is separating this ID-space from unrelated uuid.New
// uses elsewhere in the codebase (e.g. common/jobid's random job-ids).
var policyIDNamespace = uuid.MustParse("6f1c3a2e-8b4d-4e11-9a7c-2d5f8e0b1c34")

type Metadata struct {
	ID         string    `json:"-"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	DisabledAt time.Time `json:"disabled_at,omitempty"`
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

// PolicyBase holds everything shared across every policy type: identity,
// client-filter matching, and on-disk bookkeeping. Embedded by value in
// every concrete policy type -- never used on its own.
type PolicyBase struct {
	Metadata      Metadata      `json:"metadata"`
	ClientFilters ClientFilters `json:"client_filters"`
	SourcePath    string        `json:"-"`
	Type          string        `json:"-"`
}

func (b PolicyBase) Meta() Metadata         { return b.Metadata }
func (b PolicyBase) Filters() ClientFilters { return b.ClientFilters }
func (b PolicyBase) Path() string           { return b.SourcePath }
func (b PolicyBase) Kind() string           { return b.Type }

// setIdentity assigns the fields policy-server itself computes -- never
// read from or written to the on-disk policy JSON -- after a policy file
// has been parsed and validated: its on-disk path, its type (the subfolder
// it was loaded from), and its deterministic ID. BackupPolicy overrides
// this to also derive its ObjectFilters' IDs from the same id.
func (b *PolicyBase) setIdentity(sourcePath, policyType, id string) {
	b.SourcePath = sourcePath
	b.Type = policyType
	b.Metadata.ID = id
}

// clone deep-copies the reference-typed fields PolicyBase owns. Used by
// every concrete type's Clone() to build its own PolicyBase field.
func (b PolicyBase) clone() PolicyBase {
	hostnames := make([]string, len(b.ClientFilters.Hostnames))
	copy(hostnames, b.ClientFilters.Hostnames)
	labels := make(map[string]string, len(b.ClientFilters.Labels))
	for k, v := range b.ClientFilters.Labels {
		labels[k] = v
	}
	return PolicyBase{
		Metadata:      b.Metadata,
		SourcePath:    b.SourcePath,
		Type:          b.Type,
		ClientFilters: ClientFilters{Hostnames: hostnames, Labels: labels},
	}
}

// Policy is anything policy-server can load, cache, and serve: a shared
// identity (PolicyBase) plus type-specific data and behavior only its own
// concrete type (BackupPolicy, StoragePolicy) knows how to validate, copy,
// and convert to its wire representation.
type Policy interface {
	Meta() Metadata
	Filters() ClientFilters
	Path() string
	Kind() string
	Matches(hostname string, labels map[string]string) bool
	Validate() error
	Clone() Policy
	ToProto(includeClientFilters bool) *pb.Policy
	setIdentity(sourcePath, policyType, id string)
}

// policyParsers maps a policy type name (a policies/ subfolder's base name)
// to the function that unmarshals that type's on-disk JSON schema. Adding a
// new policy type means writing its parseXPolicyJSON and adding one entry
// here -- no other code in this file changes.
var policyParsers = map[string]func(data []byte) (Policy, error){
	"backup":  parseBackupPolicyJSON,
	"storage": parseStoragePolicyJSON,
}

// validateCommon checks the fields every policy type shares, independent of
// where it came from (a file on disk, or a Create/UpdatePolicy RPC
// request): metadata.name must be non-empty, and every
// client_filters.hostnames glob pattern must be syntactically valid
// (path.Match's syntax).
func validateCommon(base PolicyBase) error {
	if base.Metadata.Name == "" {
		return fmt.Errorf("metadata.name is required")
	}
	for _, pattern := range base.ClientFilters.Hostnames {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid hostname pattern %q: %w", pattern, err)
		}
	}
	return nil
}

// parsePolicyFile reads, unmarshals (via policyParsers[policyType]), and
// validates a single policy JSON file, then assigns the identity fields
// policy-server itself computes: SourcePath, Type (policyType -- the
// caller's own knowledge of which type subfolder filePath was found in, see
// Cache.Reload), and a deterministic ID derived from policyType and the
// file's basename. A policyType absent from policyParsers is reported the
// same way a malformed file is -- there is no schema to unmarshal an
// unrecognized type into.
func parsePolicyFile(filePath, policyType string) (Policy, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filePath, err)
	}

	parse, ok := policyParsers[policyType]
	if !ok {
		return nil, fmt.Errorf("%s: unrecognized policy type %q", filePath, policyType)
	}
	p, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", filePath, err)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", filePath, err)
	}

	id := uuid.NewSHA1(policyIDNamespace, []byte(filepath.Join(policyType, filepath.Base(filePath))))
	p.setIdentity(filePath, policyType, id.String())

	return p, nil
}
