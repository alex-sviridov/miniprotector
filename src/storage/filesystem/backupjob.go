package filesystem

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage"
)

// EnsureBackupJob idempotently records that a backup job has started. Safe
// to call once per stream of a multi-stream job — only the first call for a
// given jobID creates a row; later calls are no-ops.
func (s *Store) EnsureBackupJob(jobID, sourceHost string) error {
	record := BackupJobRecord{
		JobID:      jobID,
		SourceHost: sourceHost,
		StartedAt:  time.Now(),
		Status:     storage.JobStatusInProgress,
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}},
		DoNothing: true,
	}).Create(&record).Error
}

// GetBackupJob returns the job record, for source-host verification and
// status checks in the BackupCommit RPC and the stall watchdog.
func (s *Store) GetBackupJob(jobID string) (*storage.BackupJob, error) {
	var record BackupJobRecord
	err := s.db.Where("job_id = ?", jobID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("backup job not found: %s", jobID)
	}
	if err != nil {
		return nil, err
	}
	return &storage.BackupJob{
		JobID:      record.JobID,
		SourceHost: record.SourceHost,
		StartedAt:  record.StartedAt,
		FinishedAt: record.FinishedAt,
		Status:     record.Status,
	}, nil
}

// FinishBackupJob marks a backup job complete by setting finished_at.
func (s *Store) FinishBackupJob(jobID string) error {
	return s.db.Model(&BackupJobRecord{}).
		Where("job_id = ?", jobID).
		Update("finished_at", time.Now()).Error
}
