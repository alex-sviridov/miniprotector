package filesystem


import (
	"github.com/alex-sviridov/miniprotector/reader/filesystem"
)

func (s *Store) FileDataExists(fi filesystem.FileInfo) (bool, error) {
	return false, nil
}

func (s *Store) GetFile(fi filesystem.FileInfo) ([]byte, error) {
	return nil, nil
}

func (s *Store) CheckFileConsistency(fi filesystem.FileInfo) error {
	return nil
}
