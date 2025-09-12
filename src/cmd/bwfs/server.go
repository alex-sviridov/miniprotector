package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/config"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"

	pb "github.com/alex-sviridov/miniprotector/api"

	"google.golang.org/grpc/peer"
)

type backupServer struct {
	pb.UnimplementedBackupServiceServer
	config *config.Config
	store  *wfs.Store
	logger *slog.Logger
}

func NewBackupServer(ctx context.Context, logger *slog.Logger, storagePath string) (*backupServer, error) {
	conf := config.GetConfigFromContext(ctx)

	store, err := wfs.New(storagePath)
	if err != nil {
		return nil, err
	}
	return &backupServer{
		logger: logger,
		config: conf,
		store:  store,
	}, nil
}

// ProcessBackupStream handles the streaming connection
func (server *backupServer) ProcessBackupStream(stream pb.BackupService_ProcessBackupStreamServer) error {
	ctx := stream.Context()

	// Get client connection info at start
	var clientAddr, clientAuthType string = "unknown", "none"

	if peer, ok := peer.FromContext(ctx); ok {
		clientAddr = peer.Addr.String()

		// Add auth info if available
		if peer.AuthInfo != nil {
			clientAuthType = peer.AuthInfo.AuthType()
		}
	}

	// Add gRPC stream context info to logs
	streamInfo := fmt.Sprintf("%p", stream)
	logger := server.logger.With(
		slog.String("client_addr", clientAddr),
		slog.Any("grpc_auth_type", clientAuthType),
		slog.String("stream_id", streamInfo),
	)
	ctx = context.WithValue(ctx, config.ContextKey, server.config)

	h := newStreamHandler(ctx, logger, server.store)

	for {
		request, err := stream.Recv()

		if err == io.EOF {
			h.logger.Info("Client stopped sending")
			return nil
		}
		if err != nil {
			h.logger.Error("Error receiving", "error", err)
			return err
		}
		if request == nil {
			continue
		}

		if err := h.handleRequest(ctx, stream, request); err != nil {
			h.logger.Error("Error handling request", "error", err)
		}
		if h.EOF {
			if err := h.fileWritten(ctx, stream); err != nil {
				h.logger.Error("Error finalizing file", "error", err)
			}
		}
	}
}
