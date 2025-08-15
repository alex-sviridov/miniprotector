package filesystem


import (
	"github.com/alex-sviridov/miniprotector/workload"
)

func (s *Store) FileDataExists(object workload.BackupObject) (bool, error) {
	return false, nil
}

func (s *Store) GetFile(object workload.BackupObject) ([]byte, error) {
	return nil, nil
}

func (s *Store) CheckFileConsistency(object workload.BackupObject) error {
	return nil
}
