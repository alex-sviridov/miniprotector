// src/cmd/api-server/server.go
package main

import (
	"context"
	"log/slog"
	"net/http"

	pb "github.com/alex-sviridov/miniprotector/api"
	"google.golang.org/grpc"
)

// clientManagerClient is the subset of pb.ClientManagerServiceClient the
// clients handlers (Task 9) need -- satisfied by the real generated
// client, and by a fake in tests.
type clientManagerClient interface {
	ListClients(ctx context.Context, in *pb.ListClientsRequest, opts ...grpc.CallOption) (*pb.ListClientsResponse, error)
	GetClient(ctx context.Context, in *pb.GetClientRequest, opts ...grpc.CallOption) (*pb.Client, error)
}

// catalogQueryClient is the subset of pb.CatalogServiceClient the catalog
// handler (Task 10) needs.
type catalogQueryClient interface {
	ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error)
}

type server struct {
	clientManager clientManagerClient
	catalog       catalogQueryClient
	logger        *slog.Logger
}

func newServer(cm clientManagerClient, catalog catalogQueryClient, logger *slog.Logger) *server {
	return &server{clientManager: cm, catalog: catalog, logger: logger}
}

// registerRoutes wires up every REST endpoint. handleListClients,
// handleGetClient (Task 9), and handleListCatalog (Task 10) are defined
// in their own files; declaring the routes here means this file compiles
// once those methods exist, without needing placeholder stubs.
func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clients", s.handleListClients)
	mux.HandleFunc("GET /api/v1/clients/{hostname}", s.handleGetClient)
	mux.HandleFunc("GET /api/v1/catalog", s.handleListCatalog)
}
