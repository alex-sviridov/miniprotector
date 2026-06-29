package filesystem

import "time"

type ChunkRecord struct {
	Hash      string `gorm:"primaryKey"`
	Size      int64
	CreatedAt time.Time
}

type FileDataRecord struct {
	ID         string `gorm:"primaryKey"`
	FileID     string `gorm:"index"`
	Size       int64
	Checksum   []byte
	ChunkCount int
	CreatedAt  time.Time
}

type FileDataChunkRecord struct {
	FileDataID string `gorm:"primaryKey"`
	ChunkHash  string `gorm:"primaryKey"`
	Index      int64  `gorm:"primaryKey"`
}

type FileVersionRecord struct {
	ID        string `gorm:"primaryKey"`
	ObjectID  string `gorm:"index"`
	FileID    string
	Metadata  []byte
	Ctime     int64
	CreatedAt time.Time
}
