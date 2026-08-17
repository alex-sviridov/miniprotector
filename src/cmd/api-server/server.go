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
	ListStoreFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error)
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
	GetNodeCertStatus(ctx context.Context, in *pb.GetNodeCertStatusRequest, opts ...grpc.CallOption) (*pb.NodeCertStatus, error)
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

// registerRoutes wires up every REST endpoint, each individually wrapped
// in requireBearerToken -- unlike main.go's previous single blanket wrap
// around the whole mux, this lets a route opt out (see requireWSTicket,
// ws_tickets.go, used by the WS routes Tasks 6/10 register on this same
// mux) without needing a second top-level handler/mux to compose.
func (s *server) registerRoutes(mux *http.ServeMux, token string) {
	bearer := func(h http.HandlerFunc) http.Handler { return requireBearerToken(token, h) }

	mux.Handle("GET /api/v1/clients", bearer(s.handleListClients))
	mux.Handle("GET /api/v1/clients/{hostname}", bearer(s.handleGetClient))
	mux.Handle("POST /api/v1/clients", bearer(s.handleAddClient))
	mux.Handle("POST /api/v1/clients/{hostname}/reenroll", bearer(s.handleReEnrollClient))
	mux.Handle("POST /api/v1/clients/{hostname}/revoke", bearer(s.handleRevokeClient))
	mux.Handle("POST /api/v1/clients/{hostname}/unrevoke", bearer(s.handleUnrevokeClient))
	mux.Handle("PATCH /api/v1/clients/{hostname}/description", bearer(s.handleUpdateDescription))
	mux.Handle("PATCH /api/v1/clients/{hostname}/attributes", bearer(s.handleUpdateAttributes))
	mux.Handle("PATCH /api/v1/clients/{hostname}/sans", bearer(s.handleUpdateSANs))
	mux.Handle("GET /api/v1/clients/{hostname}/cert-status", bearer(s.handleGetClientCertStatus))
	mux.Handle("GET /api/v1/catalog", bearer(s.handleListCatalog))
	mux.Handle("GET /api/v1/catalog/clients", bearer(s.handleListCatalogClients))
	mux.Handle("GET /api/v1/catalog/jobs", bearer(s.handleListCatalogJobs))
	mux.Handle("GET /api/v1/catalog/directories", bearer(s.handleListCatalogDirectories))
	mux.Handle("GET /api/v1/catalog/stores", bearer(s.handleListCatalogStores))
	mux.Handle("GET /api/v1/catalog/directories/children", bearer(s.handleListCatalogDirectoryChildren))
	mux.Handle("GET /api/v1/policies", bearer(s.handleListPolicies))
	mux.Handle("GET /api/v1/policies/{id}", bearer(s.handleGetPolicy))
	mux.Handle("POST /api/v1/policies", bearer(s.handleCreatePolicy))
	mux.Handle("POST /api/v1/policies/adhoc", bearer(s.handleCreateAdhocPolicy))
	mux.Handle("PUT /api/v1/policies/{id}", bearer(s.handleUpdatePolicy))
	mux.Handle("DELETE /api/v1/policies/{id}", bearer(s.handleDeletePolicy))
	mux.Handle("POST /api/v1/storage-policies", bearer(s.handleCreateStoragePolicy))
	mux.Handle("PUT /api/v1/storage-policies/{id}", bearer(s.handleUpdateStoragePolicy))
	mux.Handle("POST /api/v1/restore", bearer(s.handleCreateRestore))
	mux.Handle("GET /api/v1/jobs", bearer(s.handleListJobs))
	mux.Handle("GET /api/v1/jobs/{job_id}/logs", bearer(s.handleGetJobLogs))
}
