package filesystem

import (
	"fmt"
	"time"

	"github.com/alex-sviridov/miniprotector/workload"
	"github.com/gofrs/flock"
)

func (fi FileInfo) Lock(timeout time.Duration) (workload.Unlocker, error) {
	// Do not lock any non-file object, return ok
	if fi.GetType() != 'f' {
		return fileUnlocker{}, nil
	}

	fileLock := flock.New(fi.path)

	start := time.Now()
	for time.Since(start) < timeout {
		if locked, err := fileLock.TryLock(); err != nil {
			return fileUnlocker{}, err
		} else if locked {
			return fileUnlocker{fileLock: fileLock}, nil
		}
		time.Sleep(10 * time.Millisecond)
	}

	return fileUnlocker{}, fmt.Errorf("timeout acquiring lock after %v", timeout)
}

type fileUnlocker struct {
	fileLock *flock.Flock
}

func (fu fileUnlocker) Unlock() error {
	if fu.fileLock == nil {
		return nil
	}
	return fu.fileLock.Unlock()
}
