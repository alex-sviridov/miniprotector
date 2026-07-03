package catalog

import (
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func New(basePath string) (*Store, error) {
	db, err := openDB(basePath)
	if err != nil {
		return nil, fmt.Errorf("open catalog db: %w", err)
	}
	return &Store{db: db}, nil
}

// Entry mirrors EntryRecord's replicated fields, decoupled from the gorm
// model so callers (the gRPC server) don't need to import gorm tags.
type Entry struct {
	SourceNode      string
	JobID           string
	ObjectID        string
	Metadata        []byte
	Ctime           int64
	SourceSeq       int64
	SourceCreatedAt time.Time
}

// EnsureEntries idempotently persists batch: a row already present for a
// given (SourceNode, JobID, ObjectID) is left untouched rather than
// erroring — catalogsync retries a batch it isn't sure was received, so a
// resend after a partial success must be a safe no-op.
func (s *Store) EnsureEntries(batch []Entry) error {
	if len(batch) == 0 {
		return nil
	}
	records := make([]EntryRecord, len(batch))
	now := time.Now()
	for i, e := range batch {
		records[i] = EntryRecord{
			SourceNode:      e.SourceNode,
			JobID:           e.JobID,
			ObjectID:        e.ObjectID,
			Metadata:        e.Metadata,
			Ctime:           e.Ctime,
			SourceSeq:       e.SourceSeq,
			SourceCreatedAt: e.SourceCreatedAt,
			ReceivedAt:      now,
		}
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_node"}, {Name: "job_id"}, {Name: "object_id"}},
		DoNothing: true,
	}).Create(&records).Error
}

// Count returns the total number of persisted entries.
func (s *Store) Count() (int64, error) {
	var n int64
	err := s.db.Model(&EntryRecord{}).Count(&n).Error
	return n, err
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
