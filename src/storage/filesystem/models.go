package filesystem

import "time"

// ChunkRecord indexes one chunk stored in the pack log: Segment and Offset
// locate its record (the header start), Size is the data length. A row is only
// ever written after its bytes were fsynced (see Store.flush).
type ChunkRecord struct {
	Hash string `gorm:"primaryKey"`
	// (segment, size) is a covering index: compaction's per-segment live-byte
	// sum runs under the exclusive guard and must not scan the whole table,
	// and the same index serves selecting one segment's rows.
	Size      int64 `gorm:"index:idx_chunk_segment_size,priority:2"`
	Segment   int64 `gorm:"index:idx_chunk_segment_size,priority:1"`
	Offset    int64
	CreatedAt time.Time
}

type FileDataRecord struct {
	UUID       string `gorm:"primaryKey"`
	FileID     string `gorm:"index"` // retained for uniqueness/display; not parsed on the query path anymore
	SourceHost string `gorm:"index:idx_file_data_path_host,priority:2"`
	Path       string `gorm:"index:idx_file_data_path_host,priority:1"`
	Mtime      int64
	Size       int64
	Checksum   []byte
	ChunkCount int
	CreatedAt  time.Time
}

type FileDataChunkRecord struct {
	FileID string `gorm:"primaryKey"`
	// ChunkHash is also indexed on its own: the primary key leads with
	// file_id, so the "chunks no link references" anti-join (online vacuum)
	// would otherwise scan the whole table every batch.
	ChunkHash string `gorm:"primaryKey;index"`
	Index     int64  `gorm:"primaryKey"`
}

type FileVersionRecord struct {
	Seq        int64  `gorm:"primaryKey;autoIncrement"`
	ObjectID   string `gorm:"uniqueIndex:idx_job_object;index:idx_file_version_object_created,priority:1"`
	JobID      string `gorm:"uniqueIndex:idx_job_object"`
	SourceHost string `gorm:"index:idx_file_version_path_host,priority:2"`
	Path       string `gorm:"index:idx_file_version_path_host,priority:1"`
	Type       string // single char, from FileInfo.GetType() -- 'f', 'd', 'l', ...
	Metadata   []byte
	Ctime      int64
	ExpireAt   *int64    `gorm:"index"` // unix seconds; NULL = no expiry recorded / never expires
	CreatedAt  time.Time `gorm:"index:idx_file_version_object_created,priority:2"`
}

type BackupJobRecord struct {
	JobID      string `gorm:"primaryKey"`
	SourceHost string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string `gorm:"default:in_progress"`
}

// FileVersionDeletionRecord logs one deleted file version, written in the
// same transaction as the delete itself (see deleteVersions). catalogsync
// replicates the log so the catalog drops the version too; Seq is its
// never-reused ordering key, like FileVersionRecord.Seq.
type FileVersionDeletionRecord struct {
	Seq       int64 `gorm:"primaryKey;autoIncrement"`
	JobID     string
	ObjectID  string
	DeletedAt int64 `gorm:"index"` // unix seconds
}

func (FileVersionDeletionRecord) TableName() string { return "file_version_deletions" }
