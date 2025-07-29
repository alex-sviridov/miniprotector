package main

import (
	"context"
	"fmt"
	"os"

	"os/signal"
	"syscall"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/network"
)

func main() {
	// Configuration constants
	const (
		configPath = "/home/alasviridov/miniprotector/local.conf"
		appName    = "bwfs"
		jobId      = "BackupWriterJob"
	)

	ctx := context.WithValue(context.Background(), "appName", appName)

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

	// Initialize logger (quiet=false since writer needs to log received files)
	logger, err := common.NewLogger(config, ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "logger", logger)
	defer logger.Close()

	logger.Info("Started backup writer with parameters: Storage=%s, Port=%d",
		arguments.StoragePath, arguments.Port)

	// Create message handler
	handler := NewBackupMessageHandler(*config, ctx, arguments.StoragePath)

	// Create generic network server
	server := network.NewServer(config, ctx, arguments.Port, handler)

	// Make channel to catch Ctrl+C
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Start server in background
	go func() {
		logger.Info("Starting backup writer server...")
		if err := server.Start(); err != nil {
			logger.Error("Server error: %v", err)
		}
	}()

	// Wait for Ctrl+C
	<-stop

	// When Ctrl+C pressed, we get here
	logger.Info("Ctrl+C pressed, stopping server...")

	// Properly shutdown server (this will remove socket file)
	server.Shutdown()
}
