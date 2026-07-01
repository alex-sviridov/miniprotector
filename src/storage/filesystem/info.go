package filesystem

import (
	"os"
	"path/filepath"
	"strings"
	"time"

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

func (s *Store) Vacuum() (*storage.VacuumResult, error) {
	result := &storage.VacuumResult{}

	// Step 1: remove incomplete FileData older than threshold
	cutoff := time.Now().Add(-vacuumIncompleteThreshold)
	res := s.db.Where("checksum IS NULL AND created_at < ?", cutoff).Delete(&FileDataRecord{})
	result.IncompleteFileData = res.RowsAffected

	// Step 2: remove FileData with no FileVersion referencing them
	res = s.db.Where("file_id NOT IN (SELECT object_id FROM file_version_records)").
		Where("checksum IS NOT NULL").
		Delete(&FileDataRecord{})
	result.OrphanedFileDataRemoved = res.RowsAffected

	// Step 3: remove FileDataChunkRecord rows whose file_id no longer has
	// any FileDataRecord at all (a file_id can be shared by multiple
	// FileDataRecord attempts, so a chunk link is only safe to remove once
	// none of them remain).
	res = s.db.Where("file_id NOT IN (SELECT file_id FROM file_data_records)").Delete(&FileDataChunkRecord{})
	result.OrphanedChunkLinksRemoved = res.RowsAffected

	// Step 4: remove ChunkRecord rows with no FileDataChunkRecord referencing them
	res = s.db.Where("hash NOT IN (SELECT chunk_hash FROM file_data_chunk_records)").Delete(&ChunkRecord{})
	result.OrphanedChunksRemoved = res.RowsAffected

	// Step 5: walk chunk files; delete any not in chunk_records (includes temp files)
	chunksRoot := filepath.Join(s.basePath, "chunks")
	filepath.WalkDir(chunksRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		// Reconstruct hash from path: last three segments are [aa][bb][rest]
		rel, _ := filepath.Rel(chunksRoot, path)
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) != 3 {
			// temp file or unexpected structure — delete
			info, statErr := d.Info()
			if statErr == nil {
				result.BytesReclaimed += info.Size()
			}
			os.Remove(path)
			return nil
		}
		hexHash := parts[0] + parts[1] + parts[2]

		var count int64
		s.db.Model(&ChunkRecord{}).Where("hash = ?", hexHash).Count(&count)
		if count == 0 {
			info, statErr := d.Info()
			if statErr == nil {
				result.BytesReclaimed += info.Size()
			}
			os.Remove(path)
		}
		return nil
	})

	return result, nil
}
