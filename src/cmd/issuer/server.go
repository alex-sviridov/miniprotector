package main

import (
	"context"
	"crypto/x509"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
)

// mintAndSignFunc mints a token for hostname/sans and signs csr against the
// CA, embedding attributes via the sign request's TemplateData, returning
// the full PEM-encoded certificate chain. Production wires this to
// mintAndSign (mintsign.go); tests inject a stub so this file's unit tests
// never touch a real CA.
type mintAndSignFunc func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error)

// issuerServer implements IssuerService: the sole RPC an already-
// bootstrapped node calls to obtain a fresh operating certificate. The
// caller's identity is always the verified mTLS peer hostname -- never a
// request field -- looked up against the client-manager database this
// binary shares. A revoked hostname is refused outright, regardless of
// whether its bootstrap credential is otherwise perfectly valid.
type issuerServer struct {
	pb.UnimplementedIssuerServiceServer
	store    *clientmanagerstore.Store
	mintSign mintAndSignFunc
	logger   *slog.Logger
}

func newIssuerServer(store *clientmanagerstore.Store, mintSign mintAndSignFunc, logger *slog.Logger) *issuerServer {
	return &issuerServer{store: store, mintSign: mintSign, logger: logger}
}

func (s *issuerServer) RequestOperatingCert(ctx context.Context, req *pb.RequestOperatingCertRequest) (*pb.RequestOperatingCertResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, fmt.Errorf("determine caller identity: %w", err)
	}

	client, err := s.store.GetClient(hostname)
	if err != nil {
		return nil, fmt.Errorf("hostname %s not tracked: %w", hostname, err)
	}
	if client.Revoked {
		return nil, fmt.Errorf("hostname %s is revoked", hostname)
	}

	attrRecords, err := s.store.KV(hostname, clientmanagerstore.KindAttribute)
	if err != nil {
		return nil, fmt.Errorf("load attributes for %s: %w", hostname, err)
	}
	attributes := make(map[string]string, len(attrRecords))
	for _, a := range attrRecords {
		attributes[a.Key] = a.Value
	}

	csr, err := x509.ParseCertificateRequest(req.GetCsrDer())
	if err != nil {
		return nil, fmt.Errorf("parse csr: %w", err)
	}

	chainPEM, err := s.mintSign(hostname, client.SANsList(), attributes, csr)
	if err != nil {
		return nil, fmt.Errorf("issue certificate for %s: %w", hostname, err)
	}

	if err := s.store.UpdateLastSeen(hostname, time.Now()); err != nil {
		s.logger.Error("failed to update last_seen", "hostname", hostname, "error", err)
	}

	return &pb.RequestOperatingCertResponse{CertChainPem: chainPEM}, nil
}

// DescribeSANs returns the caller's own current SAN alias list, read live
// from the same database RequestOperatingCert consults -- the only
// unauthenticated-adjacent-looking read in this service, but it reveals
// nothing the caller isn't already entitled to know about itself, and it
// mints/signs nothing. No revoked check: a revoked host's SANs are still
// readable; only issuance (RequestOperatingCert) is refused.
func (s *issuerServer) DescribeSANs(ctx context.Context, _ *pb.DescribeSANsRequest) (*pb.DescribeSANsResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, fmt.Errorf("determine caller identity: %w", err)
	}

	client, err := s.store.GetClient(hostname)
	if err != nil {
		return nil, fmt.Errorf("hostname %s not tracked: %w", hostname, err)
	}

	return &pb.DescribeSANsResponse{Sans: client.SANsList()}, nil
}
