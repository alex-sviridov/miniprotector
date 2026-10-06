//go:build linux

// restoreio_linux.go holds the Linux write-path hooks for rwfs restore:
// fallocate preallocation, early writeback, page-cache hygiene and
// directory fsync. See
// docs/superpowers/specs/2026-10-06-rwfs-write-tuning-design.md.
package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// osPreallocate reserves size bytes with fallocate (mode 0 also sets the
// file length). A filesystem without fallocate support degrades to
// Truncate; any other error -- notably ENOSPC -- is returned so a full
// disk fails the file before a single chunk is consumed.
func osPreallocate(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	for {
		err := unix.Fallocate(int(f.Fd()), 0, 0, size)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOSYS):
			return f.Truncate(size)
		default:
			return fmt.Errorf("fallocate %d bytes: %w", size, err)
		}
	}
}

// osStartWriteback asks the kernel to begin writing [off, off+n) back
// without waiting for it. Best-effort: a failure only means the later
// fsync does more work.
func osStartWriteback(f *os.File, off, n int64) {
	_ = unix.SyncFileRange(int(f.Fd()), off, n, unix.SYNC_FILE_RANGE_WRITE)
}

// osDropCache evicts f's clean pages so a large restore does not push the
// user's working set out of the page cache. Only effective after Sync.
func osDropCache(f *os.File) {
	_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
}

// osSyncDir fsyncs a directory so renames into it survive a crash.
func osSyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
