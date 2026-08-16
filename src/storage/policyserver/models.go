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

// NodeCertStatus is the most recently reported bootstrap-refresh status
// for hostname -- separate from CheckinRecord (scoped to (PolicyID,
// Hostname) pairs, tracking which policies a node is actively polling)
// because this is a node-wide property with no policy_id to key on:
// bootstrap-refresh is agent's own built-in task, never a policy fetched
// from policy-server. Absence of a row (see Store.CertStatusForHost's
// bool return) means "never reported", distinct from a present row with
// an empty LastError, which means "reported healthy as of LastAttemptAt".
type NodeCertStatus struct {
	Hostname      string `gorm:"primaryKey"`
	LastError     string
	LastAttemptAt time.Time
}
