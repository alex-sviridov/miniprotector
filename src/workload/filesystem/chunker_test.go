package filesystem

import (
	"crypto/rand"
	"fmt"
	"hash/crc32"
	"os"
	"testing"
	"github.com/alex-sviridov/miniprotector/workload"

	"github.com/gofrs/flock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"lukechampine.com/blake3"
)

// testData defines different data types for testing
type testData struct {
	name     string
	size     int
	dataType string
}

func generateTestData() []testData {
	return []testData{
		{"text_small", 100, "text"},
		{"binary_small", 200, "binary"},
		{"text_medium", 5000, "text"},
		{"binary_medium", 8192, "binary"},
		{"pattern_data", 2048, "pattern"},
	}
}

func createDataByType(dataType string, size int) []byte {
	if size == 0 {
		return []byte{}
	}

	data := make([]byte, size)

	switch dataType {
	case "text":
		text := "Lorem ipsum dolor sit amet, consectetur adipiscing elit. Sed do eiusmod tempor incididunt ut labore. "
		for i := 0; i < size; i++ {
			data[i] = text[i%len(text)]
		}
	case "binary":
		rand.Read(data)
	case "pattern":
		for i := 0; i < size; i++ {
			data[i] = byte((i + i/1000) % 256)
		}
	case "static":
		copy(data, []byte("Hello, World! This is test data."))
	}

	return data
}

func TestChunkIterator_EmptyFile(t *testing.T) {
	data := []byte{}
	tempFile := createTempFile(t, data)
	defer os.Remove(tempFile)

	fi := FileInfo{path: tempFile}
	chunks := collectChunks(t, fi)

	assert.Empty(t, chunks, "Empty file should produce no chunks")
}

func TestChunkIterator_SmallFile(t *testing.T) {
	testCases := generateTestData()

	for _, tc := range testCases {
		if tc.size >= ChunkSize {
			continue // Skip large files for this test
		}

		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}
			chunks := collectChunks(t, fi)

			require.Len(t, chunks, 1, "Small file should produce exactly one chunk")

			chunk := chunks[0]
			assert.Equal(t, int64(0), chunk.Index())
			assert.Equal(t, data, chunk.Data())
			assert.True(t, chunk.IsEOF(), "Single chunk should have EOF=true")
		})
	}
}

func TestChunkIterator_ExactlyOneChunk(t *testing.T) {
	testCases := []testData{
		{"binary_64KB", ChunkSize, "binary"},
		{"text_64KB", ChunkSize, "text"},
		{"pattern_64KB", ChunkSize, "pattern"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}
			chunks := collectChunks(t, fi)

			require.Len(t, chunks, 1, "64KB file should produce exactly one chunk")

			chunk := chunks[0]
			assert.Equal(t, int64(0), chunk.Index())
			assert.Equal(t, data, chunk.Data())
			assert.True(t, chunk.IsEOF(), "Single chunk should have EOF=true")
		})
	}
}

func TestChunkIterator_BoundaryConditions(t *testing.T) {
	testCases := []struct {
		name           string
		size           int
		expectedChunks int
		dataType       string
	}{
		{"one_byte_over", ChunkSize + 1, 2, "binary"},
		{"two_chunks_exact", ChunkSize * 2, 2, "binary"},
		{"two_chunks_plus_small", ChunkSize*2 + 1000, 3, "binary"},
		{"three_chunks_minus_one", ChunkSize*3 - 1, 3, "binary"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}
			chunks := collectChunks(t, fi)

			require.Len(t, chunks, tc.expectedChunks, "Should have correct number of chunks")

			// Verify only last chunk has EOF=true
			for i, chunk := range chunks {
				if i == len(chunks)-1 {
					assert.True(t, chunk.IsEOF(), "Last chunk should have EOF=true")
				} else {
					assert.False(t, chunk.IsEOF(), "Non-last chunk should have EOF=false")
				}
			}
		})
	}
}

func TestChunkIterator_MultipleChunks(t *testing.T) {
	testCases := []struct {
		name     string
		size     int
		dataType string
	}{
		{"large_binary", ChunkSize*3 + 5000, "binary"},
		{"large_text", ChunkSize*2 + 1000, "text"},
		{"very_large", ChunkSize * 5, "binary"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}
			chunks := collectChunks(t, fi)

			expectedChunks := (tc.size + ChunkSize - 1) / ChunkSize
			require.Len(t, chunks, expectedChunks, "Should have correct number of chunks")

			// Verify chunk sizes
			for i, chunk := range chunks {
				if i < len(chunks)-1 {
					assert.Len(t, chunk.Data(), ChunkSize, "Non-final chunk should be full size")
				} else {
					expectedLastSize := tc.size % ChunkSize
					if expectedLastSize == 0 {
						expectedLastSize = ChunkSize
					}
					assert.Len(t, chunk.Data(), expectedLastSize, "Final chunk should have correct size")
				}
			}
		})
	}
}

func TestChunkIterator_HashSizes(t *testing.T) {
	testCases := []struct {
		name     string
		size     int
		dataType string
	}{
		{"small_text", 500, "text"},
		{"medium_binary", 10000, "binary"},
		{"large_pattern", ChunkSize + 1000, "pattern"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}
			chunks := collectChunks(t, fi)

			require.NotEmpty(t, chunks, "Should have at least one chunk")

			for i, chunk := range chunks {
				assert.Len(t, chunk.Hash(), 32, "BLAKE3 hash should be 32 bytes for chunk %d", i)
			}
		})
	}
}

func TestChunkIterator_HashCorrectness(t *testing.T) {
	data := createDataByType("static", 100)
	tempFile := createTempFile(t, data)
	defer os.Remove(tempFile)

	fi := FileInfo{path: tempFile}
	chunks := collectChunks(t, fi)

	require.Len(t, chunks, 1, "Should have exactly one chunk")

	chunk := chunks[0]

	// Verify BLAKE3 hash
	expectedHash := blake3.Sum256(data)
	assert.Equal(t, expectedHash[:], chunk.Hash(), "BLAKE3 hash should be correct")

	// Verify CRC32 checksum
	expectedCRC := crc32.ChecksumIEEE(data)
	assert.Equal(t, chunk.Checksum(), expectedCRC, "CRC32 should be correct")
}

func TestChunkIterator_HashUniqueness(t *testing.T) {
	// Create file with multiple chunks of random data
	data := make([]byte, ChunkSize*3+500)
	rand.Read(data)

	tempFile := createTempFile(t, data)
	defer os.Remove(tempFile)

	fi := FileInfo{path: tempFile}
	chunks := collectChunks(t, fi)

	require.Greater(t, len(chunks), 1, "Need multiple chunks for uniqueness test")

	// Verify all chunks have different hashes
	for i := 0; i < len(chunks); i++ {
		for j := i + 1; j < len(chunks); j++ {
			assert.NotEqual(t, chunks[i].Hash(), chunks[j].Hash(),
				"Chunks %d and %d should have different BLAKE3 hashes", i, j)
			assert.NotEqual(t, chunks[i].Checksum(), chunks[j].Checksum(),
				"Chunks %d and %d should have different CRC32 checksums", i, j)
		}
	}
}

func TestChunkIterator_DataIntegrity(t *testing.T) {
	testCases := []struct {
		name     string
		size     int
		dataType string
	}{
		{"small_integrity", 1000, "binary"},
		{"medium_integrity", ChunkSize + 5000, "binary"},
		{"large_integrity", ChunkSize*3 + 2000, "binary"},
		{"text_integrity", ChunkSize*2 + 500, "text"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			originalData := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, originalData)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}
			chunks := collectChunks(t, fi)

			// Reconstruct data from chunks
			reconstructed := make([]byte, 0, len(originalData))
			for _, chunk := range chunks {
				reconstructed = append(reconstructed, chunk.Data()...)
			}

			assert.Equal(t, originalData, reconstructed, "Reconstructed data should match original")
		})
	}
}

func TestChunkIterator_IndexProgression(t *testing.T) {
	testCases := []struct {
		name string
		size int
	}{
		{"two_chunks", ChunkSize*2 + 500},
		{"three_chunks", ChunkSize*3 + 1000},
		{"five_chunks", ChunkSize*5 + 200},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType("binary", tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}
			chunks := collectChunks(t, fi)

			expectedIndex := int64(0)
			for i, chunk := range chunks {
				assert.Equal(t, expectedIndex, chunk.Index(),
					"Chunk %d should have position %d", i, expectedIndex)
				expectedIndex += int64(len(chunk.Data()))
			}

			assert.Equal(t, int64(tc.size), expectedIndex,
				"Final position should equal file size")
		})
	}
}

func TestChunkIterator_IncrementalCRC32(t *testing.T) {
	testCases := []struct {
		name     string
		size     int
		dataType string
	}{
		{"medium_file", ChunkSize*2 + 1000, "binary"},
		{"large_file", ChunkSize*4 + 500, "binary"},
		{"text_file", ChunkSize*3 + 200, "text"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			// Calculate whole-file CRC32
			expectedCRC := crc32.ChecksumIEEE(data)

			// Calculate incremental CRC32
			fi := FileInfo{path: tempFile}
			incrementalCRC := crc32.NewIEEE()
			chunkCount := 0

			for chunk, err := range fi.ChunkIterator() {
				require.NoError(t, err)
				require.NotNil(t, chunk)

				incrementalCRC.Write(chunk.Data())
				chunkCount++
			}

			assert.Greater(t, chunkCount, 1, "Should have multiple chunks")

			actualCRC := incrementalCRC.Sum32()
			assert.Equal(t, expectedCRC, actualCRC,
				"Incremental CRC32 should equal whole-file CRC32")
		})
	}
}

func TestChunkIterator_FileNotFound(t *testing.T) {
	fi := FileInfo{path: "/nonexistent/file.txt"}

	for chunk, err := range fi.ChunkIterator() {
		assert.Nil(t, chunk)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no such file or directory")
		break
	}
}

func TestChunkIterator_FileLocking(t *testing.T) {
	data := createDataByType("text", 1000)
	tempFile := createTempFile(t, data)
	defer os.Remove(tempFile)

	// Lock the file externally
	externalLock := flock.New(tempFile)
	locked, err := externalLock.TryLock()
	require.NoError(t, err)
	require.True(t, locked)
	defer externalLock.Unlock()

	// ChunkIterator should still work as it doesn't implement file locking
	// (The comment in chunker.go says "File is not locked! Lock the file before chunking.")
	fi := FileInfo{path: tempFile}
	chunks := collectChunks(t, fi)

	require.Len(t, chunks, 1, "Should have exactly one chunk")
	assert.Equal(t, data, chunks[0].Data(), "Data should match")
	assert.True(t, chunks[0].IsEOF(), "Single chunk should have EOF=true")
}

func TestChunkIterator_EarlyTermination(t *testing.T) {
	data := make([]byte, ChunkSize*4)
	rand.Read(data)
	tempFile := createTempFile(t, data)
	defer os.Remove(tempFile)

	fi := FileInfo{path: tempFile}
	chunkCount := 0

	for chunk, err := range fi.ChunkIterator() {
		require.NoError(t, err)
		require.NotNil(t, chunk)

		chunkCount++
		if chunkCount == 2 {
			break // Stop early
		}
	}

	assert.Equal(t, 2, chunkCount, "Should stop iteration early")
}

// Benchmark tests
func BenchmarkChunkIterator(b *testing.B) {
	sizes := []int{
		ChunkSize,       // 64KB
		ChunkSize * 10,  // 640KB
		ChunkSize * 100, // 6.4MB
	}

	for _, size := range sizes {
		b.Run(formatSize(size), func(b *testing.B) {
			data := make([]byte, size)
			rand.Read(data)

			tempFile := createTempFile(b, data)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}

			b.ResetTimer()
			b.SetBytes(int64(size))

			for i := 0; i < b.N; i++ {
				for chunk, err := range fi.ChunkIterator() {
					if err != nil {
						b.Fatal(err)
					}
					if chunk == nil {
						break
					}
				}
			}
		})
	}
}

// Helper functions

func createTempFile(tb testing.TB, data []byte) string {
	tempFile, err := os.CreateTemp("", "chunk_test_*.bin")
	require.NoError(tb, err)

	_, err = tempFile.Write(data)
	require.NoError(tb, err)

	err = tempFile.Close()
	require.NoError(tb, err)

	return tempFile.Name()
}

func collectChunks(t *testing.T, fi FileInfo) []workload.Chunk {
	var chunks []workload.Chunk

	for chunk, err := range fi.ChunkIterator() {
		require.NoError(t, err, "Should not get error during iteration")
		require.NotNil(t, chunk, "Chunk should not be nil")
		chunks = append(chunks, chunk)
	}

	return chunks
}

func uint32FromBytes(b []byte) uint32 {
	if len(b) != 4 {
		return 0
	}
	// Read as big-endian (most significant byte first)
	return uint32(b[3]) | uint32(b[2])<<8 | uint32(b[1])<<16 | uint32(b[0])<<24
}

func formatSize(size int) string {
	if size >= 1024*1024 {
		mb := float64(size) / (1024 * 1024)
		return fmt.Sprintf("%.1fMB", mb)
	}
	if size >= 1024 {
		kb := float64(size) / 1024
		return fmt.Sprintf("%.1fKB", kb)
	}
	return fmt.Sprintf("%dB", size)
}
