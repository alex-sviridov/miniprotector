package connection

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
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
