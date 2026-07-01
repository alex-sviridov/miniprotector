// backupreader reads backup data and sends it to writers.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/workload/filesystem"

	"os/signal"
	"syscall"
)

// main goes
func main() {

	// Configuration constants
	const (
		appName = "brfs"
		jobId   = "BackupJob"
	)

	// Put context variables
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = context.WithValue(ctx, "appName", appName)
	ctx = context.WithValue(ctx, "jobId", jobId)

	// Get configuration
	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, config.ContextKey, conf)

	// Get arguments
	arguments, err := parseArguments(conf)
	if err != nil {
		if errors.Is(err, errHelpRequested) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", arguments.Quiet)
	ctx = context.WithValue(ctx, common.HostnameContextKey, common.GetHostname())

	// Initialize logger
	logger, logfile := logging.NewLogger(ctx) 
	defer logfile.Close()

	logger.Info("Backup reader started",
		"sourceFolder", arguments.SourceFolder,
		"writerHost", arguments.WriterHost,
		"writerPort", arguments.WriterPort,
		"streamsCount", arguments.Streams,
	)

	// Get files list
	filesList, err := filesystem.Discover(arguments.SourceFolder)
	if err != nil {
		logger.Error("Error traversing the directory", "error", err)
		return
	}
	logger.Info("Directory scanned", "filesCount", len(filesList))
	filesBackupState := make(map[string]bool)
	for _, file := range filesList {
		filesBackupState[file.ID()] = false
	}

	// Create gRPC connection
	conn, err := connection.Connect(arguments.WriterHost, arguments.WriterPort, 5)
	if err != nil {
		logger.Error("Error connecting to server", "error", err)
		return
	}
	client := pb.NewBackupServiceClient(conn)
	logger.Info("Connected to server")

	// Process files using shared gRPC connection
	resultsCh := processFilesList(ctx, logger, client, filesList, arguments.Streams)
	for result := range resultsCh {
		// Process each result as it arrives
		filesBackupState[result.FileID] = result.Success
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
	if failedCount == 0 {
		state = "success"
	}
	logger.Info("Backup finished",
		"state", state,
		"count.success", successCount,
		"count.failed", failedCount,
	)
}
