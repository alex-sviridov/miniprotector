// clientmanager-api exposes read-only gRPC access to client-manager's
// enrolled-client data (clientmanager.sqlite), the same file issuer
// already shares -- client-manager itself stays a network-surface-free
// CLI tool. See docs/components/clientmanager-api.md and
// docs/superpowers/specs/2026-07-14-api-server-design.md.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
	"google.golang.org/grpc"
)

func main() {
	const appName = "clientmanager-api"

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

	arguments, err := parseArguments(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		logger.Error("var directory resolution failed", "error", err)
		os.Exit(1)
	}
	store, err := clientmanagerstore.New(varDir)
	if err != nil {
		logger.Error("failed to open client-manager store", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	srv := NewClientManagerAPIServer(store, logger)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("clientmanager-api started", "port", arguments.Port)

	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
		pb.RegisterClientManagerServiceServer(s, srv)
	}); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
