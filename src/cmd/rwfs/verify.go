package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"log/slog"
	"sync"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/checksum"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"lukechampine.com/blake3"
)

type verifyResult struct {
	fileUUID   string
	source     string
	path       string
	ok         bool
	reason     string
	chunkIndex int64
	size       int64
	chunkCount int32
}

func runVerify(logger *slog.Logger, host string, port int, serverName, pathFilter, filter string, streams, retries int, quiet bool) error {
	conn, err := connection.Connect(host, port, 5)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()

	listClient := pb.NewListServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	resp, err := listClient.ListFiles(ctx, &pb.ListRequest{
		ServerName: serverName,
		Path:       pathFilter,
		Filter:     filter,
	})
	cancel()
	if err != nil {
		return fmt.Errorf("list files: %w", err)
	}

	var rows []*pb.FileRow
	for _, r := range resp.Rows {
		if r.Type == "f" && r.Size > 0 {
			rows = append(rows, r)
		}
	}

	if len(rows) == 0 {
		logger.Info("summary", "verified", 0, "warnings", 0)
		return nil
	}

	restoreClient := pb.NewRestoreServiceClient(conn)
	workCh := make(chan *pb.FileRow, len(rows))
	for _, r := range rows {
		workCh <- r
	}
	close(workCh)

	resultCh := make(chan verifyResult, len(rows))

	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for row := range workCh {
				resultCh <- verifyFileWithRetry(context.Background(), logger, restoreClient, row, retries)
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	total := 0
	warnings := 0
	for result := range resultCh {
		total++
		if result.ok {
			if !quiet {
				logger.Info("verified",
					"source", result.source,
					"path", result.path,
					"file_uuid", result.fileUUID,
					"chunks", result.chunkCount,
					"size", result.size,
				)
			}
		} else {
			warnings++
			attrs := []any{
				"source", result.source,
				"path", result.path,
				"file_uuid", result.fileUUID,
				"reason", result.reason,
			}
			if result.reason == "blake3_mismatch" {
				attrs = append(attrs, "chunk_index", result.chunkIndex)
			}
			logger.Warn("verification failed", attrs...)
		}
	}

	logger.Info("summary", "verified", total, "warnings", warnings)
	if warnings > 0 {
		return fmt.Errorf("%d file(s) failed verification", warnings)
	}
	return nil
}

func verifyFileWithRetry(ctx context.Context, logger *slog.Logger, client pb.RestoreServiceClient, row *pb.FileRow, maxRetries int) verifyResult {
	var result verifyResult
	for attempt := 1; attempt <= maxRetries; attempt++ {
		result = verifyFile(ctx, client, row)
		if result.ok || result.reason == "blake3_mismatch" || result.reason == "crc_mismatch" {
			return result
		}
		if attempt < maxRetries {
			logger.Warn("stream error, retrying",
				"path", row.Path,
				"file_uuid", row.FileUuid,
				"attempt", attempt,
				"reason", result.reason,
			)
		}
	}
	return result
}

func verifyFile(parent context.Context, client pb.RestoreServiceClient, row *pb.FileRow) verifyResult {
	base := verifyResult{
		fileUUID: row.FileUuid,
		source:   row.Source,
		path:     row.Path,
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	stream, err := client.RestoreFile(ctx, &pb.RestoreRequest{FileUuid: row.FileUuid})
	if err != nil {
		base.reason = fmt.Sprintf("stream error: %v", err)
		return base
	}

	firstEvent, err := stream.Recv()
	if err != nil {
		base.reason = fmt.Sprintf("stream error: %v", err)
		return base
	}
	meta := firstEvent.GetMeta()
	if meta == nil {
		base.reason = "stream error: expected RestoreFileMeta as first event"
		return base
	}
	base.size = meta.Size
	base.chunkCount = meta.ChunkCount

	hasher := crc32.NewIEEE()

	for {
		event, err := stream.Recv()
		if err != nil {
			base.reason = fmt.Sprintf("stream error: %v", err)
			return base
		}
		chunk := event.GetChunk()
		if chunk == nil {
			base.reason = "stream error: expected RestoreChunk"
			return base
		}

		computed := blake3.Sum256(chunk.Data)
		if !bytes.Equal(computed[:], chunk.Hash) {
			base.reason = "blake3_mismatch"
			base.chunkIndex = chunk.Index
			return base
		}

		checksum.FeedChunk(hasher, crc32.ChecksumIEEE(chunk.Data))

		if chunk.Eof {
			break
		}
	}

	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], hasher.Sum32())
	if !bytes.Equal(buf[:], meta.ExpectedChecksum) {
		base.reason = "crc_mismatch"
		return base
	}

	base.ok = true
	return base
}
