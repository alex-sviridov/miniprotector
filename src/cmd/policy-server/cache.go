package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
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
		// Deep copy the Policy: Metadata and RPO are plain value types,
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
		}

		// Copy the slice and map contents
		copy(out[i].ClientFilters.Hostnames, p.ClientFilters.Hostnames)
		for k, v := range p.ClientFilters.Labels {
			out[i].ClientFilters.Labels[k] = v
		}
		copy(out[i].ObjectFilters, p.ObjectFilters)
		copy(out[i].BackupWindow, p.BackupWindow)
	}
	return out
}

// Reload re-reads every *.json file directly under dir, replacing the
// cached policy list with whatever parsed successfully. A file that fails
// to parse is logged and skipped -- it doesn't block the rest of the
// directory from loading. If dir contains at least one *.json file and
// every single one failed to parse, the previous good cache is left in
// place (an error is returned) rather than swapped to an empty list -- an
// empty policies/ directory is a valid "no policies" state, but a reload
// that produced zero successes out of one-or-more attempts is treated as a
// failed reload, not an intentional empty state.
func (c *Cache) Reload(dir string, logger *slog.Logger) error {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return fmt.Errorf("list policy files in %s: %w", dir, err)
	}

	loaded := make([]Policy, 0, len(matches))
	for _, filePath := range matches {
		p, err := parsePolicyFile(filePath)
		if err != nil {
			logger.Error("skipping malformed policy file", "path", filePath, "error", err)
			continue
		}
		loaded = append(loaded, p)
	}

	if len(matches) > 0 && len(loaded) == 0 {
		return fmt.Errorf("reload of %s: all %d policy files failed to parse, keeping previous cache", dir, len(matches))
	}

	c.mu.Lock()
	c.policies = loaded
	c.mu.Unlock()
	return nil
}
