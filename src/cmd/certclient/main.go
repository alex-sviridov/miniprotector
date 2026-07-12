// certclient manages a node's mTLS bootstrap credential (bootstrap.crt/
// bootstrap.key) and, via operating-refresh, its short-lived operating
// certificate (client.crt/client.key) obtained from issuer.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/smallstep/certificates/ca"
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

	jobID := jobid.Resolve(args.JobID)

	ctx := context.WithValue(context.Background(), "appName", "certclient")
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)
	ctx = context.WithValue(ctx, "jobId", jobID)
	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	switch args.Action {
	case "bootstrap":
		tok, err := resolveToken(args.Token, os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Token error: %v\n", err)
			os.Exit(1)
		}
		logger.Debug("bootstrapping identity")
		client, err := ca.Bootstrap(tok)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
			os.Exit(1)
		}
		if err := bootstrap(tok, client, certsDir); err != nil {
			logger.Error("bootstrap failed", "error", err)
			fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
			os.Exit(1)
		}
		logger.Info("bootstrap succeeded", "certs_dir", certsDir)
		fmt.Println("Identity bootstrapped in", certsDir)

	case "renew":
		if conf.CAHost == "" {
			fmt.Fprintln(os.Stderr, "Configuration error: ca_host not set in local.conf")
			os.Exit(1)
		}
		client, err := ca.NewClient(fmt.Sprintf("https://%s", conf.CAHost), ca.WithRootFile(filepath.Join(certsDir, "ca.crt")))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create CA client: %v\n", err)
			os.Exit(1)
		}
		logger.Debug("renewing bootstrap credential")
		if err := renew(client, certsDir); err != nil {
			logger.Error("renew failed", "error", err)
			fmt.Fprintf(os.Stderr, "Renew failed: %v\n", err)
			os.Exit(1)
		}
		logger.Info("renew succeeded", "certs_dir", certsDir)
		fmt.Println("Identity renewed in", certsDir)

	case "operating-refresh":
		if conf.IssuerHost == "" {
			fmt.Fprintln(os.Stderr, "Configuration error: issuer_host not set in local.conf")
			os.Exit(1)
		}
		if err := operatingRefresh(certsDir, conf.IssuerHost, conf.IssuerPort, conf.ConnectionTimeOutSec, jobID, logger); err != nil {
			logger.Error("operating refresh failed", "error", err)
			fmt.Fprintf(os.Stderr, "Operating refresh failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Operating certificate refreshed in", certsDir)
	}
}
