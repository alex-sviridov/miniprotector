package mtls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/alex-sviridov/miniprotector/common/mtls/mtlstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func forwardedCtx(chain [][]byte) context.Context {
	md := metadata.MD{}
	for _, der := range chain {
		md.Append(ForwardedCertHeader, string(der))
	}
	return metadata.NewIncomingContext(context.Background(), md)
}

// runUnary pushes ctx through the ForwardedIdentity unary interceptor and
// returns the context the handler saw.
func runUnary(t *testing.T, certsDir string, ctx context.Context) (context.Context, error) {
	t.Helper()
	unary, _, err := ForwardedIdentity(certsDir)
	require.NoError(t, err)
	var seen context.Context
	_, err = unary(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/x.Y/Z"},
		func(c context.Context, _ any) (any, error) { seen = c; return nil, nil })
	return seen, err
}

func TestForwardedIdentity_ValidChainExposesIdentity(t *testing.T) {
	ca := mtlstest.NewCA(t)
	dir := ca.CertsDir(t, ca.Issue(t, "listener"))
	leaf := ca.Issue(t, "node-a.example", mtlstest.WithAttributes(map[string]string{"authz-role": "store"}))

	ctx, err := runUnary(t, dir, forwardedCtx(leaf.Certificate))
	require.NoError(t, err)

	host, err := PeerHostname(ctx)
	require.NoError(t, err)
	assert.Equal(t, "node-a.example", host)
	attrs, err := PeerAttributes(ctx)
	require.NoError(t, err)
	assert.Equal(t, "store", attrs["authz-role"])
	notAfter, err := PeerNotAfter(ctx)
	require.NoError(t, err)
	assert.False(t, notAfter.IsZero())
}

func TestForwardedIdentity_Rejections(t *testing.T) {
	ca := mtlstest.NewCA(t)
	dir := ca.CertsDir(t, ca.Issue(t, "listener"))
	otherCA := mtlstest.NewCA(t)

	cases := map[string]context.Context{
		"missing header":     metadata.NewIncomingContext(context.Background(), metadata.MD{}),
		"no metadata":        context.Background(),
		"malformed DER":      forwardedCtx([][]byte{[]byte("not a certificate")}),
		"other CA":           forwardedCtx(otherCA.Issue(t, "evil").Certificate),
		"issuer-caller tier": forwardedCtx(ca.Issue(t, "boot", mtlstest.AsIssuerCaller()).Certificate),
		"expired":            forwardedCtx(ca.Issue(t, "old", mtlstest.Expired()).Certificate),
	}
	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := runUnary(t, dir, ctx)
			require.Error(t, err)
			assert.Equal(t, codes.Unauthenticated, status.Code(err))
		})
	}
}

type ctxServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f ctxServerStream) Context() context.Context { return f.ctx }

func TestForwardedIdentity_StreamCarriesIdentityInContext(t *testing.T) {
	ca := mtlstest.NewCA(t)
	dir := ca.CertsDir(t, ca.Issue(t, "listener"))
	leaf := ca.Issue(t, "node-b.example")
	_, stream, err := ForwardedIdentity(dir)
	require.NoError(t, err)

	var host string
	err = stream(nil, ctxServerStream{ctx: forwardedCtx(leaf.Certificate)}, &grpc.StreamServerInfo{},
		func(_ any, ss grpc.ServerStream) error {
			var e error
			host, e = PeerHostname(ss.Context())
			return e
		})
	require.NoError(t, err)
	assert.Equal(t, "node-b.example", host)

	err = stream(nil, ctxServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{},
		func(_ any, _ grpc.ServerStream) error { return nil })
	assert.Equal(t, codes.Unauthenticated, status.Code(err))
}

func TestPeerChainDER_ReturnsPresentedChainLeafFirst(t *testing.T) {
	ca := mtlstest.NewCA(t)
	leaf := ca.Issue(t, "node-c.example")
	parsed, err := x509.ParseCertificate(leaf.Certificate[0])
	require.NoError(t, err)
	ctx := peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{parsed, ca.Cert}}},
	})

	chain, err := PeerChainDER(ctx)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{parsed.Raw, ca.Cert.Raw}, chain)

	_, err = PeerChainDER(context.Background())
	assert.Error(t, err)
}

func TestWithForwardedChain_ReplacesClientSuppliedValuesAndKeepsOthers(t *testing.T) {
	md := metadata.Pairs(ForwardedCertHeader, "forged", "other", "keep")
	out := WithForwardedChain(md, [][]byte{{1, 2}, {3}})

	assert.Equal(t, []string{"\x01\x02", "\x03"}, out.Get(ForwardedCertHeader))
	assert.Equal(t, []string{"keep"}, out.Get("other"))
	assert.Equal(t, []string{"forged"}, md.Get(ForwardedCertHeader), "input must not be mutated")
}

func TestForwardedIdentity_CachesSuccessfulVerificationOnly(t *testing.T) {
	ca := mtlstest.NewCA(t)
	dir := ca.CertsDir(t, ca.Issue(t, "listener"))
	good := ca.Issue(t, "node-a").Certificate
	bad := mtlstest.NewCA(t).Issue(t, "evil").Certificate

	v, err := newForwardedVerifier(dir)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err := v.verify(forwardedValues(good))
		require.NoError(t, err)
	}
	assert.EqualValues(t, 1, v.verifications.Load(), "same chain must be verified once")

	for i := 0; i < 2; i++ {
		_, err := v.verify(forwardedValues(bad))
		require.Error(t, err)
	}
	assert.EqualValues(t, 3, v.verifications.Load(), "a rejected chain is re-verified every time, never cached")
}

func forwardedValues(chain [][]byte) []string {
	out := make([]string, len(chain))
	for i, der := range chain {
		out[i] = string(der)
	}
	return out
}
