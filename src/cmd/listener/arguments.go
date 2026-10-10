package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Port  int
	Debug bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "listener",
		Short: "Terminate mTLS on one port and route control-plane gRPC to local services",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().IntVar(&args.Port, "port", conf.ListenerPort, "Port to listen on")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	if err := common.ValidatePort(args.Port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}
	return args, nil
}
