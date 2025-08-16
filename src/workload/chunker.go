package workload

type Chunk struct {
	Hash     []byte // blake3 hash for dedup
	Checksum []byte // crc32 checksum for consistency check
	Position int64  // file offset
	Data     []byte // chunk data
	EOF      bool   // end of file flag when chunk is the last one
}
