package filesystem

import (
	"hash/crc32"
	"io"
	"iter"
	"os"

	"github.com/alex-sviridov/miniprotector/workload"
	"lukechampine.com/blake3"
)

const ChunkSize = 64 * 1024 // 64KB

// ChunkIterator returns an iterator that reads the file in 64KB chunks,
// yielding each chunk with its binary BLAKE3 hash and CRC32 checksum.
// The iterator yields (chunk, nil) for successful reads and (nil, error) for
// failures including file opening or reading errors.
// The EOF field is set to true when the current chunk is the last one in the file.
// File is not locked! Lock the file before chunking.
func (fi FileInfo) ChunkIterator() iter.Seq2[*workload.Chunk, error] {
	return func(yield func(*workload.Chunk, error) bool) {

		file, err := os.Open(fi.Path)
		if err != nil {
			yield(nil, err)
			return
		}
		defer file.Close()

		// Get file size to determine when we reach EOF in chunks
		fileInfo, err := file.Stat()
		if err != nil {
			yield(nil, err)
			return
		}
		fileSize := fileInfo.Size()

		position := int64(0)
		for {
			chunk, err := loadChunk(file, position, fileSize)
			if err != nil {
				if err != io.EOF {
					yield(nil, err)
				}
				return
			}

			if !yield(chunk, nil) {
				return
			}

			// Stop if we hit EOF
			if chunk.EOF {
				return
			}

			position += int64(len(chunk.Data))
		}
	}
}

func loadChunk(file *os.File, position int64, fileSize int64) (*workload.Chunk, error) {
	_, err := file.Seek(position, io.SeekStart)
	if err != nil {
		return nil, err
	}

	buffer := make([]byte, ChunkSize)
	bytesRead, err := file.Read(buffer)
	if err != nil && err != io.EOF {
		return nil, err
	}

	if bytesRead == 0 {
		return nil, io.EOF
	}

	// Trim buffer to actual data
	data := buffer[:bytesRead]

	// Calculate binary hashes
	hasher := blake3.New(32, nil)
	hasher.Write(data)
	hash := hasher.Sum(nil) // Returns []byte

	crc := crc32.NewIEEE()
	crc.Write(data)
	checksum := crc.Sum(nil) // Returns []byte

	// EOF is true when this chunk contains the last bytes of the file
	isEOF := position+int64(bytesRead) >= fileSize

	return &workload.Chunk{
		Hash:     hash,     // Binary BLAKE3 hash (32 bytes)
		Checksum: checksum, // Binary CRC32 checksum (4 bytes)
		Position: position,
		Data:     data,
		EOF:      isEOF,
	}, nil
}
