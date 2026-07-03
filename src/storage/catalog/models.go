package catalog

import "time"

// EntryRecord is one replicated file-version entry received from a bwfs
// node via catalogsync. (SourceNode, JobID, ObjectID) is the idempotency
// key: JobID/ObjectID alone are only unique within a single bwfs node, so
// SourceNode (the CA-verified hostname of the sending node, from the
// client's mTLS certificate) disambiguates across a fleet of bwfs nodes
// replicating to the same catalog.
type EntryRecord struct {
	ID              int64  `gorm:"primaryKey;autoIncrement"`
	SourceNode      string `gorm:"uniqueIndex:idx_source_job_object"`
	JobID           string `gorm:"uniqueIndex:idx_source_job_object"`
	ObjectID        string `gorm:"uniqueIndex:idx_source_job_object"`
	Metadata        []byte
	Ctime           int64
	SourceSeq       int64
	SourceCreatedAt time.Time
	ReceivedAt      time.Time
}
