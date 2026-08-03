package policyserver

import "time"

// CheckinRecord is the most recent time hostname received policyID from
// GetPolicies. One row per (PolicyID, Hostname) pair -- upserted on every
// check-in rather than appended, so listing a policy's hosts is a direct
// scan with no aggregation, and a host that stops checking in ages out on
// its own once its one row passes the retention cutoff.
type CheckinRecord struct {
	PolicyID   string `gorm:"primaryKey"`
	Hostname   string `gorm:"primaryKey"`
	LastSeenAt time.Time
}
