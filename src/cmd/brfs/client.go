package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

// ProcessStream is the main entry point for processing files
func processStream(ctx context.Context, client pb.BackupServiceClient, fileList []files.FileInfo, streamID int32) error {

	logger := logging.GetLoggerFromContext(ctx).
		With(slog.Int("streamId", int(streamID)))

	conf := config.GetConfigFromContext(ctx)

	// Create stream with configured timeout
	timeout := time.Duration(conf.ConnectionTimeOutSec) * time.Second
	streamCtx, cancel := context.WithTimeout(ctx, timeout)
	streamCtx = context.WithValue(streamCtx, logging.ContextKey, logger)
	streamCtx = context.WithValue(streamCtx, "streamId", streamID)
	defer cancel()

	stream, err := client.ProcessBackupStream(streamCtx)
	if err != nil {
		return fmt.Errorf("failed to create stream: %w", err)
	}

	if err := sendFilesMetadata(streamCtx, stream, fileList); err != nil {
		return fmt.Errorf("file processing failed: %w", err)
	}

	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("failed to close send: %w", err)
	}

	for {
		response, err := stream.Recv()
		// with responce details
		if err == io.EOF {
			logger.Debug("Server stopped responding")
			break
		}
		if err != nil {
			return fmt.Errorf("failed to receive response: %w", err)
		}
		if response.StreamId != streamID {
			return fmt.Errorf("stream ID mismatch: expected %d, received %d", streamID, response.StreamId)
		}
		// Handle response
		if err := handleResponse(streamCtx, stream, response); err != nil {
			return fmt.Errorf("failed to handle response: %w", err)
		}
	}

	return nil
}
