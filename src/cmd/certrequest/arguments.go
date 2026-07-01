package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	Hostname     string
	SANs         []string
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}
	var caURLFlag, defaultsFile string

	cmd := &cobra.Command{
		Use:   "certrequest <hostname>",
		Short: "Mint a one-time enrollment token for a node",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, cliArgs []string) {
			args.Hostname = cliArgs[0]
		},
	}
	cmd.Flags().StringArrayVar(&args.SANs, "san", nil, "Additional SAN alias for the token (repeatable)")
	cmd.Flags().StringVar(&caURLFlag, "ca-url", "", "CA URL, e.g. https://localhost:9000 (default: read from --defaults-file)")
	cmd.Flags().StringVar(&defaultsFile, "defaults-file", "ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	cmd.Flags().StringVar(&args.RootFile, "root", "ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "ca/data/secrets/password", "Path to the provisioner password file")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	if args.Hostname == "" {
		return nil, fmt.Errorf("hostname is required")
	}

	args.CAURL = caURLFlag
	if args.CAURL == "" {
		defaultURL, err := readDefaultCAURL(defaultsFile)
		if err != nil {
			return nil, fmt.Errorf("--ca-url not given and could not be read from %s: %w", defaultsFile, err)
		}
		args.CAURL = defaultURL
	}

	return args, nil
}

// readDefaultCAURL reads the "ca-url" field out of step-ca's defaults.json.
func readDefaultCAURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var defaults struct {
		CAURL string `json:"ca-url"`
	}
	if err := json.Unmarshal(data, &defaults); err != nil {
		return "", err
	}
	if defaults.CAURL == "" {
		return "", fmt.Errorf("%s has no ca-url field", path)
	}
	return defaults.CAURL, nil
}
