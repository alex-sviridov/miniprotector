package filesystem

import (
	"fmt"
	"iter"

	"github.com/alex-sviridov/miniprotector/storage"
)

// FileDataExists checks if file content already exists (only returns true if complete)
func (s *Store) FileDataExists(fileID string) (exists bool, err error) {
	// Mock implementation - always return false (no files exist)
	return false, nil
}

// CreateFileData creates a new FileData record
func (s *Store) CreateFileData(fileID string, size int64) error {
	// Mock implementation - do nothing, return success
	return nil
}

// FinalizeFileData marks FileData as complete with CRC32
func (s *Store) FinalizeFileData(fileID string, checksum []byte) error {
	// Mock implementation - do nothing, return success
	return nil
}

// FileData retrieves FileData by fileID
func (s *Store) FileData(fileID string) (*storage.FileData, error) {
	// Mock implementation - return not found error
	return nil, fmt.Errorf("filedata not found: %s", fileID)
}

// FileDataChunks returns iterator over chunk hashes for a FileData
func (s *Store) FileDataChunks(fileID string) iter.Seq2[[]byte, error] {
	// Mock implementation - return empty iterator
	return func(yield func([]byte, error) bool) {
		// Empty iterator - no chunks
	}
}
