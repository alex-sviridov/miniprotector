//go:build windows

package filesystem

import "os"

// openForRead opens a source file for sequential chunked reading.
// OS-specific read tuning (FILE_FLAG_SEQUENTIAL_SCAN, ...) belongs here.
func openForRead(path string) (*os.File, error) {
	return os.Open(path)
}
