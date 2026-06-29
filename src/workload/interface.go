package workload

import (
	"iter"
	"time"
)

// BackupObject represents an atom of backup workload (like a single file)
type BackupObject interface {
	ID() string
	Source() string
	Mtime() int64
	Ctime() int64
	MetadataBlob() []byte
	Size() int64
	Lock(timeout time.Duration) (Unlocker, error)
	ChunkIterator() iter.Seq2[Chunk, error]
}

type Unlocker interface {
	Unlock() error
}

// BackupObjectsList represents a filterable array of Backup Objects
type BackupObjectsList interface {
	WithIncludes(patterns ...string) BackupObjectsList
	WithExcludes(patterns ...string) BackupObjectsList
}

type Chunk interface {
	Hash() []byte
	Checksum() uint32
	Index() int64
	Data() []byte
	Size() int
	IsEOF() bool
}
