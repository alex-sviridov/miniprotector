package wfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/files"
)

var testBaseTime = time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

// setupPerfTestDB creates a temporary database for performance testing
func setupPerfTestDB(tb testing.TB) (*FileDB, func()) {
	tmpDir, err := os.MkdirTemp("", "filedb_perf_test_*")
	if err != nil {
		tb.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "perf_test.db")
	db, err := NewFileDB(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		tb.Fatalf("Failed to create test database: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(tmpDir)
	}

	return db, cleanup
}

// createPerfTestFileInfo creates a FileInfo for performance testing
func createPerfTestFileInfo(id int) files.FileInfo {
	return files.FileInfo{
		Path:       fmt.Sprintf("/test/path/file_%d.txt", id),
		Name:       fmt.Sprintf("file_%d.txt", id),
		Size:       int64(1024 + id), // Vary the size slightly
		Mode:       0644,
		Owner:      1000,
		Group:      1000,
        ModTime:    testBaseTime.Add(-time.Duration(id) * time.Minute),
        AccessTime: testBaseTime.Add(-time.Duration(id) * time.Second),
        CTime:      testBaseTime.Add(-time.Duration(id) * time.Hour),
		ACL:        nil,
	}
}

func TestConcurrentWrites(t *testing.T) {
	db, cleanup := setupPerfTestDB(t)
	defer cleanup()

	numGoroutines := 10
	filesPerGoroutine := 100
	host := "perf-test-host"

	var wg sync.WaitGroup
	var mu sync.Mutex
	errors := make([]error, 0)

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < filesPerGoroutine; j++ {
				fileID := goroutineID*filesPerGoroutine + j
				fileInfo := createPerfTestFileInfo(fileID)
				checksum := fmt.Sprintf("checksum_%d", fileID)

				_, err := db.AddFile(host, fileInfo, checksum)
				if err != nil {
					mu.Lock()
					errors = append(errors, err)
					mu.Unlock()
				}
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	if len(errors) > 0 {
		t.Fatalf("Got %d errors during concurrent writes, first error: %v", len(errors), errors[0])
	}

	totalFiles := numGoroutines * filesPerGoroutine
	filesPerSecond := float64(totalFiles) / duration.Seconds()

	t.Logf("Concurrent writes: %d files in %v (%.2f files/sec)",
		totalFiles, duration, filesPerSecond)

	// Verify all files were added
	for i := 0; i < totalFiles; i++ {
		fileInfo := createPerfTestFileInfo(i)
		exists, err := db.FileExists(fileInfo.Path, host, fileInfo.ModTime, fileInfo.CTime)
		if err != nil {
			t.Fatalf("Failed to check file existence: %v", err)
		}
		if !exists {
			t.Errorf("File %d was not found after concurrent write", i)
		}
	}
}

func TestConcurrentReads(t *testing.T) {
	db, cleanup := setupPerfTestDB(t)
	defer cleanup()

	host := "perf-test-host"
	numFiles := 500

	// First, add files to read
	for i := 0; i < numFiles; i++ {
		fileInfo := createPerfTestFileInfo(i)
		checksum := fmt.Sprintf("checksum_%d", i)
		_, err := db.AddFile(host, fileInfo, checksum)
		if err != nil {
			t.Fatalf("Failed to add file %d: %v", i, err)
		}
	}

	numGoroutines := 20
	readsPerGoroutine := 50

	var wg sync.WaitGroup
	var mu sync.Mutex
	errors := make([]error, 0)
	totalReads := 0

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < readsPerGoroutine; j++ {
				fileID := (goroutineID*readsPerGoroutine + j) % numFiles
				fileInfo := createPerfTestFileInfo(fileID)

				metadata, err := db.GetFile(fileInfo.Path, host)
				if err != nil {
					mu.Lock()
					errors = append(errors, err)
					mu.Unlock()
					continue
				}
				if metadata == nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("file %d not found", fileID))
					mu.Unlock()
					continue
				}

				mu.Lock()
				totalReads++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	if len(errors) > 0 {
		t.Fatalf("Got %d errors during concurrent reads, first error: %v", len(errors), errors[0])
	}

	readsPerSecond := float64(totalReads) / duration.Seconds()
	t.Logf("Concurrent reads: %d reads in %v (%.2f reads/sec)",
		totalReads, duration, readsPerSecond)
}

func TestMixedReadWrites(t *testing.T) {
	db, cleanup := setupPerfTestDB(t)
	defer cleanup()

	host := "perf-test-host"
	numReaders := 5
	numWriters := 5
	operationsPerGoroutine := 50

	// Add some initial files for readers
	initialFiles := 100
	for i := 0; i < initialFiles; i++ {
		fileInfo := createPerfTestFileInfo(i)
		checksum := fmt.Sprintf("initial_checksum_%d", i)
		_, err := db.AddFile(host, fileInfo, checksum)
		if err != nil {
			t.Fatalf("Failed to add initial file %d: %v", i, err)
		}
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	errors := make([]error, 0)
	totalReads := 0
	totalWrites := 0

	start := time.Now()

	// Start readers
	for i := 0; i < numReaders; i++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerGoroutine; j++ {
				fileID := j % initialFiles
				fileInfo := createPerfTestFileInfo(fileID)

				metadata, err := db.GetFile(fileInfo.Path, host)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("reader %d: %v", readerID, err))
					mu.Unlock()
					continue
				}
				if metadata == nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("reader %d: file %d not found", readerID, fileID))
					mu.Unlock()
					continue
				}

				mu.Lock()
				totalReads++
				mu.Unlock()
			}
		}(i)
	}

	// Start writers
	for i := 0; i < numWriters; i++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()

			for j := 0; j < operationsPerGoroutine; j++ {
				fileID := initialFiles + writerID*operationsPerGoroutine + j
				fileInfo := createPerfTestFileInfo(fileID)
				checksum := fmt.Sprintf("writer_%d_checksum_%d", writerID, j)

				_, err := db.AddFile(host, fileInfo, checksum)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("writer %d: %v", writerID, err))
					mu.Unlock()
					continue
				}

				mu.Lock()
				totalWrites++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	if len(errors) > 0 {
		t.Fatalf("Got %d errors during mixed operations, first error: %v", len(errors), errors[0])
	}

	totalOps := totalReads + totalWrites
	opsPerSecond := float64(totalOps) / duration.Seconds()

	t.Logf("Mixed operations: %d reads + %d writes = %d total ops in %v (%.2f ops/sec)",
		totalReads, totalWrites, totalOps, duration, opsPerSecond)
}

func TestConcurrentChecksumOperations(t *testing.T) {
	db, cleanup := setupPerfTestDB(t)
	defer cleanup()

	host := "perf-test-host"
	numGoroutines := 8
	operationsPerGoroutine := 100

	var wg sync.WaitGroup
	var mu sync.Mutex
	errors := make([]error, 0)
	totalChecksumChecks := 0
	totalChecksumGets := 0

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < operationsPerGoroutine; j++ {
				fileID := goroutineID*operationsPerGoroutine + j
				fileInfo := createPerfTestFileInfo(fileID)
				checksum := fmt.Sprintf("checksum_%d", fileID)

				// Add file
				_, err := db.AddFile(host, fileInfo, checksum)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("goroutine %d: add file: %v", goroutineID, err))
					mu.Unlock()
					continue
				}

				// Check if checksum exists
				exists, err := db.FileExistsByChecksum(checksum)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("goroutine %d: checksum exists: %v", goroutineID, err))
					mu.Unlock()
					continue
				}
				if !exists {
					mu.Lock()
					errors = append(errors, fmt.Errorf("goroutine %d: checksum should exist", goroutineID))
					mu.Unlock()
					continue
				}
				mu.Lock()
				totalChecksumChecks++
				mu.Unlock()

				// Get file by checksum
				metadata, err := db.GetFileByChecksum(checksum)
				if err != nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("goroutine %d: get by checksum: %v", goroutineID, err))
					mu.Unlock()
					continue
				}
				if metadata == nil {
					mu.Lock()
					errors = append(errors, fmt.Errorf("goroutine %d: file not found by checksum", goroutineID))
					mu.Unlock()
					continue
				}
				mu.Lock()
				totalChecksumGets++
				mu.Unlock()
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	if len(errors) > 0 {
		t.Fatalf("Got %d errors during checksum operations, first error: %v", len(errors), errors[0])
	}

	totalOps := totalChecksumChecks + totalChecksumGets
	opsPerSecond := float64(totalOps) / duration.Seconds()

	t.Logf("Checksum operations: %d checks + %d gets = %d total ops in %v (%.2f ops/sec)",
		totalChecksumChecks, totalChecksumGets, totalOps, duration, opsPerSecond)
}

// Benchmark functions for Go's built-in benchmarking
func BenchmarkSingleAddFile(b *testing.B) {
	db, cleanup := setupPerfTestDB(b)
	defer cleanup()

	host := "benchmark-host"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fileInfo := createPerfTestFileInfo(i)
		checksum := fmt.Sprintf("benchmark_checksum_%d", i)

		_, err := db.AddFile(host, fileInfo, checksum)
		if err != nil {
			b.Fatalf("Failed to add file: %v", err)
		}
	}
}

func BenchmarkSingleGetFile(b *testing.B) {
	db, cleanup := setupPerfTestDB(b)
	defer cleanup()

	host := "benchmark-host"

	// Pre-populate with files
	for i := 0; i < b.N; i++ {
		fileInfo := createPerfTestFileInfo(i)
		checksum := fmt.Sprintf("benchmark_checksum_%d", i)
		_, err := db.AddFile(host, fileInfo, checksum)
		if err != nil {
			b.Fatalf("Failed to add file: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fileInfo := createPerfTestFileInfo(i)
		_, err := db.GetFile(fileInfo.Path, host)
		if err != nil {
			b.Fatalf("Failed to get file: %v", err)
		}
	}
}

func BenchmarkConcurrentWrites(b *testing.B) {
	db, cleanup := setupPerfTestDB(b)
	defer cleanup()

	host := "benchmark-host"

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			fileInfo := createPerfTestFileInfo(i)
			checksum := fmt.Sprintf("benchmark_checksum_%d", i)

			_, err := db.AddFile(host, fileInfo, checksum)
			if err != nil {
				b.Fatalf("Failed to add file: %v", err)
			}
			i++
		}
	})
}

func BenchmarkConcurrentReads(b *testing.B) {
	db, cleanup := setupPerfTestDB(b)
	defer cleanup()

	host := "benchmark-host"

	// Pre-populate with files
	numFiles := 1000
	for i := 0; i < numFiles; i++ {
		fileInfo := createPerfTestFileInfo(i)
		checksum := fmt.Sprintf("benchmark_checksum_%d", i)
		_, err := db.AddFile(host, fileInfo, checksum)
		if err != nil {
			b.Fatalf("Failed to add file: %v", err)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			fileID := i % numFiles
			fileInfo := createPerfTestFileInfo(fileID)

			_, err := db.GetFile(fileInfo.Path, host)
			if err != nil {
				b.Fatalf("Failed to get file: %v", err)
			}
			i++
		}
	})
}
