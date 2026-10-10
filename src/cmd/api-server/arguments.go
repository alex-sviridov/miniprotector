// src/cmd/api-server/arguments.go
package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Port  int
	Token string
	Debug bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "api-server",
		Short: "Unified read-only REST API for the control plane",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().IntVar(&args.Port, "port", conf.APIServerPort, "Port to listen on")
	cmd.Flags().StringVar(&args.Token, "token", conf.APIServerToken, "Bearer token required on every REST request")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	if err := common.ValidatePort(args.Port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}
	if args.Token == "" {
		return nil, fmt.Errorf("bearer token must be set (--token flag or api_server_token in local.conf)")
	}

	return args, nil
}
