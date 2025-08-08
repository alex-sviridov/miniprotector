package wfs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/files"
)

// setupTestDB creates a temporary database for testing
func setupTestDB(t *testing.T) (*FileDB, func()) {
	tmpDir, err := os.MkdirTemp("", "filedb_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	db, err := NewFileDB(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create test database: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return db, cleanup
}

// createTestFileInfo creates a sample FileInfo for testing
func createTestFileInfo() files.FileInfo {
	return files.FileInfo{
		Path:       "/test/path/file.txt",
		Name:       "file.txt",
		Size:       1024,
		Mode:       0644,
		Owner:      1000,
		Group:      1000,
		ModTime:    time.Now().Truncate(time.Second), // Truncate to avoid precision issues
		AccessTime: time.Now().Truncate(time.Second),
		CTime:      time.Now().Truncate(time.Second),
		ACL:        nil,
	}
}

func TestNewFileDB(t *testing.T) {
	t.Run("create database with file path", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "filedb_test_*")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		dbPath := filepath.Join(tmpDir, "test.db")
		db, err := NewFileDB(dbPath)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		defer db.Close()

		// Check if database file was created
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			t.Errorf("Database file was not created")
		}
	})

	t.Run("create database with directory path", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "filedb_test_*")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		db, err := NewFileDB(tmpDir)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		defer db.Close()

		// Check if default database file was created
		expectedPath := filepath.Join(tmpDir, "wfs.db")
		if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
			t.Errorf("Database file was not created at expected path")
		}
	})

	t.Run("create database with non-existent directory", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "filedb_test_*")
		if err != nil {
			t.Fatalf("Failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		dbPath := filepath.Join(tmpDir, "subdir", "test.db")
		db, err := NewFileDB(dbPath)
		if err != nil {
			t.Fatalf("Expected no error, got %v", err)
		}
		defer db.Close()

		// Check if database file was created
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			t.Errorf("Database file was not created")
		}
	})
}

func TestAddFile(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	fileInfo := createTestFileInfo()
	host := "test-host"
	checksum := "abc123"

	metadata, err := db.AddFile(host, fileInfo, checksum)
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	if metadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	if metadata.ID == 0 {
		t.Error("Expected non-zero ID")
	}

	if metadata.SourceHost != host {
		t.Errorf("Expected host %s, got %s", host, metadata.SourceHost)
	}

	if metadata.Checksum != checksum {
		t.Errorf("Expected checksum %s, got %s", checksum, metadata.Checksum)
	}

	if metadata.FileInfo.Path != fileInfo.Path {
		t.Errorf("Expected path %s, got %s", fileInfo.Path, metadata.FileInfo.Path)
	}
}

func TestFileExists(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	fileInfo := createTestFileInfo()
	host := "test-host"
	checksum := "abc123"

	// File should not exist initially
	exists, err := db.FileExists(fileInfo.Path, host, fileInfo.ModTime, fileInfo.CTime)
	if err != nil {
		t.Fatalf("Failed to check file existence: %v", err)
	}
	if exists {
		t.Error("Expected file to not exist")
	}

	// Add the file
	_, err = db.AddFile(host, fileInfo, checksum)
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	// File should exist now
	exists, err = db.FileExists(fileInfo.Path, host, fileInfo.ModTime, fileInfo.CTime)
	if err != nil {
		t.Fatalf("Failed to check file existence: %v", err)
	}
	if !exists {
		t.Error("Expected file to exist")
	}

	// Different host should not have the file
	exists, err = db.FileExists(fileInfo.Path, "different-host", fileInfo.ModTime, fileInfo.CTime)
	if err != nil {
		t.Fatalf("Failed to check file existence: %v", err)
	}
	if exists {
		t.Error("Expected file to not exist on different host")
	}
}

func TestFileExistsByChecksum(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	fileInfo := createTestFileInfo()
	host := "test-host"
	checksum := "abc123"

	// File should not exist initially
	exists, err := db.FileExistsByChecksum(checksum)
	if err != nil {
		t.Fatalf("Failed to check file existence by checksum: %v", err)
	}
	if exists {
		t.Error("Expected file to not exist")
	}

	// Empty checksum should return false
	exists, err = db.FileExistsByChecksum("")
	if err != nil {
		t.Fatalf("Failed to check file existence by checksum: %v", err)
	}
	if exists {
		t.Error("Expected empty checksum to return false")
	}

	// Add the file
	_, err = db.AddFile(host, fileInfo, checksum)
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	// File should exist now
	exists, err = db.FileExistsByChecksum(checksum)
	if err != nil {
		t.Fatalf("Failed to check file existence by checksum: %v", err)
	}
	if !exists {
		t.Error("Expected file to exist")
	}

	// Different checksum should not exist
	exists, err = db.FileExistsByChecksum("different123")
	if err != nil {
		t.Fatalf("Failed to check file existence by checksum: %v", err)
	}
	if exists {
		t.Error("Expected file with different checksum to not exist")
	}
}

func TestGetFile(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	fileInfo := createTestFileInfo()
	host := "test-host"
	checksum := "abc123"

	// File should not exist initially
	metadata, err := db.GetFile(fileInfo.Path, host)
	if err != nil {
		t.Fatalf("Failed to get file: %v", err)
	}
	if metadata != nil {
		t.Error("Expected nil metadata for non-existent file")
	}

	// Add the file
	addedMetadata, err := db.AddFile(host, fileInfo, checksum)
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	// Get the file
	retrievedMetadata, err := db.GetFile(fileInfo.Path, host)
	if err != nil {
		t.Fatalf("Failed to get file: %v", err)
	}
	if retrievedMetadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	if retrievedMetadata.ID != addedMetadata.ID {
		t.Errorf("Expected ID %d, got %d", addedMetadata.ID, retrievedMetadata.ID)
	}

	if retrievedMetadata.Checksum != checksum {
		t.Errorf("Expected checksum %s, got %s", checksum, retrievedMetadata.Checksum)
	}

	if retrievedMetadata.FileInfo.Path != fileInfo.Path {
		t.Errorf("Expected path %s, got %s", fileInfo.Path, retrievedMetadata.FileInfo.Path)
	}

	// Check ACL deserialization
	if len(retrievedMetadata.FileInfo.ACL) != len(fileInfo.ACL) {
		t.Error("ACL not properly deserialized")
	}
}

func TestGetFileByChecksum(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	fileInfo := createTestFileInfo()
	host := "test-host"
	checksum := "abc123"

	// File should not exist initially
	metadata, err := db.GetFileByChecksum(checksum)
	if err != nil {
		t.Fatalf("Failed to get file by checksum: %v", err)
	}
	if metadata != nil {
		t.Error("Expected nil metadata for non-existent file")
	}

	// Empty checksum should return nil
	metadata, err = db.GetFileByChecksum("")
	if err != nil {
		t.Fatalf("Failed to get file by checksum: %v", err)
	}
	if metadata != nil {
		t.Error("Expected nil metadata for empty checksum")
	}

	// Add the file
	addedMetadata, err := db.AddFile(host, fileInfo, checksum)
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	// Get the file by checksum
	retrievedMetadata, err := db.GetFileByChecksum(checksum)
	if err != nil {
		t.Fatalf("Failed to get file by checksum: %v", err)
	}
	if retrievedMetadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	if retrievedMetadata.ID != addedMetadata.ID {
		t.Errorf("Expected ID %d, got %d", addedMetadata.ID, retrievedMetadata.ID)
	}

	if retrievedMetadata.Checksum != checksum {
		t.Errorf("Expected checksum %s, got %s", checksum, retrievedMetadata.Checksum)
	}
}

func TestUpdateFile(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	fileInfo := createTestFileInfo()
	host := "test-host"
	checksum := "abc123"

	// Add the file
	addedMetadata, err := db.AddFile(host, fileInfo, checksum)
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	// Update file info
	updatedFileInfo := fileInfo
	updatedFileInfo.Size = 2048
	updatedFileInfo.Mode = 0755
	updatedChecksum := "def456"

	err = db.UpdateFile(fileInfo.Path, host, addedMetadata.BackupTime, updatedFileInfo, updatedChecksum)
	if err != nil {
		t.Fatalf("Failed to update file: %v", err)
	}

	// Get the updated file
	retrievedMetadata, err := db.GetFile(fileInfo.Path, host)
	if err != nil {
		t.Fatalf("Failed to get updated file: %v", err)
	}
	if retrievedMetadata == nil {
		t.Fatal("Expected metadata, got nil")
	}

	if retrievedMetadata.FileInfo.Size != 2048 {
		t.Errorf("Expected size 2048, got %d", retrievedMetadata.FileInfo.Size)
	}

	if retrievedMetadata.FileInfo.Mode != 0755 {
		t.Errorf("Expected mode 0755, got %d", retrievedMetadata.FileInfo.Mode)
	}

	if retrievedMetadata.Checksum != updatedChecksum {
		t.Errorf("Expected checksum %s, got %s", updatedChecksum, retrievedMetadata.Checksum)
	}

	// Try to update non-existent file
	err = db.UpdateFile("/non/existent/path", host, addedMetadata.BackupTime, updatedFileInfo, updatedChecksum)
	if err == nil {
		t.Error("Expected error when updating non-existent file")
	}
}

func TestDeleteFile(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	fileInfo := createTestFileInfo()
	host := "test-host"
	checksum := "abc123"

	// Add the file
	addedMetadata, err := db.AddFile(host, fileInfo, checksum)
	if err != nil {
		t.Fatalf("Failed to add file: %v", err)
	}

	// Verify file exists
	exists, err := db.FileExists(fileInfo.Path, host, fileInfo.ModTime, fileInfo.CTime)
	if err != nil {
		t.Fatalf("Failed to check file existence: %v", err)
	}
	if !exists {
		t.Error("Expected file to exist before deletion")
	}

	// Delete the file
	err = db.DeleteFile(fileInfo.Path, host, addedMetadata.BackupTime)
	if err != nil {
		t.Fatalf("Failed to delete file: %v", err)
	}

	// Verify file no longer exists
	retrievedMetadata, err := db.GetFile(fileInfo.Path, host)
	if err != nil {
		t.Fatalf("Failed to get file after deletion: %v", err)
	}
	if retrievedMetadata != nil {
		t.Error("Expected file to be deleted")
	}

	// Try to delete non-existent file
	err = db.DeleteFile("/non/existent/path", host, addedMetadata.BackupTime)
	if err == nil {
		t.Error("Expected error when deleting non-existent file")
	}
}

func TestMultipleFiles(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	host := "test-host"

	// Add multiple files
	for i := 0; i < 3; i++ {
		fileInfo := createTestFileInfo()
		fileInfo.Path = filepath.Join("/test", "file"+string(rune('0'+i))+".txt")
		fileInfo.Name = "file" + string(rune('0'+i)) + ".txt"
		checksum := "checksum" + string(rune('0'+i))

		_, err := db.AddFile(host, fileInfo, checksum)
		if err != nil {
			t.Fatalf("Failed to add file %d: %v", i, err)
		}
	}

	// Verify all files exist
	for i := 0; i < 3; i++ {
		path := filepath.Join("/test", "file"+string(rune('0'+i))+".txt")
		metadata, err := db.GetFile(path, host)
		if err != nil {
			t.Fatalf("Failed to get file %d: %v", i, err)
		}
		if metadata == nil {
			t.Errorf("File %d should exist", i)
		}
	}
}

func TestClose(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	err := db.Close()
	if err != nil {
		t.Errorf("Failed to close database: %v", err)
	}

	// Second close should not error
	err = db.Close()
	if err != nil {
		t.Errorf("Second close should not error: %v", err)
	}
}
