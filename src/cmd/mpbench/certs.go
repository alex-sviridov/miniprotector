package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// attributeExtensionOID is the X.509 extension bwfs reads caller roles from
// (see common/mtls/peer.go).
var attributeExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}

// WriteIdentity writes a throwaway CA and one leaf certificate carrying the
// "client" role into <cfgDir>/certs, in the layout common/mtls loads. The same
// identity serves bwfs, brfs and rwfs.
func WriteIdentity(cfgDir string) error {
	certsDir := filepath.Join(cfgDir, "certs")
	if err := os.MkdirAll(certsDir, 0o755); err != nil {
		return err
	}

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mpbench-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "mpbench-node"},
		DNSNames:     []string{"bwfs.internal", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtraExtensions: []pkix.Extension{{
			Id:    attributeExtensionOID,
			Value: []byte(`{"authz-role":"client"}`),
		}},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return err
	}

	write := func(name, blockType string, der []byte, mode os.FileMode) error {
		return os.WriteFile(filepath.Join(certsDir, name),
			pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), mode)
	}
	if err := write("ca.crt", "CERTIFICATE", caDER, 0o644); err != nil {
		return err
	}
	if err := write("client.crt", "CERTIFICATE", leafDER, 0o644); err != nil {
		return err
	}
	return write("client.key", "EC PRIVATE KEY", keyDER, 0o600)
}

// WriteLocalConf writes the minimal local.conf the three binaries need and
// creates the log directory it points at.
func WriteLocalConf(cfgDir, logDir string, streams int) error {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	conf := fmt.Sprintf("default_port=8080\ndefault_streams=%d\nlog_dir=%s\nFileLockTimeoutSec=5\nConnectionTimeOutSec=30\n",
		streams, logDir)
	return os.WriteFile(filepath.Join(cfgDir, "local.conf"), []byte(conf), 0o644)
}
