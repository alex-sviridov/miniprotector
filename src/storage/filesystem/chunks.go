package filesystem

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"

	"lukechampine.com/blake3"

	"github.com/alex-sviridov/miniprotector/storage"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) chunkPath(hexHash string) string {
	return filepath.Join(s.basePath, "chunks", hexHash[0:2], hexHash[2:4], hexHash[4:])
}

func (s *Store) ChunkExists(chunkHash []byte) error {
	path := s.chunkPath(hex.EncodeToString(chunkHash))
	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return storage.ErrChunkNotFound
	}
	return err
}

func (s *Store) StoreChunk(chunkHash []byte, data []byte) error {
	sum := blake3.Sum256(data)
	if !bytes.Equal(chunkHash, sum[:]) {
		return fmt.Errorf("chunk hash mismatch")
	}

	hexHash := hex.EncodeToString(chunkHash)
	finalPath := s.chunkPath(hexHash)

	if _, err := os.Stat(finalPath); err == nil {
		return nil // already exists
	}

	dir := filepath.Dir(finalPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create chunk dir: %w", err)
	}

	tmpPath := fmt.Sprintf("%s.%016x.tmp", finalPath, rand.Uint64())
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write chunk temp: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename chunk: %w", err)
	}

	record := ChunkRecord{Hash: hexHash, Size: int64(len(data))}
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error
}

func (s *Store) LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error {
	record := FileDataChunkRecord{
		FileID:    fileID,
		ChunkHash: hex.EncodeToString(chunkHash),
		Index:     index,
	}
	return s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&record).Error
}

// MarkChunkCorrupted removes a chunk that failed to read correctly (missing
// or otherwise unusable) along with every DB record that depends on it, so
// affected files are treated as needing a fresh upload on the next backup.
func (s *Store) MarkChunkCorrupted(chunkHash []byte) error {
	hexHash := hex.EncodeToString(chunkHash)

	path := s.chunkPath(hexHash)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove corrupted chunk file: %w", err)
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		var links []FileDataChunkRecord
		if err := tx.Where("chunk_hash = ?", hexHash).Find(&links).Error; err != nil {
			return fmt.Errorf("find files depending on chunk: %w", err)
		}

		if err := tx.Where("chunk_hash = ?", hexHash).Delete(&FileDataChunkRecord{}).Error; err != nil {
			return fmt.Errorf("remove chunk links: %w", err)
		}
		if err := tx.Where("hash = ?", hexHash).Delete(&ChunkRecord{}).Error; err != nil {
			return fmt.Errorf("remove chunk record: %w", err)
		}

		fileIDs := make([]string, len(links))
		for i, link := range links {
			fileIDs[i] = link.FileID
		}
		if len(fileIDs) > 0 {
			if err := tx.Where("file_id IN ?", fileIDs).Delete(&FileDataRecord{}).Error; err != nil {
				return fmt.Errorf("invalidate dependent file data: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) ReadChunk(chunkHash []byte) ([]byte, error) {
	path := s.chunkPath(hex.EncodeToString(chunkHash))
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, storage.ErrChunkNotFound
		}
		return nil, fmt.Errorf("read chunk: %w", err)
	}
	return data, nil
}
