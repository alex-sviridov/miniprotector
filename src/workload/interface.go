package workload

import (
	"iter"
)

// BackupObject represents an atom of backup workload (like a single file)
type BackupObject interface {
	GetId() string
	GetSize() int64
	Print() string
	ChunkIterator() iter.Seq2[*Chunk, error]
	match(string) bool
}

// BackupObjectsList represents a filterable array of Backup Objects
type BackupObjectsList interface {
	WithIncludes(patterns ...string) BackupObjectsList
	WithExcludes(patterns ...string) BackupObjectsList
}
