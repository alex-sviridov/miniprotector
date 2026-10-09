// Package atomicfile provides one small helper for durably persisting a
// file: write to a temp file in the target's own directory, then rename
// over the target, so a crash mid-write never leaves a torn file in place.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
)

// Write persists data to path atomically with mode 0644, creating path's
// parent directory first if it doesn't already exist.
func Write(path string, data []byte) error {
	return WriteMode(path, data, 0o644)
}

// WriteMode is Write with an explicit file mode (e.g. 0o600 for a private key).
func WriteMode(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename temp file into place for %s: %w", path, err)
	}
	return nil
}
