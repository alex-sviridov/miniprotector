// Package mtls loads mutual-TLS credentials for miniprotector's gRPC
// transport. Every node (bwfs, brfs, rwfs) presents the same identity cert
// regardless of its client/server role: ca.crt, client.crt, client.key in a
// single directory.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc/credentials"
)

const (
	caCertFile    = "ca.crt"
	identCertFile = "client.crt"
	identKeyFile  = "client.key"
)

func loadCertAndPool(certsDir string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, identCertFile),
		filepath.Join(certsDir, identKeyFile),
	)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load identity cert/key from %s: %w", certsDir, err)
	}

	caPEM, err := os.ReadFile(filepath.Join(certsDir, caCertFile))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read CA cert from %s: %w", certsDir, err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("parse CA cert from %s: no valid certificates found", certsDir)
	}

	return cert, caPool, nil
}

func serverTLSConfig(certsDir string) (*tls.Config, error) {
	cert, caPool, err := loadCertAndPool(certsDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

// isLoopbackHost reports whether host is a loopback address/name where
// hostname verification against a cert's SAN would be an artificial
// provisioning burden (anything reachable via loopback is already running on
// the same trusted machine).
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func verifyChainOnly(caPool *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("no certificate presented by peer")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parse peer certificate: %w", err)
		}
		intermediates := x509.NewCertPool()
		for _, raw := range rawCerts[1:] {
			c, err := x509.ParseCertificate(raw)
			if err != nil {
				return fmt.Errorf("parse peer intermediate certificate: %w", err)
			}
			intermediates.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: caPool, Intermediates: intermediates}); err != nil {
			return fmt.Errorf("verify peer certificate chain: %w", err)
		}
		return nil
	}
}

func clientTLSConfig(certsDir, host string) (*tls.Config, error) {
	cert, caPool, err := loadCertAndPool(certsDir)
	if err != nil {
		return nil, err
	}

	if isLoopbackHost(host) {
		return &tls.Config{
			Certificates:          []tls.Certificate{cert},
			InsecureSkipVerify:    true, // hostname check disabled; chain is still verified below
			VerifyPeerCertificate: verifyChainOnly(caPool),
		}, nil
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		ServerName:   host,
	}, nil
}

// LoadServerCredentials builds gRPC transport credentials for a server that
// requires and verifies every client's certificate against certsDir/ca.crt.
// Any client cert signed by that CA is trusted; there is no CN/SAN allowlist.
func LoadServerCredentials(certsDir string) (credentials.TransportCredentials, error) {
	cfg, err := serverTLSConfig(certsDir)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

// LoadClientCredentials builds gRPC transport credentials for dialing host.
// Hostname/SAN verification is skipped for loopback hosts (localhost,
// 127.0.0.0/8, ::1); every other host must match a SAN on the server's
// presented certificate.
func LoadClientCredentials(certsDir, host string) (credentials.TransportCredentials, error) {
	cfg, err := clientTLSConfig(certsDir, host)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}
