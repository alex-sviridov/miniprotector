package storage

import (
	"errors"
	"iter"
	"time"
)

var ErrChunkNotFound = errors.New("chunk not found")

const (
	JobStatusInProgress = "in_progress"
	JobStatusSuccess    = "success"
	JobStatusFailure    = "failure"
)

// BackupStore represents contract for any backup storage
// Used by backup server to store file data and metadata incrementally
type BackupStore interface {
	// FileData operations - check if file content already exists (only returns true if complete)
	FileDataExists(fileID string) (exists bool, err error)
	CreateFileData(fileID string, size int64) error
	FinalizeFileData(fileID string, checksum []byte) error

	// Chunk operations - handle individual chunks as they arrive over network
	ChunkExists(chunkHash []byte) error
	StoreChunk(chunkHash []byte, data []byte) error
	LinkChunkToFileData(chunkHash []byte, fileID string, index int64) error
	ReadChunk(chunkHash []byte) (data []byte, err error)

	// MarkChunkCorrupted reacts to a chunk read failure (missing file, I/O
	// error) discovered during restore or verify. It removes the chunk file
	// if it's still present, deletes the chunk's DB records, and invalidates
	// the FileData of every file that depended on it, so the next backup
	// sees those files as needing re-upload instead of skipping them forever.
	MarkChunkCorrupted(chunkHash []byte) error

	// FileVersion operations - create metadata version for each backup
	EnsureFileVersion(jobID, objectID string, metadata []byte, ctime int64) error
	RemoveFileVersion(versionID string) error

	// Backup job operations - track discrete backup runs (one brfs invocation each).
	EnsureBackupJob(jobID, sourceHost string) error
	GetBackupJob(jobID string) (*BackupJob, error)
	FileVersionsForJob(jobID string) ([]string, error)
	FinalizeBackupJob(jobID string, success bool) (bool, error)
	FailStaleInProgressJobs() (int64, error)

	// Query operations for restore
	LatestFileVersion(objectID string) (*FileVersion, error)
	FileVersionAtTime(objectID string, timestamp time.Time) (*FileVersion, error)
	FileVersionsInPeriod(from, to time.Time) ([]*FileVersion, error)
	FileData(fileID string) (*FileData, error)
	FileDataChunks(fileID string) iter.Seq2[[]byte, error] // Returns ordered chunk hashes

	// Storage information
	StoreInfo() (*StoreInfo, error)
	Close() error

	// Cleanup operations
	Vacuum() (*VacuumResult, error) // Remove orphaned FileData and Chunks
}

// FileData represents file content information (immutable once created)
type FileData struct {
	UUID       string
	FileID     string // Unique file identifier (e.g., host:path:mtime)
	Size       int64
	CRC32      uint32 // CRC32 checksum of entire file content
	ChunkCount int
	CreatedAt  time.Time
}

// FileVersion represents file metadata for a specific backup
type FileVersion struct {
	UUID      string
	ObjectID  string    // Natural key of the backed-up entity (file today; other entity types later)
	Metadata  []byte    // File attributes, permissions, etc.
	Ctime     int64     // File change time
	CreatedAt time.Time // When backup occurred
}

// BackupJob represents a discrete backup run (one brfs invocation).
type BackupJob struct {
	JobID      string
	SourceHost string
	StartedAt  time.Time
	FinishedAt *time.Time
	Status     string // JobStatusInProgress | JobStatusSuccess | JobStatusFailure
}

// StoreInfo provides statistics about storage usage
type StoreInfo struct {
	TotalFileVersions int64
	TotalFileData     int64
	TotalChunks       int64
	TotalSize         int64
	UniqueChunks      int64 // Number of unique chunks (deduplication info)
}

// VacuumResult provides feedback about cleanup operations
type VacuumResult struct {
	OrphanedFileDataRemoved   int64 // FileData with no FileVersions
	OrphanedChunkLinksRemoved int64 // FileDataChunkRecord rows with no FileDataRecord reference
	OrphanedChunksRemoved     int64 // Chunks with no FileData references
	BytesReclaimed            int64 // Storage space freed
	IncompleteFileData        int64 // FileData with CRC32=0 (optional cleanup)
}
