package filesystem

import "github.com/alex-sviridov/miniprotector/storage"

// StoreInfo retrieves storage statistics
func (s *Store) StoreInfo() (*storage.StoreInfo, error) {
	// Mock implementation - return empty statistics
	return &storage.StoreInfo{
		TotalFileVersions: 0,
		TotalFileData:     0,
		TotalChunks:       0,
		TotalSize:         0,
		UniqueChunks:      0,
	}, nil
}

// Vacuum removes orphaned FileData and Chunks
func (s *Store) Vacuum() (*storage.VacuumResult, error) {
	// Mock implementation - return no cleanup performed
	return &storage.VacuumResult{
		OrphanedFileDataRemoved: 0,
		OrphanedChunksRemoved:   0,
		BytesReclaimed:          0,
		IncompleteFileData:      0,
	}, nil
}