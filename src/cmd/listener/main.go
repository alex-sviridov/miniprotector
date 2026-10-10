// listener is the single network entry point for a host's control-plane gRPC
// services: it terminates mTLS on listener_port, verifies the client
// certificate, and proxies each call over a Unix socket to the local backend
// serving that gRPC service (see common/svcproxy). A service not running on
// this host gets UNIMPLEMENTED.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/common/svcproxy"
	"google.golang.org/grpc"
)

func main() {
	const appName = "listener"

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

	ctx := logging.WithAppName(context.Background(), appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = logging.WithDebugMode(ctx, arguments.Debug)
	ctx = logging.WithQuietMode(ctx, false)
	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}
	runDir, err := config.ResolveRunDir(conf)
	if err != nil {
		logger.Error("run directory resolution failed", "error", err)
		os.Exit(1)
	}
	if runDir, err = filepath.Abs(runDir); err != nil {
		logger.Error("run directory resolution failed", "error", err)
		os.Exit(1)
	}
	creds, err := mtls.LoadServerCredentials(certsDir)
	if err != nil {
		logger.Error("failed to load server credentials", "error", err)
		os.Exit(1)
	}

	proxy := svcproxy.New(logger, runDir)
	defer proxy.Close()

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("listener started", "port", arguments.Port, "run_dir", runDir)
	if err := connection.StartServerWithCredentials(signalCtx, logger, arguments.Port, creds, nil,
		func(*grpc.Server) {}, proxy.ServerOptions()...); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
