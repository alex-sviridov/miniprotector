package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
)

// genThrowawayCA creates a self-signed CA key/cert pair for this test only
// -- entirely in-memory, no Docker or external CA involved. This test is
// about certrequest serve's own authorization check over a REAL TLS
// handshake, not about the enrollment-token-minting CA (step-ca), which is
// a separate trust domain already exercised by serve_e2e_test.go.
func genThrowawayCA(t *testing.T) (caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caCertPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return cert, key, pemBytes
}

// genLeafCertsDir signs a leaf cert for hostname using caCert/caKey, and
// writes ca.crt/client.crt/client.key into a fresh temp directory matching
// the layout common/mtls expects (see common/mtls/mtls.go).
func genLeafCertsDir(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caCertPEM []byte, hostname string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: hostname},
		DNSNames:     []string{hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	require.NoError(t, err)

	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), caCertPEM, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
	return dir
}

func freeTCPPortRealMTLS(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestMintEnrollmentToken_RealMTLSRejectsWrongHostname proves the
// authorization check holds over a genuine TLS handshake: two leaf certs,
// both validly signed by the same CA (so both pass chain verification),
// but only one matches the configured trusted-caller hostname.
func TestMintEnrollmentToken_RealMTLSRejectsWrongHostname(t *testing.T) {
	caCert, caKey, caCertPEM := genThrowawayCA(t)
	serverDir := genLeafCertsDir(t, caCert, caKey, caCertPEM, "broker-server.test")
	trustedCallerDir := genLeafCertsDir(t, caCert, caKey, caCertPEM, "trusted-caller.test")
	untrustedCallerDir := genLeafCertsDir(t, caCert, caKey, caCertPEM, "attacker.test")

	called := false
	mint := func(hostname string, sans []string) (string, error) {
		called = true
		return "tok-abc", nil
	}
	srv := newBrokerServer("trusted-caller.test", mint)

	port := freeTCPPortRealMTLS(t)
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- connection.StartServer(ctx, testLoggerRealMTLS(), port, serverDir, func(s *grpc.Server) {
			pb.RegisterEnrollmentBrokerServiceServer(s, srv)
		})
	}()
	t.Cleanup(func() {
		cancel()
		<-errCh
	})
	require.Eventually(t, func() bool {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond)
		if err != nil {
			return false
		}
		conn.Close()
		return true
	}, 5*time.Second, 50*time.Millisecond, "server did not start listening")

	// Trusted caller: real handshake, real chain verification, matching hostname -> succeeds.
	goodConn, err := connection.Connect("localhost", port, 5, trustedCallerDir)
	require.NoError(t, err)
	defer goodConn.Close()
	goodClient := pb.NewEnrollmentBrokerServiceClient(goodConn)
	_, err = goodClient.MintEnrollmentToken(context.Background(), &pb.MintEnrollmentTokenRequest{Hostname: "node-x"})
	require.NoError(t, err)
	require.True(t, called, "mint should have been called for the trusted caller")

	// Untrusted caller: real handshake, chain verifies fine (same CA), but
	// wrong hostname -> must be rejected by brokerServer's own auth check,
	// not by TLS itself.
	called = false
	badConn, err := connection.Connect("localhost", port, 5, untrustedCallerDir)
	require.NoError(t, err)
	defer badConn.Close()
	badClient := pb.NewEnrollmentBrokerServiceClient(badConn)
	_, err = badClient.MintEnrollmentToken(context.Background(), &pb.MintEnrollmentTokenRequest{Hostname: "node-x"})
	require.Error(t, err)
	require.False(t, called, "mint must not be called for an untrusted caller even over a real, validly-CA-signed handshake")
}

func testLoggerRealMTLS() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}
