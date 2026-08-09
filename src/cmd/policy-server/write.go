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
	"google.golang.org/protobuf/types/known/timestamppb"
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

// disabledAtFromProto converts a possibly-nil disabled_at field to
// time.Time, treating "field not set" as the zero time -- distinct from
// (*timestamppb.Timestamp).AsTime()'s own nil-safe behavior, which maps a
// nil Timestamp to the Unix epoch (1970), not Go's zero time.Time (year 1).
func disabledAtFromProto(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
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
func storageFieldsSet(port int32, config string) bool {
	return port != 0 || config != ""
}

// backupFieldsSet reports whether any backup-only field is non-default --
// used to reject a request mixing backup fields into a storage policy.
func backupFieldsSet(objectFilters []*pb.ObjectFilter, rpo string, backupWindow []string, storagePolicyID string) bool {
	return len(objectFilters) > 0 || rpo != "" || len(backupWindow) > 0 || storagePolicyID != ""
}

// policyFieldsGetter is the subset of pb.CreatePolicyRequest/
// pb.UpdatePolicyRequest that buildPolicy needs to construct a concrete
// Policy -- both proto messages implement it with identical getters, so one
// switch below serves both RPCs.
type policyFieldsGetter interface {
	GetObjectFilters() []*pb.ObjectFilter
	GetRpo() string
	GetBackupWindow() []string
	GetStoragePolicyId() string
	GetPort() int32
	GetConfig() string
}

// buildPolicy constructs the concrete "backup" or "storage" Policy kind
// asks for out of base and req's type-specific fields, rejecting a request
// that also sets fields belonging to the other type. Shared by
// buildPolicyForCreate (kind == req.GetType()) and buildPolicyForUpdate
// (kind == existing.Kind(), since a policy's type is immutable via update).
// "restore" is handled separately in buildPolicyForCreate, not routed
// through here or through policyFieldsGetter -- it's create-only (see
// buildPolicyForUpdate) and UpdatePolicyRequest has no source_store field
// for policyFieldsGetter to expose.
func buildPolicy(kind string, base PolicyBase, req policyFieldsGetter) (Policy, error) {
	switch kind {
	case "backup":
		if storageFieldsSet(req.GetPort(), req.GetConfig()) {
			return nil, fmt.Errorf("a backup policy must not set port/config")
		}
		return &BackupPolicy{
			PolicyBase:      base,
			ObjectFilters:   fromProtoObjectFilters(req.GetObjectFilters()),
			RPO:             req.GetRpo(),
			BackupWindow:    req.GetBackupWindow(),
			StoragePolicyID: req.GetStoragePolicyId(),
		}, nil
	case "storage":
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetStoragePolicyId()) {
			return nil, fmt.Errorf("a storage policy must not set object_filters/rpo/backup_window/storage_policy_id")
		}
		return &StoragePolicy{
			PolicyBase: base,
			Port:       int(req.GetPort()),
			Config:     json.RawMessage(req.GetConfig()),
		}, nil
	default:
		return nil, fmt.Errorf("unknown policy type %q", kind)
	}
}

// buildPolicyForCreate constructs the concrete Policy req.GetType() asks
// for. The returned Policy's Metadata.ID/SourcePath/Type are left zero --
// Cache.Reload assigns them once the caller writes the file and reloads.
func buildPolicyForCreate(req *pb.CreatePolicyRequest, now time.Time) (Policy, error) {
	base := PolicyBase{
		Metadata: Metadata{
			Name:       req.GetName(),
			CreatedAt:  now,
			UpdatedAt:  now,
			DisabledAt: disabledAtFromProto(req.GetDisabledAt()),
		},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
	}
	if req.GetType() == "restore" {
		if backupFieldsSet(req.GetObjectFilters(), req.GetRpo(), req.GetBackupWindow(), req.GetStoragePolicyId()) || req.GetPort() != 0 {
			return nil, fmt.Errorf("a restore policy must not set object_filters/rpo/backup_window/storage_policy_id/port")
		}
		return &RestorePolicy{
			PolicyBase:  base,
			SourceStore: req.GetSourceStore(),
			Config:      json.RawMessage(req.GetConfig()),
		}, nil
	}
	if req.GetSourceStore() != "" {
		return nil, fmt.Errorf("only a restore policy may set source_store")
	}
	return buildPolicy(req.GetType(), base, req)
}

// buildPolicyForUpdate constructs the same concrete type as the existing
// policy being updated -- a policy's type is immutable via UpdatePolicy, so
// kind comes from the existing record (existing.Kind()), not the request.
func buildPolicyForUpdate(req *pb.UpdatePolicyRequest, kind string, existingMeta Metadata, now time.Time) (Policy, error) {
	if kind == "restore" {
		return nil, fmt.Errorf("restore policies cannot be updated")
	}
	if kind != "backup" && kind != "storage" {
		return nil, fmt.Errorf("existing policy has unknown type %q", kind)
	}
	base := PolicyBase{
		Metadata: Metadata{
			Name:       req.GetName(),
			CreatedAt:  existingMeta.CreatedAt,
			UpdatedAt:  now,
			DisabledAt: disabledAtFromProto(req.GetDisabledAt()),
		},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
	}
	return buildPolicy(kind, base, req)
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
	if bp, ok := p.(*BackupPolicy); ok {
		if sp, found := s.cache.FindByID(bp.StoragePolicyID); !found || sp.Kind() != "storage" {
			s.logger.Error("CreatePolicy: storage_policy_id does not reference an existing storage policy", "storage_policy_id", bp.StoragePolicyID)
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("storage policy %q not found", bp.StoragePolicyID))
		}
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
	pp := created.ToProto(true)
	attachDestination(ctx, pp, s.cache, s.checkins, s.logger)
	return pp, nil
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
	if bp, ok := p.(*BackupPolicy); ok {
		if sp, found := s.cache.FindByID(bp.StoragePolicyID); !found || sp.Kind() != "storage" {
			s.logger.Error("UpdatePolicy: storage_policy_id does not reference an existing storage policy", "id", req.GetId(), "storage_policy_id", bp.StoragePolicyID)
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("storage policy %q not found", bp.StoragePolicyID))
		}
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
	pp := updated.ToProto(true)
	attachDestination(ctx, pp, s.cache, s.checkins, s.logger)
	return pp, nil
}

// referencingBackupPolicies returns the Meta().Name of every backup policy
// in policies whose StoragePolicyID equals storagePolicyID -- used by
// DeletePolicy to block removing a storage policy that's still in use.
func referencingBackupPolicies(policies []Policy, storagePolicyID string) []string {
	var names []string
	for _, p := range policies {
		if bp, ok := p.(*BackupPolicy); ok && bp.StoragePolicyID == storagePolicyID {
			names = append(names, bp.Meta().Name)
		}
	}
	return names
}

// DeletePolicy removes the policy file backing id and reloads the cache.
// Deleting a "storage" policy is refused, with the names of every
// referencing backup policy, if any backup policy's storage_policy_id
// still points at it.
func (s *policyServerServer) DeletePolicy(ctx context.Context, req *pb.DeletePolicyRequest) (*pb.DeletePolicyResponse, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}
	if existing.Kind() == "storage" {
		if inUse := referencingBackupPolicies(s.cache.Policies(), req.GetId()); len(inUse) > 0 {
			return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("storage policy in use by: %s", strings.Join(inUse, ", ")))
		}
	}

	if err := os.Remove(existing.Path()); err != nil {
		s.logger.Error("DeletePolicy: remove failed", "path", existing.Path(), "error", err)
		return nil, status.Error(codes.Internal, "failed to remove policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("DeletePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after delete")
	}

	// The policy file is already gone and the cache already reloaded, so the
	// delete has effectively succeeded from the caller's perspective -- a
	// failure here is logged rather than failing the RPC. It's not swept
	// under the rug forever: DeleteOlderThan's cleanup tick ages these rows
	// out on its own. Best-effort now prevents a recreated policy that
	// reuses this deleted one's deterministic id (derived from its
	// filename) from immediately inheriting its stale check-ins.
	//
	// Detached from ctx (context.WithoutCancel) rather than bound to it:
	// this cleanup must still be attempted even if the caller has already
	// disconnected or timed out by the time we get here -- exactly the
	// scenario this comment is about -- so it can't be allowed to inherit
	// the caller's cancellation.
	if err := s.checkins.DeleteForPolicy(context.WithoutCancel(ctx), req.GetId()); err != nil {
		s.logger.Error("DeletePolicy: failed to delete check-in rows", "id", req.GetId(), "error", err)
	}

	s.logger.Info("DeletePolicy", "id", req.GetId(), "path", existing.Path())
	return &pb.DeletePolicyResponse{}, nil
}
