package clientmanager

import (
	"encoding/json"
	"time"
)

// ClientRecord is one enrolled client tracked by client-manager.
type ClientRecord struct {
	Hostname   string `gorm:"primaryKey"`
	AddedAt    time.Time
	SANs       string // JSON-encoded []string; "" or "[]" means no extra SANs
	Revoked    bool
	RevokedAt  *time.Time
	LastSeenAt *time.Time
}

// SANsList decodes rec.SANs (JSON-encoded) back into a string slice. A
// malformed or empty value decodes to nil, never an error -- SANs are an
// optional convenience, not something a caller should have to handle a
// parse-failure branch for.
func (rec *ClientRecord) SANsList() []string {
	if rec.SANs == "" {
		return nil
	}
	var sans []string
	if err := json.Unmarshal([]byte(rec.SANs), &sans); err != nil {
		return nil
	}
	return sans
}

// KVKind distinguishes annotation-only descriptions from attributes meant
// to be baked into a client's certificate by a future CA-side mechanism
// (phase 2, not this package).
type KVKind string

const (
	KindDescription KVKind = "description"
	KindAttribute   KVKind = "attribute"
)

// ClientKVRecord is one key/value pair (description or attribute) attached
// to a client. (Hostname, Kind, Key) is the primary key.
type ClientKVRecord struct {
	Hostname string `gorm:"primaryKey"`
	Kind     KVKind `gorm:"primaryKey"`
	Key      string `gorm:"primaryKey"`
	Value    string
}
