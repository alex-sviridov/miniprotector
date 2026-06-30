//go:build e2e

package e2e

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const chunkSize = 64 * 1024 // must match workload/filesystem.ChunkSize

type fileRecord struct {
	size     int64
	checksum uint32 // CRC32-of-chunk-CRC32s, matching bwfs FinalizeFileData
}

// fileSizes defines the sizes of files in each subdirectory.
// 8 files per subdir, total ~125 MiB per subdir = ~250 MiB total.
var fileSizes = []int64{
	4 * 1024 * 1024,  // 4 MiB
	8 * 1024 * 1024,  // 8 MiB
	12 * 1024 * 1024, // 12 MiB
	16 * 1024 * 1024, // 16 MiB
	20 * 1024 * 1024, // 20 MiB
	24 * 1024 * 1024, // 24 MiB
	28 * 1024 * 1024, // 28 MiB
	32 * 1024 * 1024, // 32 MiB
} // sum = 144 MiB per subdir, ~288 MiB total

// generateTestData creates subA/ and subB/ under rootDir, each with 8 files
// of varying sizes. Returns a map from relative path (e.g. "subA/file0.bin")
// to fileRecord with size and CRC32-of-chunk-CRC32s checksum.
func generateTestData(t *testing.T, rootDir string) map[string]fileRecord {
	t.Helper()
	records := make(map[string]fileRecord)
	rng := rand.New(rand.NewSource(42)) // deterministic for reproducibility

	for _, subdir := range []string{"subA", "subB"} {
		dir := filepath.Join(rootDir, subdir)
		require.NoError(t, os.MkdirAll(dir, 0755))
		for i, size := range fileSizes {
			name := fmt.Sprintf("file%d.bin", i)
			rel := filepath.Join(subdir, name)
			path := filepath.Join(rootDir, rel)
			checksum := writeFile(t, path, size, rng)
			records[rel] = fileRecord{size: size, checksum: checksum}
		}
	}
	return records
}

// writeFile writes size bytes of pseudo-random data to path in chunkSize
// increments and returns the CRC32-of-chunk-CRC32s matching bwfs.
func writeFile(t *testing.T, path string, size int64, rng *rand.Rand) uint32 {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()

	fileHasher := crc32.NewIEEE()
	buf := make([]byte, chunkSize)
	remaining := size

	for remaining > 0 {
		n := int64(len(buf))
		if n > remaining {
			n = remaining
		}
		chunk := buf[:n]
		_, err := rng.Read(chunk)
		require.NoError(t, err)
		_, err = f.Write(chunk)
		require.NoError(t, err)

		chunkCRC := crc32.ChecksumIEEE(chunk)
		// Feed chunk CRC big-endian into file hasher — matches handler.go:feedChecksum
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], chunkCRC)
		fileHasher.Write(b[:])

		remaining -= n
	}
	return fileHasher.Sum32()
}
