package policyserver

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := openDB(varDir)
	if err != nil {
		return nil, fmt.Errorf("open policy-server db: %w", err)
	}
	return &Store{db: db}, nil
}

// RecordCheckin upserts hostname's check-in for policyID, setting
// LastSeenAt to at -- overwrites any existing row for the same
// (policyID, hostname) pair rather than appending. GORM's Save does not
// insert a new row for an already-set composite primary key, so the
// upsert must go through an explicit ON CONFLICT clause.
func (s *Store) RecordCheckin(policyID, hostname string, at time.Time) error {
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "policy_id"}, {Name: "hostname"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_seen_at"}),
	}).Create(&CheckinRecord{PolicyID: policyID, Hostname: hostname, LastSeenAt: at}).Error
}

// CheckinsForPolicy returns every host that has checked in for policyID,
// ordered by hostname, each already holding its most recent check-in time
// (see CheckinRecord). Returns an empty slice, not an error, for a policyID
// with no check-ins.
func (s *Store) CheckinsForPolicy(policyID string) ([]CheckinRecord, error) {
	var out []CheckinRecord
	err := s.db.Where("policy_id = ?", policyID).Order("hostname").Find(&out).Error
	return out, err
}

// DeleteOlderThan removes every check-in whose LastSeenAt is strictly
// before cutoff, returning how many rows were removed.
func (s *Store) DeleteOlderThan(cutoff time.Time) (int64, error) {
	res := s.db.Where("last_seen_at < ?", cutoff).Delete(&CheckinRecord{})
	return res.RowsAffected, res.Error
}

// DeleteForPolicy removes every check-in row for policyID. Called by
// DeletePolicy so a recreated policy that reuses a deleted one's
// deterministic id (derived from its filename) never inherits stale
// check-ins from the policy it replaced.
func (s *Store) DeleteForPolicy(policyID string) error {
	return s.db.Where("policy_id = ?", policyID).Delete(&CheckinRecord{}).Error
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
