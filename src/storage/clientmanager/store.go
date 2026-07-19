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

// LoadClientView returns hostname's full record: base fields plus resolved
// description and attribute key/value pairs. Returns ErrClientNotFound if
// hostname isn't tracked.
func (s *Store) LoadClientView(hostname string) (*ClientView, error) {
	rec, err := s.GetClient(hostname)
	if err != nil {
		return nil, err
	}

	view := &ClientView{
		Hostname:   rec.Hostname,
		Revoked:    rec.Revoked,
		RevokedAt:  rec.RevokedAt,
		LastSeenAt: rec.LastSeenAt,
		SANs:       rec.SANsList(),
	}

	descs, err := s.KV(hostname, KindDescription)
	if err != nil {
		return nil, err
	}
	view.Descriptions = make(map[string]string, len(descs))
	for _, d := range descs {
		view.Descriptions[d.Key] = d.Value
	}

	attrs, err := s.KV(hostname, KindAttribute)
	if err != nil {
		return nil, err
	}
	view.Attributes = make(map[string]string, len(attrs))
	for _, a := range attrs {
		view.Attributes[a.Key] = a.Value
	}

	return view, nil
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

// UpdateLastSeen records the most recent time hostname successfully
// obtained an operating certificate. Best-effort telemetry -- callers
// should log rather than fail a request on this returning an error.
// Returns ErrClientNotFound if hostname isn't tracked.
func (s *Store) UpdateLastSeen(hostname string, at time.Time) error {
	res := s.db.Model(&ClientRecord{}).Where("hostname = ?", hostname).Update("last_seen_at", at)
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

// AddSAN appends alias to hostname's SAN list if not already present -- a
// no-op, not an error, if it's already there. Returns ErrClientNotFound if
// hostname isn't tracked.
func (s *Store) AddSAN(hostname, alias string) error {
	rec, err := s.GetClient(hostname)
	if err != nil {
		return err
	}
	sans := rec.SANsList()
	for _, existing := range sans {
		if existing == alias {
			return nil
		}
	}
	return s.setSANs(hostname, append(sans, alias))
}

// RemoveSAN removes alias from hostname's SAN list if present -- a no-op,
// not an error, if it isn't there. Returns ErrClientNotFound if hostname
// isn't tracked.
func (s *Store) RemoveSAN(hostname, alias string) error {
	rec, err := s.GetClient(hostname)
	if err != nil {
		return err
	}
	sans := rec.SANsList()
	filtered := make([]string, 0, len(sans))
	for _, existing := range sans {
		if existing != alias {
			filtered = append(filtered, existing)
		}
	}
	return s.setSANs(hostname, filtered)
}

func (s *Store) setSANs(hostname string, sans []string) error {
	sansJSON, err := json.Marshal(sans)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	return s.db.Model(&ClientRecord{}).Where("hostname = ?", hostname).Update("sa_ns", string(sansJSON)).Error
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
