package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/workload"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// defaultWindow is the in-flight chunk limit when none is configured: one
// chunk at a time, i.e. stop-and-wait. The shipped default (config
// default_window) is larger; see docs/components/brfs.md.
const defaultWindow = 1

// pendingReply is one reply bwfs still owes us. bwfs answers a stream's
// requests strictly in the order it received them, so replies are matched to
// requests by position, not by looking them up.
type pendingReply struct {
	chunk workload.Chunk
	data  bool // false: awaiting ChunkNeeded for the hash; true: ChunkResult for the data
}

// transferChunks sends one file's chunks with a sliding window: up to window
// chunks are in flight (hash sent, not yet fully acknowledged) at once, so a
// round trip is shared by the whole window instead of paid per chunk. A chunk
// leaves the window when bwfs says it already has it, or confirms its data.
// Returns the file's CRC32 over its chunk checksums, in chunk order.
//
// bwfs accounts for chunks in arrival order, which with window > 1 is not
// index order (a stored chunk is accounted for at hash time, a new one only
// when its data lands); it reassembles the order itself (see chunkOrder).
func transferChunks(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, rd *responseReader, file filesystem.FileInfo, window int) ([]byte, error) {
	if window < 1 {
		window = defaultWindow
	}
	var (
		fileCRC  = crc32.NewIEEE()
		expect   []pendingReply
		inflight int
	)

	handle := func(resp *pb.FileResponse) error {
		if len(expect) == 0 {
			return fmt.Errorf("unexpected response with no request outstanding")
		}
		p := expect[0]
		expect = expect[1:]
		hashHex := hex.EncodeToString(p.chunk.Hash())

		if p.data {
			res := resp.GetChunkResult()
			if res == nil || hex.EncodeToString(res.Hash) != hashHex {
				return fmt.Errorf("expected ChunkResult for chunk %s", hashHex)
			}
			if !res.Success {
				return fmt.Errorf("chunk transmission failed")
			}
			inflight--
			return nil
		}

		needed := resp.GetChunkNeeded()
		if needed == nil || hex.EncodeToString(needed.Hash) != hashHex {
			return fmt.Errorf("expected ChunkNeeded for chunk %s", hashHex)
		}
		logger.Debug("Chunk", "chunk_hash", hashHex, "needed", needed.Needed)
		if !needed.Needed {
			inflight--
			return nil
		}
		// TODO: Transmission retry
		if err := sendChunkData(logger, stream, p.chunk); err != nil {
			return fmt.Errorf("failed to send chunk data: %w", err)
		}
		expect = append(expect, pendingReply{chunk: p.chunk, data: true})
		return nil
	}

	for chunk, err := range file.ChunkIterator() {
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk: %w", err)
		}
		var crcBytes [4]byte
		binary.BigEndian.PutUint32(crcBytes[:], chunk.Checksum())
		fileCRC.Write(crcBytes[:])

		for inflight >= window {
			resp, err := rd.next(ctx)
			if err != nil {
				return nil, fmt.Errorf("failed to get chunk response: %w", err)
			}
			if err := handle(resp); err != nil {
				return nil, err
			}
		}
		if err := sendChunkHash(logger, stream, chunk); err != nil {
			return nil, fmt.Errorf("failed to send chunk hash: %w", err)
		}
		expect = append(expect, pendingReply{chunk: chunk})
		inflight++

		// Act on replies that have already arrived so requested data goes out
		// without waiting for the window to fill.
		for {
			resp, ok, err := rd.poll()
			if err != nil {
				return nil, fmt.Errorf("failed to get chunk response: %w", err)
			}
			if !ok {
				break
			}
			if err := handle(resp); err != nil {
				return nil, err
			}
		}
	}
	for inflight > 0 {
		resp, err := rd.next(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get chunk response: %w", err)
		}
		if err := handle(resp); err != nil {
			return nil, err
		}
	}

	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], fileCRC.Sum32())
	return sum[:], nil
}

func sendChunkHash(logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, chunk workload.Chunk) error {
	logger.Debug("Sending chunk metadata", "chunk_hash", hex.EncodeToString(chunk.Hash()))
	return stream.Send(&pb.FileRequest{
		RequestType: &pb.FileRequest_ChunkHash{
			ChunkHash: &pb.ChunkHash{
				Hash:     chunk.Hash(),
				Index:    int64(chunk.Index()),
				Size:     int64(len(chunk.Data())),
				Eof:      chunk.IsEOF(),
				Checksum: chunk.Checksum(),
			},
		},
	})
}

func sendChunkData(logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, chunk workload.Chunk) error {
	logger.Debug("Sending chunk data", "chunk_hash", hex.EncodeToString(chunk.Hash()))
	return stream.Send(&pb.FileRequest{
		RequestType: &pb.FileRequest_ChunkData{
			ChunkData: &pb.ChunkData{
				Hash:  chunk.Hash(),
				Index: int64(chunk.Index()),
				Data:  chunk.Data(),
				Eof:   chunk.IsEOF(),
			},
		},
	})
}
