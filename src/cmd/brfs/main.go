// backupreader reads backup data and sends it to writers.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/logging"

	"sync"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type BackupResult struct {
	Filename string
	Success  bool
}

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
	ctx = context.WithValue(ctx, common.HostnameContextKey, common.GetHostname())

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
	filesList, err := files.ListRecursive(arguments.SourceFolder)
	if err != nil {
		logger.Error("Error traversing the directory", "error", err)
		return
	}
	logger.Info("Directory scanned", "filesCount", len(filesList))
	filesBackupState := make(map[string]bool)
	for _, file := range filesList {
		filesBackupState[file.Path] = false
	}

	// Split into streams
	streams, err := files.SplitByStreams(filesList, arguments.Streams)
	if err != nil {
		logger.Error("Error splitting by streams", "error", err)
		return
	}
	logger.Info("Splitted by streams", "streamsCount", arguments.Streams, "filesCount", len(streams[0]))

	// Connect to server
	conn, err := grpc.NewClient(fmt.Sprintf("%s:%d", arguments.WriterHost, arguments.WriterPort), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("Failed to connect", "error", err)
		return
	}
	defer conn.Close()

	// Create protobuf client
	client := pb.NewBackupServiceClient(conn)

	logger.Info("Connected to server")

	// Process files concurrently using multiple streams
	resultsCh := make(chan BackupResult)

	var wg sync.WaitGroup
	for i, stream := range streams {
		wg.Add(1)
		go func(streamIndex int, streamData []files.FileInfo) {
			defer wg.Done()
			processStream(ctx, client, streamData, int32(streamIndex+1), resultsCh)
		}(i, stream)
	}

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	for result := range resultsCh {
		// Process each result as it arrives
		filesBackupState[result.Filename] = true
	}

	// Final analysis
	successCount := 0
	failedCount := 0

	for _, success := range filesBackupState {
		if success {
			successCount++
		} else {
			failedCount++
		}
	}

	state := "failed"
	if failedCount == 0 { state = "success" }
	logger.Info("Backup finished", 
		"state", state,
		"count.success", successCount, 
		"count.failed", failedCount,
	)
}
