package filesystem

import (
	"github.com/alex-sviridov/miniprotector/storage"
)

// Store implements storage.BackupStore using filesystem backend
type Store struct {
	basePath string
}

// New creates a new filesystem-based backup store
func New(basePath string) (store *Store, err error) {
	store = &Store{
		basePath: basePath,
	}
	err = nil
	return store, err
}

func (s *Store) Close() error {
	return nil
}

// Ensure Store implements BackupStore interface
var _ storage.BackupStore = (*Store)(nil)
