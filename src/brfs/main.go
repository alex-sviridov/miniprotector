package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/network"
)

func main() {

	// Configuration constants
	const (
		configPath = "/home/alasviridov/miniprotector/local.conf"
		appName    = "brfs"
		jobId      = "BackupJob"
	)

	ctx := context.WithValue(context.Background(), "jobId", jobId)

	// Get configuration
	config, err := common.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "config", config)

	// Get arguments
	arguments, err := parseArguments(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	// Initialize logger
	logger, err := common.NewLogger(config, appName, ctx.Value("jobId").(string), arguments.Debug, arguments.Quiet)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "logger", logger)
	defer logger.Close()

	logger.Info("Started with parameters: Source=%s, Dest=%s:%d, Streams=%d",
		arguments.SourceFolder, arguments.WriterHost, arguments.WriterPort, arguments.Streams)

	// Get files list
	items, err := listRecursive(ctx, arguments.SourceFolder)
	if err != nil {
		logger.Error("Error: %v\n", err)
		return
	}

	// Split into streams
	streams := splitByStreams(ctx, items, arguments.Streams)
	logger.Info("Streams %d", len(streams))

	// Create network client
	client := network.NewClient(arguments.WriterHost, arguments.WriterPort, logger)

	// Process streams with persistent connections
	if err := processStreamsWithPersistentConnections(ctx, client, streams, logger); err != nil {
		logger.Error("Processing error: %v", err)
		os.Exit(1)
	}

	logger.Info("All streams completed successfully")
}
