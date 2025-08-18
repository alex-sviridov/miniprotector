package main

import (
	"context"
	"fmt"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

// processOneFile handles the complete lifecycle of one file
func processOneFile(ctx context.Context, stream pb.BackupService_ProcessBackupStreamClient, file filesystem.FileInfo) error {
	logger := logging.GetLoggerFromContext(ctx).With(slog.String("file", file.Path))

	// Send file info
	if err := sendSingleFileInfo(ctx, stream, file); err != nil {
		return fmt.Errorf("failed to send file info: %w", err)
	}

	// Wait for server response
	response, err := waitForFileNeededResponse(ctx, stream, file.GetId())
	if err != nil {
		return fmt.Errorf("failed to get file needed response: %w", err)
	}

	// Process the response
	logger.Debug("File needed response", "needed", response.Needed)

	return nil
}

// sendSingleFileInfo sends metadata for one file
func sendSingleFileInfo(ctx context.Context, stream pb.BackupService_ProcessBackupStreamClient, file filesystem.FileInfo) error {
	streamId := ctx.Value("streamId").(int32)
	encoded, err := file.Encode()
	if err != nil {
		return fmt.Errorf("failed to encode file info: %w", err)
	}
	request := &pb.FileRequest{
		StreamId: streamId,
		RequestType: &pb.FileRequest_FileInfo{
			FileInfo: &pb.FileInfo{
				FileId:     file.GetId(),
				Attributes: encoded,
			},
		},
	}

	if err := stream.Send(request); err != nil {
		return fmt.Errorf("failed to send file info: %w", err)
	}

	return nil
}

// waitForFileNeededResponse waits for server's FileNeeded response for specific file
func waitForFileNeededResponse(ctx context.Context, stream pb.BackupService_ProcessBackupStreamClient, expectedFileID string) (*pb.FileNeeded, error) {
	for {
		response, err := stream.Recv()
		if err != nil {
			return nil, fmt.Errorf("failed to receive response: %w", err)
		}

		// Validate stream ID
		if err := validateStreamID(ctx, response); err != nil {
			return nil, err
		}

		// Check if it's the FileNeeded response we're waiting for
		if fileNeeded := response.GetFileNeeded(); fileNeeded != nil {
			if fileNeeded.FileId == expectedFileID {
				return fileNeeded, nil
			}
			// Log unexpected file ID but continue waiting
			logger := logging.GetLoggerFromContext(ctx)
			logger.Warn("Received FileNeeded for unexpected file",
				"expected", expectedFileID, "received", fileNeeded.FileId)
		}

		// Continue receiving if it's not what we're waiting for
	}
}

func validateStreamID(ctx context.Context, response *pb.FileResponse) error {
	expectedID, ok := ctx.Value("streamId").(int32)
	if !ok {
		return fmt.Errorf("streamId not found in context")
	}
	if response.StreamId != expectedID {
		return fmt.Errorf("stream ID mismatch: expected %d, received %d", expectedID, response.StreamId)
	}
	return nil
}
