package main

import (
	"context"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	wfs "github.com/alex-sviridov/miniprotector/storage/filesystem"
)

type listServer struct {
	pb.UnimplementedListServiceServer
	store  *wfs.Store
	logger *slog.Logger
}

func NewListServer(store *wfs.Store, logger *slog.Logger) *listServer {
	return &listServer{store: store, logger: logger}
}

func (s *listServer) ListFiles(ctx context.Context, req *pb.ListRequest) (*pb.ListResponse, error) {
	rows, err := queryFileRows(s.store, req.GetServerName(), req.GetPath(), req.GetFilter())
	if err != nil {
		s.logger.Error("ListFiles query failed", "error", err)
		return nil, err
	}

	pbRows := make([]*pb.FileRow, len(rows))
	for i, r := range rows {
		pbRows[i] = &pb.FileRow{
			FileDataId: r.FileDataID,
			Source:     r.Source,
			Type:       r.Type,
			Path:       r.Path,
			Timestamp:  r.Timestamp,
			Size:       r.Size,
			Chunks:     int32(r.Chunks),
			Versions:   r.Versions,
			CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
		}
	}
	return &pb.ListResponse{Rows: pbRows}, nil
}

