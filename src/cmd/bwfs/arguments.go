package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

// Command line flags
var (
	port  int
	debug bool
)

// Arguments holds parsed command line arguments
type Arguments struct {
	StoragePath string
	Port        int
	Debug       bool
	Quiet       bool
}

// parseArguments uses Cobra to parse command line arguments
func parseArguments(conf *config.Config) (*Arguments, error) {
	cmd := &cobra.Command{
		Use:   "bwfs <storage_path>",
		Short: "Backup writer tool for receiving files",
		Args:  cobra.ExactArgs(1),
		Run:   func(cmd *cobra.Command, args []string) {}, // Empty - just for parsing
	}

	// Add flags
	cmd.Flags().IntVar(&port, "port", conf.DefaultPort, "Port to listen on")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging")
	cmd.Flags().BoolVar(&debug, "quiet", false, "Enable quiet mode")

	// Parse arguments and flags
	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	// Get the storage path from parsed args
	storagePath := cmd.Flags().Args()[0]

	// Validate port
	if err := common.ValidatePort(port); err != nil {
		return nil, fmt.Errorf("port error: %w", err)
	}

	return &Arguments{
		StoragePath: storagePath,
		Port:        port,
		Debug:       debug,
	}, nil
}
