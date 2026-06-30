package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Action     string // "list" | "verify"
	ServerName string
	PathFilter string
	BwfsHost   string
	BwfsPort   int
	Output     string // "table" | "json" (list only)
	Filter     string
	Debug      bool
	Quiet      bool
	Streams    int // verify only
	Retries    int // verify only

	listPositional string
	bwfsTarget     string
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "rwfs <command>",
		Short: "Restore writer filesystem tool",
	}

	listCmd := &cobra.Command{
		Use:   "list [[server_name:]path] <bwfs_host:port>",
		Short: "List files available on a remote bwfs server",
		Args:  cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, cliArgs []string) {
			args.Action = "list"
			if len(cliArgs) == 1 {
				args.bwfsTarget = cliArgs[0]
			} else {
				args.listPositional = cliArgs[0]
				args.bwfsTarget = cliArgs[1]
			}
		},
	}
	listCmd.Flags().StringVar(&args.Output, "output", "table", "Output format: table or json")
	listCmd.Flags().StringVar(&args.Filter, "filter", "", "Filter by text in file path")
	listCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	listCmd.Flags().BoolVar(&args.Quiet, "quiet", false, "Suppress console logging")

	verifyCmd := &cobra.Command{
		Use:   "verify [[server_name:]path] <bwfs_host:port>",
		Short: "Verify integrity of files stored on a remote bwfs server",
		Args:  cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, cliArgs []string) {
			args.Action = "verify"
			if len(cliArgs) == 1 {
				args.bwfsTarget = cliArgs[0]
			} else {
				args.listPositional = cliArgs[0]
				args.bwfsTarget = cliArgs[1]
			}
		},
	}
	verifyCmd.Flags().StringVar(&args.Filter, "filter", "", "Filter by text in file path")
	verifyCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	verifyCmd.Flags().BoolVar(&args.Quiet, "quiet", false, "Suppress per-file success lines (warnings and summary always shown)")
	verifyCmd.Flags().IntVar(&args.Streams, "streams", 4, "Number of concurrent verification workers")
	verifyCmd.Flags().IntVar(&args.Retries, "retries", 3, "Max retry attempts per file on stream error")

	rootCmd.AddCommand(listCmd)
	rootCmd.AddCommand(verifyCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: list, verify")
	}

	if args.Action == "list" {
		if args.Output != "table" && args.Output != "json" {
			return nil, fmt.Errorf("--output must be 'table' or 'json', got: %q", args.Output)
		}
	}

	if args.Action == "verify" {
		if err := common.ValidateStreamsCount(args.Streams); err != nil {
			return nil, fmt.Errorf("--streams: %w", err)
		}
		if args.Retries < 1 {
			return nil, fmt.Errorf("--retries must be at least 1, got: %d", args.Retries)
		}
	}

	serverName, path, err := common.ParseServerPath(args.listPositional)
	if err != nil {
		return nil, fmt.Errorf("positional error: %w", err)
	}
	if serverName == "" {
		serverName = common.GetHostname()
	}
	args.ServerName = serverName
	args.PathFilter = path

	host, port, err := common.ParseDestination(args.bwfsTarget, "localhost", conf.DefaultPort)
	if err != nil {
		return nil, fmt.Errorf("invalid bwfs target: %w", err)
	}
	args.BwfsHost = host
	args.BwfsPort = port

	return args, nil
}
