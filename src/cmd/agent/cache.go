package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// PolicyState is one policy's reconciliation history, persisted as part of
// Cache. NextRetryAt is only meaningful when ConsecutiveFailures > 0 — it's
// set once when a failure happens (see run in reconcile.go) rather than
// recomputed from backoff() on every check, so the retry threshold can't
// drift between checks, or between the daemon and `list-policies`.
type PolicyState struct {
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastAttemptAt       *time.Time `json:"last_attempt_at"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
	NextRetryAt         *time.Time `json:"next_retry_at,omitempty"`
}

// Cache is keyed by Policy.ID and persisted as one JSON file.
type Cache map[string]PolicyState

// readCache returns an empty Cache if the file is missing or unparseable —
// every policy then looks "never run", which is the fail-safe direction:
// on any doubt, assume not yet done, never assume done.
func readCache(path string) (Cache, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Cache{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, nil
	}
	if c == nil {
		c = Cache{}
	}
	return c, nil
}

// writeCache persists c atomically: write to a temp file in the same
// directory, then rename over the target, so a crash mid-write never
// leaves a torn cache file. Creates the parent directory if it doesn't
// exist yet.
func writeCache(path string, c Cache) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache directory: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write temp cache: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename cache into place: %w", err)
	}
	return nil
}
