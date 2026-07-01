// certrequest mints a one-time enrollment token for a node, run on or near
// the CA host.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/smallstep/certificates/ca"
)

func main() {
	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	passwordBytes, err := os.ReadFile(args.PasswordFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read password file: %v\n", err)
		os.Exit(1)
	}
	password := []byte(strings.TrimSpace(string(passwordBytes)))

	provisioner, err := ca.NewProvisioner(args.Provisioner, "", args.CAURL, password, ca.WithRootFile(args.RootFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load provisioner: %v\n", err)
		os.Exit(1)
	}

	sans := append([]string{args.Hostname}, args.SANs...)
	token, err := provisioner.Token(args.Hostname, sans...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to mint token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(token)
}
