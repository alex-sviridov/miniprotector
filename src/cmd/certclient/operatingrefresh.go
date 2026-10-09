// operatingrefresh.go implements certclient operating-refresh: obtaining a
// fresh, short-lived operating certificate from issuer using the node's
// long-lived bootstrap credential, and writing it to the standard
// client.crt/client.key path every other component already expects.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/atomicfile"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/identity"
	"github.com/alex-sviridov/miniprotector/common/jobid"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
)

// issuerClient is the subset of pb.IssuerServiceClient runOperatingRefresh
// needs -- satisfied directly by the real generated client, and by a fake
// in tests, mirroring this package's existing signer/renewer pattern.
type issuerClient interface {
	RequestOperatingCert(ctx context.Context, in *pb.RequestOperatingCertRequest, opts ...grpc.CallOption) (*pb.RequestOperatingCertResponse, error)
}

// operatingRefresh is the real, network-dialing entry point main.go calls:
// it authenticates to issuer with the bootstrap credential and delegates
// to runOperatingRefresh. jobID rides the RPC as outgoing job-id metadata,
// so issuer's own log for this exact refresh attempt is correlatable back
// to this process's local log.
func operatingRefresh(certsDir, issuerHost string, issuerPort, timeoutSec int, jobID string, logger *slog.Logger) error {
	conn, err := connection.ConnectWithIdentity(issuerHost, issuerPort, timeoutSec, certsDir, "bootstrap.crt", "bootstrap.key")
	if err != nil {
		return fmt.Errorf("connect to issuer: %w", err)
	}
	defer conn.Close()

	client := pb.NewIssuerServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	ctx = jobid.Outgoing(ctx, jobID)

	return runOperatingRefresh(ctx, certsDir, client, logger)
}

// runOperatingRefresh is the testable core: given an already-connected
// issuerClient, it determines this node's hostname, builds a CSR for just that
// hostname against a load-or-generate operating keypair, and writes the
// resulting certificate chain to client.crt. Any SAN aliases are decided by
// issuer, not the node.
func runOperatingRefresh(ctx context.Context, certsDir string, client issuerClient, logger *slog.Logger) error {
	hostname, err := hostnameFromBootstrapCert(certsDir)
	if err != nil {
		return fmt.Errorf("determine hostname from bootstrap credential: %w", err)
	}

	key, err := identity.LoadOrCreateKey(filepath.Join(certsDir, "client.key"))
	if err != nil {
		return fmt.Errorf("load or generate operating key: %w", err)
	}
	csrDER, err := identity.NewCSR(hostname, key)
	if err != nil {
		return fmt.Errorf("build CSR: %w", err)
	}

	logger.Debug("requesting operating certificate", "hostname", hostname)
	certResp, err := client.RequestOperatingCert(ctx, &pb.RequestOperatingCertRequest{CsrDer: csrDER})
	if err != nil {
		return fmt.Errorf("request operating cert: %w", err)
	}

	if err := atomicfile.Write(filepath.Join(certsDir, "client.crt"), certResp.GetCertChainPem()); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}
	logger.Info("operating certificate refreshed", "hostname", hostname)
	return nil
}

// hostnameFromBootstrapCert returns this node's own hostname from its
// bootstrap credential, by the same rule servers use to identify a peer
// (mtls.HostnameFromCert) -- safe and coordination-free, since hostnames
// don't change post-enrollment.
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
	return mtls.HostnameFromCert(leaf)
}
