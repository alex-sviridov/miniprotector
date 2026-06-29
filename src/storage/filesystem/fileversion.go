package filesystem

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/alex-sviridov/miniprotector/storage"
)

func (s *Store) CreateFileVersion(objectID, fileID string, metadata []byte, ctime int64) (string, error) {
	id := uuid.New().String()
	record := FileVersionRecord{
		ID:        id,
		ObjectID:  objectID,
		FileID:    fileID,
		Metadata:  metadata,
		Ctime:     ctime,
		CreatedAt: time.Now(),
	}
	if err := s.db.Create(&record).Error; err != nil {
		return "", err
	}
	return id, nil
}

func (s *Store) RemoveFileVersion(versionID string) error {
	return s.db.Delete(&FileVersionRecord{}, "id = ?", versionID).Error
}

func (s *Store) LatestFileVersion(objectID string) (*storage.FileVersion, error) {
	var record FileVersionRecord
	err := s.db.
		Where("object_id = ?", objectID).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("file version not found: %s", objectID)
	}
	if err != nil {
		return nil, err
	}
	return toStorageFileVersion(&record), nil
}

func (s *Store) FileVersionAtTime(objectID string, timestamp time.Time) (*storage.FileVersion, error) {
	var record FileVersionRecord
	err := s.db.
		Where("object_id = ? AND created_at <= ?", objectID, timestamp).
		Order("created_at DESC").
		First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("file version not found at time %v: %s", timestamp, objectID)
	}
	if err != nil {
		return nil, err
	}
	return toStorageFileVersion(&record), nil
}

func (s *Store) FileVersionsInPeriod(from, to time.Time) ([]*storage.FileVersion, error) {
	var records []FileVersionRecord
	err := s.db.
		Where("created_at BETWEEN ? AND ?", from, to).
		Order("created_at ASC").
		Find(&records).Error
	if err != nil {
		return nil, err
	}
	result := make([]*storage.FileVersion, len(records))
	for i, r := range records {
		result[i] = toStorageFileVersion(&r)
	}
	return result, nil
}

func toStorageFileVersion(r *FileVersionRecord) *storage.FileVersion {
	return &storage.FileVersion{
		ID:        r.ID,
		ObjectID:  r.ObjectID,
		FileID:    r.FileID,
		Metadata:  r.Metadata,
		Ctime:     r.Ctime,
		CreatedAt: r.CreatedAt,
	}
}
