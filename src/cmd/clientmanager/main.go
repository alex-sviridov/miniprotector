// client-manager owns the persistent list of enrolled clients: when they
// were added, their annotations and RBAC attributes, and whether they've
// been revoked. Enrollment itself is delegated to certrequest serve's
// MintEnrollmentToken RPC -- client-manager never holds the CA's
// provisioner password. See docs/components/client-manager.md.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/alex-sviridov/miniprotector/common/config"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

func main() {
	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	varDir, err := config.ResolveVarDir(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Var directory resolution failed: %v\n", err)
		os.Exit(1)
	}
	store, err := clientmanagerstore.New(varDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open client-manager store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	if err := run(conf, certsDir, store, args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
}

// run dispatches on args.Action. Broken out from main so tests can drive
// it directly against a temp-dir store without touching os.Exit.
func run(conf *config.Config, certsDir string, store *clientmanagerstore.Store, args *Arguments, out io.Writer) error {
	switch args.Action {
	case "add":
		return runAdd(conf, certsDir, store, args, mintToken, out)
	case "re-enroll":
		return runReEnroll(conf, certsDir, store, args, mintToken, out)
	case "list":
		return runList(store, out)
	case "show":
		return runShow(store, args, out)
	case "revoke":
		return runRevoke(store, args)
	case "unrevoke":
		return runUnrevoke(store, args)
	case "description-set":
		return runKVSet(store, clientmanagerstore.KindDescription, args)
	case "description-unset":
		return runKVUnset(store, clientmanagerstore.KindDescription, args)
	case "attribute-set":
		return runKVSet(store, clientmanagerstore.KindAttribute, args)
	case "attribute-unset":
		return runKVUnset(store, clientmanagerstore.KindAttribute, args)
	default:
		return fmt.Errorf("unknown action %q", args.Action)
	}
}
