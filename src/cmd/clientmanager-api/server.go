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
		client, err := s.toProtoClient(rec)
		if err != nil {
			s.logger.Error("ListClients: load kv failed", "hostname", rec.Hostname, "error", err)
			return nil, status.Errorf(codes.Internal, "list clients: %v", err)
		}
		clients[i] = client
	}
	return &pb.ListClientsResponse{Clients: clients}, nil
}

func (s *clientManagerAPIServer) GetClient(ctx context.Context, req *pb.GetClientRequest) (*pb.Client, error) {
	rec, err := s.store.GetClient(req.GetHostname())
	if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return nil, status.Errorf(codes.NotFound, "client %s not found", req.GetHostname())
	}
	if err != nil {
		s.logger.Error("GetClient: query failed", "hostname", req.GetHostname(), "error", err)
		return nil, status.Errorf(codes.Internal, "get client: %v", err)
	}
	return s.toProtoClient(*rec)
}

func (s *clientManagerAPIServer) toProtoClient(rec clientmanagerstore.ClientRecord) (*pb.Client, error) {
	client := &pb.Client{
		Hostname: rec.Hostname,
		Revoked:  rec.Revoked,
		Sans:     rec.SANsList(),
	}
	if rec.RevokedAt != nil {
		client.RevokedAt = rec.RevokedAt.Unix()
	}
	if rec.LastSeenAt != nil {
		client.LastSeenAt = rec.LastSeenAt.Unix()
	}

	descs, err := s.store.KV(rec.Hostname, clientmanagerstore.KindDescription)
	if err != nil {
		return nil, err
	}
	client.Descriptions = make(map[string]string, len(descs))
	for _, d := range descs {
		client.Descriptions[d.Key] = d.Value
	}

	attrs, err := s.store.KV(rec.Hostname, clientmanagerstore.KindAttribute)
	if err != nil {
		return nil, err
	}
	client.Attributes = make(map[string]string, len(attrs))
	for _, a := range attrs {
		client.Attributes[a.Key] = a.Value
	}

	return client, nil
}
