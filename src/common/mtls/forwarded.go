package mtls

import (
	"context"
	"crypto/x509"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// ForwardedCertHeader carries the verified client certificate chain (one
// value per certificate, leaf first, DER) from the listener to a backend
// that has no TLS peer of its own. See
// docs/superpowers/specs/2026-10-10-listener-design.md.
const ForwardedCertHeader = "x-mp-peer-cert-bin"

type forwardedPeerKey struct{}

// PeerChainDER returns the DER chain the TLS peer on ctx presented (and the
// server verified), leaf first. The listener forwards it to backends.
func PeerChainDER(ctx context.Context) ([][]byte, error) {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no peer information in context")
	}
	tlsInfo, ok := p.AuthInfo.(credentials.TLSInfo)
	if !ok {
		return nil, fmt.Errorf("peer connection is not authenticated via TLS")
	}
	certs := tlsInfo.State.PeerCertificates
	if len(certs) == 0 {
		return nil, fmt.Errorf("no peer certificate presented")
	}
	chain := make([][]byte, len(certs))
	for i, c := range certs {
		chain[i] = c.Raw
	}
	return chain, nil
}

// WithForwardedChain returns a copy of md in which ForwardedCertHeader is
// replaced (never appended to) by chain, so a value the original caller
// supplied cannot survive.
func WithForwardedChain(md metadata.MD, chain [][]byte) metadata.MD {
	out := md.Copy()
	out.Delete(ForwardedCertHeader)
	for _, der := range chain {
		out.Append(ForwardedCertHeader, string(der))
	}
	return out
}

// ForwardedIdentity builds the interceptors a Unix-socket backend installs
// ahead of RequireRoles. Each call must carry ForwardedCertHeader; the chain
// is verified against certsDir/ca.crt for client auth (so a forged header from
// a local process still needs a valid certificate), a bootstrap-tier
// certificate is refused, and the leaf is stored in the context where
// peerCert finds it. Anything else is Unauthenticated.
func ForwardedIdentity(certsDir string) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor, error) {
	roots, err := loadCAPool(certsDir)
	if err != nil {
		return nil, nil, err
	}
	resolve := func(ctx context.Context) (context.Context, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		values := md.Get(ForwardedCertHeader)
		if len(values) == 0 {
			return nil, status.Error(codes.Unauthenticated, "missing forwarded peer certificate")
		}
		leaf, err := verifyForwardedChain(values, roots)
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, "forwarded peer certificate rejected: %v", err)
		}
		return context.WithValue(ctx, forwardedPeerKey{}, leaf), nil
	}
	unary := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := resolve(ctx)
		if err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
	stream := func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := resolve(ss.Context())
		if err != nil {
			return err
		}
		return handler(srv, &identityStream{ServerStream: ss, ctx: ctx})
	}
	return unary, stream, nil
}

func verifyForwardedChain(values []string, roots *x509.CertPool) (*x509.Certificate, error) {
	certs := make([]*x509.Certificate, len(values))
	for i, v := range values {
		c, err := x509.ParseCertificate([]byte(v))
		if err != nil {
			return nil, fmt.Errorf("parse certificate %d: %w", i, err)
		}
		certs[i] = c
	}
	intermediates := x509.NewCertPool()
	for _, c := range certs[1:] {
		intermediates.AddCert(c)
	}
	leaf := certs[0]
	if _, err := leaf.Verify(x509.VerifyOptions{
		Roots:         roots,
		Intermediates: intermediates,
		KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		return nil, err
	}
	if hasIssuerCallerEKU(leaf) {
		return nil, fmt.Errorf("bootstrap/issuer-caller credential not accepted")
	}
	return leaf, nil
}

// identityStream overrides a ServerStream's context so handlers and the
// stream interceptors after ForwardedIdentity see the forwarded identity.
type identityStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *identityStream) Context() context.Context { return s.ctx }
