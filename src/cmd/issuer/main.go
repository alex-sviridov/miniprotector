// issuer mints short-lived operating certificates for already-enrolled
// nodes, refusing to do so for a revoked hostname. It shares its database
// with client-manager (same var_path, same clientmanager.sqlite file --
// not synced, the same file) and reuses common/certmint for token minting.
// See docs/components/issuer.md and
// docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md.
package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
	"google.golang.org/grpc"
)

func main() {
	const appName = "issuer"

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
	store, err := clientmanagerstore.New(varDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open client-manager store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)

	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	mintOpts := certmint.Options{
		CAURL:        args.CAURL,
		RootFile:     args.RootFile,
		Provisioner:  args.Provisioner,
		PasswordFile: args.PasswordFile,
	}
	mintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return mintAndSign(hostname, sans, attributes, csr, mintOpts, conf.OperatingCertTTLSec)
	}

	if args.Hostname == "" {
		fmt.Fprintln(os.Stderr, "Arguments error: --hostname is required")
		os.Exit(1)
	}

	selfMintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return mintAndSign(hostname, sans, attributes, csr, mintOpts, conf.IssuerSelfCertTTLSec)
	}

	logger.Info("minting own server identity", "hostname", args.Hostname)
	if err := mintSelfIdentity(args.Hostname, certsDir, args.RootFile, selfMintSign, conf.IssuerSelfCertTTLSec); err != nil {
		logger.Error("failed to mint own server identity", "error", err)
		os.Exit(1)
	}

	srv := newIssuerServer(store, mintSign, logger)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	refreshInterval := time.Duration(conf.IssuerSelfCertRefreshIntervalSec) * time.Second
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-ticker.C:
				if err := mintSelfIdentity(args.Hostname, certsDir, args.RootFile, selfMintSign, conf.IssuerSelfCertTTLSec); err != nil {
					logger.Error("self-identity refresh failed, keeping existing certificate", "error", err)
				}
			}
		}
	}()

	logger.Info("issuer started", "port", conf.IssuerPort)

	creds, err := mtls.LoadIssuerServerCredentials(certsDir)
	if err != nil {
		logger.Error("failed to load server credentials", "error", err)
		os.Exit(1)
	}

	if err := connection.StartServerWithCredentials(signalCtx, logger, conf.IssuerPort, creds, func(s *grpc.Server) {
		pb.RegisterIssuerServiceServer(s, srv)
	}); err != nil {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
}
