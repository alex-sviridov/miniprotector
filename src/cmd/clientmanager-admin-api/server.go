// src/cmd/clientmanager-admin-api/server.go
package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/certmint"
	clientmanagerstore "github.com/alex-sviridov/miniprotector/storage/clientmanager"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// minter mints an enrollment token for hostname/sans using the given
// provisioner credentials. certmint.Mint's own signature already matches
// this exactly, so production code passes it directly with no wrapper;
// tests inject a stub. Mirrors client-manager's own minter type.
type minter func(hostname string, sans []string, opts certmint.Options) (string, error)

type clientManagerAdminServer struct {
	pb.UnimplementedClientManagerAdminServiceServer
	store    *clientmanagerstore.Store
	mint     minter
	mintOpts certmint.Options
	logger   *slog.Logger
}

func NewClientManagerAdminServer(store *clientmanagerstore.Store, mint minter, mintOpts certmint.Options, logger *slog.Logger) *clientManagerAdminServer {
	return &clientManagerAdminServer{store: store, mint: mint, mintOpts: mintOpts, logger: logger}
}

func (s *clientManagerAdminServer) AddClient(ctx context.Context, req *pb.AddClientRequest) (*pb.AddClientResponse, error) {
	hostname := req.GetHostname()
	if hostname == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}

	if _, err := s.store.GetClient(hostname); err == nil {
		return nil, status.Errorf(codes.AlreadyExists, "client %s already enrolled", hostname)
	} else if !errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		s.logger.Error("AddClient: check existing failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "check existing client: %v", err)
	}

	token, err := s.mint(hostname, req.GetSans(), s.mintOpts)
	if err != nil {
		s.logger.Error("AddClient: mint failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "mint token: %v", err)
	}

	if err := s.store.AddClient(hostname, req.GetSans(), time.Now()); err != nil {
		s.logger.Error("AddClient: record failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "record client: %v", err)
	}

	return &pb.AddClientResponse{Token: token}, nil
}

func (s *clientManagerAdminServer) ReEnrollClient(ctx context.Context, req *pb.ReEnrollClientRequest) (*pb.ReEnrollClientResponse, error) {
	hostname := req.GetHostname()
	rec, err := s.store.GetClient(hostname)
	if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
		return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
	}
	if err != nil {
		s.logger.Error("ReEnrollClient: query failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "get client: %v", err)
	}

	sans := req.GetSans()
	if len(sans) == 0 {
		sans = rec.SANsList()
	}

	token, err := s.mint(hostname, sans, s.mintOpts)
	if err != nil {
		s.logger.Error("ReEnrollClient: mint failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "mint token: %v", err)
	}

	return &pb.ReEnrollClientResponse{Token: token}, nil
}

func (s *clientManagerAdminServer) RevokeClient(ctx context.Context, req *pb.RevokeClientRequest) (*pb.Client, error) {
	return s.setRevoked(req.GetHostname(), true)
}

func (s *clientManagerAdminServer) UnrevokeClient(ctx context.Context, req *pb.UnrevokeClientRequest) (*pb.Client, error) {
	return s.setRevoked(req.GetHostname(), false)
}

func (s *clientManagerAdminServer) setRevoked(hostname string, revoked bool) (*pb.Client, error) {
	if err := s.store.SetRevoked(hostname, revoked, time.Now()); err != nil {
		if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
			return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
		}
		s.logger.Error("setRevoked: update failed", "hostname", hostname, "revoked", revoked, "error", err)
		return nil, status.Errorf(codes.Internal, "update revoked: %v", err)
	}
	return s.loadClient(hostname)
}

func (s *clientManagerAdminServer) UpdateDescription(ctx context.Context, req *pb.UpdateClientKVRequest) (*pb.Client, error) {
	return s.updateKV(req, clientmanagerstore.KindDescription)
}

func (s *clientManagerAdminServer) UpdateAttributes(ctx context.Context, req *pb.UpdateClientKVRequest) (*pb.Client, error) {
	return s.updateKV(req, clientmanagerstore.KindAttribute)
}

func (s *clientManagerAdminServer) updateKV(req *pb.UpdateClientKVRequest, kind clientmanagerstore.KVKind) (*pb.Client, error) {
	hostname := req.GetHostname()
	for key, value := range req.GetSet() {
		if err := s.store.SetKV(hostname, kind, key, value); err != nil {
			if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
				return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
			}
			s.logger.Error("updateKV: set failed", "hostname", hostname, "kind", kind, "key", key, "error", err)
			return nil, status.Errorf(codes.Internal, "set %s: %v", kind, err)
		}
	}
	for _, key := range req.GetUnset() {
		if err := s.store.UnsetKV(hostname, kind, key); err != nil {
			if errors.Is(err, clientmanagerstore.ErrClientNotFound) {
				return nil, status.Errorf(codes.NotFound, "client %s not found", hostname)
			}
			s.logger.Error("updateKV: unset failed", "hostname", hostname, "kind", kind, "key", key, "error", err)
			return nil, status.Errorf(codes.Internal, "unset %s: %v", kind, err)
		}
	}
	return s.loadClient(hostname)
}

// loadClient loads hostname's full record for a response, used by every
// RPC below AddClient/ReEnrollClient that returns the updated Client.
func (s *clientManagerAdminServer) loadClient(hostname string) (*pb.Client, error) {
	view, err := s.store.LoadClientView(hostname)
	if err != nil {
		s.logger.Error("loadClient: query failed", "hostname", hostname, "error", err)
		return nil, status.Errorf(codes.Internal, "load client: %v", err)
	}
	return toProtoClient(view), nil
}

// toProtoClient converts a resolved client view into its wire
// representation. Deliberately a local copy of clientmanager-api's
// identical helper -- separate main packages, and storage/clientmanager
// can't import the generated pb package without an import cycle.
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
