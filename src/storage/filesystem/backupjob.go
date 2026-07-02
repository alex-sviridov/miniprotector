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

// FinalizeBackupJob atomically transitions a job from in_progress to
// success/failure. On failure it also purges the job's file_versions rows
// in the same transaction — raw chunk/file data is reclaimed later by
// Vacuum, out of scope here. Returns false (no-op) if the job was already
// finalized, guarding the race between BackupCommit and the stall watchdog,
// and making duplicate/retried BackupCommit calls idempotent.
func (s *Store) FinalizeBackupJob(jobID string, success bool) (bool, error) {
	newStatus := storage.JobStatusFailure
	if success {
		newStatus = storage.JobStatusSuccess
	}

	var changed bool
	err := s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		result := tx.Model(&BackupJobRecord{}).
			Where("job_id = ? AND status = ?", jobID, storage.JobStatusInProgress).
			Updates(map[string]any{"status": newStatus, "finished_at": now})
		if result.Error != nil {
			return result.Error
		}
		changed = result.RowsAffected > 0
		if changed && !success {
			if err := tx.Delete(&FileVersionRecord{}, "job_id = ?", jobID).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return changed, err
}

// FailStaleInProgressJobs bulk-transitions every in_progress job to failure
// (purging their file_versions in the same transaction). Called once at
// bwfs startup to clean up jobs orphaned by an unclean previous shutdown.
func (s *Store) FailStaleInProgressJobs() (int64, error) {
	var count int64
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var jobIDs []string
		if err := tx.Model(&BackupJobRecord{}).
			Where("status = ?", storage.JobStatusInProgress).
			Pluck("job_id", &jobIDs).Error; err != nil {
			return err
		}
		if len(jobIDs) == 0 {
			return nil
		}
		if err := tx.Delete(&FileVersionRecord{}, "job_id IN ?", jobIDs).Error; err != nil {
			return err
		}
		now := time.Now()
		result := tx.Model(&BackupJobRecord{}).
			Where("job_id IN ?", jobIDs).
			Updates(map[string]any{"status": storage.JobStatusFailure, "finished_at": now})
		if result.Error != nil {
			return result.Error
		}
		count = result.RowsAffected
		return nil
	})
	return count, err
}
