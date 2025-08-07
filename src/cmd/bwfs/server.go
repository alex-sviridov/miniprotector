package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
)

type BackupStream struct {
	config         *config.Config
	logger         *slog.Logger
	filesProcessed int
}

// ProcessBackupStream handles the streaming connection
func (s *BackupServer) ProcessBackupStream(stream pb.BackupService_ProcessBackupStreamServer) error {
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

	// Create logger with connections info
	bs := &BackupStream{
		config:         s.config,
		logger:         s.logger,
		filesProcessed: 0,
	}
	bs.logger = s.logger.With(
		slog.String("client_addr", clientAddr),
		slog.Any("grpc_auth_type", clientAuthType),
	)

	bs.logger.Info("New backup stream connected")

	var clientStreamID int32 = -1

	for {
		// Receive a message from client
		req, err := stream.Recv()
		if err == io.EOF {
			s.logger.Info("Client stopped sending",
				"total_files", bs.filesProcessed)
			return nil
		}
		if err != nil {
			s.logger.Error("Error receiving", "error", err)
			return err
		}

		// Set stream ID from first message and update logger ONCE
		if clientStreamID == -1 {
			clientStreamID = req.StreamId
			bs.logger = bs.logger.With(slog.Int("client_stream_id", int(clientStreamID)))
		}

		if err := bs.handleResponse(stream, req); err != nil {
			return err
		}
	}
}

// startServer creates and starts the gRPC server on the specified port
// Creates and connects BackupServer with storage
// This is a blocking call that serves until an error occurs.
func startServer(ctx context.Context, port int, storagePath string) error {
	logger := logging.GetLoggerFromContext(ctx)
	conf := config.GetConfigFromContext(ctx)

	// Create TCP listener
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	// Create and configure gRPC server and Backup server
	grpcServer := grpc.NewServer()
	backupServer, err := NewBackupServer(conf, logger, storagePath)
	if err != nil {
		return err
	}
	pb.RegisterBackupServiceServer(grpcServer, backupServer)

	logger.Info("Server ready, accepting connections")

	return grpcServer.Serve(listener)
}
