package mtls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/json"
	"fmt"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// hostnameFromCert extracts the verified hostname identity from cert: the
// first SAN entry, falling back to the Subject CommonName if no SAN is
// present. Shared by PeerHostname (gRPC) and PeerHostnameFromConnState
// (plain net/http, e.g. log-gateway) so both transports apply the exact
// same identity rule.
func hostnameFromCert(cert *x509.Certificate) (string, error) {
	if len(cert.DNSNames) > 0 {
		return cert.DNSNames[0], nil
	}
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName, nil
	}
	return "", fmt.Errorf("peer certificate has no SAN or CommonName")
}

// PeerHostname extracts the verified hostname identity from the client
// certificate presented on ctx's gRPC peer connection: the first SAN entry,
// falling back to the Subject CommonName if no SAN is present. client-manager
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
	return hostnameFromCert(tlsInfo.State.PeerCertificates[0])
}

// PeerHostnameFromConnState is PeerHostname's plain-HTTP equivalent, for a
// server (like log-gateway) that terminates TLS via net/http.Server
// directly rather than gRPC. Same identity rule as PeerHostname: the first
// SAN entry, falling back to Subject CommonName. state is typically an
// *http.Request's own TLS field.
func PeerHostnameFromConnState(state *tls.ConnectionState) (string, error) {
	if state == nil || len(state.PeerCertificates) == 0 {
		return "", fmt.Errorf("no peer certificate presented")
	}
	return hostnameFromCert(state.PeerCertificates[0])
}

// attributeExtensionOID identifies the custom X.509 extension issuer embeds
// on every operating certificate it mints, carrying the hostname's current
// attribute key/value pairs (deploy/control-plane/ca/templates/leaf.tpl).
// Non-critical; present only when the hostname has at least one attribute
// set.
var attributeExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}

// PeerAttributes extracts and JSON-decodes the attribute extension from the
// client certificate presented on ctx's gRPC peer connection, as embedded by
// issuer (see attributeExtensionOID). Returns an empty, non-nil map -- not
// an error -- when the peer certificate carries no such extension, since
// that's the normal case for a hostname with no attributes set.
func PeerAttributes(ctx context.Context) (map[string]string, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, fmt.Errorf("peer connection is not authenticated via TLS")
	}
	if len(tlsInfo.State.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no peer certificate presented")
	}
	cert := tlsInfo.State.PeerCertificates[0]
	for _, ext := range cert.Extensions {
		if !ext.Id.Equal(attributeExtensionOID) {
			continue
		}
		attrs := make(map[string]string)
		if err := json.Unmarshal(ext.Value, &attrs); err != nil {
			return nil, fmt.Errorf("parse attribute extension: %w", err)
		}
		return attrs, nil
	}
	return map[string]string{}, nil
}
