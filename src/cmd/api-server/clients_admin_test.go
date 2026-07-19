// src/cmd/api-server/clients_admin_test.go
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/alex-sviridov/miniprotector/api"
)

// fakeClientManagerAdminClient implements the full clientManagerAdminClient
// interface -- Tasks 8-11 each exercise a different subset of its methods.
type fakeClientManagerAdminClient struct {
	addResp    *pb.AddClientResponse
	addErr     error
	lastAddReq *pb.AddClientRequest

	reEnrollResp    *pb.ReEnrollClientResponse
	reEnrollErr     error
	lastReEnrollReq *pb.ReEnrollClientRequest

	revokeResp *pb.Client
	revokeErr  error

	unrevokeResp *pb.Client
	unrevokeErr  error

	updateDescResp    *pb.Client
	updateDescErr     error
	lastUpdateDescReq *pb.UpdateClientKVRequest

	updateAttrResp    *pb.Client
	updateAttrErr     error
	lastUpdateAttrReq *pb.UpdateClientKVRequest

	updateSANsResp    *pb.Client
	updateSANsErr     error
	lastUpdateSANsReq *pb.UpdateClientSANsRequest
}

func (f *fakeClientManagerAdminClient) AddClient(ctx context.Context, in *pb.AddClientRequest, opts ...grpc.CallOption) (*pb.AddClientResponse, error) {
	f.lastAddReq = in
	return f.addResp, f.addErr
}

func (f *fakeClientManagerAdminClient) ReEnrollClient(ctx context.Context, in *pb.ReEnrollClientRequest, opts ...grpc.CallOption) (*pb.ReEnrollClientResponse, error) {
	f.lastReEnrollReq = in
	return f.reEnrollResp, f.reEnrollErr
}

func (f *fakeClientManagerAdminClient) RevokeClient(ctx context.Context, in *pb.RevokeClientRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	return f.revokeResp, f.revokeErr
}

func (f *fakeClientManagerAdminClient) UnrevokeClient(ctx context.Context, in *pb.UnrevokeClientRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	return f.unrevokeResp, f.unrevokeErr
}

func (f *fakeClientManagerAdminClient) UpdateDescription(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	f.lastUpdateDescReq = in
	return f.updateDescResp, f.updateDescErr
}

func (f *fakeClientManagerAdminClient) UpdateAttributes(ctx context.Context, in *pb.UpdateClientKVRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	f.lastUpdateAttrReq = in
	return f.updateAttrResp, f.updateAttrErr
}

func (f *fakeClientManagerAdminClient) UpdateSANs(ctx context.Context, in *pb.UpdateClientSANsRequest, opts ...grpc.CallOption) (*pb.Client, error) {
	f.lastUpdateSANsReq = in
	return f.updateSANsResp, f.updateSANsErr
}

func newServerWithAdmin(fake clientManagerAdminClient) *server {
	srv := newServer(nil, nil, nil, testLogger())
	srv.clientManagerAdmin = fake
	return srv
}

func TestHandleAddClient_ReturnsTokenAnd201(t *testing.T) {
	fake := &fakeClientManagerAdminClient{addResp: &pb.AddClientResponse{Token: "tok-abc"}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{"hostname":"node-1","sans":["alias.internal"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "node-1", body["hostname"])
	assert.Equal(t, "tok-abc", body["token"])
	assert.Equal(t, "node-1", fake.lastAddReq.GetHostname())
	assert.Equal(t, []string{"alias.internal"}, fake.lastAddReq.GetSans())
}

func TestHandleAddClient_MissingHostnameReturns400(t *testing.T) {
	srv := newServerWithAdmin(&fakeClientManagerAdminClient{})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleAddClient_MalformedJSONReturns400(t *testing.T) {
	srv := newServerWithAdmin(&fakeClientManagerAdminClient{})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{not json`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleAddClient_DuplicateHostnameReturns409(t *testing.T) {
	fake := &fakeClientManagerAdminClient{addErr: status.Error(codes.AlreadyExists, "client node-1 already enrolled")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients", strings.NewReader(`{"hostname":"node-1"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
}

func TestHandleReEnrollClient_ReturnsToken(t *testing.T) {
	fake := &fakeClientManagerAdminClient{reEnrollResp: &pb.ReEnrollClientResponse{Token: "tok-fresh"}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/node-1/reenroll", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "tok-fresh", body["token"])
	assert.Equal(t, "node-1", fake.lastReEnrollReq.GetHostname())
}

func TestHandleReEnrollClient_NoBodyIsAccepted(t *testing.T) {
	fake := &fakeClientManagerAdminClient{reEnrollResp: &pb.ReEnrollClientResponse{Token: "tok-fresh"}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/node-1/reenroll", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleReEnrollClient_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerAdminClient{reEnrollErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/ghost/reenroll", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleRevokeClient_ReturnsUpdatedClient(t *testing.T) {
	fake := &fakeClientManagerAdminClient{revokeResp: &pb.Client{Hostname: "node-1", Revoked: true}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/node-1/revoke", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["revoked"])
}

func TestHandleRevokeClient_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerAdminClient{revokeErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/ghost/revoke", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleUnrevokeClient_ReturnsUpdatedClient(t *testing.T) {
	fake := &fakeClientManagerAdminClient{unrevokeResp: &pb.Client{Hostname: "node-1", Revoked: false}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/clients/node-1/unrevoke", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, false, body["revoked"])
}

func TestHandleUpdateDescription_SendsSetAndUnset(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateDescResp: &pb.Client{Hostname: "node-1", Descriptions: map[string]string{"owner": "alice"}}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/description", strings.NewReader(`{"set":{"owner":"alice"},"unset":["old"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]string{"owner": "alice"}, fake.lastUpdateDescReq.GetSet())
	assert.Equal(t, []string{"old"}, fake.lastUpdateDescReq.GetUnset())
}

func TestHandleUpdateDescription_MalformedJSONReturns400(t *testing.T) {
	srv := newServerWithAdmin(&fakeClientManagerAdminClient{})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/description", strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateDescription_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateDescErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/ghost/description", strings.NewReader(`{"set":{"k":"v"}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleUpdateAttributes_SendsSetAndUnset(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateAttrResp: &pb.Client{Hostname: "node-1", Attributes: map[string]string{"role": "db"}}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/attributes", strings.NewReader(`{"set":{"role":"db"}}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]string{"role": "db"}, fake.lastUpdateAttrReq.GetSet())
}

func TestHandleUpdateSANs_SendsAddAndRemove(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateSANsResp: &pb.Client{Hostname: "node-1", Sans: []string{"new.internal"}}}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/sans", strings.NewReader(`{"add":["new.internal"],"remove":["old.internal"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"new.internal"}, fake.lastUpdateSANsReq.GetAdd())
	assert.Equal(t, []string{"old.internal"}, fake.lastUpdateSANsReq.GetRemove())
}

func TestHandleUpdateSANs_MalformedJSONReturns400(t *testing.T) {
	srv := newServerWithAdmin(&fakeClientManagerAdminClient{})
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/node-1/sans", strings.NewReader(`{bad`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateSANs_UnknownHostnameReturns404(t *testing.T) {
	fake := &fakeClientManagerAdminClient{updateSANsErr: status.Error(codes.NotFound, "client ghost not found")}
	srv := newServerWithAdmin(fake)
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/clients/ghost/sans", strings.NewReader(`{"add":["x.internal"]}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}
