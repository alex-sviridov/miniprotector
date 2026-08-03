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

// ResolveDestination looks up storagePolicyID among the currently-loaded
// policies and, if it names a "storage" policy, returns its "host:port"
// computed from that policy's ClientFilters.Hostnames[0] and Port. ok is
// false if storagePolicyID doesn't resolve to a storage policy at all --
// unknown id, an id belonging to a non-storage policy, or a storage policy
// with no hostname set. Used by attachDestination (server.go) to resolve a
// backup policy's Destination live on every read.
func (c *Cache) ResolveDestination(storagePolicyID string) (string, bool) {
	p, ok := c.FindByID(storagePolicyID)
	if !ok || p.Kind() != "storage" {
		return "", false
	}
	sp, ok := p.(*StoragePolicy)
	if !ok || len(sp.ClientFilters.Hostnames) == 0 {
		return "", false
	}
	return fmt.Sprintf("%s:%d", sp.ClientFilters.Hostnames[0], sp.Port), true
}
