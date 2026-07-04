package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments for whichever subcommand
// was invoked; Action tells the caller which fields are populated.
type Arguments struct {
	Action   string // "add" | "re-enroll" | "list" | "show" | "revoke" | "unrevoke" |
	                 // "description-set" | "description-unset" | "attribute-set" | "attribute-unset" |
	                 // "san-add" | "san-remove"
	Hostname string
	SANs     []string // Additional SAN aliases for add/re-enroll
	KVPairs  []string // "key=value" strings, for description/attribute set
	Key      string   // for description/attribute unset
	SanAlias string   // for san add/remove

	// Provisioner credentials for add/re-enroll -- client-manager holds
	// the CA's provisioner password directly, replacing certrequest.
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}
	var caURLFlag, defaultsFile string

	rootCmd := &cobra.Command{
		Use:   "client-manager <command>",
		Short: "Manage enrolled clients: list, annotate, revoke",
	}

	addCmd := &cobra.Command{
		Use:   "add <hostname>",
		Short: "Enroll a new client and record it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "add"
			args.Hostname = cliArgs[0]
			return nil
		},
	}
	addCmd.Flags().StringArrayVar(&args.SANs, "san", nil, "Additional SAN alias for the token (repeatable)")
	addCmd.Flags().StringVar(&caURLFlag, "ca-url", "", "CA URL, e.g. https://localhost:9000 (default: read from --defaults-file)")
	addCmd.Flags().StringVar(&defaultsFile, "defaults-file", "deploy/control-plane/ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	addCmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	addCmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	addCmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")

	reEnrollCmd := &cobra.Command{
		Use:   "re-enroll <hostname>",
		Short: "Mint a fresh token for an already-tracked client",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "re-enroll"
			args.Hostname = cliArgs[0]
			return nil
		},
	}
	reEnrollCmd.Flags().StringArrayVar(&args.SANs, "san", nil, "Additional SAN alias for the fresh token (repeatable; overrides the stored SANs from add-time if given)")
	reEnrollCmd.Flags().StringVar(&caURLFlag, "ca-url", "", "CA URL, e.g. https://localhost:9000 (default: read from --defaults-file)")
	reEnrollCmd.Flags().StringVar(&defaultsFile, "defaults-file", "deploy/control-plane/ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	reEnrollCmd.Flags().StringVar(&args.RootFile, "root", "deploy/control-plane/ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	reEnrollCmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	reEnrollCmd.Flags().StringVar(&args.PasswordFile, "password-file", "deploy/control-plane/ca/data/secrets/password", "Path to the provisioner password file")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all tracked clients",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			args.Action = "list"
			return nil
		},
	}

	showCmd := &cobra.Command{
		Use:   "show <hostname>",
		Short: "Show a client's full detail",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "show"
			args.Hostname = cliArgs[0]
			return nil
		},
	}

	revokeCmd := &cobra.Command{
		Use:   "revoke <hostname>",
		Short: "Mark a client revoked (does not yet block renewal -- see phase 2)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "revoke"
			args.Hostname = cliArgs[0]
			return nil
		},
	}

	unrevokeCmd := &cobra.Command{
		Use:   "unrevoke <hostname>",
		Short: "Clear a client's revoked flag",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, cliArgs []string) error {
			args.Action = "unrevoke"
			args.Hostname = cliArgs[0]
			return nil
		},
	}

	descriptionCmd := &cobra.Command{Use: "description", Short: "Manage a client's human-facing annotations"}
	descriptionCmd.AddCommand(
		&cobra.Command{
			Use:   "set <hostname> key=value [key=value...]",
			Short: "Set one or more description key/value pairs",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "description-set"
				args.Hostname = cliArgs[0]
				args.KVPairs = cliArgs[1:]
				return nil
			},
		},
		&cobra.Command{
			Use:   "unset <hostname> <key>",
			Short: "Remove a description key",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "description-unset"
				args.Hostname = cliArgs[0]
				args.Key = cliArgs[1]
				return nil
			},
		},
	)

	attributeCmd := &cobra.Command{Use: "attribute", Short: "Manage a client's RBAC attributes (baked into future certificates)"}
	attributeCmd.AddCommand(
		&cobra.Command{
			Use:   "set <hostname> key=value [key=value...]",
			Short: "Set one or more attribute key/value pairs",
			Args:  cobra.MinimumNArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "attribute-set"
				args.Hostname = cliArgs[0]
				args.KVPairs = cliArgs[1:]
				return nil
			},
		},
		&cobra.Command{
			Use:   "unset <hostname> <key>",
			Short: "Remove an attribute key",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "attribute-unset"
				args.Hostname = cliArgs[0]
				args.Key = cliArgs[1]
				return nil
			},
		},
	)

	sanCmd := &cobra.Command{Use: "san", Short: "Manage a client's SAN aliases"}
	sanCmd.AddCommand(
		&cobra.Command{
			Use:   "add <hostname> <alias>",
			Short: "Add a SAN alias (applied on the client's next credential refresh)",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "san-add"
				args.Hostname = cliArgs[0]
				args.SanAlias = cliArgs[1]
				return nil
			},
		},
		&cobra.Command{
			Use:   "remove <hostname> <alias>",
			Short: "Remove a SAN alias (applied on the client's next credential refresh)",
			Args:  cobra.ExactArgs(2),
			RunE: func(cmd *cobra.Command, cliArgs []string) error {
				args.Action = "san-remove"
				args.Hostname = cliArgs[0]
				args.SanAlias = cliArgs[1]
				return nil
			},
		},
	)

	rootCmd.AddCommand(addCmd, reEnrollCmd, listCmd, showCmd, revokeCmd, unrevokeCmd, descriptionCmd, attributeCmd, sanCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}

	if args.Action == "add" || args.Action == "re-enroll" {
		args.CAURL = caURLFlag
		if args.CAURL == "" {
			defaultURL, err := readDefaultCAURL(defaultsFile)
			if err != nil {
				return nil, fmt.Errorf("--ca-url not given and could not be read from %s: %w", defaultsFile, err)
			}
			args.CAURL = defaultURL
		}
	}

	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: add, re-enroll, list, show, revoke, unrevoke, description, attribute, san")
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
