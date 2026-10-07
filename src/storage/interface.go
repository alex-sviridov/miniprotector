package storage

import (
	"context"
	"errors"
	"iter"
	"time"
)

var ErrChunkNotFound = errors.New("chunk not found")

// ErrChunkCorrupt means a chunk's stored bytes are lost for good: they fail
// verification, their segment is gone, or the index row is damaged. Callers
// may drop the chunk (MarkChunkCorrupted). Other read errors (I/O, database
// busy) may be transient and must not lead to dropping anything.
var ErrChunkCorrupt = errors.New("chunk corrupt")

// ErrReclaimIncomplete wraps a Vacuum error that happened only while freeing
// pack segment space, after the database cleanup was committed. The store is
// consistent; some disk space is just not reclaimed yet.
var ErrReclaimIncomplete = errors.New("segment reclaim incomplete")

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

	// MarkChunkCorrupted reacts to a chunk found unusable (ErrChunkCorrupt or
	// ErrChunkNotFound) during restore or compaction. It deletes the chunk's
	// record and links and flags the FileData of every file that depended on
	// it as damaged (kept, not deleted, so the loss stays visible until its
	// versions expire). Dedup ignores damaged FileData, so the next backup
	// uploads those files again instead of skipping them forever.
	MarkChunkCorrupted(chunkHash []byte) error

	// FileVersion operations - create metadata version for each backup
	EnsureFileVersion(jobID, objectID, sourceHost, path, objType string, metadata []byte, ctime int64, expireAt int64) error
	RemoveFileVersion(jobID, objectID string) error

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
	Vacuum() (*VacuumResult, error) // Remove orphaned FileData and Chunks (startup only: assumes nothing is in flight)

	// BeginBackupOp marks one backup-stream message as being handled and
	// returns the function that ends it. CleanupExpired/VacuumOnline run
	// their batches only while no backup operation is in progress, which is
	// what makes it safe to run them while backups are active.
	BeginBackupOp() (end func())
	// CleanupExpired deletes file versions whose expire_at has passed (never
	// those of an in_progress job; never a NULL expire_at), in bounded
	// batches. With dryRun it only counts them.
	CleanupExpired(ctx context.Context, now time.Time, batchSize int, dryRun bool) (*CleanupResult, error)
	// VacuumOnline is Vacuum for a live store: bounded batches, no disk walk,
	// incomplete file data only after incompleteGrace.
	VacuumOnline(ctx context.Context, batchSize int, incompleteGrace time.Duration) (*VacuumResult, error)
	// PruneDeletionLog drops deletion-log rows older than olderThan.
	PruneDeletionLog(ctx context.Context, olderThan time.Time) (int64, error)
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
	JobID     string
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
	// BytesReclaimed is the disk space freed: bytes of removed segments minus bytes compaction copied.
	// Only meaningful when the run returned no error (an interrupted compaction can make it negative).
	BytesReclaimed     int64
	IncompleteFileData int64 // FileData with CRC32=0 (optional cleanup)
	SegmentsRemoved    int64 // Sealed pack segments deleted because no chunk row referenced them
	SegmentsCompacted  int64 // Sealed pack segments whose live chunks were moved, then deleted
}

// CleanupResult reports what CleanupExpired did (or, with DryRun, would do).
type CleanupResult struct {
	VersionsExpired int64
	DryRun          bool
}
