// certclient bootstraps or renews this node's mTLS identity from the CA.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/smallstep/certificates/ca"
)

func main() {
	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

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
	if conf.CAHost == "" {
		fmt.Fprintln(os.Stderr, "Configuration error: ca_host not set in local.conf")
		os.Exit(1)
	}

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	if hasExistingIdentity(certsDir) {
		client, err := ca.NewClient(fmt.Sprintf("https://%s", conf.CAHost), ca.WithRootFile(filepath.Join(certsDir, "ca.crt")))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create CA client: %v\n", err)
			os.Exit(1)
		}
		if err := renew(client, certsDir); err != nil {
			fmt.Fprintf(os.Stderr, "Renew failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Identity renewed in", certsDir)
		return
	}

	tok, err := resolveToken(args.Token, os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Token error: %v\n", err)
		os.Exit(1)
	}

	client, err := ca.Bootstrap(tok)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	if err := bootstrap(tok, client, certsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Identity bootstrapped in", certsDir)
}
