package main

import (
	"fmt"
	"hash"
	"hash/crc32"

	"github.com/alex-sviridov/miniprotector/common/checksum"
)

// maxPendingChunks caps how many chunks may sit in a chunkOrder waiting for an
// earlier gap to fill. A well-behaved brfs never has more than its --window
// chunks in flight, so this only trips on a buggy or hostile client that would
// otherwise make bwfs buffer without bound. Memory is O(window), never
// O(file size).
const maxPendingChunks = 1024

type orderedChunk struct {
	size int64
	crc  uint32
	eof  bool
}

// chunkOrder folds one file's chunk CRCs into the whole-file CRC32 in index
// (byte-offset) order, however the chunks are accounted for. With a sliding
// window, brfs has several chunks in flight, so a chunk already stored on the
// server (accounted for at hash time) can be accounted for before an earlier
// chunk whose data is still in transit; chunks ahead of such a gap wait in
// pending until the gap fills.
type chunkOrder struct {
	hasher  hash.Hash32
	next    int64 // byte offset of the next chunk to fold in
	pending map[int64]orderedChunk
	done    bool // the EOF chunk has been folded in: every chunk is accounted for
}

func newChunkOrder() *chunkOrder {
	return &chunkOrder{hasher: crc32.NewIEEE(), pending: make(map[int64]orderedChunk)}
}

// add accounts for one chunk (index is its byte offset in the file).
func (o *chunkOrder) add(index, size int64, crc uint32, eof bool) error {
	if o.done {
		return fmt.Errorf("chunk at %d after end of file", index)
	}
	if size <= 0 {
		return fmt.Errorf("chunk at %d has non-positive size %d", index, size)
	}
	if _, dup := o.pending[index]; dup || index < o.next {
		return fmt.Errorf("chunk at %d already accounted for", index)
	}
	if index != o.next {
		if len(o.pending) >= maxPendingChunks {
			return fmt.Errorf("more than %d chunks ahead of a gap at offset %d", maxPendingChunks, o.next)
		}
		o.pending[index] = orderedChunk{size: size, crc: crc, eof: eof}
		return nil
	}
	o.apply(orderedChunk{size: size, crc: crc, eof: eof})
	for !o.done {
		c, ok := o.pending[o.next]
		if !ok {
			break
		}
		delete(o.pending, o.next)
		o.apply(c)
	}
	return nil
}

func (o *chunkOrder) apply(c orderedChunk) {
	checksum.FeedChunk(o.hasher, c.crc)
	o.next += c.size
	o.done = c.eof
}

func (o *chunkOrder) sum32() uint32 { return o.hasher.Sum32() }
