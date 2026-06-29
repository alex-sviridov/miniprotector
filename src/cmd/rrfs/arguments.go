package main

import (
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

type Arguments struct {
	StoragePath string
	Action      string // "server"
	Port        int
	Debug       bool
	Quiet       bool
}

func parseArguments(conf *config.Config) (*Arguments, error) {
	// storage_path is the first positional arg before the subcommand.
	// Extract it before cobra sees os.Args, same pattern as bwfs.
	if len(os.Args) < 3 {
		return nil, fmt.Errorf("usage: rrfs <storage_path> <server> [flags]")
	}
	storagePath := os.Args[1]
	os.Args = append(os.Args[:1], os.Args[2:]...)

	args := &Arguments{StoragePath: storagePath}

	rootCmd := &cobra.Command{
		Use:   "rrfs <storage_path> <command>",
		Short: "Restore reader filesystem tool",
	}

	serverCmd := &cobra.Command{
		Use:   "server",
		Short: "Start the restore reader server",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "server" },
	}
	serverCmd.Flags().IntVar(&args.Port, "port", conf.DefaultPort, "Port to listen on")
	serverCmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")
	serverCmd.Flags().BoolVar(&args.Quiet, "quiet", false, "Suppress console logging")

	rootCmd.AddCommand(serverCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: server")
	}

	if err := common.ValidatePort(args.Port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}

	absPath, err := common.ValidatePath(args.StoragePath)
	if err != nil {
		return nil, fmt.Errorf("storage path error: %w", err)
	}
	args.StoragePath = absPath

	return args, nil
}
