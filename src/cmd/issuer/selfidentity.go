// selfidentity.go: issuer mints its own mTLS server identity directly,
// using the CA provisioner access it already holds for RequestOperatingCert
// -- no enrollment token, no certclient, no dependency on a running issuer
// (it can't call itself). Safe to call repeatedly: each call generates a
// brand-new keypair and certificate; nothing else in the system depends on
// issuer's specific keypair staying stable across restarts or refreshes.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

func mintSelfIdentity(hostname, certsDir, rootFile string, mint mintAndSignFunc, ttlSec int) error {
	rootPEM, err := os.ReadFile(rootFile)
	if err != nil {
		return fmt.Errorf("read CA root %s: %w", rootFile, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: []string{hostname},
	}, key)
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

	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		return fmt.Errorf("create certs dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "ca.crt"), rootPEM, 0o644); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "client.crt"), chainPEM, 0o644); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(certsDir, "client.key"), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write client.key: %w", err)
	}

	return nil
}
