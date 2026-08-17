package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
	const appName = "rwfs"

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

	// verify --quiet only suppresses per-file success lines, not all console output.
	// list --quiet suppresses all console output (original behaviour).
	quietForLogger := arguments.Quiet && arguments.Action != "verify"
	ctx = context.WithValue(ctx, "quietMode", quietForLogger)

	// Resolve the correlation ID before the logger is built, so every line
	// rwfs writes carries job_id, and pass it down so the bwfs RPCs carry
	// it as outgoing metadata too (mirrors brfs/policyclient).
	jobID := jobid.Resolve(arguments.JobID)
	ctx = context.WithValue(ctx, "jobId", jobID)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("Certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	switch arguments.Action {
	case "list":
		if err := runList(arguments.BwfsHost, arguments.BwfsPort, arguments.ServerName, arguments.PathFilter, arguments.Filter, arguments.Output, certsDir, jobID); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.ServerName, arguments.PathFilter, arguments.Filter, arguments.RulesStdin, os.Stdin, arguments.Streams, arguments.Retries, arguments.Quiet, certsDir, jobID); err != nil {
			logger.Error("Verify failed", "error", err)
			os.Exit(1)
		}
	case "restore":
		// TODO(task 3): 4 is a temporary placeholder for --streams wiring --
		// Task 3 replaces this with arguments.Streams plus the actual flag.
		if err := runRestore(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.Overwrite, os.Stdin, arguments.Quiet, 4, certsDir, jobID); err != nil {
			logger.Error("Restore failed", "error", err)
			os.Exit(1)
		}
	}
}
