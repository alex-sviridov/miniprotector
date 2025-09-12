package filesystem

import (
	"fmt"
	"time"

	"github.com/alex-sviridov/miniprotector/storage"
)

// CreateFileVersion creates a new metadata version for each backup
func (s *Store) CreateFileVersion(objectID string, fileID string, metadata []byte, ctime int64) (versionID string, err error) {
	// Mock implementation - return a fake version ID
	return fmt.Sprintf("version_%d", time.Now().UnixNano()), nil
}

// RemoveFileVersion removes a specific file version
func (s *Store) RemoveFileVersion(versionID string) error {
	// Mock implementation - do nothing, return success
	return nil
}

// LatestFileVersion retrieves the most recent version of a file
func (s *Store) LatestFileVersion(objectID string) (*storage.FileVersion, error) {
	// Mock implementation - return not found error
	return nil, fmt.Errorf("file version not found: %s", objectID)
}

// FileVersionAtTime retrieves file version at specific timestamp
func (s *Store) FileVersionAtTime(objectID string, timestamp time.Time) (*storage.FileVersion, error) {
	// Mock implementation - return not found error
	return nil, fmt.Errorf("file version not found at time %v: %s", timestamp, objectID)
}

// FileVersionsInPeriod retrieves all file versions within time period
func (s *Store) FileVersionsInPeriod(from, to time.Time) ([]*storage.FileVersion, error) {
	// Mock implementation - return empty slice
	return []*storage.FileVersion{}, nil
}