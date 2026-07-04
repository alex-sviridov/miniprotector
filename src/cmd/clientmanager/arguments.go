package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments for whichever subcommand
// was invoked; Action tells the caller which fields are populated.
type Arguments struct {
	Action   string // "add" | "re-enroll" (more added in later tasks)
	Hostname string
	SANs     []string // "key=value" strings, for description/attribute set (Task 9)
	KVPairs  []string // "key=value" strings, for description/attribute set (Task 9)
	Key      string   // for description/attribute unset (Task 9)
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

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

	rootCmd.AddCommand(addCmd, reEnrollCmd, listCmd, showCmd, revokeCmd, unrevokeCmd, descriptionCmd, attributeCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: add, re-enroll, list, show, revoke, unrevoke, description, attribute")
	}
	return args, nil
}
