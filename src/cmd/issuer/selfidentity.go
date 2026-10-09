// selfidentity.go: issuer mints its own mTLS server identity directly,
// using the CA provisioner access it already holds for RequestOperatingCert
// -- no enrollment token, no certclient, no dependency on a running issuer
// (it can't call itself). Safe to call repeatedly: the key is generated once
// and reused, so each call only replaces client.crt, atomically; key and
// certificate are never swapped as a pair.
package main

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/atomicfile"
	"github.com/alex-sviridov/miniprotector/common/identity"
)

func mintSelfIdentity(hostname, certsDir, rootFile string, mint mintAndSignFunc) error {
	rootPEM, err := os.ReadFile(rootFile)
	if err != nil {
		return fmt.Errorf("read CA root %s: %w", rootFile, err)
	}

	// certsDir holds client.key; create it with the tighter 0o700 first.
	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		return fmt.Errorf("create certs dir: %w", err)
	}
	key, err := identity.LoadOrCreateKey(filepath.Join(certsDir, "client.key"))
	if err != nil {
		return fmt.Errorf("load or create key: %w", err)
	}
	csrDER, err := identity.NewCSR(hostname, key)
	if err != nil {
		return fmt.Errorf("build CSR: %w", err)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return fmt.Errorf("parse CSR: %w", err)
	}

	chainPEM, err := mint(hostname, nil, nil, csr)
	if err != nil {
		return fmt.Errorf("mint and sign self identity: %w", err)
	}

	if err := atomicfile.Write(filepath.Join(certsDir, "ca.crt"), rootPEM); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}
	if err := atomicfile.Write(filepath.Join(certsDir, "client.crt"), chainPEM); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}
	return nil
}
