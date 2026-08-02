package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type fakePolicyServiceClient struct {
	listResp    *pb.ListPoliciesResponse
	listErr     error
	lastListReq *pb.ListPoliciesRequest

	createResp    *pb.Policy
	createErr     error
	lastCreateReq *pb.CreatePolicyRequest

	updateResp    *pb.Policy
	updateErr     error
	lastUpdateReq *pb.UpdatePolicyRequest

	deleteResp    *pb.DeletePolicyResponse
	deleteErr     error
	lastDeleteReq *pb.DeletePolicyRequest
}

func (f *fakePolicyServiceClient) ListPolicies(ctx context.Context, in *pb.ListPoliciesRequest, opts ...grpc.CallOption) (*pb.ListPoliciesResponse, error) {
	f.lastListReq = in
	return f.listResp, f.listErr
}

func (f *fakePolicyServiceClient) CreatePolicy(ctx context.Context, in *pb.CreatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error) {
	f.lastCreateReq = in
	return f.createResp, f.createErr
}

func (f *fakePolicyServiceClient) UpdatePolicy(ctx context.Context, in *pb.UpdatePolicyRequest, opts ...grpc.CallOption) (*pb.Policy, error) {
	f.lastUpdateReq = in
	return f.updateResp, f.updateErr
}

func (f *fakePolicyServiceClient) DeletePolicy(ctx context.Context, in *pb.DeletePolicyRequest, opts ...grpc.CallOption) (*pb.DeletePolicyResponse, error) {
	f.lastDeleteReq = in
	return f.deleteResp, f.deleteErr
}

func TestHandleListPolicies_ReturnsDataEnvelope(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{
		Policies: []*pb.Policy{{Id: "p1", Name: "nightly", Destination: "bwfs:8080"}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	assert.Equal(t, "nightly", data[0].(map[string]any)["name"])
}

func TestHandleListPolicies_BackendErrorTranslated(t *testing.T) {
	fake := &fakePolicyServiceClient{listErr: status.Error(codes.Unavailable, "down")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestHandleListPolicies_PassesTypeQueryParamThrough(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies?type=storage", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastListReq)
	assert.Equal(t, "storage", fake.lastListReq.GetType())
}

func TestHandleListPolicies_NoTypeParamSendsEmptyType(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastListReq)
	assert.Equal(t, "", fake.lastListReq.GetType())
}

func TestHandleGetPolicy_ReturnsMatchingPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{
		Policies: []*pb.Policy{
			{Id: "p1", Name: "nightly"},
			{Id: "p2", Name: "weekly"},
		},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies/p2", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "weekly", body["name"])
}

func TestHandleGetPolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{listResp: &pb.ListPoliciesResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/policies/ghost", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestToPolicyDTO_ConvertsTimestampsToUnixSecondsAndClientFilters(t *testing.T) {
	p := &pb.Policy{
		Id:            "p1",
		Name:          "nightly",
		CreatedAt:     timestamppb.New(time.Unix(1752400000, 0)),
		UpdatedAt:     timestamppb.New(time.Unix(1752400010, 0)),
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"web-*"}, Labels: map[string]string{"env": "prod"}},
		ObjectFilters: []*pb.ObjectFilter{{Id: "f1", Path: "/data", Include: []string{"*.sql"}}},
		Rpo:           "24h",
		BackupWindow:  []string{"0 2 * * *"},
		Destination:   "bwfs:8080",
		Type:          "backup",
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, int64(1752400000), dto.CreatedAt)
	assert.Equal(t, int64(1752400010), dto.UpdatedAt)
	assert.Equal(t, []string{"web-*"}, dto.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, dto.ClientFilters.Labels)
	require.Len(t, dto.ObjectFilters, 1)
	assert.Equal(t, "f1", dto.ObjectFilters[0].ID)
	assert.Equal(t, "/data", dto.ObjectFilters[0].Path)
	assert.Equal(t, "backup", dto.Type)
}

func TestToPolicyDTO_IncludesStorageFields(t *testing.T) {
	p := &pb.Policy{
		Id:     "s1",
		Name:   "east-1-storage",
		Type:   "storage",
		Port:   9400,
		Config: `{"backend": "filesystem", "root": "/data/storage"}`,
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, int32(9400), dto.Port)
	assert.Equal(t, `{"backend": "filesystem", "root": "/data/storage"}`, dto.Config)
}

func TestHandleCreatePolicy_ReturnsCreatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1", Name: "nightly", Destination: "bwfs:8080"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "nightly", fake.lastCreateReq.GetName())
	assert.Equal(t, "backup", fake.lastCreateReq.GetType())
	assert.Equal(t, "bwfs:8080", fake.lastCreateReq.GetDestination())
}

func TestHandleCreatePolicy_PassesClientAndObjectFiltersThrough(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "web",
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www", "include": ["*.html"]}]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, []string{"web-*"}, fake.lastCreateReq.GetClientFilters().GetHostnames())
	require.Len(t, fake.lastCreateReq.GetObjectFilters(), 1)
	assert.Equal(t, "/var/www", fake.lastCreateReq.GetObjectFilters()[0].GetPath())
}

func TestHandleCreatePolicy_MalformedJSONReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastCreateReq, "backend must not be called on malformed input")
}

func TestHandleCreatePolicy_BackendValidationErrorReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{createErr: status.Error(codes.InvalidArgument, "metadata.name is required")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdatePolicy_ReturnsUpdatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{Id: "p1", Name: "nightly-renamed"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly-renamed", "destination": "bwfs:9090"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/p1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	assert.Equal(t, "p1", fake.lastUpdateReq.GetId())
	assert.Equal(t, "nightly-renamed", fake.lastUpdateReq.GetName())
	assert.Equal(t, "bwfs:9090", fake.lastUpdateReq.GetDestination())
}

func TestHandleUpdatePolicy_MalformedJSONReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/p1", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastUpdateReq)
}

func TestHandleUpdatePolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{updateErr: status.Error(codes.NotFound, "policy \"ghost\" not found")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/ghost", strings.NewReader(`{"name": "x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleDeletePolicy_ReturnsNoContent(t *testing.T) {
	fake := &fakePolicyServiceClient{deleteResp: &pb.DeletePolicyResponse{}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/policies/p1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.NotNil(t, fake.lastDeleteReq)
	assert.Equal(t, "p1", fake.lastDeleteReq.GetId())
}

func TestHandleDeletePolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{deleteErr: status.Error(codes.NotFound, "policy \"p1\" not found")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/policies/p1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestHandleCreateStoragePolicy_ReturnsCreatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{
		Id: "s1", Name: "east-1-storage", Type: "storage",
		Port: 9400, Config: `{"backend": "filesystem"}`,
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"storage-east-1.internal"}, Labels: map[string]string{}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "east-1-storage",
		"client_filters": {"hostnames": ["storage-east-1.internal"], "labels": {}},
		"port": 9400,
		"config": "{\"backend\": \"filesystem\"}"
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Equal(t, "storage", fake.lastCreateReq.GetType())
	assert.Equal(t, "east-1-storage", fake.lastCreateReq.GetName())
	assert.Equal(t, []string{"storage-east-1.internal"}, fake.lastCreateReq.GetClientFilters().GetHostnames())
	assert.Equal(t, int32(9400), fake.lastCreateReq.GetPort())
	assert.Equal(t, `{"backend": "filesystem"}`, fake.lastCreateReq.GetConfig())

	var respBody map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &respBody))
	assert.Equal(t, []any{"storage-east-1.internal"}, respBody["client_filters"].(map[string]any)["hostnames"])
}

func TestHandleCreateStoragePolicy_MalformedJSONReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-policies", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Nil(t, fake.lastCreateReq, "backend must not be called on malformed input")
}

func TestHandleCreateStoragePolicy_BackendValidationErrorReturns400(t *testing.T) {
	fake := &fakePolicyServiceClient{createErr: status.Error(codes.InvalidArgument, "port must be between 1 and 65535, got 0")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/storage-policies", strings.NewReader(`{"name": "x"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleUpdateStoragePolicy_ReturnsUpdatedPolicy(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{
		Id: "s1", Name: "east-1-storage-renamed", Type: "storage",
		Port: 9401, Config: `{"backend": "filesystem"}`,
		ClientFilters: &pb.ClientFilters{Hostnames: []string{"storage-east-2.internal"}, Labels: map[string]string{}},
	}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "east-1-storage-renamed",
		"client_filters": {"hostnames": ["storage-east-2.internal"], "labels": {}},
		"port": 9401,
		"config": "{\"backend\": \"filesystem\"}"
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage-policies/s1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	assert.Equal(t, "s1", fake.lastUpdateReq.GetId())
	assert.Equal(t, []string{"storage-east-2.internal"}, fake.lastUpdateReq.GetClientFilters().GetHostnames())
	assert.Equal(t, int32(9401), fake.lastUpdateReq.GetPort())
}

func TestHandleUpdateStoragePolicy_UnknownIDReturns404(t *testing.T) {
	fake := &fakePolicyServiceClient{updateErr: status.Error(codes.NotFound, "policy \"ghost\" not found")}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage-policies/ghost", strings.NewReader(`{"name": "x", "port": 1, "config": "{}"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestToPolicyDTO_IncludesDisabledAtWhenSet(t *testing.T) {
	p := &pb.Policy{
		Id:         "p1",
		Name:       "adhoc-x",
		DisabledAt: timestamppb.New(time.Unix(1754000000, 0)),
	}

	dto := toPolicyDTO(p)

	assert.Equal(t, int64(1754000000), dto.DisabledAt)
}

func TestToPolicyDTO_OmitsDisabledAtWhenUnset(t *testing.T) {
	p := &pb.Policy{Id: "p1", Name: "nightly"}

	dto := toPolicyDTO(p)

	assert.Equal(t, int64(0), dto.DisabledAt)
	data, err := json.Marshal(dto)
	require.NoError(t, err)
	assert.NotContains(t, string(data), "disabled_at")
}

func TestHandleCreatePolicy_SetsDisabledAtWhenProvided(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080", "disabled_at": 1754000000}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	require.NotNil(t, fake.lastCreateReq.GetDisabledAt())
	assert.Equal(t, int64(1754000000), fake.lastCreateReq.GetDisabledAt().AsTime().Unix())
}

func TestHandleCreatePolicy_OmittedDisabledAtLeavesItUnset(t *testing.T) {
	fake := &fakePolicyServiceClient{createResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/policies", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.NotNil(t, fake.lastCreateReq)
	assert.Nil(t, fake.lastCreateReq.GetDisabledAt())
}

func TestHandleUpdatePolicy_EchoesDisabledAtBack(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080", "disabled_at": 1754000000}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/p1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	require.NotNil(t, fake.lastUpdateReq.GetDisabledAt())
	assert.Equal(t, int64(1754000000), fake.lastUpdateReq.GetDisabledAt().AsTime().Unix())
}

func TestHandleUpdatePolicy_OmittedDisabledAtClearsIt(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{Id: "p1"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{"name": "nightly", "destination": "bwfs:8080"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/policies/p1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	assert.Nil(t, fake.lastUpdateReq.GetDisabledAt(), "full-replace semantics: omitting disabled_at clears it, same as every other optional field")
}

func TestHandleUpdateStoragePolicy_EchoesDisabledAtBack(t *testing.T) {
	fake := &fakePolicyServiceClient{updateResp: &pb.Policy{Id: "s1", Type: "storage"}}
	srv := newServer(nil, nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	body := strings.NewReader(`{
		"name": "east-1-storage",
		"client_filters": {"hostnames": ["storage-east-1.internal"], "labels": {}},
		"port": 9400,
		"config": "{}",
		"disabled_at": 1754000000
	}`)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/storage-policies/s1", body)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastUpdateReq)
	require.NotNil(t, fake.lastUpdateReq.GetDisabledAt())
	assert.Equal(t, int64(1754000000), fake.lastUpdateReq.GetDisabledAt().AsTime().Unix())
}
