package filesystem

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
	"github.com/alex-sviridov/miniprotector/storage/pack"
)

type Store struct {
	basePath string
	db       *gorm.DB
	lockFile *os.File

	// log appends chunk bytes to pack segments; nil for NewReadOnly, which
	// reads segments directly and never writes chunks.
	log *pack.Log
	// pending holds appended chunks and links not yet in the database, and
	// flushMu serializes flush; see pending.go.
	pending *pending
	flushMu sync.Mutex

	// opGuard separates backup-stream operations (shared) from cleanup/vacuum
	// batches (exclusive); see BeginBackupOp and gc.go.
	opGuard sync.RWMutex
}

func New(basePath string) (*Store, error) {
	return newWithOptions(basePath, pack.Options{})
}

// newWithOptions is New with pack options; tests use it for small segments.
func newWithOptions(basePath string, opts pack.Options) (*Store, error) {
	// Stores written before pack segments kept one file per chunk under
	// chunks/. There is no migration: refuse loudly instead of serving a store
	// whose chunks we cannot see.
	if _, err := os.Stat(filepath.Join(basePath, "chunks")); err == nil {
		return nil, fmt.Errorf("store at %s uses the legacy chunks/ layout, which is no longer supported", basePath)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("check for legacy chunks dir: %w", err)
	}
	if err := os.MkdirAll(basePath, 0o755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
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

	s := &Store{basePath: basePath, db: db, lockFile: lockFile, pending: newPending()}
	// Opening the log recovers a torn tail left by a crash. Taking the flock
	// first guarantees no other writer is appending meanwhile.
	s.log, err = pack.Open(s.packDir(), opts)
	if err != nil {
		closeDB(db)
		lockFile.Close()
		return nil, fmt.Errorf("open pack log: %w", err)
	}
	return s, nil
}

// NewReadOnly opens the store for read-only administrative use (e.g. CLI listing).
// It does not acquire the exclusive flock, so it can run alongside a live bwfs server.
// SQLite WAL mode allows concurrent readers with no blocking. It opens no pack
// log: reading a chunk needs only the segment directory.
func NewReadOnly(basePath string) (*Store, error) {
	db, err := openDB(basePath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	return &Store{basePath: basePath, db: db, pending: newPending()}, nil
}

func (s *Store) packDir() string { return filepath.Join(s.basePath, "packs") }

// RawDB returns the underlying *gorm.DB for read-only administrative queries.
// Not part of BackupStore interface — only for CLI tooling.
func (s *Store) RawDB() *gorm.DB { return s.db }

// Close flushes pending chunks and links, then closes the log, the database
// and the lock. Everything is closed even if the flush fails; the first error
// is returned so the caller knows the last writes may not be durable.
func (s *Store) Close() error {
	var errs []error
	if err := s.flush(); err != nil {
		errs = append(errs, fmt.Errorf("flush: %w", err))
	}
	if s.log != nil {
		if err := s.log.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close pack log: %w", err))
		}
	}
	if err := closeDB(s.db); err != nil {
		errs = append(errs, err)
	}
	if s.lockFile != nil {
		if err := s.lockFile.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func closeDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

var _ storage.BackupStore = (*Store)(nil)
