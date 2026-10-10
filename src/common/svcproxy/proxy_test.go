package svcproxy

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/common/mtls/mtlstest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func ctx5(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestServiceFromMethod(t *testing.T) {
	got, err := ServiceFromMethod("/catalogservice.CatalogService/SyncFileVersions")
	require.NoError(t, err)
	assert.Equal(t, "catalogservice.CatalogService", got)

	for _, bad := range []string{"", "x", "/", "/svc", "/svc/", "/../x/Y", "/a..b/Y", "/a b/Y", "no-slash/Y"} {
		_, err := ServiceFromMethod(bad)
		assert.Error(t, err, "%q", bad)
	}
}

func TestProxy_UnaryCarriesVerifiedIdentity(t *testing.T) {
	s := startStack(t, nil)
	conn := s.client(t, "node-a.example", map[string]string{"authz-role": "store"})

	var out wrapperspb.StringValue
	require.NoError(t, conn.Invoke(ctx5(t), "/test.Echo/Whoami", wrapperspb.String("x"), &out))
	assert.Equal(t, "node-a.example|store", out.Value)
}

func TestProxy_BackendRoleDenialPassesThrough(t *testing.T) {
	s := startStack(t, map[string][]string{"/test.Echo/Whoami": {"store"}})

	var out wrapperspb.StringValue
	ok := s.client(t, "store-node", map[string]string{"authz-role": "store"})
	require.NoError(t, ok.Invoke(ctx5(t), "/test.Echo/Whoami", wrapperspb.String("x"), &out))

	denied := s.client(t, "client-node", map[string]string{"authz-role": "client"})
	err := denied.Invoke(ctx5(t), "/test.Echo/Whoami", wrapperspb.String("x"), &out)
	assert.Equal(t, codes.PermissionDenied, status.Code(err))
}

// withRole is a mtlstest option minting a certificate with the given authz-role.
func withRole(role string) mtlstest.LeafOption {
	return mtlstest.WithAttributes(map[string]string{"authz-role": role})
}

func TestProxy_ClientSuppliedForwardedHeaderIsOverwritten(t *testing.T) {
	s := startStack(t, map[string][]string{"/test.Echo/Whoami": {"store"}})
	// A "client"-role caller tries to pass itself off as a "store" node.
	forged := s.ca.Issue(t, "forged-store", withRole("store"))
	conn := s.client(t, "client-node", map[string]string{"authz-role": "client"})

	ctx := metadata.AppendToOutgoingContext(ctx5(t), mtls.ForwardedCertHeader, string(forged.Certificate[0]))
	var out wrapperspb.StringValue
	err := conn.Invoke(ctx, "/test.Echo/Whoami", wrapperspb.String("x"), &out)
	assert.Equal(t, codes.PermissionDenied, status.Code(err), "forged header must not grant the store role")
}

func TestProxy_Streaming(t *testing.T) {
	s := startStack(t, nil)
	conn := s.client(t, "node-a", nil)

	t.Run("server streaming", func(t *testing.T) {
		st, err := conn.NewStream(ctx5(t), &grpc.StreamDesc{ServerStreams: true}, "/test.Echo/Count")
		require.NoError(t, err)
		require.NoError(t, st.SendMsg(wrapperspb.String("n")))
		require.NoError(t, st.CloseSend())
		for i, want := range []string{"n-0", "n-1", "n-2"} {
			var out wrapperspb.StringValue
			require.NoError(t, st.RecvMsg(&out), i)
			assert.Equal(t, want, out.Value)
		}
		assert.Equal(t, io.EOF, st.RecvMsg(&wrapperspb.StringValue{}))
	})

	t.Run("client streaming", func(t *testing.T) {
		st, err := conn.NewStream(ctx5(t), &grpc.StreamDesc{ClientStreams: true}, "/test.Echo/Collect")
		require.NoError(t, err)
		for _, v := range []string{"a", "b", "c"} {
			require.NoError(t, st.SendMsg(wrapperspb.String(v)))
		}
		require.NoError(t, st.CloseSend())
		var out wrapperspb.StringValue
		require.NoError(t, st.RecvMsg(&out))
		assert.Equal(t, "a,b,c", out.Value)
	})

	t.Run("bidirectional", func(t *testing.T) {
		st, err := conn.NewStream(ctx5(t), &grpc.StreamDesc{ClientStreams: true, ServerStreams: true}, "/test.Echo/Bidi")
		require.NoError(t, err)
		for _, v := range []string{"x", "y"} {
			require.NoError(t, st.SendMsg(wrapperspb.String(v)))
			var out wrapperspb.StringValue
			require.NoError(t, st.RecvMsg(&out))
			assert.Equal(t, "echo:"+v, out.Value)
		}
		require.NoError(t, st.CloseSend())
		assert.Equal(t, io.EOF, st.RecvMsg(&wrapperspb.StringValue{}))
	})
}

func TestProxy_UnknownServiceIsUnimplemented(t *testing.T) {
	s := startStack(t, nil)
	conn := s.client(t, "node-a", nil)

	err := conn.Invoke(ctx5(t), "/test.Missing/X", wrapperspb.String("x"), &wrapperspb.StringValue{})
	assert.Equal(t, codes.Unimplemented, status.Code(err))
	assert.Contains(t, status.Convert(err).Message(), "not served on this host")
}

func TestProxy_StaleSocketIsUnavailable(t *testing.T) {
	s := startStack(t, nil)
	conn := s.client(t, "node-a", nil)

	sock, err := connection.ServiceSocketPath(s.runDir, "test.Stale")
	require.NoError(t, err)
	ln, err := net.Listen("unix", sock)
	require.NoError(t, err)
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	require.NoError(t, ln.Close())
	_, statErr := os.Stat(sock)
	require.NoError(t, statErr)

	err = conn.Invoke(ctx5(t), "/test.Stale/X", wrapperspb.String("x"), &wrapperspb.StringValue{})
	assert.Equal(t, codes.Unavailable, status.Code(err))
}
