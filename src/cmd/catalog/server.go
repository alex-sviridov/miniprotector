package main

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	catalogstore "github.com/alex-sviridov/miniprotector/storage/catalog"
)

type catalogServer struct {
	pb.UnimplementedCatalogServiceServer
	store  *catalogstore.Store
	logger *slog.Logger
}

func NewCatalogServer(store *catalogstore.Store, logger *slog.Logger) *catalogServer {
	return &catalogServer{store: store, logger: logger}
}

func (s *catalogServer) SyncFileVersions(ctx context.Context, req *pb.SyncRequest) (*pb.SyncResponse, error) {
	sourceNode, err := mtls.PeerHostname(ctx)
	if err != nil {
		s.logger.Error("SyncFileVersions: could not determine peer identity", "error", err)
		return nil, err
	}

	entries := req.GetEntries()
	batch := make([]catalogstore.Entry, len(entries))
	for i, e := range entries {
		batch[i] = catalogstore.Entry{
			SourceNode:      sourceNode,
			JobID:           e.GetJobId(),
			ObjectID:        e.GetObjectId(),
			Metadata:        e.GetMetadata(),
			Ctime:           e.GetCtime(),
			SourceSeq:       e.GetSourceSeq(),
			SourceCreatedAt: time.Unix(e.GetCreatedAt(), 0).UTC(),
		}
	}

	if err := s.store.EnsureEntries(batch); err != nil {
		s.logger.Error("SyncFileVersions: persist failed", "error", err, "count", len(batch))
		return nil, err
	}

	s.logger.Info("SyncFileVersions: batch persisted", "source_node", sourceNode, "count", len(batch))
	return &pb.SyncResponse{}, nil
}
