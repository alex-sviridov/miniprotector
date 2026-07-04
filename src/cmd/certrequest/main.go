// certrequest mints a one-time enrollment token for a node, run on or near
// the CA host. It also runs as a persistent broker (`certrequest serve`)
// that mints tokens on behalf of client-manager over mTLS -- see
// docs/components/certrequest.md's "serve mode" section.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"google.golang.org/grpc"
)

func main() {
	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	if args.Action == "serve" {
		runServe(args)
		return
	}

	token, err := certmint.Mint(args.Hostname, args.SANs, certmint.Options{
		CAURL:        args.CAURL,
		RootFile:     args.RootFile,
		Provisioner:  args.Provisioner,
		PasswordFile: args.PasswordFile,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(token)
}

func runServe(args *Arguments) {
	const appName = "certrequest"

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
	if conf.ClientManagerHost == "" {
		fmt.Fprintln(os.Stderr, "client_manager_host not set in local.conf")
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

	mintOpts := certmint.Options{
		CAURL:        args.CAURL,
		RootFile:     args.RootFile,
		Provisioner:  args.Provisioner,
		PasswordFile: args.PasswordFile,
	}
	mint := func(hostname string, sans []string) (string, error) {
		return certmint.Mint(hostname, sans, mintOpts)
	}
	srv := newBrokerServer(conf.ClientManagerHost, mint)

	signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("certrequest serve started", "port", conf.CertrequestPort, "trusted_caller", conf.ClientManagerHost)

	if err := connection.StartServer(signalCtx, logger, conf.CertrequestPort, certsDir, func(s *grpc.Server) {
		pb.RegisterEnrollmentBrokerServiceServer(s, srv)
	}); err != nil {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
}
