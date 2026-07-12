// log-gateway is an mTLS-terminating HTTP reverse proxy in front of Loki --
// the only new network-facing binary the fleet log aggregation design
// introduces. It shares no database and calls no other service besides
// Loki. See docs/components/log-gateway.md and
// docs/superpowers/specs/2026-07-11-fleet-log-aggregation-design.md.
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/common/mtls"
)

func main() {
	const appName = "log-gateway"

	args, err := parseArguments()
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

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	tlsConfig, err := mtls.ServerTLSConfig(certsDir)
	if err != nil {
		logger.Error("tls config failed", "error", err)
		os.Exit(1)
	}

	srv := newLogGatewayServer(args.LokiURL, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("/loki/api/v1/push", srv.ServeHTTP)
	httpServer := &http.Server{Handler: mux, TLSConfig: tlsConfig}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", conf.LogGatewayPort))
	if err != nil {
		logger.Error("listen failed", "port", conf.LogGatewayPort, "error", err)
		os.Exit(1)
	}
	tlsListener := tls.NewListener(listener, tlsConfig)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		<-signalCtx.Done()
		logger.Info("shutting down log-gateway")
		_ = httpServer.Shutdown(context.Background())
	}()

	logger.Info("log-gateway started", "port", conf.LogGatewayPort, "loki_url", args.LokiURL)
	if err := httpServer.Serve(tlsListener); err != nil && err != http.ErrServerClosed {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
}
