package filesystem

import (
	"encoding/hex"
	"errors"
	"fmt"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

// parseFileID splits "fs://host:type:path:mtime" into its components, so
// SourceHost/Type/Path/Mtime can be derived once, at write time, instead of
// re-parsed on every query -- mirrors cmd/bwfs/list.go's parseFileID
// exactly (duplicated, not imported: this is package "filesystem", that is
// package "main").
func parseFileID(fileID string) (source, objType, path string, mtime int64) {
	const prefix = "fs://"
	if !strings.HasPrefix(fileID, prefix) {
		return "", "", fileID, 0
	}
	rest := fileID[len(prefix):]
	tokens := strings.Split(rest, ":")
	if len(tokens) < 4 {
		return "", "", fileID, 0
	}
	source = tokens[0]
	objType = tokens[1]
	mt, err := strconv.ParseInt(tokens[len(tokens)-1], 10, 64)
	if err != nil {
		return "", "", fileID, 0
	}
	path = strings.Join(tokens[2:len(tokens)-1], ":")
	return source, objType, path, mt
}

func (s *Store) FileDataExists(fileID string) (bool, error) {
	var record FileDataRecord
	err := s.db.
		Where("file_id = ? AND checksum IS NOT NULL", fileID).
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) CreateFileData(fileID string, size int64) error {
	source, _, path, mtime := parseFileID(fileID)
	record := FileDataRecord{
		UUID:       uuid.New().String(),
		FileID:     fileID,
		SourceHost: source,
		Path:       path,
		Mtime:      mtime,
		Size:       size,
		CreatedAt:  time.Now(),
	}
	return s.db.Create(&record).Error
}

func (s *Store) FinalizeFileData(fileID string, checksum []byte) error {
	return s.db.Model(&FileDataRecord{}).
		Where("file_id = ? AND checksum IS NULL", fileID).
		Updates(map[string]any{
			"checksum": checksum,
			"chunk_count": s.db.Model(&FileDataChunkRecord{}).
				Where("file_id = ?", fileID).
				Select("count(*)"),
		}).Error
}

func (s *Store) FileData(fileID string) (*storage.FileData, error) {
	var record FileDataRecord
	err := s.db.
		Where("file_id = ? AND checksum IS NOT NULL", fileID).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("filedata not found: %s", fileID)
	}
	if err != nil {
		return nil, err
	}
	return &storage.FileData{
		UUID:       record.UUID,
		FileID:     record.FileID,
		Size:       record.Size,
		ChunkCount: record.ChunkCount,
		CreatedAt:  record.CreatedAt,
	}, nil
}

func (s *Store) FileDataChunks(fileID string) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		var links []FileDataChunkRecord
		err := s.db.
			Where("file_id = ?", fileID).
			Order("`index` ASC").
			Find(&links).Error
		if err != nil {
			yield(nil, err)
			return
		}
		for _, link := range links {
			decoded, err := hex.DecodeString(link.ChunkHash)
			if err != nil {
				yield(nil, fmt.Errorf("decode chunk hash: %w", err))
				return
			}
			if !yield(decoded, nil) {
				return
			}
		}
	}
}
