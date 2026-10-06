package filesystem

import (
	"encoding/hex"
	"hash/crc32"
	"io"
	"iter"

	chunkers "github.com/PlakarKorp/go-cdc-chunkers"
	_ "github.com/PlakarKorp/go-cdc-chunkers/chunkers/fastcdc"
	"github.com/alex-sviridov/miniprotector/workload"
	"lukechampine.com/blake3"
)

// FastCDC content-defined chunking: boundaries follow the data, so inserting
// or deleting bytes in a file only changes the chunks around the edit.
// NormalChunkSize must be a power of two (library requirement).
const (
	MinChunkSize    = 16 * 1024
	NormalChunkSize = 64 * 1024
	MaxChunkSize    = 256 * 1024

	// "fastcdc" alone is the library's legacy variant; the versioned name is the current one.
	cdcAlgorithm = "fastcdc-v1.0.0"
)

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

// ChunkIterator returns an iterator that reads the file in content-defined chunks
// of 16-256 KB, average 64 KB, yielding each chunk with its BLAKE3 hash and CRC32
// checksum. EOF is true on the last chunk. File must be locked before calling.
func (fi FileInfo) ChunkIterator() iter.Seq2[workload.Chunk, error] {
	return func(yield func(workload.Chunk, error) bool) {
		file, err := openForRead(fi.path)
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

		chunker, err := chunkers.NewChunker(cdcAlgorithm, file, &chunkers.ChunkerOpts{
			MinSize:    MinChunkSize,
			NormalSize: NormalChunkSize,
			MaxSize:    MaxChunkSize,
		})
		if err != nil {
			yield(nil, err)
			return
		}

		position := int64(0)
		for {
			data, err := chunker.Next()
			if err != nil && err != io.EOF {
				yield(nil, err)
				return
			}
			if len(data) > 0 {
				// data aliases the chunker's scan buffer and is overwritten by the
				// next call; chunks outlive that (they sit in the send window).
				owned := make([]byte, len(data))
				copy(owned, data)
				eof := err == io.EOF || position+int64(len(owned)) >= fileSize
				chunk := NewChunk(nil, position, eof, owned)
				if !yield(*chunk, nil) {
					return
				}
				position += int64(len(owned))
				if eof {
					return
				}
			}
			if err == io.EOF {
				return
			}
		}
	}
}

// Ensure Chunk implements workload.Chunk interface
var _ workload.Chunk = (*Chunk)(nil)
