// src/cmd/api-server/server.go
package main

import (
	"context"
	"log/slog"
	"net/http"
	"time"

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

// clientManagerAdminClient is the subset of pb.ClientManagerAdminServiceClient
// the client-write handlers need -- the full RPC surface, satisfied by the
// real generated client and by a fake in tests.
type clientManagerAdminClient interface {
	AddClient(ctx context.Context, in *pb.AddClientRequest, opts ...grpc.CallOption) (*pb.AddClientResponse, error)
	ReEnrollClient(ctx context.Context, in *pb.ReEnrollClientRequest, opts ...grpc.CallOption) (*pb.ReEnrollClientResponse, error)
	RevokeClient(ctx context.Context, in *pb.RevokeClientRequest, opts ...grpc.CallOption) (*pb.Client, error)
	UnrevokeClient(ctx context.Context, in *pb.UnrevokeClientRequest, opts ...grpc.CallOption) (*pb.Client, error)
	UpdateDescription(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error)
	UpdateAttributes(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error)
	UpdateSANs(ctx context.Context, in *pb.UpdateClientSANsRequest, opts ...grpc.CallOption) (*pb.Client, error)
}

// catalogQueryClient is the subset of pb.CatalogServiceClient the catalog
// handlers (Tasks 5-6) need.
type catalogQueryClient interface {
	ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error)
	ListClientFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
	ListJobFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
	ListDirectoryFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
	ListDirectoryChildren(ctx context.Context, in *pb.ListDirectoryChildrenRequest, opts ...grpc.CallOption) (*pb.ListDirectoryChildrenResponse, error)
}

// policyServiceClient is the subset of pb.PolicyServiceClient the policies
// handlers (Tasks 8-11) need -- api-server never calls GetPolicies, the
// identity-scoped RPC mesh nodes use.
type policyServiceClient interface {
	ListPolicies(ctx context.Context, in *pb.ListPoliciesRequest, opts ...grpc.CallOption) (*pb.ListPoliciesResponse, error)
	CreatePolicy(ctx context.Context, in *pb.CreatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error)
	UpdatePolicy(ctx context.Context, in *pb.UpdatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error)
	DeletePolicy(ctx context.Context, in *pb.DeletePolicyRequest, opts ...grpc.CallOption) (*pb.DeletePolicyResponse, error)
}

type server struct {
	clientManager      clientManagerClient
	clientManagerAdmin clientManagerAdminClient
	catalog            catalogQueryClient
	policy             policyServiceClient
	loki               lokiQuerier
	logger             *slog.Logger
	adhocPolicyTimeout time.Duration
}

func newServer(cm clientManagerClient, catalog catalogQueryClient, policy policyServiceClient, logger *slog.Logger) *server {
	return &server{clientManager: cm, catalog: catalog, policy: policy, logger: logger}
}

// registerRoutes wires up every REST endpoint. handleListClients,
// handleGetClient (Task 9), and handleListCatalog (Task 10) are defined
// in their own files; declaring the routes here means this file compiles
// once those methods exist, without needing placeholder stubs.
func (s *server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/clients", s.handleListClients)
	mux.HandleFunc("GET /api/v1/clients/{hostname}", s.handleGetClient)
	mux.HandleFunc("POST /api/v1/clients", s.handleAddClient)
	mux.HandleFunc("POST /api/v1/clients/{hostname}/reenroll", s.handleReEnrollClient)
	mux.HandleFunc("POST /api/v1/clients/{hostname}/revoke", s.handleRevokeClient)
	mux.HandleFunc("POST /api/v1/clients/{hostname}/unrevoke", s.handleUnrevokeClient)
	mux.HandleFunc("PATCH /api/v1/clients/{hostname}/description", s.handleUpdateDescription)
	mux.HandleFunc("PATCH /api/v1/clients/{hostname}/attributes", s.handleUpdateAttributes)
	mux.HandleFunc("PATCH /api/v1/clients/{hostname}/sans", s.handleUpdateSANs)
	mux.HandleFunc("GET /api/v1/catalog", s.handleListCatalog)
	mux.HandleFunc("GET /api/v1/catalog/clients", s.handleListCatalogClients)
	mux.HandleFunc("GET /api/v1/catalog/jobs", s.handleListCatalogJobs)
	mux.HandleFunc("GET /api/v1/catalog/directories", s.handleListCatalogDirectories)
	mux.HandleFunc("GET /api/v1/catalog/directories/children", s.handleListCatalogDirectoryChildren)
	mux.HandleFunc("GET /api/v1/policies", s.handleListPolicies)
	mux.HandleFunc("GET /api/v1/policies/{id}", s.handleGetPolicy)
	mux.HandleFunc("POST /api/v1/policies", s.handleCreatePolicy)
	mux.HandleFunc("POST /api/v1/policies/adhoc", s.handleCreateAdhocPolicy)
	mux.HandleFunc("PUT /api/v1/policies/{id}", s.handleUpdatePolicy)
	mux.HandleFunc("DELETE /api/v1/policies/{id}", s.handleDeletePolicy)
	mux.HandleFunc("POST /api/v1/storage-policies", s.handleCreateStoragePolicy)
	mux.HandleFunc("PUT /api/v1/storage-policies/{id}", s.handleUpdateStoragePolicy)
	mux.HandleFunc("GET /api/v1/jobs", s.handleListJobs)
	mux.HandleFunc("GET /api/v1/jobs/{job_id}/logs", s.handleGetJobLogs)
}
