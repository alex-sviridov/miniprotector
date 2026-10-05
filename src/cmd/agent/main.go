// agent is a node-level process that reconciles local state against a
// small set of policies compiled into the binary. v1 has exactly one:
// renew this node's mTLS identity via certclient on a fixed interval.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
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

	switch arguments.Action {
	case "serve":
		os.Exit(serve(conf, arguments, varDir, cachePath, policiesCachePath))

	case "list-policies":
		os.Exit(listPolicies(conf, cachePath, policiesCachePath))
	}
}

// serve runs the "serve" subcommand: sets up logging, Vector, and the
// reconcile loop, returning a process exit code rather than calling
// os.Exit directly -- every defer registered along the way (most
// importantly logfile.Close()) must run before the process actually exits,
// which os.Exit alone does not guarantee.
func serve(conf *config.Config, arguments *Arguments, varDir, cachePath, policiesCachePath string) int {
	const appName = "agent"

	if err := os.MkdirAll(varDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create var directory %s: %v\n", varDir, err)
		return 1
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
		return 1
	}

	vectorBinary, err := resolveVectorBinary()
	if err != nil {
		logger.Error("vector binary resolution failed", "error", err)
		return 1
	}
	bwfsBinary := resolveExecPath("bwfs")
	catalogsyncBinary := resolveExecPath("catalogsync")
	storageMgr := newStorageManager(logger)

	derivedFunc := newDerivedFunc(policiesCachePath, filepath.Join(varDir, "retention"), logger, conf, bwfsBinary, catalogsyncBinary)

	hostname, err := hostnameFromBootstrapCert(certsDir)
	if err != nil {
		logger.Error("hostname resolution from bootstrap credential failed", "error", err)
		return 1
	}
	vectorConfig, err := renderVectorConfig(conf.LogDir, varDir, certsDir, conf.LogGatewayHost, conf.LogGatewayPort, hostname)
	if err != nil {
		logger.Error("vector config render failed", "error", err)
		return 1
	}
	vectorConfigPath := filepath.Join(varDir, "vector-config.yaml")
	if err := os.WriteFile(vectorConfigPath, []byte(vectorConfig), 0o644); err != nil {
		logger.Error("vector config write failed", "path", vectorConfigPath, "error", err)
		return 1
	}

	reconcileInterval := time.Duration(conf.ReconcileIntervalSec) * time.Second
	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	vectorSup := newVectorSupervisor(vectorBinary, vectorConfigPath, logger)
	vectorSup.Start(signalCtx)
	defer vectorSup.Stop()
	defer storageMgr.StopAll()

	onSuccess := func(policyID string) {
		if policyID == "operating-refresh" {
			vectorSup.TriggerRestart()
		}
	}

	logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath, "vector_config", vectorConfigPath)
	if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, derivedFunc, conf.MaxConcurrentBackupJobs, onSuccess, storageMgr, defaultBackoffPolicy); err != nil {
		logger.Error("agent exited with error", "error", err)
		return 1
	}
	return 0
}

// newDerivedFunc returns the reconcile loop's per-tick policy/storage-task
// derivation function -- reads policies-cache.json exactly once per tick,
// and, on any read failure, still returns the three static policies (see
// policies(conf)) so bootstrap-refresh/operating-refresh/policy-update
// keep running even when the cache is missing or unreadable. policy-update
// is the only thing that (re)creates that file, so suppressing it here
// would deadlock a fresh install forever -- see docs/superpowers/specs/
// 2026-08-28-agent-reliability-refactor-design.md for the incident this
// guards against. A missing file (fresh install, not yet fetched) logs at
// Debug; anything else (permission error, corrupt JSON) logs at Warn,
// since that's a real operator-actionable signal that was previously
// completely silent -- pruning and storage supervision both freeze on a
// persistently unreadable cache with nothing in agent.log to show it.
func newDerivedFunc(policiesCachePath, retentionDir string, logger *slog.Logger, conf *config.Config, bwfsBinary, catalogsyncBinary string) func() ([]Policy, []storageTask, bool) {
	return func() ([]Policy, []storageTask, bool) {
		cachedPolicies, ok := readCachedPolicies(policiesCachePath)
		if !ok {
			if _, statErr := os.Stat(policiesCachePath); os.IsNotExist(statErr) {
				logger.Debug("policies cache not yet present, static policies still running", "path", policiesCachePath)
			} else {
				logger.Warn("policies cache unreadable or corrupt, static policies still running but dynamic tasks skipped this tick", "path", policiesCachePath)
			}
			return policies(conf), nil, false
		}
		allPolicies := append(policies(conf), backupTasks(cachedPolicies, logger, conf, retentionDir)...)
		allPolicies = append(allPolicies, restoreTasks(cachedPolicies, logger)...)
		storageTaskList := storageTasks(cachedPolicies, logger, bwfsBinary, catalogsyncBinary)
		return allPolicies, storageTaskList, true
	}
}

// listPolicies runs the "list-policies" subcommand: reads and renders
// agent-state.json without executing anything.
func listPolicies(conf *config.Config, cachePath, policiesCachePath string) int {
	// list-policies never executes anything -- a silent logger here keeps
	// backupTasks'/storageTasks' own skip-with-log warnings out of stdout's
	// table, matching this command's existing read-only, no-noise character.
	silentLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cachedPolicies, _ := readCachedPolicies(policiesCachePath)
	allPolicies := append(policies(conf), backupTasks(cachedPolicies, silentLogger, conf, "")...)
	allPolicies = append(allPolicies, restoreTasks(cachedPolicies, silentLogger)...)
	bwfsBinary := resolveExecPath("bwfs")
	catalogsyncBinary := resolveExecPath("catalogsync")
	storageTaskList := storageTasks(cachedPolicies, silentLogger, bwfsBinary, catalogsyncBinary)
	if err := renderPolicies(os.Stdout, cachePath, time.Now(), allPolicies, storageTaskList); err != nil {
		fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
		return 1
	}
	return 0
}
