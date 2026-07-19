// src/cmd/clientmanager-api/server.go
package main

import (
	"context"
	"errors"
	"log/slog"

	pb "github.com/alex-sviridov/miniprotector/api"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// clientManagerAPIServer implements ClientManagerService: read-only
// access to the same clientmanager.sqlite file client-manager's CLI and
// issuer already share. Never writes.
type clientManagerAPIServer struct {
	pb.UnimplementedClientManagerServiceServer
	store  *clientmanagerstore.Store
	logger *slog.Logger
}

func NewClientManagerAPIServer(store *clientmanagerstore.Store, logger *slog.Logger) *clientManagerAPIServer {
	return &clientManagerAPIServer{store: store, logger: logger}
}

func (s *clientManagerAPIServer) ListClients(ctx context.Context, _ *pb.ListClientsRequest) (*pb.ListClientsResponse, error) {
	recs, err := s.store.ListClients()
	if err != nil {
		s.logger.Error("ListClients: query failed", "error", err)
		return nil, status.Errorf(codes.Internal, "list clients: %v", err)
	}

	clients := make([]*pb.Client, len(recs))
	for i, rec := range recs {
		view, err := s.store.LoadClientView(rec.Hostname)
		if err != nil {
			s.logger.Error("ListClients: load view failed", "hostname", rec.Hostname, "error", err)
			return nil, status.Errorf(codes.Internal, "list clients: %v", err)
		}
		clients[i] = toProtoClient(view)
	}
	return &pb.ListClientsResponse{Clients: clients}, nil
}

func (s *clientManagerAPIServer) GetClient(ctx context.Context, req *pb.GetClientRequest) (*pb.Client, error) {
	view, err := s.store.LoadClientView(req.GetHostname())
	if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return nil, status.Errorf(codes.NotFound, "client %s not found", req.GetHostname())
	}
	if err != nil {
		s.logger.Error("GetClient: query failed", "hostname", req.GetHostname(), "error", err)
		return nil, status.Errorf(codes.Internal, "get client: %v", err)
	}
	return toProtoClient(view), nil
}

// toProtoClient converts a resolved client view into its wire
// representation. clientmanager-admin-api has its own local copy of this
// same conversion -- storage/clientmanager can't import the generated pb
// package without an import cycle, so each gRPC-facing binary does its
// own trivial field mapping from the shared ClientView.
func toProtoClient(v *clientmanagerstore.ClientView) *pb.Client {
	client := &pb.Client{
		Hostname:     v.Hostname,
		Revoked:      v.Revoked,
		Sans:         v.SANs,
		Descriptions: v.Descriptions,
		Attributes:   v.Attributes,
	}
	if v.RevokedAt != nil {
		client.RevokedAt = v.RevokedAt.Unix()
	}
	if v.LastSeenAt != nil {
		client.LastSeenAt = v.LastSeenAt.Unix()
	}
	return client
}
