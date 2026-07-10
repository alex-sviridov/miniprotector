package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "fetch"
	Debug  bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "policyclient <command>",
		Short: "Fetch backup policies from policy-server into a local cache",
	}
	rootCmd.PersistentFlags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	fetchCmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch current policies from policy-server and update the local cache",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "fetch" },
	}

	rootCmd.AddCommand(fetchCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: fetch")
	}
	return args, nil
}
