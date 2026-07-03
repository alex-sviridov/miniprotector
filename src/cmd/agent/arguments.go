package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "serve" | "list-policies"
	Debug  bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "agent <command>",
		Short: "Node agent: reconciles local state against embedded policies",
	}

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the reconcile loop",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "serve" },
	}
	serveCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	listCmd := &cobra.Command{
		Use:   "list-policies",
		Short: "Show configured policies and their reconciliation state",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "list-policies" },
	}

	rootCmd.AddCommand(serveCmd, listCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: serve, list-policies")
	}

	return args, nil
}
