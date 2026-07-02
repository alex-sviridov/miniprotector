package filesystem

import (
	"time"

	"gorm.io/gorm/clause"
)

// EnsureBackupJob idempotently records that a backup job has started. Safe
// to call once per stream of a multi-stream job — only the first call for a
// given jobID creates a row; later calls are no-ops.
func (s *Store) EnsureBackupJob(jobID, sourceHost string) error {
	record := BackupJobRecord{
		JobID:      jobID,
		SourceHost: sourceHost,
		StartedAt:  time.Now(),
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "job_id"}},
		DoNothing: true,
	}).Create(&record).Error
}

// FinishBackupJob marks a backup job complete by setting finished_at.
func (s *Store) FinishBackupJob(jobID string) error {
	return s.db.Model(&BackupJobRecord{}).
		Where("job_id = ?", jobID).
		Update("finished_at", time.Now()).Error
}
