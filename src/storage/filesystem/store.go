package filesystem


import (
	"github.com/alex-sviridov/miniprotector/storage"
)

type Store struct {
    basePath string
    // ... fields
}

func New(basePath string) storage.BackupStore {
    return &Store{basePath: basePath}
}

func (s *Store) GetStoreInfo() (storage.StoreInfo, error) {
	return storage.StoreInfo{}, nil
}