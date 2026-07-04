package clientmanager

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrClientExists   = errors.New("client already exists")
	ErrClientNotFound = errors.New("client not found")
)

type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := openDB(varDir)
	if err != nil {
		return nil, fmt.Errorf("open client-manager db: %w", err)
	}
	return &Store{db: db}, nil
}

// AddClient records a newly-enrolled client. Returns ErrClientExists if
// hostname is already tracked -- callers use re-enrollment or
// description/attribute updates for an existing client instead of add.
func (s *Store) AddClient(hostname string, sans []string, addedAt time.Time) error {
	sansJSON, err := json.Marshal(sans)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&ClientRecord{}).Where("hostname = ?", hostname).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrClientExists
		}
		return tx.Create(&ClientRecord{Hostname: hostname, SANs: string(sansJSON), AddedAt: addedAt}).Error
	})
}

// GetClient returns hostname's record, or ErrClientNotFound.
func (s *Store) GetClient(hostname string) (*ClientRecord, error) {
	var rec ClientRecord
	err := s.db.First(&rec, "hostname = ?", hostname).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrClientNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

// ListClients returns every tracked client, ordered by hostname.
func (s *Store) ListClients() ([]ClientRecord, error) {
	var recs []ClientRecord
	err := s.db.Order("hostname").Find(&recs).Error
	return recs, err
}

// SetRevoked updates hostname's revoked flag/timestamp. Returns
// ErrClientNotFound if hostname isn't tracked. Clearing the flag also
// clears revoked_at.
func (s *Store) SetRevoked(hostname string, revoked bool, at time.Time) error {
	updates := map[string]any{"revoked": revoked}
	if revoked {
		updates["revoked_at"] = at
	} else {
		updates["revoked_at"] = nil
	}
	res := s.db.Model(&ClientRecord{}).Where("hostname = ?", hostname).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrClientNotFound
	}
	return nil
}

// KV returns all rows of the given kind for hostname, ordered by key.
func (s *Store) KV(hostname string, kind KVKind) ([]ClientKVRecord, error) {
	var recs []ClientKVRecord
	err := s.db.Where("hostname = ? AND kind = ?", hostname, kind).Order("key").Find(&recs).Error
	return recs, err
}

// SetKV upserts one key/value pair for hostname. Returns ErrClientNotFound
// if hostname isn't tracked.
func (s *Store) SetKV(hostname string, kind KVKind, key, value string) error {
	if _, err := s.GetClient(hostname); err != nil {
		return err
	}
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}, {Name: "kind"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&ClientKVRecord{Hostname: hostname, Kind: kind, Key: key, Value: value}).Error
}

// UnsetKV deletes one key/value pair for hostname. Returns ErrClientNotFound
// if hostname isn't tracked.
func (s *Store) UnsetKV(hostname string, kind KVKind, key string) error {
	if _, err := s.GetClient(hostname); err != nil {
		return err
	}
	return s.db.Delete(&ClientKVRecord{}, "hostname = ? AND kind = ? AND key = ?", hostname, kind, key).Error
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
