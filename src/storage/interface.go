package storage

import (
	"github.com/alex-sviridov/miniprotector/workload"
)

// BackupStore represents contract for any backupstorage
type BackupStore interface {
	FileDataExists(object workload.BackupObject) (bool, error)
	LinkFileMetadata(object workload.BackupObject) error
	CheckFileConsistency(object workload.BackupObject) error
	RemoveFileMetadata(object workload.BackupObject) error

	GetFile(object workload.BackupObject) ([]byte, error)

	ChunkExists(hash []byte) (bool, error)
	LinkChunk(object workload.BackupObject, hash []byte) error
	StoreChunk(cd workload.Chunk) error

	RemoveOrphanChunks() error
	GetStoreInfo() (StoreInfo, error)
}

type StoreInfo struct {
	TotalFiles  int64
	TotalChunks int64
	TotalSize   int64
}
