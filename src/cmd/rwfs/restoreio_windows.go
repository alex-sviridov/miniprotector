//go:build windows

// restoreio_windows.go is the Windows counterpart of restoreio_linux.go:
// preallocation is a plain Truncate, and the writeback, cache and
// directory-sync hooks are no-ops (NTFS journals metadata, and
// os.File.Sync maps to FlushFileBuffers).
package main

import "os"

func osPreallocate(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return f.Truncate(size)
}

func osStartWriteback(*os.File, int64, int64) {}

func osDropCache(*os.File) {}

func osSyncDir(string) error { return nil }
