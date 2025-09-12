package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

type BackupResult struct {
	StreamID string
	FileID   string
	Success  bool
	Error    error
}

func processFilesList(ctx context.Context, logger *slog.Logger, client pb.BackupServiceClient, fileList []filesystem.FileInfo, streams int) <-chan BackupResult {
	resultChan := make(chan BackupResult)
	// attributes validation
	if streams <= 0 || len(fileList) == 0 {
		close(resultChan)
		return resultChan
	}
	// max(streams) = len(files)
	if streams > len(fileList) {
		streams = len(fileList)
	}

	workChan := make(chan filesystem.FileInfo)
	ctx, cancelAllStreams := context.WithCancel(ctx)

	var wg sync.WaitGroup

	for i := 0; i < streams; i++ {
		wg.Add(1)
		go stream(ctx, logger, client, workChan, resultChan, cancelAllStreams, &wg)
	}
	go func() {
		defer func() {
			close(workChan)
			wg.Wait()
			close(resultChan)
		}()

		// 1. Send all work
		for _, f := range fileList {
			select {
			case workChan <- f:
			case <-ctx.Done():
				fmt.Println("Stream processing cancelled...")
				return
			}
		}
	}()
	return resultChan
}

func stream(ctx context.Context, logger *slog.Logger, client pb.BackupServiceClient, workChan <-chan filesystem.FileInfo, resultChan chan<- BackupResult, cancelAll context.CancelFunc, wg *sync.WaitGroup) {
	defer wg.Done()

	conf := config.GetConfigFromContext(ctx)

	logger.Debug("Creating gRPC stream")
	stream, err := client.ProcessBackupStream(ctx)
	if err != nil {
		// Check if parent context was cancelled
		if ctx.Err() != nil {
			logger.Error("Failed to create stream - parent context cancelled", "parent_error", ctx.Err(), "stream_error", err)
		} else {
			logger.Error("Failed to create stream - aborting all processing", "error", err)
		}
		resultChan <- BackupResult{
			StreamID: "",
			FileID:   "",
			Success:  false,
			Error:    fmt.Errorf("failed to create stream: %w", err),
		}
		// Cancel all streams and work distribution
		cancelAll()
		return
	}
	grpc_stream_id := fmt.Sprintf("%p", stream)
	logger = logger.With(slog.String("stream_id", grpc_stream_id))

	for f := range workChan {
		fileLogger := logger.With(slog.String("file_id", f.ID()))
		err := processOneFile(ctx, fileLogger, stream, f)
		if err != nil {
			logger.Error("Failed to process file", "error", err)
			if conf.StopStreamOnFileError {
				cancelAll()
			}
		}
		resultChan <- BackupResult{
			StreamID: grpc_stream_id,
			FileID:   f.ID(),
			Success:  err == nil,
			Error:    err,
		}
	}
	if err := stream.CloseSend(); err != nil {
		resultChan <- BackupResult{
			StreamID: grpc_stream_id,
			FileID:   "",
			Success:  false,
			Error:    fmt.Errorf("failed to close stream: %w", err),
		}
	}

}
