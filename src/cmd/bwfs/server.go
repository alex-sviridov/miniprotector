package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"github.com/alex-sviridov/miniprotector/storage"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"

	pb "github.com/alex-sviridov/miniprotector/api"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type backupServer struct {
	pb.UnimplementedBackupServiceServer
	config *config.Config
	store  storage.BackupStore
	logger *slog.Logger
	jobs   *jobTracker
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
		jobs:   newJobTracker(),
	}, nil
}

// jobIDFromMetadata reads the job-id gRPC metadata key that brfs attaches
// when it opens each stream. There is no default: a stream without it is
// rejected rather than silently treated as jobless.
func jobIDFromMetadata(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("no metadata in request")
	}
	values := md.Get("job-id")
	if len(values) == 0 || values[0] == "" {
		return "", fmt.Errorf("missing job-id metadata")
	}
	return values[0], nil
}

func (server *backupServer) ProcessBackupStream(stream pb.BackupService_ProcessBackupStreamServer) error {
	ctx := stream.Context()

	jobID, err := jobIDFromMetadata(ctx)
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "job-id metadata required: %v", err)
	}

	sourceHost, err := mtls.PeerHostname(ctx)
	if err != nil {
		return status.Errorf(codes.Unauthenticated, "resolve peer identity: %v", err)
	}

	if err := server.store.EnsureBackupJob(jobID, sourceHost); err != nil {
		return status.Errorf(codes.Internal, "ensure backup job: %v", err)
	}
	server.jobs.Start(jobID)
	defer func() {
		if server.jobs.Finish(jobID) {
			if err := server.store.FinishBackupJob(jobID); err != nil {
				server.logger.Error("Failed to finish backup job", "job_id", jobID, "error", err)
			}
		}
	}()

	var clientAddr, clientAuthType string = "unknown", "none"
	if peer, ok := peer.FromContext(ctx); ok {
		clientAddr = peer.Addr.String()
		if peer.AuthInfo != nil {
			clientAuthType = peer.AuthInfo.AuthType()
		}
	}

	streamInfo := fmt.Sprintf("%p", stream)
	logger := server.logger.With(
		slog.String("client_addr", clientAddr),
		slog.Any("grpc_auth_type", clientAuthType),
		slog.String("stream_id", streamInfo),
		slog.String("job_id", jobID),
	)
	ctx = context.WithValue(ctx, config.ContextKey, server.config)

	h := newStreamHandler(ctx, logger, server.store, jobID)

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
