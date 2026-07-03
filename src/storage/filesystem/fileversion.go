package filesystem

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage"
)

// EnsureFileVersion idempotently records that objectID was observed during
// jobID's backup run. The first observation of a given (jobID, objectID)
// pair wins — a duplicate send of the same object within the same job (e.g.
// a future retry) is a safe no-op rather than a second catalog row.
func (s *Store) EnsureFileVersion(jobID, objectID string, metadata []byte, ctime int64) error {
	record := FileVersionRecord{
		JobID:     jobID,
		ObjectID:  objectID,
		Metadata:  metadata,
		Ctime:     ctime,
		CreatedAt: time.Now(),
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&record).Error
}

// RemoveFileVersion deletes the file_versions row identified by its natural
// (jobID, objectID) key.
func (s *Store) RemoveFileVersion(jobID, objectID string) error {
	return s.db.Delete(&FileVersionRecord{}, "job_id = ? AND object_id = ?", jobID, objectID).Error
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

// FileVersionsForJob returns the object IDs of every file_versions row
// recorded for jobID, for BackupCommit's hash verification.
func (s *Store) FileVersionsForJob(jobID string) ([]string, error) {
	var objectIDs []string
	err := s.db.Model(&FileVersionRecord{}).
		Where("job_id = ?", jobID).
		Pluck("object_id", &objectIDs).Error
	return objectIDs, err
}

func toStorageFileVersion(r *FileVersionRecord) *storage.FileVersion {
	return &storage.FileVersion{
		JobID:     r.JobID,
		ObjectID:  r.ObjectID,
		Metadata:  r.Metadata,
		Ctime:     r.Ctime,
		CreatedAt: r.CreatedAt,
	}
}
