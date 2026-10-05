package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// processOneFile handles the complete backup lifecycle for one file
func processOneFile(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, rd *responseReader, file filesystem.FileInfo, expireAt int64, window int) error {

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
	fileResponse, err := sendFileMetadata(ctx, logger, stream, rd, file, expireAt)
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
		var err error
		fileHash, err = transferChunks(ctx, logger, stream, rd, file, window)
		if err != nil {
			return err
		}
	}

	result, err := getFileStatus(ctx, logger, rd, file.ID())
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

func getFileStatus(ctx context.Context, logger *slog.Logger, rd *responseReader, expectedFileId string) (*pb.FileProcessingResult, error) {
	result, err := rd.wait(ctx, logger, connection.FileResult(expectedFileId))
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
func sendFileMetadata(ctx context.Context, logger *slog.Logger, stream pb.BackupService_ProcessBackupStreamClient, rd *responseReader, file filesystem.FileInfo, expireAt int64) (*pb.FileNeeded, error) {
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
				ExpireAt:   expireAt,
			},
		},
	}

	if err := stream.Send(request); err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to send file info: %w", err)
	}

	result, err := rd.wait(ctx, logger, connection.FileNeeded(file.ID()))
	if err != nil {
		return nil, err
	}

	return result.(*pb.FileNeeded), nil
}
