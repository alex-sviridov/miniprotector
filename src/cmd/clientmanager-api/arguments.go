// src/cmd/clientmanager-api/arguments.go
package main

import (
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Debug bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "clientmanager-api",
		Short: "Read-only gRPC access to client-manager's enrolled-client data",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	return args, nil
}
