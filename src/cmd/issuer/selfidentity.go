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

	"github.com/alex-sviridov/miniprotector/common/atomicfile"
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

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// atomicfile.Write's own MkdirAll uses 0o755; create certsDir with the
	// tighter 0o700 first (it holds client.key) -- MkdirAll is a no-op on an
	// already-existing directory, so this mode wins.
	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		return fmt.Errorf("create certs dir: %w", err)
	}
	if err := atomicfile.Write(filepath.Join(certsDir, "ca.crt"), rootPEM); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}

	// mtls.go's GetCertificate reloads client.crt/client.key from disk on
	// every TLS handshake, not just once at startup, so the two must never
	// be left mismatched -- a stale cert paired with a fresh key (or vice
	// versa) fails every subsequent handshake until the next scheduled
	// refresh or a restart. Writing them as two independent os.WriteFile
	// calls (even atomic ones) doesn't fix this: whichever file commits
	// second is still exposed if the write in between fails. Stage both
	// file's data into temp files first -- any failure here (disk full,
	// permission error, process killed) never touches a live file -- then
	// commit with two adjacent renames, shrinking the risk window from "the
	// entire duration of writing both files" to the gap between two
	// metadata-only syscalls.
	if err := commitClientIdentity(certsDir, chainPEM, keyPEM); err != nil {
		return fmt.Errorf("commit client identity: %w", err)
	}

	return nil
}

func commitClientIdentity(certsDir string, chainPEM, keyPEM []byte) error {
	crtPath := filepath.Join(certsDir, "client.crt")
	keyPath := filepath.Join(certsDir, "client.key")
	crtTmp := crtPath + ".tmp"
	keyTmp := keyPath + ".tmp"

	if err := os.WriteFile(crtTmp, chainPEM, 0o644); err != nil {
		return fmt.Errorf("stage client.crt: %w", err)
	}
	if err := os.WriteFile(keyTmp, keyPEM, 0o600); err != nil {
		os.Remove(crtTmp)
		return fmt.Errorf("stage client.key: %w", err)
	}
	if err := os.Rename(keyTmp, keyPath); err != nil {
		os.Remove(crtTmp)
		os.Remove(keyTmp)
		return fmt.Errorf("commit client.key: %w", err)
	}
	if err := os.Rename(crtTmp, crtPath); err != nil {
		return fmt.Errorf("commit client.crt: %w", err)
	}
	return nil
}
