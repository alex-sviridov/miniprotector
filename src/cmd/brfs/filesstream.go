package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"
)

type BackupResult struct {
	StreamID int32
	Filename string
	Success  bool
	Error    error
}

func processFilesList(ctx context.Context, client pb.BackupServiceClient, fileList []filesystem.FileInfo, streams int) <-chan BackupResult {
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

	var wg sync.WaitGroup

	for i := 0; i < streams; i++ {
		wg.Add(1)
		go stream(ctx, client, int32(i), workChan, resultChan, &wg)
	}
	go func() {
		// 1. Send all work
		for _, f := range fileList {
			select {
			case workChan <- f:
			case <-ctx.Done():
				fmt.Printf("Emergency shutdown...\n")
				close(workChan)
				wg.Wait()
				close(resultChan)
				return
			}
		}
		// 2. Signal no more work
		close(workChan)
		// 3. Wait for workers to finish
		wg.Wait()
		// 4. Signal no more results
		close(resultChan)
	}()
	return resultChan
}

func stream(ctx context.Context, client pb.BackupServiceClient, streamID int32, workChan <-chan filesystem.FileInfo, resultChan chan<- BackupResult, wg *sync.WaitGroup) {
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
		resultChan <- BackupResult{
			StreamID: streamID,
			Filename: "",
			Success:  false,
			Error:    fmt.Errorf("failed to create stream: %w", err),
		}
	}

	for f := range workChan {
		err := processOneFile(streamCtx, stream, f)
		if err != nil {
			logger.Error("Failed to process file", "file", f.Path, "error", err)
		}
		resultChan <- BackupResult{
			StreamID: streamID,
			Filename: f.Path,
			Success:  err == nil,
			Error:    err,
		}
	}
	if err := stream.CloseSend(); err != nil {
		resultChan <- BackupResult{
			StreamID: streamID,
			Filename: "",
			Success:  false,
			Error:    fmt.Errorf("failed to close stream: %w", err),
		}
	}

}
