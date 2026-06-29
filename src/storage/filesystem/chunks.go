package filesystem

import (
	"bytes"
	"fmt"

	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
)

func (s *Store) ChunkExists(chunkHash []byte) error {
	return storage.ErrChunkNotFound
}

func (s *Store) StoreChunk(chunkHash []byte, data []byte) error {
	hash32 := blake3.Sum256(data)
	hash := hash32[:]
	if !bytes.Equal(chunkHash, hash) {
		return fmt.Errorf("chunk hash mismatch")
	}
	return nil
}

func (s *Store) LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error {
	return nil
}

func (s *Store) ReadChunk(chunkHash []byte) ([]byte, error) {
	return nil, fmt.Errorf("chunk not found: %x", chunkHash)
}
