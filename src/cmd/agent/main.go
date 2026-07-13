// agent is a node-level process that reconciles local state against a
// small set of policies compiled into the binary. v1 has exactly one:
// renew this node's mTLS identity via certclient on a fixed interval.
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
)

func main() {
	const appName = "agent"

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

	arguments, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Var directory resolution failed: %v\n", err)
		os.Exit(1)
	}
	cachePath := filepath.Join(varDir, "agent-state.json")
	policiesCachePath := filepath.Join(varDir, "policies-cache.json")

	// policiesFunc combines the three static policies with the dynamic
	// backup tasks derived from policies-cache.json -- called fresh every
	// reconcile tick (not resolved once here) so agent serve notices
	// policy-update's cache changing over time without needing a restart.
	// ok is false whenever backupTasks's own read of policies-cache.json
	// failed this tick -- see reconcile.go's prune, which must not treat a
	// failed read as "every backup task was removed."
	policiesFunc := func() ([]Policy, bool) {
		tasks, ok := backupTasks(policiesCachePath, conf)
		return append(policies(conf), tasks...), ok
	}

	switch arguments.Action {
	case "serve":
		if err := os.MkdirAll(varDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create var directory %s: %v\n", varDir, err)
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

		vectorBinary, err := resolveVectorBinary()
		if err != nil {
			logger.Error("vector binary resolution failed", "error", err)
			os.Exit(1)
		}
		hostname, err := hostnameFromBootstrapCert(certsDir)
		if err != nil {
			logger.Error("hostname resolution from bootstrap credential failed", "error", err)
			os.Exit(1)
		}
		vectorConfig, err := renderVectorConfig(conf.LogDir, varDir, certsDir, conf.LogGatewayHost, conf.LogGatewayPort, hostname)
		if err != nil {
			logger.Error("vector config render failed", "error", err)
			os.Exit(1)
		}
		vectorConfigPath := filepath.Join(varDir, "vector-config.yaml")
		if err := os.WriteFile(vectorConfigPath, []byte(vectorConfig), 0o644); err != nil {
			logger.Error("vector config write failed", "path", vectorConfigPath, "error", err)
			os.Exit(1)
		}

		reconcileInterval := time.Duration(conf.ReconcileIntervalSec) * time.Second
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		vectorSup := newVectorSupervisor(vectorBinary, vectorConfigPath, logger)
		vectorSup.Start(signalCtx)
		defer vectorSup.Stop()

		onSuccess := func(policyID string) {
			if policyID == "operating-refresh" {
				vectorSup.TriggerRestart()
			}
		}

		logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath, "vector_config", vectorConfigPath)
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, policiesFunc, conf.MaxConcurrentBackupJobs, onSuccess); err != nil {
			logger.Error("agent exited with error", "error", err)
			os.Exit(1)
		}

	case "list-policies":
		allPolicies, _ := policiesFunc()
		if err := renderPolicies(os.Stdout, cachePath, time.Now(), allPolicies); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
	}
}
