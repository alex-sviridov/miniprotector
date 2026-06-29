package main

import (
	"context"
	"fmt"
	"os"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"google.golang.org/grpc"
)

func main() {
	const (
		configPath = "../.config/local.conf"
		appName    = "bwfs"
	)

	ctx := context.WithValue(context.Background(), "appName", appName)

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
		logger.Info("Backup writer started",
			"StoragePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)
		backupServer, err := NewBackupServer(ctx, logger, arguments.StoragePath)
		if err != nil {
			logger.Error("Server initialization failed", "error", err)
			os.Exit(1)
		}
		defer backupServer.store.Close()

		if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {
			pb.RegisterBackupServiceServer(s, backupServer)
		}); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}

	case "list":
		if err := runList(logger, arguments.StoragePath, arguments.Output, arguments.Filter); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
	}
}
