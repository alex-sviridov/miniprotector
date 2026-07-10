// policyclient fetches this node's applicable backup policies from
// policy-server and caches them locally as policies-cache.json. It does not
// act on the cached policies -- scheduling or running backups from the
// cache is separate, later work.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
)

func main() {
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

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Var directory resolution failed: %v\n", err)
		os.Exit(1)
	}
	cachePath := filepath.Join(varDir, "policies-cache.json")

	ctx := context.WithValue(context.Background(), "appName", "policyclient")
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)
	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	switch args.Action {
	case "fetch":
		if conf.PolicyServerHost == "" {
			fmt.Fprintln(os.Stderr, "Configuration error: policy_server_host not set in local.conf")
			os.Exit(1)
		}
		if err := fetchAndCache(certsDir, conf.PolicyServerHost, conf.PolicyServerPort, conf.ConnectionTimeOutSec, cachePath, logger); err != nil {
			logger.Error("fetch failed", "error", err)
			fmt.Fprintf(os.Stderr, "Fetch failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Policy cache updated at", cachePath)
	}
}
