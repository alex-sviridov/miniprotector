package filesystem

import "time"

type ChunkRecord struct {
	Hash      string `gorm:"primaryKey"`
	Size      int64
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
	FileID    string `gorm:"primaryKey"`
	ChunkHash string `gorm:"primaryKey"`
	Index     int64  `gorm:"primaryKey"`
}

type FileVersionRecord struct {
	Seq       int64  `gorm:"primaryKey;autoIncrement"`
	ObjectID  string `gorm:"uniqueIndex:idx_job_object;index:idx_file_version_object_created,priority:1"`
	JobID     string `gorm:"uniqueIndex:idx_job_object"`
	Metadata  []byte
	Ctime     int64
	CreatedAt time.Time `gorm:"index:idx_file_version_object_created,priority:2"`
}

type BackupJobRecord struct {
	JobID      string `gorm:"primaryKey"`
	SourceHost string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string `gorm:"default:in_progress"`
}
