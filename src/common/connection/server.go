package connection

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// StartServer creates and starts a gRPC server on the specified port,
// requiring mutual TLS using the certs in certsDir (ca.crt, client.crt,
// client.key). The register callback receives the bare *grpc.Server so
// callers can register any service (backup, restore, …) without this
// package importing service-specific proto packages.
func StartServer(ctx context.Context, logger *slog.Logger, port int, certsDir string, register func(*grpc.Server)) error {
	creds, err := mtls.LoadServerCredentials(certsDir)
	if err != nil {
		return fmt.Errorf("failed to load server credentials: %w", err)
	}
	return StartServerWithCredentials(ctx, logger, port, creds, register)
}

// StartServerWithCredentials is StartServer, parameterized on already-built
// transport credentials instead of loading the default certsDir/client.crt
// identity -- used by callers presenting a different credential requirement
// (issuer, which requires bootstrap/issuer-caller peer certs rather than the
// default operating-tier check; see mtls.LoadIssuerServerCredentials).
func StartServerWithCredentials(ctx context.Context, logger *slog.Logger, port int, creds credentials.TransportCredentials, register func(*grpc.Server)) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	grpcServer := grpc.NewServer(grpc.Creds(creds))
	register(grpcServer)

	logger.Info("Server ready, accepting connections")

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	return grpcServer.Serve(listener)
}
