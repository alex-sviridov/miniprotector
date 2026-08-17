// catalogsync replicates a bwfs node's file_versions to a backup catalog,
// asynchronously and independently of bwfs's own availability.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

const initialBackoff = 1 * time.Second

func main() {
	const appName = "catalogsync"

	arguments, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

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

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	replicaReader, err := wfs.OpenReplicaReader(arguments.StoragePath)
	if err != nil {
		logger.Error("failed to open bwfs store read-only", "error", err)
		os.Exit(1)
	}
	defer replicaReader.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}
	sender, err := selectSender(conf, logger, certsDir)
	if err != nil {
		logger.Error("failed to set up catalog sender", "error", err)
		os.Exit(1)
	}
	if closer, ok := sender.(interface{ Close() error }); ok {
		defer closer.Close()
	}
	cursorFile := filepath.Join(arguments.StoragePath, "catalogsync.cursor")

	cfg := syncConfig{
		BatchSize:      conf.CatalogSyncBatchSize,
		PollInterval:   time.Duration(conf.CatalogSyncPollIntervalSec) * time.Second,
		InitialBackoff: initialBackoff,
		MaxBackoff:     time.Duration(conf.CatalogSyncMaxBackoffSec) * time.Second,
	}

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("catalogsync started", "storage_path", arguments.StoragePath, "batch_size", cfg.BatchSize)

	if err := run(signalCtx, logger, replicaReader, sender, cursorFile, cfg); err != nil {
		logger.Error("catalogsync exited with error", "error", err)
		os.Exit(1)
	}
}
