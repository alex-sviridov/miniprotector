package connection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"time"

	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// StartServer creates and starts a gRPC server on the specified port,
// requiring mutual TLS using the certs in certsDir (ca.crt, client.crt,
// client.key). roleRequirements, if non-nil, is wired as a per-method
// role-authorization interceptor via mtls.RequireRoles -- nil means no
// role restriction on any RPC this server registers. The register
// callback receives the bare *grpc.Server so callers can register any
// service (backup, restore, …) without this package importing
// service-specific proto packages. opts are extra grpc.ServerOptions
// (e.g. a stats handler) appended after the defaults.
func StartServer(ctx context.Context, logger *slog.Logger, port int, certsDir string, roleRequirements map[string][]string, register func(*grpc.Server), opts ...grpc.ServerOption) error {
	creds, err := mtls.LoadServerCredentials(certsDir)
	if err != nil {
		return fmt.Errorf("failed to load server credentials: %w", err)
	}
	return StartServerWithCredentials(ctx, logger, port, creds, roleRequirements, register, opts...)
}

// StartServerWithCredentials is StartServer, parameterized on already-built
// transport credentials instead of loading the default certsDir/client.crt
// identity -- used by callers presenting a different credential requirement
// (issuer, which requires bootstrap/issuer-caller peer certs rather than the
// default operating-tier check; see mtls.LoadIssuerServerCredentials).
func StartServerWithCredentials(ctx context.Context, logger *slog.Logger, port int, creds credentials.TransportCredentials, roleRequirements map[string][]string, register func(*grpc.Server), opts ...grpc.ServerOption) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	unaryInterceptor, streamInterceptor := mtls.RequireRoles(roleRequirements)
	grpcServer := grpc.NewServer(append([]grpc.ServerOption{
		grpc.Creds(creds),
		grpc.ChainUnaryInterceptor(unaryInterceptor),
		grpc.ChainStreamInterceptor(streamInterceptor),
	}, append(windowServerOptions(), opts...)...)...)
	register(grpcServer)

	logger.Info("Server ready, accepting connections")

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	return grpcServer.Serve(listener)
}

// StartServiceServer serves a backend that sits behind the listener: instead of
// a TCP port it listens on one Unix socket per gRPC service register adds
// (<runDir>/services/<ServiceName>.sock, mode 0600). There is no TLS here --
// the listener has already verified the client -- so every call must carry the
// certificate chain it forwards (mtls.ForwardedCertHeader); mtls.ForwardedIdentity
// re-verifies that chain against certsDir/ca.crt before roleRequirements are
// checked. It blocks until ctx is cancelled (returning nil) or serving fails.
func StartServiceServer(ctx context.Context, logger *slog.Logger, certsDir, runDir string, roleRequirements map[string][]string, register func(*grpc.Server), opts ...grpc.ServerOption) error {
	idUnary, idStream, err := mtls.ForwardedIdentity(certsDir)
	if err != nil {
		return fmt.Errorf("failed to build forwarded-identity check: %w", err)
	}
	roleUnary, roleStream := mtls.RequireRoles(roleRequirements)
	grpcServer := grpc.NewServer(append([]grpc.ServerOption{
		grpc.ChainUnaryInterceptor(idUnary, roleUnary),
		grpc.ChainStreamInterceptor(idStream, roleStream),
	}, append(windowServerOptions(), opts...)...)...)
	register(grpcServer)

	names := make([]string, 0)
	for name := range grpcServer.GetServiceInfo() {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return fmt.Errorf("no gRPC services registered")
	}

	if err := os.MkdirAll(ServicesDir(runDir), 0o700); err != nil {
		return fmt.Errorf("create services dir: %w", err)
	}
	var listeners []net.Listener
	closeAll := func() {
		for _, l := range listeners {
			l.Close() // UnixListener unlinks its socket on Close
		}
	}
	for _, name := range names {
		path, err := ServiceSocketPath(runDir, name)
		if err != nil {
			closeAll()
			return err
		}
		if err := removeStaleSocket(path); err != nil {
			closeAll()
			return fmt.Errorf("service %s: %w", name, err)
		}
		l, err := net.Listen("unix", path)
		if err != nil {
			closeAll()
			return fmt.Errorf("listen on %s: %w", path, err)
		}
		listeners = append(listeners, l)
		if err := os.Chmod(path, 0o600); err != nil {
			closeAll()
			return fmt.Errorf("chmod %s: %w", path, err)
		}
		logger.Info("Service socket ready", "service", name, "socket", path)
	}

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	errCh := make(chan error, len(listeners))
	for _, l := range listeners {
		go func() { errCh <- grpcServer.Serve(l) }()
	}
	var first error
	for range listeners {
		if err := <-errCh; err != nil && first == nil {
			first = err
		}
	}
	closeAll()
	return first
}

// removeStaleSocket deletes a socket file left behind by a crashed process,
// but refuses if something is still accepting on it -- a second instance of
// the same service must not steal the first one's socket.
func removeStaleSocket(path string) error {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if c, err := net.DialTimeout("unix", path, 200*time.Millisecond); err == nil {
		c.Close()
		return fmt.Errorf("already running (socket %s is live)", path)
	}
	return os.Remove(path)
}
