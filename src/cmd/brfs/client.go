package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

// ProcessStream is the main entry point for processing files
func processStream(ctx context.Context, client pb.BackupServiceClient, fileList []filesystem.FileInfo, streamID int32, ch chan<- BackupResult) error {

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
		// with response details
		if err == io.EOF {
			logger.Debug("Server stopped responding")
			break
		}
		if err != nil {
			return fmt.Errorf("failed to receive response: %w", err)
		}
		if err := validateStreamID(streamCtx, response); err != nil {
			return err
		}
		if err := handleResponse(streamCtx, response, ch); err != nil {
			return fmt.Errorf("failed to handle response: %w", err)
		}
	}

	return nil
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