package filesystem

import (
	"time"

	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

const vacuumIncompleteThreshold = time.Hour

func (s *Store) StoreInfo() (*storage.StoreInfo, error) {
	var totalVersions, totalFileData, totalChunks, totalSize int64

	if err := s.db.Model(&FileVersionRecord{}).Count(&totalVersions).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&FileDataRecord{}).Where("checksum IS NOT NULL").Count(&totalFileData).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&ChunkRecord{}).Count(&totalChunks).Error; err != nil {
		return nil, err
	}
	if err := s.db.Model(&ChunkRecord{}).Select("COALESCE(SUM(size), 0)").Scan(&totalSize).Error; err != nil {
		return nil, err
	}

	return &storage.StoreInfo{
		TotalFileVersions: totalVersions,
		TotalFileData:     totalFileData,
		TotalChunks:       totalChunks,
		TotalSize:         totalSize,
		UniqueChunks:      totalChunks,
	}, nil
}

// Vacuum removes everything no file version references, in one transaction.
// It runs at startup, before backups are served.
func (s *Store) Vacuum() (*storage.VacuumResult, error) {
	result := &storage.VacuumResult{}

	// Flush first so every stored chunk has its row and link in the database;
	// otherwise a chunk whose link is still pending would look orphaned.
	if err := s.flush(); err != nil {
		return nil, err
	}

	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Step 1: remove incomplete FileData older than threshold
		cutoff := time.Now().Add(-vacuumIncompleteThreshold)
		res := tx.Where("checksum IS NULL AND created_at < ?", cutoff).Delete(&FileDataRecord{})
		if res.Error != nil {
			return res.Error
		}
		result.IncompleteFileData = res.RowsAffected

		// Step 2: remove FileData with no FileVersion referencing them
		res = tx.Where("file_id NOT IN (SELECT object_id FROM file_version_records)").
			Where("checksum IS NOT NULL").
			Delete(&FileDataRecord{})
		if res.Error != nil {
			return res.Error
		}
		result.OrphanedFileDataRemoved = res.RowsAffected

		// Step 3: remove FileDataChunkRecord rows whose file_id no longer has
		// any FileDataRecord at all (a file_id can be shared by multiple
		// FileDataRecord attempts, so a chunk link is only safe to remove once
		// none of them remain).
		res = tx.Where("file_id NOT IN (SELECT file_id FROM file_data_records)").Delete(&FileDataChunkRecord{})
		if res.Error != nil {
			return res.Error
		}
		result.OrphanedChunkLinksRemoved = res.RowsAffected

		// Step 4: remove ChunkRecord rows with no FileDataChunkRecord referencing them
		res = tx.Where("hash NOT IN (SELECT chunk_hash FROM file_data_chunk_records)").Delete(&ChunkRecord{})
		if res.Error != nil {
			return res.Error
		}
		result.OrphanedChunksRemoved = res.RowsAffected

		return nil
	})
	if err != nil {
		return nil, err
	}

	return result, nil
}
