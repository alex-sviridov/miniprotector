package mtls

import (
	"context"
	"fmt"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// PeerHostname extracts the verified hostname identity from the client
// certificate presented on ctx's gRPC peer connection: the first SAN entry,
// falling back to the Subject CommonName if no SAN is present. certrequest
// always places the primary hostname first in a cert's SAN list, so this
// reflects the CA-verified node identity rather than anything the caller
// could self-report over the wire.
func PeerHostname(ctx context.Context) (string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return "", fmt.Errorf("no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return "", fmt.Errorf("peer connection is not authenticated via TLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return "", fmt.Errorf("no peer certificate presented")
	}
	cert := tlsInfo.State.PeerCertificates[0]
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0], nil
	}
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName, nil
	}
	return "", fmt.Errorf("peer certificate has no SAN or CommonName")
}
