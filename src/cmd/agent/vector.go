// vector.go: agent's ownership of the bundled Vector process's binary
// resolution, config generation, and supervision. See
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// resolveVectorBinary finds the Vector binary colocated with agent's own
// executable -- unlike realExec's resolution for certclient/policyclient/
// brfs, there is deliberately no $PATH fallback: Vector is a third-party
// tool that may already exist elsewhere on a host for an unrelated
// purpose, and silently picking up a different, unpinned version there
// would be a correctness landmine, not a convenience.
func resolveVectorBinary() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("determine own executable path: %w", err)
	}
	return resolveVectorBinaryIn(filepath.Dir(exePath))
}

// resolveVectorBinaryIn is resolveVectorBinary's testable core.
func resolveVectorBinaryIn(dir string) (string, error) {
	candidate := filepath.Join(dir, "vector")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("vector binary not found at %s (bundled alongside agent, no $PATH fallback): %w", candidate, err)
	}
	return candidate, nil
}
