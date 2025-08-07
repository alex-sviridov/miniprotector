// backupreader reads backup data and sends it to writers.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/logging"

	"sync"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// main goes
func main() {

	// Configuration constants
	const (
		configPath = "../.config/local.conf"
		appName    = "brfs"
		jobId      = "BackupJob"
	)

	// Put context variables
	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, "jobId", jobId)

	// Get configuration
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, config.ContextKey, conf)

	// Get arguments
	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", arguments.Quiet)

	// Initialize logger
	logger, logfile, _ := logging.NewLogger(ctx) // Never fails
	defer func() {
		if logfile != nil {
			logfile.Close()
		}
	}()
	ctx = context.WithValue(ctx, logging.ContextKey, logger)

	logger.Info("Backup reader started",
		"sourceFolder", arguments.SourceFolder,
		"writerHost", arguments.WriterHost,
		"writerPort", arguments.WriterPort,
		"streamsCount", arguments.Streams,
	)

	// Get files list
	items, err := files.ListRecursive(arguments.SourceFolder)
	logger.Info("Directory scanned", "filesCount", len(items))
	if err != nil {
		logger.Error("Error", "error", err)
		return
	}

	// Split into streams
	streams := files.SplitByStreams(items, arguments.Streams)
	logger.Info("Splitted by streams", "streamsCount", arguments.Streams, "filesCount", len(streams[0]))

	// Connect to server
	conn, err := grpc.NewClient(fmt.Sprintf("%s:%d", arguments.WriterHost, arguments.WriterPort), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("Failed to connect: %v", err)
	}
	defer conn.Close()

	// Create protobuf client
	client := pb.NewBackupServiceClient(conn)

	logger.Info("Connected to server.")

	// Process files concurrently using multiple streams
	var wg sync.WaitGroup
	streamErrorChan := make(chan error, len(streams))

	for i, stream := range streams {
		if len(stream) > 0 {
			wg.Add(1)
			go func(ctx context.Context, client pb.BackupServiceClient, stream []files.FileInfo, streamID int32) {
				defer wg.Done()
				if err := processStream(ctx, client, stream, streamID); err != nil {
					logger.Error("Stream failed", "streamID", streamID, "error", err)
					streamErrorChan <- err
				}
			}(ctx, client, stream, int32(i+1))
		}
	}

	// Wait for all streams to complete
	wg.Wait()
	close(streamErrorChan)

	if len(streamErrorChan) == len(streams) {
		logger.Error("All streams failed")
	} else if len(streamErrorChan) > 0 {
		logger.Error("Some streams failed")
	} else {
		logger.Info("All streams completed successfully")
	}
}
