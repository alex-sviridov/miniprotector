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

type BackupStream struct {
	pb.UnimplementedBackupServiceServer
	storagePath    string
	config         *config.Config
	writer         *wfs.Writer
	logger         *slog.Logger
	filesProcessed int
}

func NewBackupStream(ctx context.Context, storagePath string) (*BackupStream, error) {
	logger := logging.GetLoggerFromContext(ctx)
	conf := config.GetConfigFromContext(ctx)

	writer, err := wfs.NewWriter(ctx, storagePath)
	if err != nil {
		return nil, err
	}
	return &BackupStream{
		logger:         logger,
		config:         conf,
		storagePath:    storagePath,
		writer:         writer,
		filesProcessed: 0,
	}, nil
}

// ProcessBackupStream handles the streaming connection
func (s *BackupStream) ProcessBackupStream(stream pb.BackupService_ProcessBackupStreamServer) error {
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
	s.logger = s.logger.With(
		slog.String("client_addr", clientAddr),
		slog.Any("grpc_auth_type", clientAuthType),
	)

	s.logger.Info("New backup stream connected")

	for {
		// Receive a message from client
		req, err := stream.Recv()
		if err == io.EOF {
			s.logger.Info("Client stopped sending",
				"total_files", s.filesProcessed)
			return nil
		}
		if err != nil {
			s.logger.Error("Error receiving", "error", err)
			return err
		}

		if err := s.handleResponse(stream, req); err != nil {
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
	backupStream, err := NewBackupStream(ctx, storagePath)
	if err != nil {
		return err
	}
	defer backupStream.writer.Close()
	pb.RegisterBackupServiceServer(grpcServer, backupStream)

	logger.Info("Server ready, accepting connections")

	return grpcServer.Serve(listener)
}
