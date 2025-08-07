package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
	// Configuration constants
	const (
		configPath = "../.config/local.conf"
		appName    = "bwfs"
	)

	ctx := context.WithValue(context.Background(), "appName", appName)

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

	logger.Info("Backup writer started",
		"StoragePath", arguments.StoragePath,
		"serverPort", arguments.Port,
	)

	// Start server
	if err := startServer(ctx, arguments.Port, arguments.StoragePath); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
