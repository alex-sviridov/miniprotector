package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/network"
)

func main() {

	// Configuration constants
	const (
		configPath = "/home/alasviridov/miniprotector/local.conf"
		appName    = "brfs"
		jobId      = "BackupJob"
	)

	// Put context variables
	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, "jobId", jobId)

	// Get configuration
	config, err := common.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	// Get arguments
	arguments, err := parseArguments(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", arguments.Quiet)

	// Initialize logger
	logger, err := common.NewLogger(config, ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "logger", logger)
	defer logger.Close()

	logger.Info("Started with parameters: Source=%s, Dest=%s:%d, Streams=%d",
		arguments.SourceFolder, arguments.WriterHost, arguments.WriterPort, arguments.Streams)

	// Get files list
	items, err := files.ListRecursive(arguments.SourceFolder)
	logger.Info("Directory scanned. Found %d items", len(items))
	if err != nil {
		logger.Error("Error: %v\n", err)
		return
	}

	// Split into streams
	streams := files.SplitByStreams(items, arguments.Streams)
	logger.Info("Splitted into %d streams by %d files", arguments.Streams, len(streams[0]))

	// Create network client
	client := network.NewClient(arguments.WriterHost, arguments.WriterPort, logger)

	// Process streams with persistent connections
	if err := processStreams(config, ctx, client, streams); err != nil {
		logger.Error("Processing error: %v", err)
		os.Exit(1)
	}

	logger.Info("All streams completed successfully")
}
