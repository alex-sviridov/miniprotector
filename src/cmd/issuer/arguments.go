package main

import (
	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
	Debug        bool
	Hostname     string
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	cmd := &cobra.Command{
		Use:   "issuer",
		Short: "Mint short-lived, attribute-bearing operating certificates for already-enrolled nodes",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&args.CAURL, "ca-url", "https://localhost:9000", "CA URL, e.g. https://localhost:9000")
	cmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")
	cmd.Flags().StringVar(&args.Hostname, "hostname", "", "This issuer instance's own hostname, embedded as the CommonName/SAN of its self-minted server certificate (must match whatever issuer_host other nodes are configured to dial)")
	cmd.Flags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return args, nil
}
