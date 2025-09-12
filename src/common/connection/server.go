package connection

import (
	"context"
	"fmt"
	"net"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
)

// StartServer creates and starts the gRPC server on the specified port
// Requires BackupServiceServer interface as it will serve new connections 
// via ProcessBackupStream function of this interface.
// This is a blocking call that serves until an error occurs.
func StartServer(ctx context.Context, logger *slog.Logger, port int, srv pb.BackupServiceServer) error {
	// Create TCP listener
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	// Create and configure gRPC server and Backup server
	grpcServer := grpc.NewServer()
	pb.RegisterBackupServiceServer(grpcServer, srv)

	logger.Info("Server ready, accepting connections")

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	return grpcServer.Serve(listener)
}
