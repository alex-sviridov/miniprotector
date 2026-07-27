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
// the returned slice/elements never affects the cache.
func (c *Cache) Policies() []Policy {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Policy, len(c.policies))
	for i, p := range c.policies {
		// Deep copy the Policy: Metadata, RPO, and Destination are plain value types,
		// but Hostnames, Labels, ObjectFilters, and BackupWindow are reference types.
		out[i] = Policy{
			Metadata: p.Metadata, // plain types: string, time.Time, time.Time
			ClientFilters: ClientFilters{
				Hostnames: make([]string, len(p.ClientFilters.Hostnames)),
				Labels:    make(map[string]string, len(p.ClientFilters.Labels)),
			},
			ObjectFilters: make([]ObjectFilter, len(p.ObjectFilters)),
			RPO:           p.RPO, // plain string
			BackupWindow:  make([]string, len(p.BackupWindow)),
			Destination:   p.Destination, // plain string
			SourcePath:    p.SourcePath,  // plain string
			Type:          p.Type,        // plain string
		}

		// Copy the slice and map contents
		copy(out[i].ClientFilters.Hostnames, p.ClientFilters.Hostnames)
		for k, v := range p.ClientFilters.Labels {
			out[i].ClientFilters.Labels[k] = v
		}
		for j, f := range p.ObjectFilters {
			out[i].ObjectFilters[j] = ObjectFilter{
				ID:      f.ID,
				Path:    f.Path,
				Include: append([]string(nil), f.Include...),
				Exclude: append([]string(nil), f.Exclude...),
			}
		}
		copy(out[i].BackupWindow, p.BackupWindow)
	}
	return out
}

// Reload re-reads every *.json file found one level under dir -- i.e.
// dir/<type>/*.json for every immediate subdirectory <type> of dir --
// tagging each loaded policy with that subdirectory's name as its Type. A
// *.json file sitting directly under dir, outside any type subfolder, is
// logged and skipped, the same as a malformed file -- it doesn't block the
// rest of the directory from loading. Reload does not validate subfolder
// names against a whitelist of known types; an unrecognized subfolder is
// still loaded and tagged with its literal name -- deciding what an
// unrecognized type means is left to downstream consumers (agent today).
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
		if p.Metadata.ID == id {
			return p, true
		}
	}
	return Policy{}, false
}

// FindBySourcePath returns the currently-loaded policy parsed from exactly
// this file path. Used by CreatePolicy to look up the policy it just wrote,
// once Reload has re-parsed it and computed its ID.
func (c *Cache) FindBySourcePath(path string) (Policy, bool) {
	for _, p := range c.Policies() {
		if p.SourcePath == path {
			return p, true
		}
	}
	return Policy{}, false
}
