package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/alex-sviridov/miniprotector/common"
	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/spf13/cobra"
)

// errHelpRequested signals that cobra already printed help output for
// -h/--help; the caller should exit cleanly instead of reporting an error.
var errHelpRequested = errors.New("help requested")

// splitPatterns splits a comma-separated flag value into a pattern list;
// an empty string produces a nil (empty) slice rather than []string{""}.
func splitPatterns(raw string) []string {
	if raw == "" {
		return nil
	}
	return strings.Split(raw, ",")
}

// Command line flags
var (
	destination   string
	streams       int
	windowFlag    int
	debug         bool
	quiet         bool
	jobIDFlag     string
	includeFlag   string
	excludeFlag   string
	retentionFile string
)

// Arguments holds parsed command line arguments
type Arguments struct {
	SourceFolder  string
	WriterHost    string
	WriterPort    int
	Streams       int
	Window        int
	Debug         bool
	Quiet         bool
	JobID         string
	Include       []string
	Exclude       []string
	RetentionFile string
}

// windowDefault is the --window default: the configured default_window, or
// the built-in defaultWindow when the config does not provide one.
func windowDefault(conf *config.Config) int {
	if conf.DefaultWindow < 1 {
		return defaultWindow
	}
	return conf.DefaultWindow
}

// parseArguments uses Cobra to parse command line arguments
func parseArguments(conf *config.Config) (*Arguments, error) {
	cmd := &cobra.Command{
		Use:   "brfs <source_folder>",
		Short: "Backup tool for reading files",
		Args:  cobra.ExactArgs(1),
		Run:   func(cmd *cobra.Command, args []string) {}, // Empty - just for parsing
	}

	// Add flags
	cmd.Flags().StringVar(&destination, "destination", "", "Writer destination in format host:port")
	cmd.Flags().IntVar(&streams, "streams", conf.DefaultStreams, "Number of streams")
	cmd.Flags().IntVar(&windowFlag, "window", windowDefault(conf), "Max chunks in flight per stream (1 = send one chunk at a time)")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable debug logging")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "Suppress stdout logging")
	cmd.Flags().StringVar(&jobIDFlag, "job-id", "", "Backup job ID (auto-generated if omitted)")
	cmd.Flags().StringVar(&includeFlag, "include", "*", "Comma-separated glob patterns; only matching files are backed up")
	cmd.Flags().StringVar(&excludeFlag, "exclude", "", "Comma-separated glob patterns; matching files/directories are skipped")

	cmd.Flags().StringVar(&retentionFile, "retention-file", "", "JSON retention matrix resolved by agent; per-file expire_at is stamped from it (omit to send none)")

	// Parse arguments and flags
	if err := cmd.Execute(); err != nil {
		return nil, err
	}

	// Cobra intercepts -h/--help before running Args validation or Run,
	// returning a nil error after printing help. Detect that case so we
	// don't index into an empty positional args slice below.
	if helpRequested, _ := cmd.Flags().GetBool("help"); helpRequested {
		return nil, errHelpRequested
	}

	// Get the source folder from parsed args
	sourceFolder := cmd.Flags().Args()[0]

	// Validate source folder
	validatedSourceFolder, err := common.ValidatePath(sourceFolder)
	if err != nil {
		return nil, fmt.Errorf("Source directory unavailable: %w", err)
	}

	// Parse destination
	host, port, err := common.ParseDestination(destination, "localhost", conf.DefaultPort)
	if err != nil {
		return nil, fmt.Errorf("invalid destination: %w", err)
	}

	// Validate streams count
	if err := common.ValidateStreamsCount(streams); err != nil {
		return nil, fmt.Errorf("streams error: %w", err)
	}

	if windowFlag < 1 {
		return nil, fmt.Errorf("window error: must be at least 1, got %d", windowFlag)
	}

	return &Arguments{
		SourceFolder:  validatedSourceFolder,
		WriterHost:    host,
		WriterPort:    port,
		Streams:       streams,
		Window:        windowFlag,
		Debug:         debug,
		Quiet:         quiet,
		JobID:         jobIDFlag,
		Include:       splitPatterns(includeFlag),
		Exclude:       splitPatterns(excludeFlag),
		RetentionFile: retentionFile,
	}, nil
}
