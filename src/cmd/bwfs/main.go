package main

import (
	"context"
	"fmt"
	"os"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"google.golang.org/grpc"
)

func main() {
	const appName = "bwfs"

	ctx := context.WithValue(context.Background(), "appName", appName)

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

		vacuumResult, err := backupServer.store.Vacuum()
		if err != nil {
			logger.Error("Startup vacuum failed", "error", err)
			os.Exit(1)
		}
		logger.Info("Startup vacuum completed",
			"orphaned_file_data_removed", vacuumResult.OrphanedFileDataRemoved,
			"orphaned_chunk_links_removed", vacuumResult.OrphanedChunkLinksRemoved,
			"orphaned_chunks_removed", vacuumResult.OrphanedChunksRemoved,
			"incomplete_file_data_removed", vacuumResult.IncompleteFileData,
			"bytes_reclaimed", vacuumResult.BytesReclaimed,
		)

		listStore, err := wfs.NewReadOnly(arguments.StoragePath)
		if err != nil {
			logger.Error("List store initialization failed", "error", err)
			os.Exit(1)
		}
		defer listStore.Close()
		listSrv := NewListServer(listStore, logger)

		restoreStore, err := wfs.NewReadOnly(arguments.StoragePath)
		if err != nil {
			logger.Error("Restore store initialization failed", "error", err)
			os.Exit(1)
		}
		defer restoreStore.Close()
		restoreSrv := NewRestoreServer(restoreStore, logger)

		if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {
			pb.RegisterBackupServiceServer(s, backupServer)
			pb.RegisterListServiceServer(s, listSrv)
			pb.RegisterRestoreServiceServer(s, restoreSrv)
		}); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}

	case "list":
		if err := runList(logger, arguments.StoragePath, arguments.ServerName, arguments.PathFilter, arguments.Output, arguments.Filter); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
	}
}
