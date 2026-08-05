package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
)

type fakeCatalogQueryClient struct {
	resp          *pb.ListEntriesResponse
	err           error
	lastReq       *pb.ListEntriesRequest
	facetsResp    *pb.ListFacetsResponse
	facetsErr     error
	lastFacetsReq *pb.ListFacetsRequest
}

func (f *fakeCatalogQueryClient) ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error) {
	f.lastReq = in
	return f.resp, f.err
}

func (f *fakeCatalogQueryClient) ListClientFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error) {
	f.lastFacetsReq = in
	return f.facetsResp, f.facetsErr
}

func (f *fakeCatalogQueryClient) ListJobFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error) {
	f.lastFacetsReq = in
	return f.facetsResp, f.facetsErr
}

func TestHandleListCatalog_ReturnsDataAndHasMore(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{
		Entries: []*pb.Entry{{Id: 1, StoreHost: "bwfs-a", SourceHost: "database", Path: "/var/log/syslog"}},
		HasMore: true,
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["has_more"])
	data := body["data"].([]any)
	require.Len(t, data, 1)
	entry := data[0].(map[string]any)
	assert.Equal(t, "/var/log/syslog", entry["path"])
	assert.Equal(t, "bwfs-a", entry["store_host"])
	assert.Equal(t, "database", entry["source_host"])
}

func TestHandleListCatalog_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?source_host=database&store_host=bwfs-a&pattern=/var/log&limit=10&starting_after=42", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, "database", fake.lastReq.GetSourceHost())
	assert.Equal(t, "bwfs-a", fake.lastReq.GetStoreHost())
	assert.Equal(t, "/var/log", fake.lastReq.GetPattern())
	assert.Equal(t, int32(10), fake.lastReq.GetLimit())
	assert.Equal(t, int64(42), fake.lastReq.GetStartingAfter())
}

func TestHandleListCatalog_InvalidLimitReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?limit=not-a-number", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_LimitOutOfRangeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?limit=501", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_PassesNewFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_after=1000&received_before=2000&source_hosts=database,webserver&job_names=nightly-db,weekly-full", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, int64(1000), fake.lastReq.GetReceivedAfter())
	assert.Equal(t, int64(2000), fake.lastReq.GetReceivedBefore())
	assert.Equal(t, []string{"database", "webserver"}, fake.lastReq.GetSourceHosts())
	assert.Equal(t, []string{"nightly-db", "weekly-full"}, fake.lastReq.GetJobNames())
}

func TestHandleListCatalog_CommaParamsTrimWhitespace(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?source_hosts=database,%20webserver", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, []string{"database", "webserver"}, fake.lastReq.GetSourceHosts())
}

func TestHandleListCatalog_OmittedNewFiltersLeaveFieldsZero(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, int64(0), fake.lastReq.GetReceivedAfter())
	assert.Nil(t, fake.lastReq.GetSourceHosts())
}

func TestHandleListCatalog_InvalidReceivedAfterReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_after=not-a-number", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_NegativeReceivedBeforeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_before=-5", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalogClients_ReturnsFacetData(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{
		Facets: []*pb.Facet{{Name: "database", Count: 3, LastSeen: 1752400000}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/clients", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	facet := data[0].(map[string]any)
	assert.Equal(t, "database", facet["name"])
	assert.Equal(t, float64(3), facet["count"])
	assert.Equal(t, float64(1752400000), facet["last_seen"])
}

func TestHandleListCatalogClients_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/clients?received_after=1000&received_before=2000&pattern=/var&job_names=nightly-db", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, int64(1000), fake.lastFacetsReq.GetReceivedAfter())
	assert.Equal(t, int64(2000), fake.lastFacetsReq.GetReceivedBefore())
	assert.Equal(t, "/var", fake.lastFacetsReq.GetPattern())
	assert.Equal(t, []string{"nightly-db"}, fake.lastFacetsReq.GetJobNames())
}

func TestHandleListCatalogJobs_ReturnsFacetData(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{
		Facets: []*pb.Facet{{Name: "nightly-db", Count: 7, LastSeen: 1752400000}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	facet := data[0].(map[string]any)
	assert.Equal(t, "nightly-db", facet["name"])
	assert.Equal(t, float64(7), facet["count"])
}

func TestHandleListCatalogJobs_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs?received_after=1000&source_hosts=database,webserver", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, int64(1000), fake.lastFacetsReq.GetReceivedAfter())
	assert.Equal(t, []string{"database", "webserver"}, fake.lastFacetsReq.GetSourceHosts())
}

func TestHandleListCatalogJobs_InvalidReceivedBeforeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs?received_before=-5", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
