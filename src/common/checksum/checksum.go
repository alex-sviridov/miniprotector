package checksum

import (
	"encoding/binary"
	"hash"
)

// FeedChunk writes a chunk's CRC32 as 4-byte big-endian into a running file-level CRC32 hasher.
// Call once per chunk in index order; the final Sum32() matches FileDataRecord.Checksum.
func FeedChunk(h hash.Hash32, chunkCRC uint32) {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], chunkCRC)
	h.Write(buf[:])
}
