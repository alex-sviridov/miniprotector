package filesystem

import (
	"encoding/hex"
	"hash/crc32"
	"io"
	"iter"
	"os"

	"github.com/alex-sviridov/miniprotector/workload"
	"lukechampine.com/blake3"
)

const ChunkSize = 64 * 1024 // 64KB

type Chunk struct {
	hash     []byte // blake3 hash for dedup
	checksum uint32 // CRC32-IEEE for integrity — feed into file-level checksum
	index    int64  // file offset
	data     []byte // chunk data
	eof      bool   // end of file flag when chunk is the last one
}

func NewChunk(hash []byte, index int64, eof bool, data []byte) *Chunk {
	if hash == nil && data != nil {
		hash32 := blake3.Sum256(data)
		hash = hash32[:]
	}
	var checksum uint32
	if data != nil {
		checksum = crc32.ChecksumIEEE(data)
	}
	return &Chunk{
		hash:     hash,
		checksum: checksum,
		index:    index,
		data:     data,
		eof:      eof,
	}
}

func (c Chunk) Hash() []byte {
	return c.hash[:]
}

func (c Chunk) Checksum() uint32 {
	return c.checksum
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
// yielding each chunk with its BLAKE3 hash and CRC32 checksum.
// EOF is true on the last chunk. File must be locked before calling.
func (fi FileInfo) ChunkIterator() iter.Seq2[workload.Chunk, error] {
	return func(yield func(workload.Chunk, error) bool) {

		file, err := os.Open(fi.path)
		if err != nil {
			yield(nil, err)
			return
		}
		defer file.Close()

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

			if chunk.eof {
				return
			}

			position += int64(len(chunk.data))
		}
	}
}

func loadChunk(file *os.File, position int64, fileSize int64) (*Chunk, error) {
	buffer := make([]byte, ChunkSize)
	bytesRead, err := io.ReadFull(file, buffer)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, err
	}

	if bytesRead == 0 {
		return nil, io.EOF
	}

	data := buffer[:bytesRead]
	hash32 := blake3.Sum256(data)
	isEOF := position+int64(bytesRead) >= fileSize

	return &Chunk{
		hash:     hash32[:],
		checksum: crc32.ChecksumIEEE(data),
		index:    position,
		data:     data,
		eof:      isEOF,
	}, nil
}

// Ensure Chunk implements workload.Chunk interface
var _ workload.Chunk = (*Chunk)(nil)
