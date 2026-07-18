package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

type fakeClientManagerClient struct {
	listResp *pb.ListClientsResponse
	listErr  error
	getResp  *pb.Client
	getErr   error
}

func (f *fakeClientManagerClient) ListClients(ctx context.Context, in *pb.ListClientsRequest, opts ...grpc.CallOption) (*pb.ListClientsResponse, error) {
	return f.listResp, f.listErr
}

func (f *fakeClientManagerClient) GetClient(ctx context.Context, in *pb.GetClientRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	return f.getResp, f.getErr
}

func TestHandleListClients_ReturnsDataEnvelope(t *testing.T) {
	fake := &fakeClientManagerClient{listResp: &pb.ListClientsResponse{
		Clients: []*pb.Client{{Hostname: "node-1", Sans: []string{"a.internal"}}},
	}}
	srv := newServer(fake, nil, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	assert.Equal(t, "node-1", data[0].(map[string]any)["hostname"])
}

func TestHandleListClients_BackendErrorTranslated(t *testing.T) {
	fake := &fakeClientManagerClient{listErr: status.Error(codes.Unavailable, "down")}
	srv := newServer(fake, nil, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandleGetClient_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerClient{getErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServer(fake, nil, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients/ghost", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleGetClient_ReturnsClientObject(t *testing.T) {
	fake := &fakeClientManagerClient{getResp: &pb.Client{Hostname: "node-1", Revoked: true}}
	srv := newServer(fake, nil, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/clients/node-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "node-1", body["hostname"])
	assert.Equal(t, true, body["revoked"])
}
