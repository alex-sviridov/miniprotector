package filesystem

import (
	"io"
	"iter"

	"encoding/hex"
	"os"

	"github.com/alex-sviridov/miniprotector/workload"
	"lukechampine.com/blake3"
)

const ChunkSize = 64 * 1024 // 64KB

type Chunk struct {
	hash  []byte // blake3 hash for dedup
	index int64  // file offset
	data  []byte // chunk data
	eof   bool   // end of file flag when chunk is the last one
}

func NewChunk(hash []byte, index int64, eof bool, data []byte) *Chunk {
	if hash == nil && data != nil {
		hash32 := blake3.Sum256(data)
		hash = hash32[:]
	}
	return &Chunk{
		hash:  hash,
		index: index,
		data:  data,
		eof:   eof,
	}
}

func (c Chunk) Hash() []byte {
	return c.hash[:]
}

func (c Chunk) String() string {
	s := hex.EncodeToString(c.hash[:])
	return s[:4] + "..." + s[len(s)-4:]
}

func (c Chunk) Index() int64 {
	return c.index
}

func (c Chunk) Data() []byte {
	return c.data
}

func (c Chunk) IsEOF() bool {
	return c.eof
}

func (c Chunk) Size() int {
	return len(c.data)
}

// ChunkIterator returns an iterator that reads the file in 64KB chunks,
// yielding each chunk with its binary BLAKE3 hash and CRC32 checksum.
// The iterator yields (chunk, nil) for successful reads and (nil, error) for
// failures including file opening or reading errors.
// The EOF field is set to true when the current chunk is the last one in the file.
// File is not locked! Lock the file before chunking.
func (fi FileInfo) ChunkIterator() iter.Seq2[workload.Chunk, error] {
	return func(yield func(workload.Chunk, error) bool) {

		file, err := os.Open(fi.path)
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

			if !yield(*chunk, nil) {
				return
			}

			// Stop if we hit EOF
			if chunk.eof {
				return
			}

			position += int64(len(chunk.data))
		}
	}
}

func loadChunk(file *os.File, position int64, fileSize int64) (*Chunk, error) {
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
	hash32 := blake3.Sum256(data)
	hash := hash32[:]

	// EOF is true when this chunk contains the last bytes of the file
	isEOF := position+int64(bytesRead) >= fileSize

	return &Chunk{
		hash:  hash, // Binary BLAKE3 hash (32 bytes)
		index: position,
		data:  data,
		eof:   isEOF,
	}, nil
}

// Ensure Chunk implements Chunk interface
var _ workload.Chunk = (*Chunk)(nil)
