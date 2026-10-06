package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
	"google.golang.org/grpc"
)

func main() {
	const appName = "bwfs"

	ctx := logging.WithAppName(context.Background(), appName)

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
	connection.SetFlowControlWindow(conf.GrpcWindowBytes)

	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}
	ctx = logging.WithDebugMode(ctx, arguments.Debug)
	ctx = logging.WithQuietMode(ctx, arguments.Quiet)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	switch arguments.Action {
	case "server":
		logger.Info("Backup writer started",
			"StoragePath", arguments.StoragePath,
			"serverPort", arguments.Port,
		)

		// Every other gRPC server in this repo wires signal.NotifyContext
		// before starting -- bwfs was the one outlier, meaning
		// common/connection/server.go's existing GracefulStop() path (on
		// <-ctx.Done()) was dead code here: a SIGTERM killed bwfs
		// immediately, hard-terminating any in-flight BackupService/
		// RestoreService stream instead of letting it finish. This matters
		// now specifically because agent (see docs/components/agent.md's
		// "Storage-policy supervision") routinely sends bwfs SIGTERM --
		// on its own shutdown, and whenever a storage policy is edited or
		// removed.
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		backupServer, err := NewBackupServer(signalCtx, logger, arguments.StoragePath)
		if err != nil {
			logger.Error("Server initialization failed", "error", err)
			os.Exit(1)
		}
		defer func() {
			// Close flushes pending chunks; a failure means the last writes
			// may not be durable, which the operator must be able to see.
			if err := backupServer.store.Close(); err != nil {
				logger.Error("Closing the store failed", "error", err)
			}
		}()

		if err := startupVacuum(logger, backupServer.store); err != nil {
			logger.Error("Startup vacuum failed", "error", err)
			os.Exit(1)
		}

		staleCount, err := backupServer.store.FailStaleInProgressJobs()
		if err != nil {
			logger.Error("Startup job reconciliation failed", "error", err)
			os.Exit(1)
		}
		if staleCount > 0 {
			logger.Warn("Marked stale in-progress jobs as failed after restart", "count", staleCount)
		}

		go watchStaleJobs(signalCtx, backupServer, time.Duration(conf.JobTimeoutSec)*time.Second)
		startStoreGC(signalCtx, logger, backupServer.store, gcSettingsFrom(conf))

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

		certsDir, err := config.ResolveCertsDir()
		if err != nil {
			logger.Error("Certs directory resolution failed", "error", err)
			os.Exit(1)
		}

		connCounter := &connCounter{}
		startStatusReporter(signalCtx, logger, conf, certsDir, statusSource{
			policyID: arguments.PolicyID,
			port:     arguments.Port,
			root:     arguments.StoragePath,
			conns:    connCounter,
			jobs:     backupServer.liveness,
		})

		if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, roleRequirements(), func(s *grpc.Server) {
			pb.RegisterBackupServiceServer(s, backupServer)
			pb.RegisterListServiceServer(s, listSrv)
			pb.RegisterRestoreServiceServer(s, restoreSrv)
		}, grpc.StatsHandler(connCounter)); err != nil {
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

// startupVacuum runs the startup Vacuum and logs its result. A failure while
// reclaiming segment space (say, one unreadable sector) only warns: the
// database cleanup is committed and the store is consistent, and refusing to
// start would turn a space problem into a crash loop. Any other failure is
// returned and is fatal.
func startupVacuum(logger *slog.Logger, store storage.BackupStore) error {
	res, err := store.Vacuum()
	if errors.Is(err, storage.ErrReclaimIncomplete) && res != nil {
		logger.Warn("Startup vacuum could not reclaim all segment space; continuing",
			append(vacuumResultAttrs(res), "error", err)...)
		return nil
	}
	if err != nil {
		return err
	}
	logger.Info("Startup vacuum completed", vacuumResultAttrs(res)...)
	return nil
}

func vacuumResultAttrs(res *storage.VacuumResult) []any {
	return []any{
		"orphaned_file_data_removed", res.OrphanedFileDataRemoved,
		"orphaned_chunk_links_removed", res.OrphanedChunkLinksRemoved,
		"orphaned_chunks_removed", res.OrphanedChunksRemoved,
		"incomplete_file_data_removed", res.IncompleteFileData,
		"segments_removed", res.SegmentsRemoved,
		"segments_compacted", res.SegmentsCompacted,
		"bytes_reclaimed", res.BytesReclaimed,
	}
}
