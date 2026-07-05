// operatingrefresh.go implements certclient operating-refresh: obtaining a
// fresh, short-lived operating certificate from issuer using the node's
// long-lived bootstrap credential, and writing it to the standard
// client.crt/client.key path every other component already expects.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"google.golang.org/grpc"
)

// issuerClient is the subset of pb.IssuerServiceClient runOperatingRefresh
// needs -- satisfied directly by the real generated client, and by a fake
// in tests, mirroring this package's existing signer/renewer pattern.
type issuerClient interface {
	DescribeSANs(ctx context.Context, in *pb.DescribeSANsRequest, opts ...grpc.CallOption) (*pb.DescribeSANsResponse, error)
	RequestOperatingCert(ctx context.Context, in *pb.RequestOperatingCertRequest, opts ...grpc.CallOption) (*pb.RequestOperatingCertResponse, error)
}

// operatingRefresh is the real, network-dialing entry point main.go calls:
// it authenticates to issuer with the bootstrap credential and delegates
// to runOperatingRefresh.
func operatingRefresh(certsDir, issuerHost string, issuerPort, timeoutSec int, logger *slog.Logger) error {
	conn, err := connection.ConnectWithIdentity(issuerHost, issuerPort, timeoutSec, certsDir, "bootstrap.crt", "bootstrap.key")
	if err != nil {
		return fmt.Errorf("connect to issuer: %w", err)
	}
	defer conn.Close()

	client := pb.NewIssuerServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	return runOperatingRefresh(ctx, certsDir, client, logger)
}

// runOperatingRefresh is the testable core: given an already-connected
// issuerClient, it determines this node's hostname and current SAN list,
// builds a matching CSR against a load-or-generate operating keypair, and
// writes the resulting certificate chain to client.crt.
func runOperatingRefresh(ctx context.Context, certsDir string, client issuerClient, logger *slog.Logger) error {
	hostname, err := hostnameFromBootstrapCert(certsDir)
	if err != nil {
		return fmt.Errorf("determine hostname from bootstrap credential: %w", err)
	}

	logger.Debug("fetching current SAN list", "hostname", hostname)
	sansResp, err := client.DescribeSANs(ctx, &pb.DescribeSANsRequest{})
	if err != nil {
		return fmt.Errorf("describe SANs: %w", err)
	}

	key, err := loadOrGenerateOperatingKey(certsDir)
	if err != nil {
		return fmt.Errorf("load or generate operating key: %w", err)
	}

	csrDER, err := buildOperatingCSR(hostname, sansResp.GetSans(), key)
	if err != nil {
		return fmt.Errorf("build CSR: %w", err)
	}

	logger.Debug("requesting operating certificate", "hostname", hostname, "sans", sansResp.GetSans())
	certResp, err := client.RequestOperatingCert(ctx, &pb.RequestOperatingCertRequest{CsrDer: csrDER})
	if err != nil {
		return fmt.Errorf("request operating cert: %w", err)
	}

	if err := os.WriteFile(filepath.Join(certsDir, "client.crt"), certResp.GetCertChainPem(), 0o644); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}
	logger.Info("operating certificate refreshed", "hostname", hostname)
	return nil
}

// hostnameFromBootstrapCert parses this node's own hostname from its
// bootstrap credential's Subject.CommonName -- safe and coordination-free,
// since hostnames don't change post-enrollment.
func hostnameFromBootstrapCert(certsDir string) (string, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, "bootstrap.crt"),
		filepath.Join(certsDir, "bootstrap.key"),
	)
	if err != nil {
		return "", fmt.Errorf("load bootstrap credential: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("parse bootstrap certificate: %w", err)
	}
	if leaf.Subject.CommonName == "" {
		return "", fmt.Errorf("bootstrap certificate has no CommonName")
	}
	return leaf.Subject.CommonName, nil
}

// loadOrGenerateOperatingKey loads certsDir/client.key if it already
// exists, else generates a fresh ECDSA keypair and persists it. The
// operating credential's keypair is generated once and reused across every
// subsequent refresh -- only the certificate itself is re-obtained each
// cycle.
func loadOrGenerateOperatingKey(certsDir string) (*ecdsa.PrivateKey, error) {
	keyPath := filepath.Join(certsDir, "client.key")

	data, err := os.ReadFile(keyPath)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("parse %s: no PEM block found", keyPath)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", keyPath, err)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", keyPath, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", keyPath, err)
	}
	return key, nil
}

// buildOperatingCSR builds a CSR whose DNSNames exactly match what
// certmint.Mint will authorize: hostname plus sans, in that order
// (certmint.Mint builds its token's SAN claim as
// append([]string{hostname}, sans...) -- confirmed against a real CA by
// this phase's e2e test, cmd/issuer/e2e_test.go's
// TestE2E_MintAndSignEmbedsSANsInCertificate). A CSR omitting hostname
// from DNSNames does NOT satisfy the exact-match validator even though
// hostname is also the CSR's CommonName -- CommonName and DNSNames are
// validated independently.
func buildOperatingCSR(hostname string, sans []string, key *ecdsa.PrivateKey) ([]byte, error) {
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: append([]string{hostname}, sans...),
	}
	return x509.CreateCertificateRequest(rand.Reader, template, key)
}
