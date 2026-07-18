// src/cmd/api-server/main.go
// api-server exposes a unified, read-only REST API in front of the
// control plane's clientmanager-api and catalog gRPC services, for
// browsers and admin tools that don't hold a mesh mTLS client
// certificate. See docs/components/api-server.md and
// docs/superpowers/specs/2026-07-14-api-server-design.md.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
	const appName = "api-server"

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

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	cmConn, err := connection.Connect(conf.ClientManagerAPIHost, conf.ClientManagerAPIPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to clientmanager-api failed", "error", err)
		os.Exit(1)
	}
	defer cmConn.Close()

	catalogConn, err := connection.Connect(conf.CatalogHost, conf.CatalogPort, conf.ConnectionTimeOutSec, certsDir)
	if err != nil {
		logger.Error("connect to catalog failed", "error", err)
		os.Exit(1)
	}
	defer catalogConn.Close()

	srv := newServer(pb.NewClientManagerServiceClient(cmConn), pb.NewCatalogServiceClient(catalogConn), logger)

	mux := http.NewServeMux()
	srv.registerRoutes(mux)
	handler := requireBearerToken(arguments.Token, mux)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	httpServer := &http.Server{Addr: fmt.Sprintf(":%d", arguments.Port), Handler: handler}
	go func() {
		<-signalCtx.Done()
		logger.Info("shutting down api-server...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
	}()

	logger.Info("api-server started", "port", arguments.Port)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", "error", err)
		os.Exit(1)
	}
}
