package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"google.golang.org/grpc"
)

func main() {
	const appName = "rrfs"

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx = context.WithValue(ctx, "appName", appName)

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

	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", arguments.Quiet)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	switch arguments.Action {
	case "server":
		logger.Info("Restore reader started",
			"storagePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)
		srv, err := NewRestoreServer(ctx, logger, arguments.StoragePath)
		if err != nil {
			logger.Error("Server initialization failed", "error", err)
			os.Exit(1)
		}
		defer srv.store.Close()

		if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {
			// Restore service registration goes here once the proto is defined.
			_ = s
		}); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
	}
}
