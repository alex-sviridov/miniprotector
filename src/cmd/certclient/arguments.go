package main

import "github.com/spf13/cobra"

// Arguments holds parsed command line arguments.
type Arguments struct {
	Token string
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}
	cmd := &cobra.Command{
		Use:   "certclient",
		Short: "Bootstrap or renew this node's mTLS identity from the CA",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) {},
	}
	cmd.Flags().StringVar(&args.Token, "token", "",
		"Enrollment token for first-time bootstrap (prefer MP_CERT_TOKEN or the stdin prompt over this flag on shared hosts)")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return args, nil
}
