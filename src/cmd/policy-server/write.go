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
// writes the new policy file before reloading the cache. The filename it
// picks is permanent for that policy's lifetime -- it's what the policy's
// id derives from.
func (s *policyServerServer) CreatePolicy(ctx context.Context, req *pb.CreatePolicyRequest) (*pb.Policy, error) {
	now := time.Now().UTC()
	p := Policy{
		Metadata:      Metadata{Name: req.GetName(), CreatedAt: now, UpdatedAt: now},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
		ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
		RPO:           req.GetRpo(),
		BackupWindow:  req.GetBackupWindow(),
		Destination:   req.GetDestination(),
	}
	if err := validatePolicy(p); err != nil {
		s.logger.Error("CreatePolicy: validation failed", "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	slug := slugify(p.Metadata.Name)
	if slug == "" {
		return nil, status.Error(codes.InvalidArgument, "name must contain at least one alphanumeric character")
	}
	filename, err := uniqueFilename(s.policiesDir, slug)
	if err != nil {
		s.logger.Error("CreatePolicy: filename allocation failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to allocate a policy filename")
	}
	filePath := filepath.Join(s.policiesDir, filename)

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
	s.logger.Info("CreatePolicy", "id", created.Metadata.ID, "name", created.Metadata.Name, "path", filePath)
	return toProtoPolicyAdmin(created), nil
}

// UpdatePolicy fully replaces an existing policy's editable fields,
// identified by id. The on-disk filename -- and therefore the policy's id,
// which derives from it -- never changes; only the file's content does.
// CreatedAt is preserved from the existing record; UpdatedAt is set to now.
func (s *policyServerServer) UpdatePolicy(ctx context.Context, req *pb.UpdatePolicyRequest) (*pb.Policy, error) {
	existing, ok := s.cache.FindByID(req.GetId())
	if !ok {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("policy %q not found", req.GetId()))
	}

	p := Policy{
		Metadata:      Metadata{Name: req.GetName(), CreatedAt: existing.Metadata.CreatedAt, UpdatedAt: time.Now().UTC()},
		ClientFilters: fromProtoClientFilters(req.GetClientFilters()),
		ObjectFilters: fromProtoObjectFilters(req.GetObjectFilters()),
		RPO:           req.GetRpo(),
		BackupWindow:  req.GetBackupWindow(),
		Destination:   req.GetDestination(),
	}
	if err := validatePolicy(p); err != nil {
		s.logger.Error("UpdatePolicy: validation failed", "id", req.GetId(), "error", err)
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	if err := atomicWriteJSON(existing.SourcePath, p); err != nil {
		s.logger.Error("UpdatePolicy: write failed", "path", existing.SourcePath, "error", err)
		return nil, status.Error(codes.Internal, "failed to write policy file")
	}
	if err := s.cache.Reload(s.policiesDir, s.logger); err != nil {
		s.logger.Error("UpdatePolicy: reload failed", "error", err)
		return nil, status.Error(codes.Internal, "failed to reload policies after write")
	}

	updated, ok := s.cache.FindBySourcePath(existing.SourcePath)
	if !ok {
		return nil, status.Error(codes.Internal, "policy not found in cache after update")
	}
	s.logger.Info("UpdatePolicy", "id", updated.Metadata.ID, "name", updated.Metadata.Name, "path", existing.SourcePath)
	return toProtoPolicyAdmin(updated), nil
}
