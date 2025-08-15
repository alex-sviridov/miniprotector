package filesystem


import (
	"github.com/alex-sviridov/miniprotector/workload"
)

func (s *Store) ChunkExists(hash []byte) (bool, error) {
	return false, nil
}

func (s *Store) StoreChunk(chunk workload.Chunk) error {
	return nil
}

func (s *Store) LinkChunk(object workload.BackupObject, hash []byte) error {
	return nil
}
    
func (s *Store) RemoveOrphanChunks() error {
	return nil
}