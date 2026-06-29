package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	StoragePath string
	Action      string // "server" | "list"
	// server flags
	Port  int
	Debug bool
	Quiet bool
	// list flags
	Output string // "table" | "json"
	Filter string
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "bwfs <storage_path> <command>",
		Short: "Backup writer filesystem tool",
		Args:  cobra.ExactArgs(1),
	}

	// server subcommand
	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Start the backup writer server",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			args.Action = "server"
			args.StoragePath = rootCmd.Flags().Args()[0]
		},
	}
	serverCmd.Flags().IntVar(&args.Port, "port", conf.DefaultPort, "Port to listen on")
	serverCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	serverCmd.Flags().BoolVar(&args.Quiet, "quiet", false, "Enable quiet mode")

	// list subcommand
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List stored file data",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			args.Action = "list"
			args.StoragePath = rootCmd.Flags().Args()[0]
		},
	}
	listCmd.Flags().StringVar(&args.Output, "output", "table", "Output format: table or json")
	listCmd.Flags().StringVar(&args.Filter, "filter", "", "Filter by text in file path")
	listCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	rootCmd.AddCommand(serverCmd, listCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: server or list")
	}

	if args.Action == "server" {
		if err := common.ValidatePort(args.Port); err != nil {
			return nil, fmt.Errorf("port error: %w", err)
		}
	}

	if args.Action == "list" && args.Output != "table" && args.Output != "json" {
		return nil, fmt.Errorf("--output must be 'table' or 'json', got: %q", args.Output)
	}

	return args, nil
}
