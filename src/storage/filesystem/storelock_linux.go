//go:build linux

package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func acquireLock(basePath string) (*os.File, error) {
	lockPath := filepath.Join(basePath, "metadata.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("store at %s already in use by another process", basePath)
		}
		return nil, fmt.Errorf("acquire store lock: %w", err)
	}
	return f, nil
}
