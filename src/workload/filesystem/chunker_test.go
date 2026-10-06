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
		if tc.size >= MinChunkSize {
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
		{"binary_min", MinChunkSize, "binary"},
		{"text_min", MinChunkSize, "text"},
		{"pattern_min", MinChunkSize, "pattern"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			fi := FileInfo{path: tempFile}
			chunks := collectChunks(t, fi)

			require.Len(t, chunks, 1, "file of MinChunkSize should produce exactly one chunk")

			chunk := chunks[0]
			assert.Equal(t, int64(0), chunk.Index())
			assert.Equal(t, data, chunk.Data())
			assert.True(t, chunk.IsEOF(), "Single chunk should have EOF=true")
		})
	}
}

// assertCDCInvariants checks the content-defined chunking contract: non-final
// chunks are within [MinChunkSize, MaxChunkSize], the final chunk is at most
// MaxChunkSize, offsets are contiguous, only the last chunk has EOF=true, and
// the chunks reassemble to the original data.
func assertCDCInvariants(t *testing.T, data []byte, chunks []workload.Chunk) {
	t.Helper()
	require.NotEmpty(t, chunks)

	var reassembled []byte
	expectedIndex := int64(0)
	for i, chunk := range chunks {
		assert.Equal(t, expectedIndex, chunk.Index(), "chunk %d offset", i)
		assert.LessOrEqual(t, len(chunk.Data()), MaxChunkSize, "chunk %d too large", i)
		if i < len(chunks)-1 {
			assert.GreaterOrEqual(t, len(chunk.Data()), MinChunkSize, "non-final chunk %d too small", i)
			assert.False(t, chunk.IsEOF(), "Non-last chunk should have EOF=false")
		} else {
			assert.True(t, chunk.IsEOF(), "Last chunk should have EOF=true")
		}
		reassembled = append(reassembled, chunk.Data()...)
		expectedIndex += int64(len(chunk.Data()))
	}
	assert.Equal(t, data, reassembled, "Reassembled data should match original")
}

func TestChunkIterator_BoundaryConditions(t *testing.T) {
	testCases := []struct {
		name     string
		size     int
		dataType string
	}{
		{"one_byte_over_min", MinChunkSize + 1, "binary"},
		{"one_byte_over_max", MaxChunkSize + 1, "binary"},
		{"two_max_plus_small", MaxChunkSize*2 + 1000, "binary"},
		{"three_max_minus_one", MaxChunkSize*3 - 1, "binary"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			chunks := collectChunks(t, FileInfo{path: tempFile})

			assertCDCInvariants(t, data, chunks)
			if tc.size > MaxChunkSize {
				assert.Greater(t, len(chunks), 1, "File larger than MaxChunkSize must be split")
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
		{"large_binary", NormalChunkSize*3 + 5000, "binary"},
		{"large_text", NormalChunkSize*2 + 1000, "text"},
		{"very_large", MaxChunkSize * 5, "binary"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := createDataByType(tc.dataType, tc.size)
			tempFile := createTempFile(t, data)
			defer os.Remove(tempFile)

			chunks := collectChunks(t, FileInfo{path: tempFile})

			assertCDCInvariants(t, data, chunks)
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
		{"large_pattern", NormalChunkSize + 1000, "pattern"},
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
	data := make([]byte, MaxChunkSize*3+500)
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
		{"medium_integrity", NormalChunkSize + 5000, "binary"},
		{"large_integrity", NormalChunkSize*3 + 2000, "binary"},
		{"text_integrity", NormalChunkSize*2 + 500, "text"},
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
		{"two_max", MaxChunkSize*2 + 500},
		{"three_max", MaxChunkSize*3 + 1000},
		{"five_max", MaxChunkSize*5 + 200},
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
		{"medium_file", MaxChunkSize*2 + 1000, "binary"},
		{"large_file", MaxChunkSize*4 + 500, "binary"},
		{"text_file", MaxChunkSize*3 + 200, "text"},
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
	data := make([]byte, MaxChunkSize*4)
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
		NormalChunkSize,       // 64KB
		NormalChunkSize * 10,  // 640KB
		NormalChunkSize * 100, // 6.4MB
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
