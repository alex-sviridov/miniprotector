package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

type Store struct {
	basePath string
	db       *gorm.DB
	lockFile *os.File
}

func New(basePath string) (*Store, error) {
	chunksDir := filepath.Join(basePath, "chunks")
	if err := os.MkdirAll(chunksDir, 0755); err != nil {
		return nil, fmt.Errorf("create chunks dir: %w", err)
	}

	lockFile, err := acquireLock(basePath)
	if err != nil {
		return nil, err
	}

	db, err := openDB(basePath)
	if err != nil {
		lockFile.Close()
		return nil, fmt.Errorf("open db: %w", err)
	}

	return &Store{basePath: basePath, db: db, lockFile: lockFile}, nil
}

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

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	if err := sqlDB.Close(); err != nil {
		return err
	}
	return s.lockFile.Close()
}

var _ storage.BackupStore = (*Store)(nil)
