package clientmanager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/alex-sviridov/miniprotector/storage/sqlitedb"
)

var (
	ErrClientExists   = errors.New("client already exists")
	ErrClientNotFound = errors.New("client not found")
	// ErrSANConflict is returned when a SAN alias (or a new hostname) collides
	// with another client's hostname or SAN alias. A certificate's SANs are
	// what peers verify when dialing, so two clients sharing a name would let
	// one present a server certificate valid for the other.
	ErrSANConflict = errors.New("name already in use by another client")
)

// checkNameFree returns ErrSANConflict (wrapped with the offending name and
// owner) if any name in names is the hostname or a SAN alias of a client other
// than self. Runs against tx so callers can make the check and the write
// atomic.
func checkNameFree(tx *gorm.DB, self string, names ...string) error {
	var recs []ClientRecord
	if err := tx.Find(&recs).Error; err != nil {
		return err
	}
	for _, rec := range recs {
		if rec.Hostname == self {
			continue
		}
		owned := append([]string{rec.Hostname}, rec.SANsList()...)
		for _, name := range names {
			for _, o := range owned {
				if o == name {
					return fmt.Errorf("%w: %q belongs to %q", ErrSANConflict, name, rec.Hostname)
				}
			}
		}
	}
	return nil
}

type Store struct {
	db *gorm.DB
}

func New(varDir string) (*Store, error) {
	db, err := sqlitedb.Open(sqlitedb.Options{
		Path:   filepath.Join(varDir, "clientmanager.sqlite"),
		Models: []any{&ClientRecord{}, &ClientKVRecord{}},
	})
	if err != nil {
		return nil, fmt.Errorf("open client-manager db: %w", err)
	}
	return &Store{db: db}, nil
}

// AddClient records a newly-enrolled client. Returns ErrClientExists if
// hostname is already tracked -- callers use re-enrollment or
// description/attribute updates for an existing client instead of add.
func (s *Store) AddClient(ctx context.Context, hostname string, sans []string, addedAt time.Time) error {
	sansJSON, err := json.Marshal(sans)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&ClientRecord{}).Where("hostname = ?", hostname).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return ErrClientExists
		}
		if err := checkNameFree(tx, hostname, append([]string{hostname}, sans...)...); err != nil {
			return err
		}
		return tx.Create(&ClientRecord{Hostname: hostname, SANs: string(sansJSON), AddedAt: addedAt}).Error
	})
}

// GetClient returns hostname's record, or ErrClientNotFound.
func (s *Store) GetClient(ctx context.Context, hostname string) (*ClientRecord, error) {
	var rec ClientRecord
	err := s.db.WithContext(ctx).First(&rec, "hostname = ?", hostname).Error
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
func (s *Store) LoadClientView(ctx context.Context, hostname string) (*ClientView, error) {
	rec, err := s.GetClient(ctx, hostname)
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

	descs, err := s.KV(ctx, hostname, KindDescription)
	if err != nil {
		return nil, err
	}
	view.Descriptions = make(map[string]string, len(descs))
	for _, d := range descs {
		view.Descriptions[d.Key] = d.Value
	}

	attrs, err := s.KV(ctx, hostname, KindAttribute)
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
func (s *Store) ListClients(ctx context.Context) ([]ClientRecord, error) {
	var recs []ClientRecord
	err := s.db.WithContext(ctx).Order("hostname").Find(&recs).Error
	return recs, err
}

// SetRevoked updates hostname's revoked flag/timestamp. Returns
// ErrClientNotFound if hostname isn't tracked. Clearing the flag also
// clears revoked_at.
func (s *Store) SetRevoked(ctx context.Context, hostname string, revoked bool, at time.Time) error {
	updates := map[string]any{"revoked": revoked}
	if revoked {
		updates["revoked_at"] = at
	} else {
		updates["revoked_at"] = nil
	}
	res := s.db.WithContext(ctx).Model(&ClientRecord{}).Where("hostname = ?", hostname).Updates(updates)
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
func (s *Store) UpdateLastSeen(ctx context.Context, hostname string, at time.Time) error {
	res := s.db.WithContext(ctx).Model(&ClientRecord{}).Where("hostname = ?", hostname).Update("last_seen_at", at)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrClientNotFound
	}
	return nil
}

// KV returns all rows of the given kind for hostname, ordered by key.
func (s *Store) KV(ctx context.Context, hostname string, kind KVKind) ([]ClientKVRecord, error) {
	var recs []ClientKVRecord
	err := s.db.WithContext(ctx).Where("hostname = ? AND kind = ?", hostname, kind).Order("key").Find(&recs).Error
	return recs, err
}

// SetKV upserts one key/value pair for hostname. Returns ErrClientNotFound
// if hostname isn't tracked.
func (s *Store) SetKV(ctx context.Context, hostname string, kind KVKind, key, value string) error {
	if kind == KindAttribute && key == RoleAttributeKey {
		if err := ValidateRole(value); err != nil {
			return err
		}
	}
	if _, err := s.GetClient(ctx, hostname); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "hostname"}, {Name: "kind"}, {Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value"}),
	}).Create(&ClientKVRecord{Hostname: hostname, Kind: kind, Key: key, Value: value}).Error
}

// UnsetKV deletes one key/value pair for hostname. Returns ErrClientNotFound
// if hostname isn't tracked.
func (s *Store) UnsetKV(ctx context.Context, hostname string, kind KVKind, key string) error {
	if _, err := s.GetClient(ctx, hostname); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Delete(&ClientKVRecord{}, "hostname = ? AND kind = ? AND key = ?", hostname, kind, key).Error
}

// AddSAN appends alias to hostname's SAN list if not already present -- a
// no-op, not an error, if it's already there. Returns ErrClientNotFound if
// hostname isn't tracked, and ErrSANConflict if alias is another client's
// hostname or alias.
func (s *Store) AddSAN(ctx context.Context, hostname, alias string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rec ClientRecord
		if err := tx.First(&rec, "hostname = ?", hostname).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrClientNotFound
			}
			return err
		}
		sans := rec.SANsList()
		for _, existing := range sans {
			if existing == alias {
				return nil
			}
		}
		if err := checkNameFree(tx, hostname, alias); err != nil {
			return err
		}
		return setSANs(tx, hostname, append(sans, alias))
	})
}

// RemoveSAN removes alias from hostname's SAN list if present -- a no-op,
// not an error, if it isn't there. Returns ErrClientNotFound if hostname
// isn't tracked.
func (s *Store) RemoveSAN(ctx context.Context, hostname, alias string) error {
	rec, err := s.GetClient(ctx, hostname)
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
	return setSANs(s.db.WithContext(ctx), hostname, filtered)
}

func setSANs(db *gorm.DB, hostname string, sans []string) error {
	sansJSON, err := json.Marshal(sans)
	if err != nil {
		return fmt.Errorf("marshal sans: %w", err)
	}
	return db.Model(&ClientRecord{}).Where("hostname = ?", hostname).Update("sa_ns", string(sansJSON)).Error
}

func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
