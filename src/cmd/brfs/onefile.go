package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/workload"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// processOneFile handles the complete backup lifecycle for one file
func processOneFile(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, file filesystem.FileInfo) error {

	conf := config.GetConfigFromContext(ctx)
	logger.Debug("Started file processing")

	// Lock file
	lockTimeout := time.Duration(conf.FileLockTimeoutSec) * time.Second
	fileLock, err := file.Lock(lockTimeout)
	if err != nil {
		return fmt.Errorf("failed to lock file: %w", err)
	}
	defer fileLock.Unlock()

	// Send file info and get server response
	fileResponse, err := sendFileMetadata(ctx, logger, stream, file)
	if err != nil {
		return fmt.Errorf("failed to get file needed response: %w", err)
	}
	logger.Debug("Got fileResponse", "is_needed", fileResponse.Needed)

	if file.Size() == 0 || file.GetType() != 'f' {
		logger.Debug("Will not send file", "file_size", file.Size(), "file_type", file.GetType())
		fileResponse.Needed = false
	}

	var fileHash []byte
	if fileResponse.Needed {
		fileChecksumHasher := crc32.NewIEEE()
		var buf [4]byte
		for chunk, err := range file.ChunkIterator() {
			if err != nil {
				return fmt.Errorf("failed to read chunk: %w", err)
			}
			binary.BigEndian.PutUint32(buf[:], chunk.Checksum())
			fileChecksumHasher.Write(buf[:])
			chunkResponse, err := sendChunkMetadata(ctx, logger, stream, chunk)
			if err != nil {
				return fmt.Errorf("failed to get chunk needed response: %w", err)
			}
			logger.Debug("Chunk", "chunk_hash", hex.EncodeToString(chunk.Hash()), "needed", chunkResponse.Needed)
			if chunkResponse.Needed {
				// TODO: Transmission retry
				if err := sendChunkData(ctx, logger, stream, chunk); err != nil {
					return fmt.Errorf("failed to send chunk data: %w", err)
				}
			}
		}
		binary.BigEndian.PutUint32(buf[:], fileChecksumHasher.Sum32())
		fileHash = make([]byte, 4)
		copy(fileHash, buf[:])
	}

	result, err := getFileStatus(ctx, logger, stream, file.ID())
	if err != nil {
		return fmt.Errorf("failed to get file status: %w", err)
	}
	if !bytes.Equal(result.Hash, fileHash) && len(fileHash) > 0 {
		logger.Error("File transmitted",
			"expected_hash", hex.EncodeToString(fileHash),
			"received_hash", hex.EncodeToString(result.Hash))
		return fmt.Errorf("Hash mismatch")
	}
	logger.Info("File transmitted", "file_hash", hex.EncodeToString(fileHash))

	return nil
}

func sendChunkData(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, chunk workload.Chunk) error {
	conf := config.GetConfigFromContext(ctx)
	timeout := time.Duration(conf.ConnectionTimeOutSec) * time.Second

	logger = logger.With(slog.String("chunk_hash", hex.EncodeToString(chunk.Hash())))

	logger.Debug("Sending chunk data")

	request := &pb.FileRequest{
		RequestType: &pb.FileRequest_ChunkData{
			ChunkData: &pb.ChunkData{
				Hash:  chunk.Hash(),
				Index: int64(chunk.Index()),
				Data:  chunk.Data(),
				Eof:   chunk.IsEOF(),
			},
		},
	}

	if err := stream.Send(request); err != nil {
		return fmt.Errorf("failed to send chunk data: %w", err)
	}

	result, err := connection.WaitForResponse(ctx, logger, stream, connection.ChunkResult(chunk.Hash()), timeout)
	if err != nil {
		return err
	}

	chunkResult := result.(*pb.ChunkResult)
	if !chunkResult.Success {
		return fmt.Errorf("chunk transmission failed")
	}

	return nil
}

func getFileStatus(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, expectedFileId string) (*pb.FileProcessingResult, error) {
	conf := config.GetConfigFromContext(ctx)
	timeout := time.Duration(conf.ConnectionTimeOutSec) * time.Second

	result, err := connection.WaitForResponse(ctx, logger, stream, connection.FileResult(expectedFileId), timeout)
	if err != nil {
		return nil, err
	}

	fileResult := result.(*pb.FileProcessingResult)
	if !fileResult.Success {
		return nil, fmt.Errorf("file transmission failed")
	}

	return fileResult, nil
}

// sendFileMetadata sends metadata for one file
func sendFileMetadata(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, file filesystem.FileInfo) (*pb.FileNeeded, error) {
	conf := config.GetConfigFromContext(ctx)
	timeout := time.Duration(conf.ConnectionTimeOutSec) * time.Second

	encoded, err := file.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode file info: %w", err)
	}
	logger.Debug("Sending file metadata", "file_info", fmt.Sprintf("%s", file))
	request := &pb.FileRequest{
		RequestType: &pb.FileRequest_FileInfo{
			FileInfo: &pb.FileInfo{
				FileId:     file.ID(),
				Attributes: encoded,
			},
		},
	}

	if err := stream.Send(request); err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to send file info: %w", err)
	}

	result, err := connection.WaitForResponse(ctx, logger, stream, connection.FileNeeded(file.ID()), timeout)
	if err != nil {
		return nil, err
	}

	return result.(*pb.FileNeeded), nil
}

// sendChunkMetadata sends metadata for one chunk
func sendChunkMetadata(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, chunk workload.Chunk) (*pb.ChunkNeeded, error) {
	conf := config.GetConfigFromContext(ctx)
	timeout := time.Duration(conf.ConnectionTimeOutSec) * time.Second

	logger = logger.With(slog.String("chunk_hash", hex.EncodeToString(chunk.Hash())))

	logger.Debug("Sending chunk metadata")

	request := &pb.FileRequest{
		RequestType: &pb.FileRequest_ChunkHash{
			ChunkHash: &pb.ChunkHash{
				Hash:     chunk.Hash(),
				Index:    int64(chunk.Index()),
				Size:     int64(len(chunk.Data())),
				Eof:      chunk.IsEOF(),
				Checksum: chunk.Checksum(),
			},
		},
	}

	if err := stream.Send(request); err != nil {
		return nil, fmt.Errorf("failed to send chunk info: %w", err)
	}

	result, err := connection.WaitForResponse(ctx, logger, stream, connection.ChunkNeeded(chunk.Hash()), timeout)
	if err != nil {
		return nil, err
	}

	return result.(*pb.ChunkNeeded), nil
}
