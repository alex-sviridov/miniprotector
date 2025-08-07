package main

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

// ProcessStream is the main entry point for processing files
func processStream(ctx context.Context, client pb.BackupServiceClient, fileList []files.FileInfo, streamID int32, wg *sync.WaitGroup) {
	defer wg.Done()

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
		logger.Error("Failed to create stream", "error", err)
		return
	}

	if err := sendFilesMetadata(streamCtx, stream, fileList); err != nil {
		logger.Error("File processing failed", "error", err)
	}

	if err := stream.CloseSend(); err != nil {
		logger.Error("Failed to close send", "error", err)
	}

	for {
		response, err := stream.Recv()
		// with responce details
		if err == io.EOF {
			logger.Debug("Server stopped responding")
			break
		}
		if err != nil {
			logger.Error("Failed to receive response", "error", err)
			break
		}
		if response.StreamId != streamID {
			logger.Error("Stream ID mismatch",
				"expected", streamID,
				"received", response.StreamId)
		}
		// Handle response
		if err := handleResponse(streamCtx, stream, response); err != nil {
			break
		}
	}

	logger.Info("Stream completed")
}
