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

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
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
	srv := newIssuerServer(store, mintSign, logger)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("issuer started", "port", conf.IssuerPort)

	if err := connection.StartServer(signalCtx, logger, conf.IssuerPort, certsDir, func(s *grpc.Server) {
		pb.RegisterIssuerServiceServer(s, srv)
	}); err != nil {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
}
