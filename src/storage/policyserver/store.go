package policyserver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage/sqlitedb"
)

type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := sqlitedb.Open(sqlitedb.Options{
		Path:   filepath.Join(varDir, "policy-server.sqlite"),
		Models: []any{&CheckinRecord{}, &NodeCertStatus{}},
	})
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
func (s *Store) RecordCheckin(ctx context.Context, policyID, hostname string, at time.Time) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "policy_id"}, {Name: "hostname"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_seen_at"}),
	}).Create(&CheckinRecord{PolicyID: policyID, Hostname: hostname, LastSeenAt: at}).Error
}

// CheckinsForPolicy returns every host that has checked in for policyID,
// ordered by LastSeenAt descending (freshest first, ties broken by hostname
// for determinism) -- the single source of truth for checkin order; nothing
// downstream re-sorts. Each record already holds its most recent check-in
// time (see CheckinRecord). Returns an empty slice, not an error, for a
// policyID with no check-ins.
func (s *Store) CheckinsForPolicy(ctx context.Context, policyID string) ([]CheckinRecord, error) {
	var out []CheckinRecord
	err := s.db.WithContext(ctx).Where("policy_id = ?", policyID).Order("last_seen_at DESC, hostname").Find(&out).Error
	return out, err
}

// DeleteOlderThan removes every check-in whose LastSeenAt is strictly
// before cutoff, returning how many rows were removed.
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := s.db.WithContext(ctx).Where("last_seen_at < ?", cutoff).Delete(&CheckinRecord{})
	return res.RowsAffected, res.Error
}

// DeleteForPolicy removes every check-in row for policyID. Called by
// DeletePolicy so a recreated policy that reuses a deleted one's
// deterministic id (derived from its filename) never inherits stale
// check-ins from the policy it replaced.
func (s *Store) DeleteForPolicy(ctx context.Context, policyID string) error {
	return s.db.WithContext(ctx).Where("policy_id = ?", policyID).Delete(&CheckinRecord{}).Error
}

// RecordCertStatus upserts hostname's current bootstrap-refresh status --
// called on every GetPolicies request, healthy or not, so a recovery
// (an empty-error report overwriting a stale failure) is captured, not
// left stuck. See docs/superpowers/specs/
// 2026-08-16-bootstrap-cert-renewal-design.md.
func (s *Store) RecordCertStatus(ctx context.Context, hostname, lastError string, lastAttemptAt time.Time) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_error", "last_attempt_at"}),
	}).Create(&NodeCertStatus{Hostname: hostname, LastError: lastError, LastAttemptAt: lastAttemptAt}).Error
}

// CertStatusForHost returns hostname's most recently recorded status.
// found is false when hostname has never called GetPolicies with this
// feature active -- distinct from a present row with an empty LastError
// (reported healthy).
func (s *Store) CertStatusForHost(ctx context.Context, hostname string) (NodeCertStatus, bool, error) {
	var out NodeCertStatus
	err := s.db.WithContext(ctx).Where("hostname = ?", hostname).First(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return NodeCertStatus{}, false, nil
	}
	if err != nil {
		return NodeCertStatus{}, false, err
	}
	return out, true, nil
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
