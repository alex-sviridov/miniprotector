package catalog

import "time"

// EntryRecord is one replicated file-version entry received from a bwfs
// node via catalogsync. (StoreNode, JobID, ObjectID) is the idempotency
// key: JobID/ObjectID alone are only unique within a single bwfs node, so
// StoreNode (the CA-verified hostname of the sending node, from the
// client's mTLS certificate) disambiguates across a fleet of bwfs nodes
// replicating to the same catalog.
type EntryRecord struct {
	ID             int64  `gorm:"primaryKey;autoIncrement"`
	StoreNode      string `gorm:"uniqueIndex:idx_store_job_object"`
	JobID          string `gorm:"uniqueIndex:idx_store_job_object;index"`
	ObjectID       string `gorm:"uniqueIndex:idx_store_job_object"`
	Metadata       []byte
	Ctime          int64
	StoreSeq       int64
	StoreCreatedAt time.Time
	// SourceHost is the real originating (backed-up) host, decoded from
	// Metadata at sync time -- distinct from StoreNode, the bwfs node that
	// sent the batch. Indexed so ListEntries can filter on it directly.
	SourceHost string `gorm:"index"`
	ReceivedAt time.Time `gorm:"index"`
}
