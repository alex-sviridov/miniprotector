// policy-server serves backup policies -- static, operator-maintained JSON
// files under $MP_CONFIG_PATH/policies/backup/ -- filtered to whatever the
// requesting client's verified hostname and certificate-embedded attribute
// labels match. It is bootstrapped and certificate-managed exactly like any
// other node in the mesh (client-manager add, agent + issuer refresh); it
// holds no database and calls no other service. See
// docs/components/policy-server.md and
// docs/superpowers/specs/2026-07-10-policy-server-design.md.
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
	checkinstore "github.com/alex-sviridov/miniprotector/storage/policyserver"
	"google.golang.org/grpc"
)

func main() {
	const appName = "policy-server"

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

	policiesDir, err := config.ResolvePoliciesDir()
	if err != nil {
		logger.Error("policies directory resolution failed", "error", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(policiesDir, 0o755); err != nil {
		logger.Error("failed to create policies directory", "path", policiesDir, "error", err)
		os.Exit(1)
	}

	cache := NewCache()
	if err := cache.Reload(policiesDir, logger); err != nil {
		logger.Error("initial policy load failed", "error", err)
		os.Exit(1)
	}

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		logger.Error("var directory resolution failed", "error", err)
		os.Exit(1)
	}
	checkins, err := checkinstore.New(varDir)
	if err != nil {
		logger.Error("failed to open check-in store", "error", err)
		os.Exit(1)
	}
	defer checkins.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	srv := NewPolicyServerServer(cache, policiesDir, logger, checkins)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := watchForReload(signalCtx, policiesDir, cache, logger); err != nil {
			logger.Error("policy watcher stopped", "error", err)
		}
	}()

	logger.Info("policy-server started", "port", arguments.Port, "policies_dir", policiesDir)

	if err := connection.StartServer(signalCtx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
		pb.RegisterPolicyServiceServer(s, srv)
	}); err != nil {
		logger.Error("Server failed", "error", err)
		os.Exit(1)
	}
}
