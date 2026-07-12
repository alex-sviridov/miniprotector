package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "bootstrap" | "renew" | "operating-refresh"
	Token  string
	Debug  bool
	JobID  string
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "certclient <command>",
		Short: "Manage this node's mTLS bootstrap credential and operating certificate",
	}
	rootCmd.PersistentFlags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	bootstrapCmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Redeem a one-time enrollment token for a long-lived bootstrap credential",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "bootstrap" },
	}
	bootstrapCmd.Flags().StringVar(&args.Token, "token", "",
		"Enrollment token for first-time bootstrap (prefer MP_CERT_TOKEN or the stdin prompt over this flag on shared hosts)")

	renewCmd := &cobra.Command{
		Use:   "renew",
		Short: "Renew the existing bootstrap credential via step-ca's /renew",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "renew" },
	}
	renewCmd.Flags().StringVar(&args.JobID, "job-id", "", "Correlation ID for this invocation's logs (auto-generated if omitted)")

	operatingRefreshCmd := &cobra.Command{
		Use:   "operating-refresh",
		Short: "Obtain a fresh operating certificate from issuer",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "operating-refresh" },
	}
	operatingRefreshCmd.Flags().StringVar(&args.JobID, "job-id", "", "Correlation ID for this invocation's logs (auto-generated if omitted); sent to issuer as job-id metadata")

	rootCmd.AddCommand(bootstrapCmd, renewCmd, operatingRefreshCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: bootstrap, renew, operating-refresh")
	}
	return args, nil
}
