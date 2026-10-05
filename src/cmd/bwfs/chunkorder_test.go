package main

import (
	"hash/crc32"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/checksum"
)

// inOrderCRC is the reference: the file checksum brfs computes, feeding chunk
// CRCs in index order.
func inOrderCRC(crcs []uint32) uint32 {
	h := crc32.NewIEEE()
	for _, c := range crcs {
		checksum.FeedChunk(h, c)
	}
	return h.Sum32()
}

func TestChunkOrder_InOrderCompletesAtEOF(t *testing.T) {
	o := newChunkOrder()
	require.NoError(t, o.add(0, 10, 111, false))
	assert.False(t, o.done)
	require.NoError(t, o.add(10, 10, 222, false))
	require.NoError(t, o.add(20, 5, 333, true))
	assert.True(t, o.done)
	assert.Equal(t, inOrderCRC([]uint32{111, 222, 333}), o.sum32())
}

func TestChunkOrder_OutOfOrderMatchesInOrderChecksum(t *testing.T) {
	o := newChunkOrder()
	// arrival: 2 (eof), 1, 0 -- nothing may complete until 0 arrives
	require.NoError(t, o.add(20, 5, 333, true))
	assert.False(t, o.done)
	require.NoError(t, o.add(10, 10, 222, false))
	assert.False(t, o.done)
	require.NoError(t, o.add(0, 10, 111, false))
	assert.True(t, o.done)
	assert.Equal(t, inOrderCRC([]uint32{111, 222, 333}), o.sum32())
	assert.Empty(t, o.pending)
}

func TestChunkOrder_EOFArrivingFirstDoesNotFinalizeEarly(t *testing.T) {
	o := newChunkOrder()
	require.NoError(t, o.add(10, 5, 2, true))
	assert.False(t, o.done, "eof chunk with an unaccounted gap before it must not complete the file")
}

func TestChunkOrder_RejectsDuplicateAndStaleIndex(t *testing.T) {
	o := newChunkOrder()
	require.NoError(t, o.add(0, 10, 1, false))
	assert.Error(t, o.add(0, 10, 1, false), "index already applied")
	require.NoError(t, o.add(20, 10, 3, false))
	assert.Error(t, o.add(20, 10, 3, false), "index already pending")
}

func TestChunkOrder_RejectsNonPositiveSize(t *testing.T) {
	o := newChunkOrder()
	assert.Error(t, o.add(0, 0, 1, false))
}

func TestChunkOrder_RejectsAddAfterDone(t *testing.T) {
	o := newChunkOrder()
	require.NoError(t, o.add(0, 10, 1, true))
	assert.Error(t, o.add(10, 10, 2, false))
}

func TestChunkOrder_PendingIsBounded(t *testing.T) {
	o := newChunkOrder()
	// A gap at 0 that never fills: pending must stop growing at the cap.
	var err error
	for i := int64(1); i <= int64(maxPendingChunks)+1 && err == nil; i++ {
		err = o.add(i*10, 10, uint32(i), false)
	}
	require.Error(t, err)
	assert.LessOrEqual(t, len(o.pending), maxPendingChunks)
}
