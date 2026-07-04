// Package certmint mints one-time CA enrollment tokens using a
// provisioner's password-protected key. Called directly by client-manager
// -- the only thing in this system that needs CA-admin-equivalent access
// to a provisioner's key.
package certmint

import (
	"fmt"
	"os"
	"strings"

	"github.com/smallstep/certificates/ca"
)

// Options bundles the inputs needed to mint a token for a hostname.
type Options struct {
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
}

// Mint decrypts the named provisioner's key (password-gated, read fresh
// from PasswordFile on every call -- never cached) and mints a one-time
// enrollment token for hostname, with sans as additional SAN aliases.
func Mint(hostname string, sans []string, opts Options) (string, error) {
	passwordBytes, err := os.ReadFile(opts.PasswordFile)
	if err != nil {
		return "", fmt.Errorf("read password file: %w", err)
	}
	password := []byte(strings.TrimSpace(string(passwordBytes)))

	provisioner, err := ca.NewProvisioner(opts.Provisioner, "", opts.CAURL, password, ca.WithRootFile(opts.RootFile))
	if err != nil {
		return "", fmt.Errorf("load provisioner: %w", err)
	}

	allSANs := append([]string{hostname}, sans...)
	token, err := provisioner.Token(hostname, allSANs...)
	if err != nil {
		return "", fmt.Errorf("mint token: %w", err)
	}
	return token, nil
}
