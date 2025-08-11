package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/alex-sviridov/miniprotector/common/wfs"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
)

type BackupServer struct {
	pb.UnimplementedBackupServiceServer
	config *config.Config
	writer *wfs.Writer
	logger *slog.Logger
}

func NewBackupServer(ctx context.Context, storagePath string) (*BackupServer, error) {
	logger := logging.GetLoggerFromContext(ctx)
	conf := config.GetConfigFromContext(ctx)

	writer, err := wfs.NewWriter(ctx, storagePath)
	if err != nil {
		return nil, err
	}
	return &BackupServer{
		logger: logger,
		config: conf,
		writer: writer,
	}, nil
}

// ProcessBackupStream handles the streaming connection
func (server *BackupServer) ProcessBackupStream(stream pb.BackupService_ProcessBackupStreamServer) error {
	streamCtx := stream.Context()

	// Get client connection info ONCE at start
	var clientAddr, clientAuthType string = "unknown", "none"

	if peer, ok := peer.FromContext(streamCtx); ok {
		clientAddr = peer.Addr.String()

		// Add auth info if available
		if peer.AuthInfo != nil {
			clientAuthType = peer.AuthInfo.AuthType()
		}
	}
	streamLogger := server.logger.With(
		slog.String("client_addr", clientAddr),
		slog.Any("grpc_auth_type", clientAuthType),
	)
	streamCtx = context.WithValue(streamCtx, logging.ContextKey, streamLogger)
	streamCtx = context.WithValue(streamCtx, config.ContextKey, server.config)

	streamLogger.Info("New backup stream connected")

	for {
		// Receive a message from client
		request, err := stream.Recv()
		if err == io.EOF {
			streamLogger.Info("Client stopped sending")
			return nil
		}
		if err != nil {
			streamLogger.Error("Error receiving", "error", err)
			return err
		}

		if err := handleRequest(streamCtx, stream, server.writer, request); err != nil {
			return err
		}
	}
}

// startServer creates and starts the gRPC server on the specified port
// Creates and connects BackupServer with storage
// This is a blocking call that serves until an error occurs.
func startServer(ctx context.Context, port int, storagePath string) error {
	logger := logging.GetLoggerFromContext(ctx)
	// Create TCP listener
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	// Create and configure gRPC server and Backup server
	grpcServer := grpc.NewServer()
	backupServer, err := NewBackupServer(ctx, storagePath)
	if err != nil {
		return err
	}
	defer backupServer.writer.Close()
	pb.RegisterBackupServiceServer(grpcServer, backupServer)

	logger.Info("Server ready, accepting connections")

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	return grpcServer.Serve(listener)
}
