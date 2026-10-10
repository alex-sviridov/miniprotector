// Package mtlstest mints throwaway CAs and certificates for tests that need
// a real mTLS identity (forwarded identity, StartServiceServer, svcproxy).
// Test-only: import it from _test.go files.
package mtlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	// attributeOID is the attribute extension issuer embeds on operating
	// certificates (mtls.attributeExtensionOID).
	attributeOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}
	// issuerCallerOID marks a bootstrap-tier certificate (mtls.oidEKUIssuerCaller).
	issuerCallerOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 3}
)

// CA is a throwaway certificate authority.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// LeafOption customizes a certificate minted by CA.Issue.
type LeafOption func(*x509.Certificate)

// WithAttributes embeds attrs as the JSON attribute extension.
func WithAttributes(attrs map[string]string) LeafOption {
	return func(c *x509.Certificate) {
		raw, err := json.Marshal(attrs)
		if err != nil {
			panic(err)
		}
		c.ExtraExtensions = append(c.ExtraExtensions, pkix.Extension{Id: attributeOID, Value: raw})
	}
}

// AsIssuerCaller adds the bootstrap-tier EKU.
func AsIssuerCaller() LeafOption {
	return func(c *x509.Certificate) {
		c.UnknownExtKeyUsage = append(c.UnknownExtKeyUsage, issuerCallerOID)
	}
}

// Expired makes the certificate's validity window end an hour ago.
func Expired() LeafOption {
	return func(c *x509.Certificate) {
		c.NotBefore = time.Now().Add(-2 * time.Hour)
		c.NotAfter = time.Now().Add(-time.Hour)
	}
}

func serial(t testing.TB) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	require.NoError(t, err)
	return n
}

// NewCA creates a self-signed CA valid for an hour.
func NewCA(t testing.TB) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &CA{Cert: cert, Key: key}
}

// Issue mints a leaf for hostname (SAN + CN) usable as both client and
// server. The returned chain is [leaf, CA].
func (c *CA) Issue(t testing.TB, hostname string, opts ...LeafOption) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	for _, o := range opts {
		o(tmpl)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.Cert, &key.PublicKey, c.Key)
	require.NoError(t, err)
	return tls.Certificate{Certificate: [][]byte{der, c.Cert.Raw}, PrivateKey: key}
}

// CertsDir writes ca.crt plus identity as client.crt/client.key into a fresh
// temp directory -- the layout every miniprotector binary reads.
func (c *CA) CertsDir(t testing.TB, identity tls.Certificate) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Cert.Raw}), 0o600))
	var chain []byte
	for _, der := range identity.Certificate {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), chain, 0o600))
	keyDER, err := x509.MarshalECPrivateKey(identity.PrivateKey.(*ecdsa.PrivateKey))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return dir
}
