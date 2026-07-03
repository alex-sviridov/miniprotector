package main

import "github.com/spf13/cobra"

// Arguments holds parsed command line arguments.
type Arguments struct {
	StoragePath string
	Debug       bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}
	cmd := &cobra.Command{
		Use:   "catalogsync <storage_path>",
		Short: "Replicate a bwfs node's file versions to a backup catalog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.StoragePath = cliArgs[0]
			return nil
		},
	}
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return args, nil
}
