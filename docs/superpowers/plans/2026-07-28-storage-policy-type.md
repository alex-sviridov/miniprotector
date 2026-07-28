# Storage Policy Type Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `"storage"` policy type (`hostname`, `port`, `config`) to `policy-server`, restructuring its Go-side `Policy` from one flat struct into an interface (`Policy`) implemented by `BackupPolicy` and `StoragePolicy`, each embedding a shared `PolicyBase`.

**Architecture:** `PolicyBase` holds identity/matching fields (`Metadata`, `ClientFilters`, `SourcePath`, `Type`) shared by every policy type. `BackupPolicy`/`StoragePolicy` embed it and add their own type-specific fields, `Validate()`, `Clone()`, and `ToProto()`. A `policyParsers` registry maps a `policies/<type>/` subfolder name to the function that unmarshals that type's JSON, so `Cache.Reload`'s directory-walking logic never changes when a type is added. The gRPC wire schema stays flat/additive (not a `oneof`) so `api-server` and every other `pb.Policy` reader outside `policy-server` keeps compiling untouched.

**Tech Stack:** Go, gRPC/protobuf (`protoc` + `protoc-gen-go`/`protoc-gen-go-grpc`), testify (`assert`/`require`).

## Global Constraints

- No back-compat shims: this project has no live deployments, so behavior changes (e.g. unrecognized policy-type subfolders now being skipped instead of loaded generically) are made outright, not gated behind a flag. (Design: `docs/superpowers/specs/2026-07-28-storage-policy-type-design.md`)
- `policy-server` never interprets a storage policy's `config` beyond checking it is well-formed JSON. (Design, "Approach")
- Proto stays flat/additive on `Policy`/`CreatePolicyRequest`/`UpdatePolicyRequest` — no `oneof` — specifically so `api-server`'s `toPolicyDTO` and friends keep compiling without changes beyond one required `Type: "backup"` field addition per write call site. (Design, "Proto: stays flat, not a `oneof`")
- `UpdatePolicyRequest` carries no `type` field — a policy's type is immutable via `UpdatePolicy`, derived from the existing record. (Design, "`CreatePolicy`/`UpdatePolicy` (`write.go`)")
- Every task must leave `cd src && go build ./... && go test ./...` green before its commit.

---

## Task 1: Refactor `Policy` into an interface (`PolicyBase` + `BackupPolicy`), zero behavior change

This is a pure refactor: every existing test's *assertions* are preserved, only the Go shapes used to reach them change (interface methods / type assertions instead of direct struct field access on `Policy`). No new policy type exists yet after this task — `"backup"` is still the only registered type.

**Files:**
- Modify: `src/cmd/policy-server/policy.go` (full rewrite)
- Create: `src/cmd/policy-server/backup_policy.go`
- Modify: `src/cmd/policy-server/filter.go` (full rewrite)
- Modify: `src/cmd/policy-server/cache.go` (full rewrite)
- Modify: `src/cmd/policy-server/server.go` (full rewrite)
- Modify: `src/cmd/policy-server/write.go` (full rewrite)
- Modify: `src/cmd/policy-server/policy_test.go` (full rewrite)
- Create: `src/cmd/policy-server/backup_policy_test.go`
- Modify: `src/cmd/policy-server/filter_test.go` (one-line change)
- Modify: `src/cmd/policy-server/cache_test.go` (full rewrite)
- Modify: `src/cmd/policy-server/write_test.go` (targeted field-access fixes only, no new fixtures yet)
- No change: `src/cmd/policy-server/server_test.go`, `src/cmd/policy-server/watch.go`, `src/cmd/policy-server/watch_test.go`, `src/cmd/policy-server/main.go` — none of these touch `Policy`'s internal Go shape (`server_test.go` only asserts on `*pb.Policy`, the wire type).

**Interfaces:**
- Produces: `type Policy interface { Meta() Metadata; Filters() ClientFilters; Path() string; Kind() string; Matches(hostname string, labels map[string]string) bool; Validate() error; Clone() Policy; ToProto(includeClientFilters bool) *pb.Policy; setIdentity(sourcePath, policyType, id string) }`, `type PolicyBase struct{ Metadata Metadata; ClientFilters ClientFilters; SourcePath string; Type string }` with value-receiver methods `Meta/Filters/Path/Kind/Matches/clone` and pointer-receiver `setIdentity`, `type BackupPolicy struct{ PolicyBase; ObjectFilters []ObjectFilter; RPO string; BackupWindow []string; Destination string }`, `func parsePolicyFile(filePath, policyType string) (Policy, error)`, `var policyParsers map[string]func([]byte) (Policy, error)` (containing only `"backup"` after this task), `func validateCommon(base PolicyBase) error`, `func (c *Cache) Policies() []Policy`, `func (c *Cache) FindByID(id string) (Policy, bool)`, `func (c *Cache) FindBySourcePath(path string) (Policy, bool)`.
- Consumes: nothing from earlier tasks (this is the first task).

- [ ] **Step 1: Replace `policy.go`**

```go
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
	"backup": parseBackupPolicyJSON,
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
```

- [ ] **Step 2: Create `backup_policy.go`**

```go
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
	if includeClientFilters {
		pp.ClientFilters = toProtoClientFilters(p.ClientFilters)
	}
	return pp
}
```

- [ ] **Step 3: Replace `filter.go`**

```go
package main

import "path"

// Matches reports whether a client with the given hostname and attribute
// labels satisfies this policy's client_filters: an empty hostname pattern
// list matches any hostname; a non-empty list requires at least one glob
// match. Every key/value pair in client_filters.labels must be present in
// labels -- extra labels the client has beyond what's listed don't
// disqualify a match. Both conditions must hold (AND); there is no
// either-hostname-or-labels mode.
func (b PolicyBase) Matches(hostname string, labels map[string]string) bool {
	if !hostnameMatches(b.ClientFilters.Hostnames, hostname) {
		return false
	}
	return labelsMatch(b.ClientFilters.Labels, labels)
}

func hostnameMatches(patterns []string, hostname string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, pattern := range patterns {
		if ok, _ := path.Match(pattern, hostname); ok {
			return true
		}
	}
	return false
}

func labelsMatch(required, actual map[string]string) bool {
	for k, v := range required {
		if actual[k] != v {
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Replace `cache.go`**

```go
package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Cache holds the current, atomically-swapped set of policies loaded from
// disk. Safe for concurrent use: GetPolicies handlers call Policies()
// concurrently with a background reload triggered by the fsnotify watcher.
type Cache struct {
	mu       sync.RWMutex
	policies []Policy
}

func NewCache() *Cache {
	return &Cache{}
}

// Policies returns a snapshot of the currently-loaded policy list; mutating
// the returned slice/elements never affects the cache. Each policy deep-
// copies itself via its own Clone().
func (c *Cache) Policies() []Policy {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Policy, len(c.policies))
	for i, p := range c.policies {
		out[i] = p.Clone()
	}
	return out
}

// Reload re-reads every *.json file found one level under dir -- i.e.
// dir/<type>/*.json for every immediate subdirectory <type> of dir. A
// *.json file sitting directly under dir, outside any type subfolder, is
// logged and skipped, the same as a malformed file -- it doesn't block the
// rest of the directory from loading. A subfolder whose name isn't
// registered in policyParsers is reported by parsePolicyFile the same way a
// malformed file is, so it's skipped file-by-file through the same branch
// below -- there is no separate "unknown type" code path here.
//
// If dir contains at least one *.json file (anywhere: stray or in a type
// subfolder) and every loadable one failed to parse, the previous good
// cache is left in place (an error is returned) rather than swapped to an
// empty list -- an empty, or entirely subfolder-less, policies/ directory
// is a valid "no policies" state, but a reload that produced zero
// successes out of one-or-more attempts is treated as a failed reload, not
// an intentional empty state.
func (c *Cache) Reload(dir string, logger *slog.Logger) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list %s: %w", dir, err)
	}

	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			logger.Warn("skipping policy file with no type subfolder", "path", filepath.Join(dir, e.Name()))
		}
	}

	type candidate struct {
		path       string
		policyType string
	}
	var candidates []candidate
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subdir := filepath.Join(dir, e.Name())
		matches, err := filepath.Glob(filepath.Join(subdir, "*.json"))
		if err != nil {
			return fmt.Errorf("list policy files in %s: %w", subdir, err)
		}
		for _, m := range matches {
			candidates = append(candidates, candidate{path: m, policyType: e.Name()})
		}
	}

	loaded := make([]Policy, 0, len(candidates))
	for _, cd := range candidates {
		p, err := parsePolicyFile(cd.path, cd.policyType)
		if err != nil {
			logger.Error("skipping malformed policy file", "path", cd.path, "error", err)
			continue
		}
		loaded = append(loaded, p)
	}

	if len(candidates) > 0 && len(loaded) == 0 {
		return fmt.Errorf("reload of %s: all %d policy files failed to parse, keeping previous cache", dir, len(candidates))
	}

	c.mu.Lock()
	c.policies = loaded
	c.mu.Unlock()
	return nil
}

// FindByID returns the currently-loaded policy with the given Metadata.ID.
// Used by UpdatePolicy/DeletePolicy, which address a policy by its
// caller-facing ID rather than its on-disk filename.
func (c *Cache) FindByID(id string) (Policy, bool) {
	for _, p := range c.Policies() {
		if p.Meta().ID == id {
			return p, true
		}
	}
	return nil, false
}

// FindBySourcePath returns the currently-loaded policy parsed from exactly
// this file path. Used by CreatePolicy to look up the policy it just wrote,
// once Reload has re-parsed it and computed its ID.
func (c *Cache) FindBySourcePath(path string) (Policy, bool) {
	for _, p := range c.Policies() {
		if p.Path() == path {
			return p, true
		}
	}
	return nil, false
}
```

- [ ] **Step 5: Replace `server.go`**

```go
package main

import (
	"context"
	"log/slog"
	"sync"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/mtls"
)

// policyServerServer implements PolicyService: the sole RPC any node calls
// to learn which backup policies target it. The caller's identity (hostname
// and attribute labels) is always derived from the verified mTLS peer
// certificate -- never a request field -- and matched against the current
// in-memory policy cache. No database, no other service is consulted.
type policyServerServer struct {
	pb.UnimplementedPolicyServiceServer
	cache       *Cache
	policiesDir string
	logger      *slog.Logger

	// writeMu serializes CreatePolicy/UpdatePolicy/DeletePolicy against each
	// other. gRPC dispatches each unary RPC to its own goroutine, so without
	// this, two concurrent writes could race: one RPC's Reload can glob+parse
	// a stale snapshot of the directory before another RPC's write lands on
	// disk, then overwrite the cache with that stale snapshot after the other
	// RPC's own (fresher) Reload already ran -- silently reverting the other
	// write from the in-memory cache even though its file is correctly on
	// disk. Readers (GetPolicies/ListPolicies) only ever call Cache.Policies(),
	// never Reload, so they're unaffected and stay fully concurrent via
	// Cache's own sync.RWMutex.
	writeMu sync.Mutex
}

func NewPolicyServerServer(cache *Cache, policiesDir string, logger *slog.Logger) *policyServerServer {
	return &policyServerServer{cache: cache, policiesDir: policiesDir, logger: logger}
}

func (s *policyServerServer) GetPolicies(ctx context.Context, _ *pb.GetPoliciesRequest) (*pb.GetPoliciesResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not determine peer identity", "error", err)
		return nil, err
	}

	jobID, err := jobid.FromIncoming(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: job-id metadata required", "hostname", hostname, "error", err)
		return nil, err
	}

	labels, err := mtls.PeerAttributes(ctx)
	if err != nil {
		s.logger.Error("GetPolicies: could not read peer attributes", "hostname", hostname, "job_id", jobID, "error", err)
		return nil, err
	}

	var matched []*pb.Policy
	for _, p := range s.cache.Policies() {
		if !p.Matches(hostname, labels) {
			continue
		}
		matched = append(matched, p.ToProto(false))
	}

	s.logger.Info("GetPolicies", "hostname", hostname, "job_id", jobID, "matched", len(matched))
	return &pb.GetPoliciesResponse{Policies: matched}, nil
}

func toProtoClientFilters(cf ClientFilters) *pb.ClientFilters {
	return &pb.ClientFilters{Hostnames: cf.Hostnames, Labels: cf.Labels}
}

// ListPolicies returns every currently-loaded policy, unfiltered by any
// caller identity -- the admin surface api-server proxies for browsing and
// editing the full policy set. Unlike GetPolicies, it is never called by a
// mesh node itself.
func (s *policyServerServer) ListPolicies(ctx context.Context, _ *pb.ListPoliciesRequest) (*pb.ListPoliciesResponse, error) {
	policies := s.cache.Policies()
	out := make([]*pb.Policy, len(policies))
	for i, p := range policies {
		out[i] = p.ToProto(true)
	}
	s.logger.Info("ListPolicies", "count", len(out))
	return &pb.ListPoliciesResponse{Policies: out}, nil
}
```

- [ ] **Step 6: Replace `write.go`**

```go
// The write RPCs (CreatePolicy, UpdatePolicy, DeletePolicy): policy-server
// is the sole writer of its own policy files, so a write RPC validates the
// proposed content, atomically writes/removes the file, then synchronously
// reloads its own in-memory cache before responding -- the caller only ever
// sees a state the cache has already picked up. See
// docs/superpowers/specs/2026-07-18-policy-management-api-design.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lowercases name and collapses every run of non-alphanumeric
// characters into a single "-", trimming any leading/trailing "-" --
// "Nightly DB Backup!" -> "nightly-db-backup".
func slugify(name string) string {
	slug := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(slug, "-")
}

// uniqueFilename returns a filename in dir based on slug that doesn't
// already exist: "<slug>.json" if free, otherwise "<slug>-2.json",
// "<slug>-3.json", etc.
func uniqueFilename(dir, slug string) (string, error) {
	candidate := slug + ".json"
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("check %s: %w", filepath.Join(dir, candidate), err)
		}
		candidate = fmt.Sprintf("%s-%d.json", slug, i)
	}
}

// atomicWriteJSON marshals v and writes it to path via a temp file in the
// same directory followed by os.Rename, so a concurrent Cache.Reload (or an
// operator's own read) never observes a half-written file. The temp file's
// create/rename does generate fsnotify events, but watchForReload filters
// every event down to the exact ".changed" path, so this produces no
// spurious reload.
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

func fromProtoClientFilters(cf *pb.ClientFilters) ClientFilters {
	return ClientFilters{Hostnames: cf.GetHostnames(), Labels: cf.GetLabels()}
}

func fromProtoObjectFilters(filters []*pb.ObjectFilter) []ObjectFilter {
	out := make([]ObjectFilter, len(filters))
	for i, f := range filters {
		out[i] = ObjectFilter{Path: f.GetPath(), Include: f.GetInclude(), Exclude: f.GetExclude()}
	}
	return out
}

// CreatePolicy validates req, allocates a filename from a slug of the
// policy's name (appending "-2", "-3", ... on collision), and atomically
// writes the new policy file into policies/backup/ (the only policy type
// this RPC creates today) before reloading the cache. The filename it
// picks is permanent for that policy's lifetime -- it's what the policy's
// id derives from.
func (s *policyServerServer) CreatePolicy(ctx context.Context, req *pb.CreatePolicyRequest) (*pb.Policy, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	p := &BackupPolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{Name: req.GetName(), CreatedAt: now, UpdatedAt: now},
			ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
		},
		ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
		RPO:           req.GetRpo(),
		BackupWindow:  req.GetBackupWindow(),
		Destination:   req.GetDestination(),
	}
	if err := p.Validate(); err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	slug := slugify(p.Metadata.Name)
	if slug == "" {
		return nil, status.Error(codes.InvalidArgument, "name must contain at least one alphanumeric character")
	}

	// Every policy created through this RPC is type "backup" -- the only
	// type that exists today. A future second type needs its own creation
	// path once it exists; see
	// docs/superpowers/specs/2026-07-20-policy-type-subfolders-design.md.
	backupDir := filepath.Join(s.policiesDir, "backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		s.logger.Error("CreatePolicy: failed to create backup policies directory", "path", backupDir, "error", err)
		return nil, status.Error(codes.Internal, "failed to create policy type directory")
	}
	filename, err := uniqueFilename(backupDir, slug)
	if err != nil {
		s.logger.Error("CreatePolicy: filename allocation failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to allocate a policy filename")
	}
	filePath := filepath.Join(backupDir, filename)

	if err := atomicWriteJSON(filePath, p); err != nil {
		s.logger.Error("CreatePolicy: write failed", "path", filePath, "error", err)
		return nil, status.Error(codes.Internal, "failed to write policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("CreatePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after write")
	}

	created, ok := s.cache.FindBySourcePath(filePath)
	if !ok {
		return nil, status.Error(codes.Internal, "policy not found in cache after create")
	}
	s.logger.Info("CreatePolicy", "id", created.Meta().ID, "name", created.Meta().Name, "path", filePath)
	return created.ToProto(true), nil
}

// UpdatePolicy fully replaces an existing policy's editable fields,
// identified by id. The on-disk filename -- and therefore the policy's id,
// which derives from it -- never changes; only the file's content does.
// CreatedAt is preserved from the existing record; UpdatedAt is set to now.
func (s *policyServerServer) UpdatePolicy(ctx context.Context, req *pb.UpdatePolicyRequest) (*pb.Policy, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}

	p := &BackupPolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{Name: req.GetName(), CreatedAt: existing.Meta().CreatedAt, UpdatedAt: time.Now().UTC()},
			ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
		},
		ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
		RPO:           req.GetRpo(),
		BackupWindow:  req.GetBackupWindow(),
		Destination:   req.GetDestination(),
	}
	if err := p.Validate(); err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := atomicWriteJSON(existing.Path(), p); err != nil {
		s.logger.Error("UpdatePolicy: write failed", "path", existing.Path(), "error", err)
		return nil, status.Error(codes.Internal, "failed to write policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("UpdatePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after write")
	}

	updated, ok := s.cache.FindBySourcePath(existing.Path())
	if !ok {
		return nil, status.Error(codes.Internal, "policy not found in cache after update")
	}
	s.logger.Info("UpdatePolicy", "id", updated.Meta().ID, "name", updated.Meta().Name, "path", existing.Path())
	return updated.ToProto(true), nil
}

// DeletePolicy removes the policy file backing id and reloads the cache.
func (s *policyServerServer) DeletePolicy(ctx context.Context, req *pb.DeletePolicyRequest) (*pb.DeletePolicyResponse, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}

	if err := os.Remove(existing.Path()); err != nil {
		s.logger.Error("DeletePolicy: remove failed", "path", existing.Path(), "error", err)
		return nil, status.Error(codes.Internal, "failed to remove policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("DeletePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after delete")
	}

	s.logger.Info("DeletePolicy", "id", req.GetId(), "path", existing.Path())
	return &pb.DeletePolicyResponse{}, nil
}
```

- [ ] **Step 7: Replace `policy_test.go`**

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePolicyFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestParsePolicyFile_SetsTypeFromArgument(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)

	p, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	assert.Equal(t, "backup", p.Kind())
}

func TestParsePolicyFile_ComputesDeterministicPolicyID(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup"},
		"object_filters": [{"path": "/var/www"}]
	}`)

	p1, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p2, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)

	assert.NotEmpty(t, p1.Meta().ID)
	assert.Equal(t, p1.Meta().ID, p2.Meta().ID, "same filename must yield the same policy ID every parse")
}

func TestParsePolicyFile_DifferentFilenamesYieldDifferentPolicyIDs(t *testing.T) {
	dir := t.TempDir()
	pathA := writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "same-name"}}`)
	pathB := writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "same-name"}}`)

	pa, err := parsePolicyFile(pathA, "backup")
	require.NoError(t, err)
	pb, err := parsePolicyFile(pathB, "backup")
	require.NoError(t, err)

	assert.NotEqual(t, pa.Meta().ID, pb.Meta().ID, "identical metadata.name in different files must not collide")
}

func TestParsePolicyFile_MissingFileFails(t *testing.T) {
	_, err := parsePolicyFile(filepath.Join(t.TempDir(), "does-not-exist.json"), "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidJSONFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `not json`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_UnrecognizedTypeFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)

	_, err := parsePolicyFile(path, "quux")
	assert.Error(t, err)
}
```

(`TestParsePolicyFile_SameBasenameInDifferentTypeSubfoldersYieldsDifferentIDs` from the old `policy_test.go` is intentionally dropped here — it compared `"backup"` against an arbitrary `"other"` type string, which `parsePolicyFile` no longer accepts. It's reintroduced in Task 3 comparing `"backup"` against the real `"storage"` type.)

- [ ] **Step 8: Create `backup_policy_test.go`**

```go
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicyFile_ValidPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-10T00:00:00Z"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)

	got, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p, ok := got.(*BackupPolicy)
	require.True(t, ok)
	assert.Equal(t, "nightly-web-backup", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.Metadata.CreatedAt)
	assert.Equal(t, []string{"web-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, p.ClientFilters.Labels)
	require.Len(t, p.ObjectFilters, 1)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, []string{"*.html", "*.css"}, p.ObjectFilters[0].Include)
	assert.Equal(t, []string{"*.tmp"}, p.ObjectFilters[0].Exclude)
	assert.NotEmpty(t, p.ObjectFilters[0].ID)
	assert.Equal(t, "24h", p.RPO)
	assert.Equal(t, []string{"0 2 * * *", "0 20 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_ObjectFiltersAtDifferentIndicesGetDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "multi.json", `{
		"metadata": {"name": "multi"},
		"object_filters": [{"path": "/a"}, {"path": "/b"}]
	}`)

	got, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p, ok := got.(*BackupPolicy)
	require.True(t, ok)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEmpty(t, p.ObjectFilters[0].ID)
	assert.NotEmpty(t, p.ObjectFilters[1].ID)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID)
}

func TestParsePolicyFile_ObjectFiltersWithIdenticalPathGetDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "duplicate-path.json", `{
		"metadata": {"name": "duplicate-path"},
		"object_filters": [
			{"path": "/var/www", "include": ["*.html"]},
			{"path": "/var/www", "exclude": ["*.log"]}
		]
	}`)

	got, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p, ok := got.(*BackupPolicy)
	require.True(t, ok)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID, "two object filters sharing a path must still get distinct IDs")
}

func TestParsePolicyFile_ObjectFilterOmitsIncludeExclude(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "minimal.json", `{
		"metadata": {"name": "minimal"},
		"object_filters": [{"path": "/data"}]
	}`)

	got, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p, ok := got.(*BackupPolicy)
	require.True(t, ok)
	require.Len(t, p.ObjectFilters, 1)
	assert.Equal(t, "/data", p.ObjectFilters[0].Path)
	assert.Empty(t, p.ObjectFilters[0].Include)
	assert.Empty(t, p.ObjectFilters[0].Exclude)
}

func TestParsePolicyFile_InvalidIncludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "include": ["["]}]
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidExcludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "exclude": ["["]}]
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_MissingNameFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{"metadata": {"name": ""}}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidHostnamePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"client_filters": {"hostnames": ["["]}
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestBackupPolicy_ValidateValidPolicyReturnsNil(t *testing.T) {
	p := &BackupPolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{Name: "ok"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-*"}},
		},
		ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"*.sql"}, Exclude: []string{"*.tmp"}}},
	}
	assert.NoError(t, p.Validate())
}

func TestBackupPolicy_ValidateMissingNameFails(t *testing.T) {
	assert.Error(t, (&BackupPolicy{}).Validate())
}

func TestBackupPolicy_ValidateInvalidHostnamePatternFails(t *testing.T) {
	p := &BackupPolicy{PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}, ClientFilters: ClientFilters{Hostnames: []string{"["}}}}
	assert.Error(t, p.Validate())
}

func TestBackupPolicy_ValidateInvalidIncludePatternFails(t *testing.T) {
	p := &BackupPolicy{
		PolicyBase:    PolicyBase{Metadata: Metadata{Name: "x"}},
		ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"["}}},
	}
	assert.Error(t, p.Validate())
}

func TestBackupPolicy_ValidateInvalidExcludePatternFails(t *testing.T) {
	p := &BackupPolicy{
		PolicyBase:    PolicyBase{Metadata: Metadata{Name: "x"}},
		ObjectFilters: []ObjectFilter{{Path: "/data", Exclude: []string{"["}}},
	}
	assert.Error(t, p.Validate())
}
```

- [ ] **Step 9: Fix `filter_test.go`**

Modify `src/cmd/policy-server/filter_test.go:77` — the only line that references the old flat `Policy` type:

```go
			p := PolicyBase{ClientFilters: tc.filters}
```

(replacing `p := Policy{ClientFilters: tc.filters}`)

- [ ] **Step 10: Replace `cache_test.go`**

```go
package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestCache_ReloadLoadsValidPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "b.json", `{"metadata": {"name": "policy-b"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	assert.Len(t, got, 2)
}

func TestCache_ReloadSkipsMalformedFileKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "good.json", `{"metadata": {"name": "policy-good"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "bad.json", `not json`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1)
	assert.Equal(t, "policy-good", got[0].Meta().Name)
}

func TestCache_ReloadAllFilesFailKeepsPreviousCache(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "good.json", `{"metadata": {"name": "policy-good"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Len(t, c.Policies(), 1)

	require.NoError(t, os.Remove(filepath.Join(dir, "backup", "good.json")))
	writePolicyFile(t, filepath.Join(dir, "backup"), "bad.json", `not json`)

	err := c.Reload(dir, testLogger())
	assert.Error(t, err)
	got := c.Policies()
	require.Len(t, got, 1, "previous good cache must be kept")
	assert.Equal(t, "policy-good", got[0].Meta().Name)
}

func TestCache_ReloadEmptyDirectoryYieldsEmptyPolicies(t *testing.T) {
	dir := t.TempDir()

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	assert.Empty(t, c.Policies())
}

func TestCache_ReloadSkipsUnrecognizedTypeSubfolder(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "other"), "b.json", `{"metadata": {"name": "policy-b"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1, "a subfolder name absent from policyParsers must be skipped, not loaded")
	assert.Equal(t, "policy-a", got[0].Meta().Name)
	assert.Equal(t, "backup", got[0].Kind())
}

func TestCache_ReloadSkipsFileDirectlyUnderPoliciesDir(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "stray.json", `{"metadata": {"name": "stray"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1, "a *.json file with no type subfolder must not be loaded")
	assert.Equal(t, "policy-a", got[0].Meta().Name)
}

func TestCache_PoliciesReturnsSnapshotCopy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{
		"metadata": {"name": "policy-a"},
		"client_filters": {
			"hostnames": ["host1", "host2"],
			"labels": {"env": "prod", "team": "platform"}
		},
		"object_filters": [{"path": "/data/*", "include": ["*.sql"], "exclude": ["*.tmp"]}],
		"rpo": "1h",
		"backup_window": ["08:00", "12:00"]
	}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	bp, ok := got[0].(*BackupPolicy)
	require.True(t, ok)

	bp.Metadata.Name = "mutated-name"
	bp.ClientFilters.Hostnames[0] = "mutated-host"
	bp.ClientFilters.Labels["env"] = "dev"
	bp.ObjectFilters[0].Path = "/mutated/*"
	bp.ObjectFilters[0].Include[0] = "mutated"
	bp.ObjectFilters[0].Exclude[0] = "mutated"
	bp.BackupWindow[0] = "23:00"

	got2 := c.Policies()
	bp2, ok := got2[0].(*BackupPolicy)
	require.True(t, ok)
	assert.Equal(t, "policy-a", bp2.Metadata.Name, "mutating Metadata.Name in returned snapshot must not affect cache")
	assert.Equal(t, "host1", bp2.ClientFilters.Hostnames[0], "mutating Hostnames in returned snapshot must not affect cache")
	assert.Equal(t, "prod", bp2.ClientFilters.Labels["env"], "mutating Labels in returned snapshot must not affect cache")
	assert.Equal(t, "/data/*", bp2.ObjectFilters[0].Path, "mutating ObjectFilters in returned snapshot must not affect cache")
	assert.Equal(t, "*.sql", bp2.ObjectFilters[0].Include[0], "mutating ObjectFilters[].Include in returned snapshot must not affect cache")
	assert.Equal(t, "*.tmp", bp2.ObjectFilters[0].Exclude[0], "mutating ObjectFilters[].Exclude in returned snapshot must not affect cache")
	assert.Equal(t, "08:00", bp2.BackupWindow[0], "mutating BackupWindow in returned snapshot must not affect cache")
	assert.NotEmpty(t, bp2.ObjectFilters[0].ID, "ObjectFilter.ID must survive the snapshot copy")
	assert.Equal(t, "backup", bp2.Type, "Type must survive the snapshot copy")
}

func TestCache_FindByIDReturnsMatchingPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	want := c.Policies()[0]
	got, ok := c.FindByID(want.Meta().ID)
	require.True(t, ok)
	assert.Equal(t, "policy-a", got.Meta().Name)
	assert.Equal(t, filepath.Join(dir, "backup", "a.json"), got.Path())
}

func TestCache_FindByIDUnknownIDReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.FindByID("does-not-exist")
	assert.False(t, ok)
}

func TestCache_FindBySourcePathReturnsMatchingPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got, ok := c.FindBySourcePath(filepath.Join(dir, "backup", "a.json"))
	require.True(t, ok)
	assert.Equal(t, "policy-a", got.Meta().Name)
}

func TestCache_FindBySourcePathUnknownPathReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.FindBySourcePath("/does/not/exist.json")
	assert.False(t, ok)
}
```

(`TestCache_ReloadTagsPoliciesWithSubfolderNameAsType` is replaced by `TestCache_ReloadSkipsUnrecognizedTypeSubfolder` above — it tested the old "load unknown types generically" behavior this task intentionally removes; see design doc's "Behavior change from `2026-07-20-policy-type-subfolders-design.md`".)

- [ ] **Step 11: Fix `write_test.go` field accesses**

Modify `src/cmd/policy-server/write_test.go`. Every reference is to internal-Go-struct fields on values returned by `srv.cache.Policies()`/`FindByID` (not to `*pb.Policy`, which is unaffected). Apply these exact replacements:

Line 196: `original := srv.cache.Policies()[0]` — unchanged; but line 199 `Id: original.Metadata.ID,` → `Id: original.Meta().ID,`.

Line 228: `original := srv.cache.Policies()[0]` — unchanged; line 233 `Id: original.Metadata.ID, Name: ""` → `Id: original.Meta().ID, Name: ""`.

Line 247: `original := srv.cache.Policies()[0]` — unchanged; line 249 `Id: original.Metadata.ID` → `Id: original.Meta().ID`.

`TestDeletePolicy_LeavesOtherPoliciesIntact` (around line 273-280):

```go
func TestDeletePolicy_LeavesOtherPoliciesIntact(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "b.json", `{"metadata": {"name": "policy-b"}}`)
	srv := newTestWriteServer(t, dir)
	var target Policy
	for _, p := range srv.cache.Policies() {
		if p.Meta().Name == "policy-a" {
			target = p
		}
	}

	_, err := srv.DeletePolicy(context.Background(), &pb.DeletePolicyRequest{Id: target.Meta().ID})

	require.NoError(t, err)
	remaining := srv.cache.Policies()
	require.Len(t, remaining, 1)
	assert.Equal(t, "policy-b", remaining[0].Meta().Name)
}
```

Every other test in `write_test.go` (the `TestSlugify`/`TestUniqueFilename*`/`TestCreatePolicy_*`/`TestUpdatePolicy_OverwritesFileKeepsIDAndCreatedAt`/`TestUpdatePolicy_UnknownIDReturnsNotFound`/`TestDeletePolicy_RemovesFileAndReloads`/`TestDeletePolicy_UnknownIDReturnsNotFound`/`TestCreatePolicy_ResponseIncludesBackupType` tests) only reads/writes `*pb.CreatePolicyRequest`/`*pb.UpdatePolicyRequest`/`*pb.Policy` or the filesystem — no changes needed.

- [ ] **Step 12: Run the full test suite and confirm it's green**

Run: `cd src && go build ./... && go test ./cmd/policy-server/...`
Expected: `ok` for `cmd/policy-server`, zero test failures, zero compile errors. Since this task changes only `policy-server`'s internal `Policy` Go type (not the proto or any other package's code), no other package should need touching — confirm with `cd src && go build ./...` (whole module) as well.

- [ ] **Step 13: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/policy-server/policy.go src/cmd/policy-server/backup_policy.go \
  src/cmd/policy-server/filter.go src/cmd/policy-server/cache.go \
  src/cmd/policy-server/server.go src/cmd/policy-server/write.go \
  src/cmd/policy-server/policy_test.go src/cmd/policy-server/backup_policy_test.go \
  src/cmd/policy-server/filter_test.go src/cmd/policy-server/cache_test.go \
  src/cmd/policy-server/write_test.go
git commit -m "refactor(policy-server): Policy interface with BackupPolicy implementation

Zero behavior change -- prep for adding a second policy type. Policy is
now an interface implemented by BackupPolicy (embedding a shared
PolicyBase), with a policyParsers registry dispatching Cache.Reload's
per-subfolder parsing instead of one fixed struct."
```

---

## Task 2: Proto changes for the storage policy type

Pure wire-schema plumbing: additive fields only, no new Go application logic yet. Verifies the whole module still builds and every existing test (including `api-server`'s) still passes with the new fields simply unused.

**Files:**
- Modify: `src/api/policyserver.proto`
- Generated (via `make proto`, do not hand-edit): `src/api/policyserver.pb.go`, `src/api/policyserver_grpc.pb.go`

**Interfaces:**
- Consumes: nothing from Task 1 (proto is independent of the Go-side refactor).
- Produces: `pb.Policy` gains `Hostname string`, `Port int32`, `Config string` (getters `GetHostname()`, `GetPort()`, `GetConfig()`). `pb.CreatePolicyRequest` gains `Type string`, `Hostname string`, `Port int32`, `Config string`. `pb.UpdatePolicyRequest` gains `Hostname string`, `Port int32`, `Config string` (no `Type`).

- [ ] **Step 1: Edit `src/api/policyserver.proto`**

Modify the `Policy` message (`src/api/policyserver.proto:48-73`):

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  // Duration string, e.g. "24h" (time.ParseDuration format). policy-server
  // never parses or evaluates this -- opaque pass-through data.
  string rpo = 5;
  // List of cron expressions (5-field). policy-server never parses or
  // evaluates these -- opaque pass-through data.
  repeated string backup_window = 6;
  string destination = 7;
  // policy-server-computed, deterministic from the policy file's name --
  // stable across reloads, changes only if the file is renamed. Not
  // present in the on-disk policy JSON schema.
  string id = 8;
  // Only ever populated by ListPolicies/CreatePolicy/UpdatePolicy -- omitted
  // by GetPolicies so a node never learns another node's targeting rules
  // from a policy that already matched its own identity.
  ClientFilters client_filters = 9;
  // Derived from the name of the subfolder the policy file was loaded from
  // (e.g. "backup" for policies/backup/*.json) -- never read from or
  // written to the on-disk policy JSON. Populated by both GetPolicies and
  // ListPolicies.
  string type = 10;
  // storage policy only -- unset/empty for a backup policy.
  string hostname = 11;
  // storage policy only.
  int32 port = 12;
  // storage policy only -- opaque JSON text, verbatim passthrough. Never
  // parsed or interpreted by policy-server beyond checking well-formedness.
  string config = 13;
}
```

Modify `CreatePolicyRequest` (`src/api/policyserver.proto:75-84`):

```proto
message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  // Any id set on an entry here is ignored -- object filter IDs are always
  // server-computed from their position in this list.
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  string destination = 6;
  // "backup" or "storage" -- required. Determines which of the fields above
  // (backup) or below (storage) are valid; mixing fields from both types is
  // rejected.
  string type = 7;
  string hostname = 8;
  int32 port = 9;
  string config = 10;
}
```

Modify `UpdatePolicyRequest` (`src/api/policyserver.proto:86-96`):

```proto
message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  // Full replacement of object_filters, not a patch -- reordering or
  // inserting entries changes the affected filters' ids.
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  // A policy's type is immutable via UpdatePolicy -- there is no type field
  // here. hostname/port/config are only valid when the policy being updated
  // is already type "storage"; object_filters/rpo/backup_window/destination
  // are only valid when it's already type "backup".
  string hostname = 8;
  int32 port = 9;
  string config = 10;
}
```

- [ ] **Step 2: Regenerate the proto Go code**

Run: `cd /home/alex/miniprotector && make proto`
Expected: `Protobuf code generated in src/api/` with no errors. Requires `protoc`, `protoc-gen-go`, and `protoc-gen-go-grpc` on `PATH` (see `make check-deps`); if any is missing, install per this repo's existing dev-environment setup before continuing -- do not hand-edit `policyserver.pb.go`/`policyserver_grpc.pb.go`.

- [ ] **Step 3: Confirm the whole module still builds and tests still pass**

Run: `cd src && go build ./... && go test ./...`
Expected: no compile errors anywhere in the module (the new fields are additive, so every existing caller of `pb.Policy`/`pb.CreatePolicyRequest`/`pb.UpdatePolicyRequest` — `api-server`, `policy-server` itself, any test fixtures — keeps compiling unchanged), and every existing test still passes since nothing yet sets or reads the new fields.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector
git add src/api/policyserver.proto src/api/policyserver.pb.go src/api/policyserver_grpc.pb.go
git commit -m "feat(policy-server): add storage policy fields to proto

Additive-only: Policy gains hostname/port/config; CreatePolicyRequest
also gains a required type selector. UpdatePolicyRequest gains
hostname/port/config but no type -- a policy's type is immutable via
UpdatePolicy. Nothing sets or reads these fields yet."
```

---

## Task 3: Add `StoragePolicy`

**Files:**
- Create: `src/cmd/policy-server/storage_policy.go`
- Modify: `src/cmd/policy-server/policy.go:56-58` (register `"storage"` in `policyParsers`)
- Create: `src/cmd/policy-server/storage_policy_test.go`
- Modify: `src/cmd/policy-server/cache_test.go` (add one mixed-type test)

**Interfaces:**
- Consumes: `Policy` interface, `PolicyBase`, `validateCommon` from Task 1; `pb.Policy.Hostname/Port/Config` from Task 2.
- Produces: `type StoragePolicy struct{ PolicyBase; Hostname string; Port int; Config json.RawMessage }`, `func parseStoragePolicyJSON(data []byte) (Policy, error)`.

- [ ] **Step 1: Create `storage_policy.go`**

```go
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
```

(No `setIdentity` override needed: `StoragePolicy` has no per-entry sub-IDs like `BackupPolicy`'s `ObjectFilters`, so it inherits `PolicyBase.setIdentity` via promotion — `*StoragePolicy` gets the pointer-receiver method on its embedded `PolicyBase` for free.)

- [ ] **Step 2: Register `"storage"` in `policy.go`'s `policyParsers`**

Modify `src/cmd/policy-server/policy.go` — change:

```go
var policyParsers = map[string]func(data []byte) (Policy, error){
	"backup": parseBackupPolicyJSON,
}
```

to:

```go
var policyParsers = map[string]func(data []byte) (Policy, error){
	"backup":  parseBackupPolicyJSON,
	"storage": parseStoragePolicyJSON,
}
```

- [ ] **Step 3: Create `storage_policy_test.go`**

```go
package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicyFile_StoragePolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "east-1.json", `{
		"metadata": {"name": "east-1-storage"},
		"client_filters": {"hostnames": ["storage-east-*"]},
		"hostname": "storage-east-1.internal",
		"port": 9400,
		"config": {"backend": "filesystem", "root": "/data/storage"}
	}`)

	got, err := parsePolicyFile(path, "storage")
	require.NoError(t, err)
	p, ok := got.(*StoragePolicy)
	require.True(t, ok)
	assert.Equal(t, "east-1-storage", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, []string{"storage-east-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, "storage-east-1.internal", p.Hostname)
	assert.Equal(t, 9400, p.Port)
	assert.JSONEq(t, `{"backend": "filesystem", "root": "/data/storage"}`, string(p.Config))
	assert.Equal(t, "storage", p.Kind())
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_SameBasenameInDifferentTypeSubfoldersYieldsDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	pathBackup := writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}}`)
	pathStorage := writePolicyFile(t, filepath.Join(dir, "storage"), "nightly.json", `{
		"metadata": {"name": "nightly"}, "hostname": "h", "port": 1, "config": {}
	}`)

	pBackup, err := parsePolicyFile(pathBackup, "backup")
	require.NoError(t, err)
	pStorage, err := parsePolicyFile(pathStorage, "storage")
	require.NoError(t, err)

	assert.NotEqual(t, pBackup.Meta().ID, pStorage.Meta().ID, "same basename in different type subfolders must not collide")
}

func TestStoragePolicy_ValidateValidPolicyReturnsNil(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "ok"}},
		Hostname:   "storage-1.internal",
		Port:       9400,
		Config:     []byte(`{"backend": "filesystem"}`),
	}
	assert.NoError(t, p.Validate())
}

func TestStoragePolicy_ValidateMissingNameFails(t *testing.T) {
	p := &StoragePolicy{Hostname: "h", Port: 1, Config: []byte(`{}`)}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidateMissingHostnameFails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Port:       9400,
		Config:     []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidatePortZeroFails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       0,
		Config:     []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidatePortAbove65535Fails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       70000,
		Config:     []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidateEmptyConfigFails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       9400,
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidateMalformedConfigJSONFails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       9400,
		Config:     []byte(`not json`),
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_CloneDeepCopiesConfig(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       9400,
		Config:     []byte(`{"a":1}`),
	}
	cloned := p.Clone().(*StoragePolicy)
	cloned.Config[2] = 'X'
	assert.Equal(t, `{"a":1}`, string(p.Config), "mutating the clone's Config must not affect the original")
}
```

- [ ] **Step 4: Add a mixed-type test to `cache_test.go`**

Append to `src/cmd/policy-server/cache_test.go`:

```go
func TestCache_ReloadLoadsBackupAndStoragePoliciesTogether(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "storage"), "b.json", `{
		"metadata": {"name": "policy-b"}, "hostname": "h", "port": 9400, "config": {}
	}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 2)
	kinds := map[string]string{}
	for _, p := range got {
		kinds[p.Meta().Name] = p.Kind()
	}
	assert.Equal(t, "backup", kinds["policy-a"])
	assert.Equal(t, "storage", kinds["policy-b"])
}
```

- [ ] **Step 5: Run the test suite and confirm it's green**

Run: `cd src && go test ./cmd/policy-server/... -run 'Storage|Cache' -v`
Expected: every `TestStoragePolicy_*`, `TestParsePolicyFile_StoragePolicyParsesAllFields`, `TestParsePolicyFile_SameBasenameInDifferentTypeSubfoldersYieldsDifferentIDs`, and `TestCache_*` test passes.

Run: `cd src && go test ./...`
Expected: entire module still green (this task only adds new code paths; nothing existing changed behavior).

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/policy-server/storage_policy.go src/cmd/policy-server/storage_policy_test.go \
  src/cmd/policy-server/policy.go src/cmd/policy-server/cache_test.go
git commit -m "feat(policy-server): add StoragePolicy type

Registers \"storage\" in policyParsers, so policies/storage/*.json files
are now loaded, validated (hostname required, port 1-65535, config
well-formed JSON), cached, and served via GetPolicies/ListPolicies --
CreatePolicy/UpdatePolicy don't support writing this type yet."
```

---

## Task 4: `CreatePolicy`/`UpdatePolicy` type-aware write dispatch

**Files:**
- Modify: `src/cmd/policy-server/write.go` (full rewrite)
- Modify: `src/cmd/policy-server/write_test.go` (add `Type: "backup"` to existing `CreatePolicyRequest` fixtures; add new tests)

**Interfaces:**
- Consumes: `BackupPolicy`/`StoragePolicy` from Tasks 1/3; `pb.CreatePolicyRequest.Type/Hostname/Port/Config`, `pb.UpdatePolicyRequest.Hostname/Port/Config` from Task 2.
- Produces: `func buildPolicyForCreate(req *pb.CreatePolicyRequest, now time.Time) (Policy, error)`, `func buildPolicyForUpdate(req *pb.UpdatePolicyRequest, kind string, existingMeta Metadata, now time.Time) (Policy, error)`.

- [ ] **Step 1: Replace `write.go`**

```go
// The write RPCs (CreatePolicy, UpdatePolicy, DeletePolicy): policy-server
// is the sole writer of its own policy files, so a write RPC validates the
// proposed content, atomically writes/removes the file, then synchronously
// reloads its own in-memory cache before responding -- the caller only ever
// sees a state the cache has already picked up. See
// docs/superpowers/specs/2026-07-18-policy-management-api-design.md and
// docs/superpowers/specs/2026-07-28-storage-policy-type-design.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify lowercases name and collapses every run of non-alphanumeric
// characters into a single "-", trimming any leading/trailing "-" --
// "Nightly DB Backup!" -> "nightly-db-backup".
func slugify(name string) string {
	slug := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	return strings.Trim(slug, "-")
}

// uniqueFilename returns a filename in dir based on slug that doesn't
// already exist: "<slug>.json" if free, otherwise "<slug>-2.json",
// "<slug>-3.json", etc.
func uniqueFilename(dir, slug string) (string, error) {
	candidate := slug + ".json"
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, candidate)); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", fmt.Errorf("check %s: %w", filepath.Join(dir, candidate), err)
		}
		candidate = fmt.Sprintf("%s-%d.json", slug, i)
	}
}

// atomicWriteJSON marshals v and writes it to path via a temp file in the
// same directory followed by os.Rename, so a concurrent Cache.Reload (or an
// operator's own read) never observes a half-written file. The temp file's
// create/rename does generate fsnotify events, but watchForReload filters
// every event down to the exact ".changed" path, so this produces no
// spurious reload.
func atomicWriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".policy-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
	}
	return nil
}

func fromProtoClientFilters(cf *pb.ClientFilters) ClientFilters {
	return ClientFilters{Hostnames: cf.GetHostnames(), Labels: cf.GetLabels()}
}

func fromProtoObjectFilters(filters []*pb.ObjectFilter) []ObjectFilter {
	out := make([]ObjectFilter, len(filters))
	for i, f := range filters {
		out[i] = ObjectFilter{Path: f.GetPath(), Include: f.GetInclude(), Exclude: f.GetExclude()}
	}
	return out
}

// storageFieldsSet reports whether any storage-only field is non-default --
// used to reject a request mixing storage fields into a backup policy.
func storageFieldsSet(hostname string, port int32, config string) bool {
	return hostname != "" || port != 0 || config != ""
}

// backupFieldsSet reports whether any backup-only field is non-default --
// used to reject a request mixing backup fields into a storage policy.
func backupFieldsSet(objectFilters []*pb.ObjectFilter, rpo string, backupWindow []string, destination string) bool {
	return len(objectFilters) > 0 || rpo != "" || len(backupWindow) > 0 || destination != ""
}

// buildPolicyForCreate constructs the concrete Policy req.GetType() asks
// for, rejecting a request that also sets fields belonging to the other
// type. The returned Policy's Metadata.ID/SourcePath/Type are left zero --
// Cache.Reload assigns them once the caller writes the file and reloads.
func buildPolicyForCreate(req *pb.CreatePolicyRequest, now time.Time) (Policy, error) {
	base := PolicyBase{
		Metadata:      Metadata{Name: req.GetName(), CreatedAt: now, UpdatedAt: now},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
	}
	switch req.GetType() {
	case "backup":
		if storageFieldsSet(req.GetHostname(), req.GetPort(), req.GetConfig()) {
			return nil, fmt.Errorf("a backup policy must not set hostname/port/config")
		}
		return &BackupPolicy{
			PolicyBase:    base,
			ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
			RPO:           req.GetRpo(),
			BackupWindow:  req.GetBackupWindow(),
			Destination:   req.GetDestination(),
		}, nil
	case "storage":
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetDestination()) {
			return nil, fmt.Errorf("a storage policy must not set object_filters/rpo/backup_window/destination")
		}
		return &StoragePolicy{
			PolicyBase: base,
			Hostname:   req.GetHostname(),
			Port:       int(req.GetPort()),
			Config:     json.RawMessage(req.GetConfig()),
		}, nil
	default:
		return nil, fmt.Errorf("unknown policy type %q", req.GetType())
	}
}

// buildPolicyForUpdate constructs the same concrete type as the existing
// policy being updated -- a policy's type is immutable via UpdatePolicy, so
// kind comes from the existing record (existing.Kind()), not the request.
func buildPolicyForUpdate(req *pb.UpdatePolicyRequest, kind string, existingMeta Metadata, now time.Time) (Policy, error) {
	base := PolicyBase{
		Metadata:      Metadata{Name: req.GetName(), CreatedAt: existingMeta.CreatedAt, UpdatedAt: now},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
	}
	switch kind {
	case "backup":
		if storageFieldsSet(req.GetHostname(), req.GetPort(), req.GetConfig()) {
			return nil, fmt.Errorf("a backup policy must not set hostname/port/config")
		}
		return &BackupPolicy{
			PolicyBase:    base,
			ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
			RPO:           req.GetRpo(),
			BackupWindow:  req.GetBackupWindow(),
			Destination:   req.GetDestination(),
		}, nil
	case "storage":
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetDestination()) {
			return nil, fmt.Errorf("a storage policy must not set object_filters/rpo/backup_window/destination")
		}
		return &StoragePolicy{
			PolicyBase: base,
			Hostname:   req.GetHostname(),
			Port:       int(req.GetPort()),
			Config:     json.RawMessage(req.GetConfig()),
		}, nil
	default:
		return nil, fmt.Errorf("existing policy has unknown type %q", kind)
	}
}

// CreatePolicy validates req, allocates a filename from a slug of the
// policy's name (appending "-2", "-3", ... on collision), and atomically
// writes the new policy file into policies/<type>/ (req.GetType(), creating
// that subdirectory if missing) before reloading the cache. The filename it
// picks is permanent for that policy's lifetime -- it's what the policy's
// id derives from.
func (s *policyServerServer) CreatePolicy(ctx context.Context, req *pb.CreatePolicyRequest) (*pb.Policy, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	p, err := buildPolicyForCreate(req, now)
	if err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := p.Validate(); err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	slug := slugify(p.Meta().Name)
	if slug == "" {
		return nil, status.Error(codes.InvalidArgument, "name must contain at least one alphanumeric character")
	}

	typeDir := filepath.Join(s.policiesDir, req.GetType())
	if err := os.MkdirAll(typeDir, 0o755); err != nil {
		s.logger.Error("CreatePolicy: failed to create policy type directory", "path", typeDir, "error", err)
		return nil, status.Error(codes.Internal, "failed to create policy type directory")
	}
	filename, err := uniqueFilename(typeDir, slug)
	if err != nil {
		s.logger.Error("CreatePolicy: filename allocation failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to allocate a policy filename")
	}
	filePath := filepath.Join(typeDir, filename)

	if err := atomicWriteJSON(filePath, p); err != nil {
		s.logger.Error("CreatePolicy: write failed", "path", filePath, "error", err)
		return nil, status.Error(codes.Internal, "failed to write policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("CreatePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after write")
	}

	created, ok := s.cache.FindBySourcePath(filePath)
	if !ok {
		return nil, status.Error(codes.Internal, "policy not found in cache after create")
	}
	s.logger.Info("CreatePolicy", "id", created.Meta().ID, "name", created.Meta().Name, "path", filePath)
	return created.ToProto(true), nil
}

// UpdatePolicy fully replaces an existing policy's editable fields,
// identified by id. The on-disk filename -- and therefore the policy's id,
// which derives from it -- never changes; only the file's content does. A
// policy's type is immutable via UpdatePolicy. CreatedAt is preserved from
// the existing record; UpdatedAt is set to now.
func (s *policyServerServer) UpdatePolicy(ctx context.Context, req *pb.UpdatePolicyRequest) (*pb.Policy, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}

	p, err := buildPolicyForUpdate(req, existing.Kind(), existing.Meta(), time.Now().UTC())
	if err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := p.Validate(); err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := atomicWriteJSON(existing.Path(), p); err != nil {
		s.logger.Error("UpdatePolicy: write failed", "path", existing.Path(), "error", err)
		return nil, status.Error(codes.Internal, "failed to write policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("UpdatePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after write")
	}

	updated, ok := s.cache.FindBySourcePath(existing.Path())
	if !ok {
		return nil, status.Error(codes.Internal, "policy not found in cache after update")
	}
	s.logger.Info("UpdatePolicy", "id", updated.Meta().ID, "name", updated.Meta().Name, "path", existing.Path())
	return updated.ToProto(true), nil
}

// DeletePolicy removes the policy file backing id and reloads the cache.
func (s *policyServerServer) DeletePolicy(ctx context.Context, req *pb.DeletePolicyRequest) (*pb.DeletePolicyResponse, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}

	if err := os.Remove(existing.Path()); err != nil {
		s.logger.Error("DeletePolicy: remove failed", "path", existing.Path(), "error", err)
		return nil, status.Error(codes.Internal, "failed to remove policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("DeletePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after delete")
	}

	s.logger.Info("DeletePolicy", "id", req.GetId(), "path", existing.Path())
	return &pb.DeletePolicyResponse{}, nil
}
```

- [ ] **Step 2: Add `Type: "backup"` to every existing `CreatePolicyRequest` fixture in `write_test.go`**

`type` is now required, so every existing test that builds a `pb.CreatePolicyRequest` (and expects it to succeed, or to fail for a reason *other than* a missing type) needs `Type: "backup"` added. Apply these edits to `src/cmd/policy-server/write_test.go`:

`TestCreatePolicy_WritesFileAndReturnsPolicyWithID`:
```go
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "Nightly DB Backup",
		Type:          "backup",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/var/lib/postgres"}},
		Rpo:           "24h",
		BackupWindow:  []string{"0 2 * * *"},
		Destination:   "bwfs:8080",
	})
```

`TestCreatePolicy_SecondCallWithSameNameGetsDistinctFile`:
```go
	req := &pb.CreatePolicyRequest{Name: "dup", Type: "backup", Destination: "bwfs:8080"}
```

`TestCreatePolicy_MissingNameReturnsInvalidArgument` (add `Type: "backup"` so this test exercises the name-validation path specifically, not the type-validation path):
```go
	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Type: "backup"})
```

`TestCreatePolicy_InvalidGlobPatternReturnsInvalidArgumentAndWritesNoFile`:
```go
	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "broken",
		Type:          "backup",
		ObjectFilters: []*pb.ObjectFilter{{Path: "/data", Include: []string{"["}}},
	})
```

`TestCreatePolicy_ConcurrentCreatesForDifferentNamesBothSurvive` (inside the goroutine's request):
```go
			_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
				Name:        name,
				Type:        "backup",
				Destination: "bwfs:8080",
			})
```

`TestCreatePolicy_ClientFiltersRoundTrip`:
```go
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:          "web",
		Type:          "backup",
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
	})
```

`TestCreatePolicy_ResponseIncludesBackupType`:
```go
	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Name: "nightly", Type: "backup"})
```

`TestUpdatePolicy_*`/`TestDeletePolicy_*` tests build `pb.UpdatePolicyRequest`/`pb.DeletePolicyRequest`, neither of which has a `Type` field — no changes needed there.

- [ ] **Step 3: Add new tests to `write_test.go`**

Append:

```go
func TestCreatePolicy_UnknownTypeReturnsInvalidArgument(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{Name: "x", Type: "quux"})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_StoragePolicyWritesIntoStorageDir(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	resp, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:     "East 1 Storage",
		Type:     "storage",
		Hostname: "storage-east-1.internal",
		Port:     9400,
		Config:   `{"backend": "filesystem"}`,
	})

	require.NoError(t, err)
	assert.NotEmpty(t, resp.Id)
	assert.Equal(t, "storage", resp.Type)
	assert.Equal(t, "storage-east-1.internal", resp.Hostname)
	assert.Equal(t, int32(9400), resp.Port)
	assert.JSONEq(t, `{"backend": "filesystem"}`, resp.Config)

	_, err = os.Stat(filepath.Join(dir, "storage", "east-1-storage.json"))
	require.NoError(t, err)
}

func TestCreatePolicy_StorageTypeWithBackupFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:        "bad",
		Type:        "storage",
		Hostname:    "h",
		Port:        9400,
		Config:      `{}`,
		Destination: "bwfs:8080",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestCreatePolicy_BackupTypeWithStorageFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	srv := newTestWriteServer(t, dir)

	_, err := srv.CreatePolicy(context.Background(), &pb.CreatePolicyRequest{
		Name:     "bad",
		Type:     "backup",
		Hostname: "h",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}

func TestUpdatePolicy_StoragePolicyRoundTripsAndTypeStaysImmutable(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1"},
		"hostname": "old-host",
		"port": 1111,
		"config": {"a": 1}
	}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	resp, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:       original.Meta().ID,
		Name:     "east-1-renamed",
		Hostname: "new-host",
		Port:     2222,
		Config:   `{"a": 2}`,
	})

	require.NoError(t, err)
	assert.Equal(t, original.Meta().ID, resp.Id, "id must stay stable across an update")
	assert.Equal(t, "storage", resp.Type, "type must stay \"storage\" -- UpdatePolicy cannot change it")
	assert.Equal(t, "new-host", resp.Hostname)
	assert.Equal(t, int32(2222), resp.Port)
	assert.JSONEq(t, `{"a": 2}`, resp.Config)
}

func TestUpdatePolicy_StorageTypeWithBackupFieldsRejected(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "storage"), "east-1.json", `{
		"metadata": {"name": "east-1"}, "hostname": "h", "port": 1111, "config": {}
	}`)
	srv := newTestWriteServer(t, dir)
	original := srv.cache.Policies()[0]

	_, err := srv.UpdatePolicy(context.Background(), &pb.UpdatePolicyRequest{
		Id:          original.Meta().ID,
		Name:        "east-1",
		Hostname:    "h",
		Port:        1111,
		Config:      `{}`,
		Destination: "bwfs:8080",
	})

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.InvalidArgument, st.Code())
}
```

- [ ] **Step 4: Run the test suite and confirm it's green**

Run: `cd src && go test ./cmd/policy-server/... -v`
Expected: every test passes, including the newly added ones and every previously-existing `TestCreatePolicy_*`/`TestUpdatePolicy_*` test (now with `Type: "backup"` where needed).

- [ ] **Step 5: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/policy-server/write.go src/cmd/policy-server/write_test.go
git commit -m "feat(policy-server): type-aware CreatePolicy/UpdatePolicy

CreatePolicy now requires a type (\"backup\" or \"storage\"), writes into
policies/<type>/ instead of a hardcoded policies/backup/, and rejects a
request mixing fields from both types. UpdatePolicy accepts
hostname/port/config but has no type field -- a policy's type stays
whatever the existing record's is."
```

---

## Task 5: `api-server` compatibility fix

**Files:**
- Modify: `src/cmd/api-server/policies.go:130-137` (add `Type: "backup"` to the one `CreatePolicyRequest` construction)
- Modify: `src/cmd/api-server/policies_test.go` (add one assertion)

**Interfaces:**
- Consumes: `pb.CreatePolicyRequest.Type` from Task 2.
- Produces: nothing new — this task only keeps `api-server` compiling and behaviorally correct now that `type` is required.

- [ ] **Step 1: Add `Type: "backup"` to `handleCreatePolicy`**

Modify `src/cmd/api-server/policies.go:130-137`:

```go
	resp, err := s.policy.CreatePolicy(r.Context(), &pb.CreatePolicyRequest{
		Name:          in.Name,
		Type:          "backup",
		ClientFilters: toProtoClientFiltersInput(in.ClientFilters),
		ObjectFilters: toProtoObjectFiltersInput(in.ObjectFilters),
		Rpo:           in.RPO,
		BackupWindow:  in.BackupWindow,
		Destination:   in.Destination,
	})
```

(`api-server` has no storage-policy input path yet — `policyInput`/`decodePolicyInput` are unchanged, so every policy created through the REST API is still, and only ever, `"backup"`.)

- [ ] **Step 2: Add an assertion to `policies_test.go`**

Find the test that asserts on `fake.lastCreateReq` after a successful `handleCreatePolicy` call (`src/cmd/api-server/policies_test.go`, around line 163-165):

```go
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "nightly", fake.lastCreateReq.GetName())
	assert.Equal(t, "backup", fake.lastCreateReq.GetType())
	assert.Equal(t, "bwfs:8080", fake.lastCreateReq.GetDestination())
```

(inserting the new `assert.Equal(t, "backup", fake.lastCreateReq.GetType())` line between the two existing assertions.)

- [ ] **Step 3: Run the test suite and confirm it's green**

Run: `cd src && go test ./cmd/api-server/...`
Expected: all tests pass.

Run: `cd src && go build ./... && go test ./...`
Expected: the whole module builds and every test across every package passes.

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector
git add src/cmd/api-server/policies.go src/cmd/api-server/policies_test.go
git commit -m "fix(api-server): set required type on CreatePolicy calls

policy-server's CreatePolicy now requires a type selector; api-server
has no storage-policy input path yet, so it always sends \"backup\",
preserving today's behavior."
```

---

## Task 6: Documentation

Per this repo's `.claude/CLAUDE.md` documentation rules: a proto change requires updating `docs/protocols/`; a feature change requires updating the affected `docs/components/` file; any merge to `main` requires a `CHANGELOG.md` entry.

**Files:**
- Modify: `docs/protocols/policy-server.md`
- Modify: `docs/components/policy-server.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing (documentation only).
- Produces: nothing (no code).

- [ ] **Step 1: Update `docs/protocols/policy-server.md`**

Replace the `message Policy { ... }` block (`docs/protocols/policy-server.md:42-53`) with:

```proto
message Policy {
  string name = 1;
  google.protobuf.Timestamp created_at = 2;
  google.protobuf.Timestamp updated_at = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string id = 8;
  ClientFilters client_filters = 9;
  string type = 10;
  string hostname = 11;
  int32 port = 12;
  string config = 13;
}
```

Replace the `message CreatePolicyRequest { ... }` block (`docs/protocols/policy-server.md:55-62`) with:

```proto
message CreatePolicyRequest {
  string name = 1;
  ClientFilters client_filters = 2;
  repeated ObjectFilter object_filters = 3;
  string rpo = 4;
  repeated string backup_window = 5;
  string destination = 6;
  string type = 7;
  string hostname = 8;
  int32 port = 9;
  string config = 10;
}
```

Replace the `message UpdatePolicyRequest { ... }` block (`docs/protocols/policy-server.md:64-72`) with:

```proto
message UpdatePolicyRequest {
  string id = 1;
  string name = 2;
  ClientFilters client_filters = 3;
  repeated ObjectFilter object_filters = 4;
  string rpo = 5;
  repeated string backup_window = 6;
  string destination = 7;
  string hostname = 8;
  int32 port = 9;
  string config = 10;
}
```

Replace the `Policy.type` bullet (`docs/protocols/policy-server.md:101-106`):

```markdown
- `Policy.type` is likewise computed, not read from the file -- derived from the name of the
  immediate subfolder the policy file lives in under `$MP_CONFIG_PATH/policies/` (`"backup"` or
  `"storage"` today). Populated by both `GetPolicies` and `ListPolicies`. `CreatePolicyRequest.type`
  is required and selects which policy type is created (`policies/<type>/`, creating that
  subdirectory if missing); a request that also sets fields belonging to the other type is rejected.
  `UpdatePolicyRequest` carries no `type` field -- a policy's type is immutable via `UpdatePolicy`,
  derived from the record being updated. See
  [Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md)
  and
  [Design: Storage Policy Type](../superpowers/specs/2026-07-28-storage-policy-type-design.md).
- `hostname`/`port`/`config` are only meaningful on a `"storage"`-typed policy -- unset/zero on a
  `"backup"`-typed one, and vice versa for `object_filters`/`rpo`/`backup_window`/`destination`.
  `config` is opaque, pass-through JSON text -- `policy-server` validates it's well-formed at load
  and write time but never interprets its contents.
```

Add a bullet to the `See Also` section (`docs/protocols/policy-server.md:124-130`):

```markdown
- [Design: Storage Policy Type](../superpowers/specs/2026-07-28-storage-policy-type-design.md)
```

- [ ] **Step 2: Update `docs/components/policy-server.md`**

Replace the "Policy types and directory layout" section (`docs/components/policy-server.md:44-57`):

```markdown
### Policy types and directory layout

A policy's type is derived from the name of the immediate subfolder its file lives in under
`$MP_CONFIG_PATH/policies/` — `policies/backup/*.json` are type `"backup"`, `policies/storage/*.json`
are type `"storage"`. Type is never read from or written to the on-disk policy JSON itself; it's
purely a function of file location, computed at load time the same way `policy-server` already
computes each policy's `id`. Each type is a distinct Go type internally (`BackupPolicy`,
`StoragePolicy`) implementing a shared `Policy` interface, with its own on-disk schema, validation,
and wire conversion — adding a further type means writing one more such type and registering its
parser, not changing `policy-server`'s directory-walking or RPC-handling code. A `*.json` sitting
directly under `policies/`, outside any type subfolder, is skipped and logged — the same "loud skip,
don't block the rest" treatment applied to a malformed file. **A subfolder name that isn't a
registered type is also skipped and logged**, the same way — there's no schema to load an
unrecognized type's file into, so it can no longer be loaded generically the way an earlier design
allowed. `CreatePolicy` requires a `type` (`"backup"` or `"storage"`) and writes into the matching
`policies/<type>/`, creating that subdirectory if missing; a request that sets fields belonging to
the other type is rejected. See
[Design: Policy Type Subfolders](../superpowers/specs/2026-07-20-policy-type-subfolders-design.md)
and [Design: Storage Policy Type](../superpowers/specs/2026-07-28-storage-policy-type-design.md).

A `"backup"` policy describes what to back up and where: `object_filters`, `rpo`, `backup_window`,
`destination`. A `"storage"` policy describes where a future storage server should run and how it
should be configured: `hostname`, `port`, and an opaque `config` JSON blob `policy-server` validates
is well-formed but never interprets — nothing in `policy-server` yet runs anything based on it.
```

Update the "Policy files and hot reload" section's opening sentence (`docs/components/policy-server.md:61-63`) to reflect the per-type schema instead of a single flat one:

```markdown
Each policy type subfolder's `*.json` file is one policy. Every type shares `metadata` (`name` plus
operator-set `created_at`/`updated_at`) and `client_filters` (`hostnames` glob list, `labels` map). A
`"backup"` policy additionally has `object_filters` (a list of `{"path": "...", "include": [...],
"exclude": [...]}` entries — `include`/`exclude` are optional glob-pattern lists, validated as
syntactically-valid patterns at load time but otherwise opaque to `policy-server`; see
[Filesystem Backup Flow](../process/filesystem-backup.md) for how `brfs` applies them), `rpo` (a
duration string, e.g. `"24h"`), `backup_window` (a list of cron expressions, e.g.
`["0 2 * * *", "0 20 * * *"]`), and `destination` (a `host:port` string, the target `bwfs` for this
policy's backups). A `"storage"` policy instead has `hostname`, `port`, and `config` (an opaque JSON
object, validated as well-formed at load time but never interpreted). `policy-server` also computes
(never reads) a deterministic ID for the policy itself — and, for a `"backup"` policy, one for each
object filter — derived from the file's name (and each filter's position) — stable across reloads,
and changes only if the file is renamed or (for a backup policy) its `object_filters` are
reordered/have entries inserted before an existing one.
```

- [ ] **Step 3: Add a `CHANGELOG.md` entry**

Insert at the top of `CHANGELOG.md`, after the `# Changelog` header and its description line (`CHANGELOG.md:1-4`):

```markdown
## 2026-07-28 — policy-server: add storage policy type

`policy-server` now supports a second policy type, `"storage"` (`hostname`, `port`, and an opaque
`config` JSON blob) alongside the existing `"backup"` type, for a future storage server to read.
Internally, `Policy` changed from one flat struct into an interface implemented by `BackupPolicy`
and `StoragePolicy`, each with its own schema, validation, and wire conversion — adding a further
type going forward is now a matter of writing one more such type and registering its parser.
`CreatePolicy` requires a `type` selector and writes into the matching `policies/<type>/`.

**Breaking change:** a `policies/<subfolder>/` whose name isn't a registered type (`"backup"` or
`"storage"`) is now skipped and logged at load time, rather than loaded generically as an earlier
design allowed — there's no schema to parse an unrecognized type's files into anymore.
```

- [ ] **Step 4: Commit**

```bash
cd /home/alex/miniprotector
git add docs/protocols/policy-server.md docs/components/policy-server.md CHANGELOG.md
git commit -m "docs: document the storage policy type

Updates the policy-server protocol doc (new Policy/CreatePolicyRequest/
UpdatePolicyRequest fields) and component doc (storage policy type,
per-type schema, the unrecognized-subfolder behavior change), plus a
CHANGELOG entry."
```

---

## Self-Review Notes

**Spec coverage** (against `docs/superpowers/specs/2026-07-28-storage-policy-type-design.md`):
- `PolicyBase`/`Policy` interface/`BackupPolicy`/`StoragePolicy` shape → Task 1 + Task 3.
- Parser registry + unrecognized-type-is-skipped behavior change → Task 1 (`policyParsers` with only `"backup"`, `TestCache_ReloadSkipsUnrecognizedTypeSubfolder`) + Task 3 (adds `"storage"`).
- `validateCommon` + per-type `Validate()` (storage: hostname/port/config rules) → Task 1 (`validateCommon`, `BackupPolicy.Validate`) + Task 3 (`StoragePolicy.Validate`).
- Proto stays flat/additive on `Policy`/`CreatePolicyRequest`/`UpdatePolicyRequest`, `CreatePolicyRequest.type` required, `UpdatePolicyRequest` has no `type` → Task 2.
- `CreatePolicy`/`UpdatePolicy` type-aware dispatch, directory-per-type, mismatched-field rejection → Task 4.
- Required `api-server` touch-up (`Type: "backup"`, not a feature) → Task 5.
- Documentation (protocol doc, component doc, changelog; README/ARCHITECTURE explicitly untouched — no topology change) → Task 6.
- Testing plan's five bullet points (`cache_test.go`, `backup_policy_test.go`/`storage_policy_test.go`, `write_test.go`, `server_test.go` coverage via existing tests, `api-server`'s fixture update) → covered across Tasks 1, 3, 4, 5 (note: `server_test.go` itself needed no new tests — `GetPolicies`/`ListPolicies` already exercise `ToProto` polymorphically for whichever policies are in the cache, and Task 3's `cache_test.go`/`storage_policy_test.go` additions cover a storage policy's field round-trip at the `Policy`/`Clone` level; the design's `server_test.go` bullet is satisfied by that combination rather than a literal new `server_test.go` test, since `GetPolicies`/`ListPolicies`'s behavior is already generic over `Policy`).

**Placeholder scan:** no `TBD`/`TODO`/"add appropriate"/"handle edge cases" phrasing anywhere in the task steps; every step shows the literal code or exact line-range edit involved.

**Type consistency:** `Policy` interface methods (`Meta`, `Filters`, `Path`, `Kind`, `Matches`, `Validate`, `Clone`, `ToProto`, `setIdentity`) are defined once in Task 1 and used with identical signatures in every later task. `buildPolicyForCreate`/`buildPolicyForUpdate` (Task 4) and `storageFieldsSet`/`backupFieldsSet` (Task 4) are each defined once and not redefined elsewhere. `policyParsers`'s value type (`func(data []byte) (Policy, error)`) matches both `parseBackupPolicyJSON` (Task 1) and `parseStoragePolicyJSON` (Task 3)'s actual signatures.
