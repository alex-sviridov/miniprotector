package main

import (
	"fmt"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/spf13/cobra"
)

// Command line flags
var (
	destination string
	streams     int
	debug       bool
	quiet       bool
)

// Arguments holds parsed command line arguments
type Arguments struct {
	SourceFolder string
	WriterHost   string
	WriterPort   int
	Streams      int
	Debug        bool
	Quiet        bool
}

// parseArguments uses Cobra to parse command line arguments
func parseArguments(config *common.Config) (*Arguments, error) {
	cmd := &cobra.Command{
		Use:   "brfs <source_folder>",
		Short: "Backup tool for reading files",
		Args:  cobra.ExactArgs(1),
		Run:   func(cmd *cobra.Command, args []string) {}, // Empty - just for parsing
	}

	// Add flags
	cmd.Flags().StringVar(&destination, "destination", "", "Writer destination in format host:port")
	cmd.Flags().IntVar(&streams, "streams", config.DefaultStreams, "Number of streams")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress stdout logging")

	// Parse arguments and flags
	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	// Get the source folder from parsed args
	sourceFolder := cmd.Flags().Args()[0]

	// Validate source folder
	validatedSourceFolder, err := common.ValidatePath(sourceFolder)
	if err != nil {
		return nil, fmt.Errorf("Source directory unavailable: %w", err)
	}

	// Parse destination
	host, port, err := common.ParseDestination(destination, "localhost", config.DefaultPort)
	if err != nil {
		return nil, fmt.Errorf("invalid destination: %w", err)
	}

	// Validate streams count
	if err := common.ValidateStreamsCount(streams); err != nil {
		return nil, fmt.Errorf("streams error: %w", err)
	}

	return &Arguments{
		SourceFolder: validatedSourceFolder,
		WriterHost:   host,
		WriterPort:   port,
		Streams:      streams,
		Debug:        debug,
		Quiet:        quiet,
	}, nil
}
