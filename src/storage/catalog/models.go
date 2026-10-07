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
	ExpireAt       *int64 // unix seconds; NULL = no expiry recorded / never expires
	StoreSeq       int64
	StoreCreatedAt time.Time
	// SourceHost is the real originating (backed-up) host, decoded from
	// Metadata at sync time -- distinct from StoreNode, the bwfs node that
	// sent the batch. Indexed so ListEntries can filter on it directly.
	SourceHost string `gorm:"index"`
	// ParentDirectory is the file's immediate containing directory, and
	// ShortFilename its bare name, both derived from Metadata at sync time
	// the same way SourceHost is, in cmd/catalog/server.go's
	// SyncFileVersions. ParentDirectory is indexed for filtering;
	// ShortFilename is display-only, not a filter dimension.
	ParentDirectory string `gorm:"index"`
	ShortFilename   string
	ReceivedAt      time.Time `gorm:"index"`
	// Damaged is not a column: ListEntries computes it per row from
	// catalog_damaged_files (see DamagedFileRecord), so it is read-only and
	// excluded from migration. It is false wherever a query does not select it.
	Damaged bool `gorm:"->;-:migration"`
}

// DamagedFileRecord says that ObjectID (a bwfs file_id) is currently damaged
// on StoreNode: restoring any version of it from that node will fail. The
// table holds a snapshot, not a log -- catalogsync periodically sends the
// node's complete damaged set and ReplaceDamagedFiles swaps it in, so a row
// that is absent means healthy. It is deliberately not joined to
// entry_records by a foreign key: damage may be reported before the version
// row has been replicated.
type DamagedFileRecord struct {
	StoreNode string `gorm:"primaryKey"`
	ObjectID  string `gorm:"primaryKey"`
}

// TableName names the table after what it holds, as catalog_directories does.
func (DamagedFileRecord) TableName() string {
	return "catalog_damaged_files"
}

// DirectoryRecord is one directory known to exist because some synced
// file's ParentDirectory chain passes through it -- not just directories
// that directly contain a file. Computed once at sync time by walking
// each file's ParentDirectory with splitPath (see
// cmd/catalog/server.go's decodeDirectoryAncestors), the same helper that
// produced ParentDirectory itself. Existence here is intentionally
// filter-independent: see ListDirectoryChildren's comment in store.go for
// why.
type DirectoryRecord struct {
	Path       string `gorm:"uniqueIndex"`
	ParentPath string `gorm:"index"` // "" for a true root: "/", "C:\", "\\server\share\"
	Name       string // short display label, e.g. "lib", or the root itself ("/", "C:\") when ParentPath == ""
	Depth      int    // 0 at a true root, increasing toward the leaf
}

// TableName overrides GORM's default pluralization (which would produce
// "directory_records") so the actual table name matches what every
// comment, docs/protocols/catalog-sync.md, and docs/components/catalog.md
// already call it: catalog_directories.
func (DirectoryRecord) TableName() string {
	return "catalog_directories"
}
