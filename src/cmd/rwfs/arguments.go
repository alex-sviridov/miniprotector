package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	Action     string // "list"
	ServerName string // source hostname filter, may be empty
	PathFilter string // path prefix filter, may be empty
	BwfsHost   string
	BwfsPort   int
	Output     string // "table" | "json"
	Filter     string
	Debug      bool
	Quiet      bool

	listPositional string // raw "[server_name:]path", staged before ParseServerPath
	bwfsTarget     string // raw "host:port", staged before ParseDestination
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

	rootCmd.AddCommand(listCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: list")
	}

	if args.Output != "table" && args.Output != "json" {
		return nil, fmt.Errorf("--output must be 'table' or 'json', got: %q", args.Output)
	}

	serverName, path, err := common.ParseServerPath(args.listPositional)
	if err != nil {
		return nil, fmt.Errorf("list positional error: %w", err)
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
