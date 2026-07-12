package main

import (
	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	LokiURL string
	Debug   bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "log-gateway",
		Short: "Verify agent-managed nodes' identity via mTLS and forward their logs to Loki, with the hostname label always derived from the verified peer certificate",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&args.LokiURL, "loki-url", "http://localhost:3100", "Base URL of the Loki instance to forward pushes to")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return args, nil
}
