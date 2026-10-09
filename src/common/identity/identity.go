// Package identity holds the small pieces every node-side certificate flow
// shares: a persistent ECDSA key and the CSR built from it. certclient (for
// nodes) and issuer (for its own server identity) both use it, so the key is
// generated once and only the certificate is replaced on each refresh -- a
// key and certificate are never swapped as a pair.
package identity

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common/atomicfile"
)

// LoadOrCreateKey loads the EC private key at path, or generates a P-256 key
// and persists it (mode 0600, atomically) if the file does not exist.
func LoadOrCreateKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("parse %s: no PEM block found", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	if err := WriteKey(path, key); err != nil {
		return nil, err
	}
	return key, nil
}

// WriteKey persists key at path as PEM, mode 0600, atomically.
func WriteKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := atomicfile.WriteMode(path, pemBytes, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// NewCSR builds a DER CSR for hostname: CommonName and a single DNS SAN, both
// hostname. Any further SANs are decided by the CA side (issuer), never the
// node.
func NewCSR(hostname string, key *ecdsa.PrivateKey) ([]byte, error) {
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: []string{hostname},
	}, key)
}
