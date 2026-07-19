// src/cmd/clientmanager-admin-api/arguments.go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Port         int
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
	Debug        bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "clientmanager-admin-api",
		Short: "CA-admin-equivalent gRPC writes onto client-manager's enrolled-client data",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().IntVar(&args.Port, "port", conf.ClientManagerAdminAPIPort, "Port to listen on")
	cmd.Flags().StringVar(&args.CAURL, "ca-url", "https://localhost:9000", "CA URL, e.g. https://localhost:9000")
	cmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	if err := common.ValidatePort(args.Port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}

	return args, nil
}
