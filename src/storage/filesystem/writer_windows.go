//go:build windows

package filesystem

import (
	"fmt"
	"os"
)

// writeChunkFile durably places data at finalPath by writing tmpPath and
// renaming it. OS-specific write tuning (fsync, rename retry, ...) belongs here.
func writeChunkFile(tmpPath, finalPath string, data []byte) error {
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write chunk temp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename chunk: %w", err)
	}
	return nil
}
