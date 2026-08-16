// Package main implements certclient, which bootstraps or renews this
// node's mTLS identity from the CA.
package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/smallstep/certificates/api"
	"github.com/smallstep/certificates/ca"
)

// signer is satisfied by *ca.Client. Isolating it lets bootstrap be unit
// tested without a live CA connection.
type signer interface {
	Sign(req *api.SignRequest) (*api.SignResponse, error)
}

// bootstrap exchanges an enrollment token for a signed identity via client,
// writing ca.crt, client.crt, and client.key into certsDir.
func bootstrap(token string, client signer, certsDir string, ttlSec int) error {
	req, pk, err := ca.CreateSignRequest(token)
	if err != nil {
		return fmt.Errorf("create sign request: %w", err)
	}
	req.NotAfter = api.NewTimeDuration(time.Now().Add(time.Duration(ttlSec) * time.Second))

	templateData, err := json.Marshal(struct {
		Tier string `json:"tier"`
	}{Tier: "bootstrap"})
	if err != nil {
		return fmt.Errorf("marshal template data: %w", err)
	}
	req.TemplateData = templateData

	sign, err := client.Sign(req)
	if err != nil {
		return fmt.Errorf("sign request: %w", err)
	}

	return writeIdentity(certsDir, sign, pk)
}

// writeIdentity writes the root, leaf+intermediate chain, and private key
// from a sign response to certsDir. Pure and independently testable — no
// network calls.
func writeIdentity(certsDir string, sign *api.SignResponse, pk crypto.PrivateKey) error {
	root, err := ca.RootCertificate(sign)
	if err != nil {
		return fmt.Errorf("extract root certificate: %w", err)
	}
	leaf, err := ca.Certificate(sign)
	if err != nil {
		return fmt.Errorf("extract leaf certificate: %w", err)
	}
	intermediate, err := ca.IntermediateCertificate(sign)
	if err != nil {
		return fmt.Errorf("extract intermediate certificate: %w", err)
	}
	ecdsaKey, ok := pk.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("unexpected private key type %T", pk)
	}
	keyDER, err := x509.MarshalECPrivateKey(ecdsaKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		return fmt.Errorf("create certs dir: %w", err)
	}

	chain := append(pemCert(leaf), pemCert(intermediate)...)
	if err := os.WriteFile(filepath.Join(certsDir, "bootstrap.crt"), chain, 0o644); err != nil {
		return fmt.Errorf("write bootstrap.crt: %w", err)
	}

	// ca.crt must include the intermediate, not just the root: it's loaded
	// as every server's ClientCAs trust pool (see common/mtls.loadCAPool),
	// and a peer whose TLS library sends only its leaf certificate (no
	// self-assembled chain -- true of at least Vector's loki sink) can only
	// be verified if the pool contains an entry the leaf chains to
	// directly. Root-only left every such peer unverifiable with "x509:
	// certificate signed by unknown authority", never caught before
	// because every other component here happens to present its full
	// leaf+intermediate chain itself.
	caPEM := append(pemCert(intermediate), pemCert(root)...)
	if err := os.WriteFile(filepath.Join(certsDir, "ca.crt"), caPEM, 0o644); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(certsDir, "bootstrap.key"), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write bootstrap.key: %w", err)
	}

	return nil
}

// pemCert PEM-encodes an x509 certificate. Shared by bootstrap.go and
// renew.go to avoid duplicating the pem.Block boilerplate.
func pemCert(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}
