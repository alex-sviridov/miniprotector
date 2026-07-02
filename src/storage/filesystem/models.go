package filesystem

import "time"

type ChunkRecord struct {
	Hash      string `gorm:"primaryKey"`
	Size      int64
	CreatedAt time.Time
}

type FileDataRecord struct {
	UUID       string `gorm:"primaryKey"`
	FileID     string `gorm:"index"`
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
	UUID      string `gorm:"primaryKey"`
	ObjectID  string `gorm:"uniqueIndex:idx_job_object"`
	JobID     string `gorm:"uniqueIndex:idx_job_object"`
	Metadata  []byte
	Ctime     int64
	CreatedAt time.Time
}

type BackupJobRecord struct {
	JobID      string `gorm:"primaryKey"`
	SourceHost string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string `gorm:"default:in_progress"`
}
