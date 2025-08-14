package filesystem


import (
	"github.com/alex-sviridov/miniprotector/reader/filesystem"
	"github.com/alex-sviridov/miniprotector/storage"
)

func (s *Store) ChunkExists(ci storage.ChunkInfo) (bool, error) {
	return false, nil
}

func (s *Store) StoreChunk(ci storage.ChunkInfo, data []byte) error {
	return nil
}

func (s *Store) LinkChunk(fi filesystem.FileInfo, ci storage.ChunkInfo) error {
	return nil
}
    
func (s *Store) RemoveOrphanChunks() error {
	return nil
}