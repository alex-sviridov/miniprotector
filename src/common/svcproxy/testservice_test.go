package svcproxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/connection"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/common/mtls/mtlstest"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// echoDesc is a hand-written service covering every RPC shape the proxy must
// carry: unary (Whoami, Echo), server streaming (Count), client streaming
// (Collect) and bidirectional streaming (Bidi). Messages are StringValue.
var echoDesc = grpc.ServiceDesc{
	ServiceName: "test.Echo",
	HandlerType: (*any)(nil),
	Methods: []grpc.MethodDesc{
		{MethodName: "Whoami", Handler: unaryHandler("/test.Echo/Whoami", func(ctx context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			host, err := mtls.PeerHostname(ctx)
			if err != nil {
				return nil, err
			}
			attrs, _ := mtls.PeerAttributes(ctx)
			return wrapperspb.String(host + "|" + attrs["authz-role"]), nil
		})},
		{MethodName: "Echo", Handler: unaryHandler("/test.Echo/Echo", func(_ context.Context, in *wrapperspb.StringValue) (*wrapperspb.StringValue, error) {
			return in, nil
		})},
	},
	Streams: []grpc.StreamDesc{
		{StreamName: "Count", ServerStreams: true, Handler: func(_ any, s grpc.ServerStream) error {
			in := &wrapperspb.StringValue{}
			if err := s.RecvMsg(in); err != nil {
				return err
			}
			for i := 0; i < 3; i++ {
				if err := s.SendMsg(wrapperspb.String(fmt.Sprintf("%s-%d", in.Value, i))); err != nil {
					return err
				}
			}
			return nil
		}},
		{StreamName: "Collect", ClientStreams: true, Handler: func(_ any, s grpc.ServerStream) error {
			var parts []string
			for {
				in := &wrapperspb.StringValue{}
				if err := s.RecvMsg(in); err == io.EOF {
					return s.SendMsg(wrapperspb.String(strings.Join(parts, ",")))
				} else if err != nil {
					return err
				}
				parts = append(parts, in.Value)
			}
		}},
		{StreamName: "Bidi", ClientStreams: true, ServerStreams: true, Handler: func(_ any, s grpc.ServerStream) error {
			for {
				in := &wrapperspb.StringValue{}
				if err := s.RecvMsg(in); err == io.EOF {
					return nil
				} else if err != nil {
					return err
				}
				if err := s.SendMsg(wrapperspb.String("echo:" + in.Value)); err != nil {
					return err
				}
			}
		}},
	},
}

func unaryHandler(fullMethod string, fn func(context.Context, *wrapperspb.StringValue) (*wrapperspb.StringValue, error)) func(any, context.Context, func(any) error, grpc.UnaryServerInterceptor) (any, error) {
	return func(srv any, ctx context.Context, dec func(any) error, interceptor grpc.UnaryServerInterceptor) (any, error) {
		in := &wrapperspb.StringValue{}
		if err := dec(in); err != nil {
			return nil, err
		}
		h := func(ctx context.Context, req any) (any, error) { return fn(ctx, req.(*wrapperspb.StringValue)) }
		if interceptor == nil {
			return h(ctx, in)
		}
		return interceptor(ctx, in, &grpc.UnaryServerInfo{Server: srv, FullMethod: fullMethod}, h)
	}
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func freePort(t testing.TB) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// stack is a running listener (TCP, mTLS) in front of a test.Echo backend
// (Unix socket) sharing one run dir.
type stack struct {
	ca     *mtlstest.CA
	runDir string
	port   int
}

// startStack boots the backend and the listener. roles is the backend's
// per-method role table.
func startStack(t testing.TB, roles map[string][]string) *stack {
	t.Helper()
	runDir, err := os.MkdirTemp("", "mp") // short path: unix sockets cap near 108 bytes
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(runDir) })

	ca := mtlstest.NewCA(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	backendCerts := ca.CertsDir(t, ca.Issue(t, "backend"))
	go connection.StartServiceServer(ctx, discard(), backendCerts, runDir, roles, func(s *grpc.Server) {
		s.RegisterService(&echoDesc, struct{}{})
	})
	sock, err := connection.ServiceSocketPath(runDir, "test.Echo")
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(sock); return err == nil }, 5*time.Second, 20*time.Millisecond)

	p := New(discard(), runDir)
	t.Cleanup(p.Close)
	creds, err := mtls.LoadServerCredentials(ca.CertsDir(t, ca.Issue(t, "listener")))
	require.NoError(t, err)
	port := freePort(t)
	go connection.StartServerWithCredentials(ctx, discard(), port, creds, nil, func(*grpc.Server) {}, p.ServerOptions()...)

	return &stack{ca: ca, runDir: runDir, port: port}
}

// client dials the listener presenting a certificate for hostname carrying attrs.
func (s *stack) client(t testing.TB, hostname string, attrs map[string]string) *grpc.ClientConn {
	t.Helper()
	var opts []mtlstest.LeafOption
	if attrs != nil {
		opts = append(opts, mtlstest.WithAttributes(attrs))
	}
	dir := s.ca.CertsDir(t, s.ca.Issue(t, hostname, opts...))
	var conn *grpc.ClientConn
	require.Eventually(t, func() bool {
		c, err := connection.Connect("localhost", s.port, 2, dir)
		if err != nil {
			return false
		}
		conn = c
		return true
	}, 10*time.Second, 50*time.Millisecond, "listener never became reachable")
	t.Cleanup(func() { conn.Close() })
	return conn
}
