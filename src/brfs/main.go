// backupreader reads backup data and sends it to writers.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/files"
	"github.com/alex-sviridov/miniprotector/common/network"
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
	logger, logfile, _ := common.NewLogger(config, ctx) // Never fails
	defer func() {
		if logfile != nil {
			logfile.Close()
		}
	}()
	ctx = context.WithValue(ctx, "logger", logger)

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

	// Create network client
	client := network.NewClient(config, ctx, arguments.WriterHost, arguments.WriterPort)

	// Process streams with persistent connections
	if err := processStreams(config, ctx, client, streams); err != nil {
		logger.Error("Processing error", "error", err)
		os.Exit(1)
	}

	logger.Info("All streams completed successfully")
}
