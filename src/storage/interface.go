package storage

import (
	"github.com/alex-sviridov/miniprotector/reader/filesystem"
)

// BackupStore represents contract for any backupstorage
type BackupStore interface {
    FileDataExists(fi filesystem.FileInfo) (bool, error)
    LinkFileMetadata(fi filesystem.FileInfo) error
    CheckFileConsistency(fi filesystem.FileInfo) error
    RemoveFileMetadata(fi filesystem.FileInfo) error

    GetFile(fi filesystem.FileInfo) ([]byte, error)
    
    ChunkExists(ci ChunkInfo) (bool, error)
    StoreChunk(ci ChunkInfo, data []byte) error
    LinkChunk(fi filesystem.FileInfo, ci ChunkInfo) error
    
    RemoveOrphanChunks() error
    GetStoreInfo() (StoreInfo, error)
}

type ChunkInfo struct {
    Hash  string
    Index int
    Size  int
}

type StoreInfo struct {
    TotalFiles  int64
    TotalChunks int64
    TotalSize   int64
}