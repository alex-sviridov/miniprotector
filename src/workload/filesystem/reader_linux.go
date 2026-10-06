//go:build linux

package filesystem

import "os"

// openForRead opens a source file for sequential chunked reading.
// OS-specific read tuning (fadvise, O_NOATIME, ...) belongs here.
func openForRead(path string) (*os.File, error) {
	return os.Open(path)
}
