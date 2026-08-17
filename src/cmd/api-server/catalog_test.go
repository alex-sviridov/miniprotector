package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	childrenResp    *pb.ListDirectoryChildrenResponse
	childrenErr     error
	lastChildrenReq *pb.ListDirectoryChildrenRequest
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

func (f *fakeCatalogQueryClient) ListDirectoryFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error) {
	f.lastFacetsReq = in
	return f.facetsResp, f.facetsErr
}

func (f *fakeCatalogQueryClient) ListStoreFacets(ctx context.Context, in *pb.ListFacetsRequest, opts ...grpc.CallOption) (*pb.ListFacetsResponse, error) {
	f.lastFacetsReq = in
	return f.facetsResp, f.facetsErr
}

func (f *fakeCatalogQueryClient) ListDirectoryChildren(ctx context.Context, in *pb.ListDirectoryChildrenRequest, opts ...grpc.CallOption) (*pb.ListDirectoryChildrenResponse, error) {
	f.lastChildrenReq = in
	return f.childrenResp, f.childrenErr
}

func TestHandleListCatalog_ReturnsDataAndHasMore(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{
		Entries: []*pb.Entry{{Id: 1, StoreHost: "bwfs-a", SourceHost: "database", Path: "/var/log/syslog"}},
		HasMore: true,
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?source_host=database&store_host=bwfs-a&pattern=/var/log&limit=10&starting_after=42", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?limit=not-a-number", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_LimitOutOfRangeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?limit=501", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_PassesNewFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_after=1000&received_before=2000&source_hosts=database,webserver&job_names=nightly-db,weekly-full", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?source_hosts=database,%20webserver", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_after=not-a-number", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_NegativeReceivedBeforeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?received_before=-5", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/clients", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/clients?received_after=1000&received_before=2000&pattern=/var&job_names=nightly-db", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs?received_after=1000&source_hosts=database,webserver", nil)
	req.Header.Set("Authorization", "Bearer test-token")
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
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs?received_before=-5", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_ReturnsParentDirectoryAndShortFilename(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{
		Entries: []*pb.Entry{{Id: 1, ParentDirectory: "/var/lib/dbdata", ShortFilename: "data.db"}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	entry := data[0].(map[string]any)
	assert.Equal(t, "/var/lib/dbdata", entry["parent_directory"])
	assert.Equal(t, "data.db", entry["short_filename"])
}

func TestHandleListCatalog_PassesParentDirectoriesQueryParamThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?parent_directories=/var/lib/dbdata,/var/www", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, []string{"/var/lib/dbdata", "/var/www"}, fake.lastReq.GetParentDirectories())
}

func TestHandleListCatalogClients_PassesParentDirectoriesQueryParamThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/clients?parent_directories=/var/lib/dbdata", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, []string{"/var/lib/dbdata"}, fake.lastFacetsReq.GetParentDirectories())
}

func TestHandleListCatalogJobs_PassesParentDirectoriesQueryParamThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/jobs?parent_directories=/var/lib/dbdata", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, []string{"/var/lib/dbdata"}, fake.lastFacetsReq.GetParentDirectories())
}

func TestHandleListCatalogDirectories_ReturnsFacetData(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{
		Facets: []*pb.Facet{{Name: "/var/lib/dbdata", Count: 2, LastSeen: 1752400000}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	facet := data[0].(map[string]any)
	assert.Equal(t, "/var/lib/dbdata", facet["name"])
	assert.Equal(t, float64(2), facet["count"])
}

func TestHandleListCatalogDirectories_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories?received_after=1000&source_hosts=database&job_names=nightly-db", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Equal(t, int64(1000), fake.lastFacetsReq.GetReceivedAfter())
	assert.Equal(t, []string{"database"}, fake.lastFacetsReq.GetSourceHosts())
	assert.Equal(t, []string{"nightly-db"}, fake.lastFacetsReq.GetJobNames())
}

func TestHandleListCatalogDirectories_IgnoresParentDirectoriesQueryParam(t *testing.T) {
	fake := &fakeCatalogQueryClient{facetsResp: &pb.ListFacetsResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories?parent_directories=/var/lib/dbdata", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastFacetsReq)
	assert.Empty(t, fake.lastFacetsReq.GetParentDirectories())
}

func TestHandleListCatalogDirectories_InvalidReceivedAfterReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories?received_after=not-a-number", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalogDirectoryChildren_ReturnsData(t *testing.T) {
	fake := &fakeCatalogQueryClient{childrenResp: &pb.ListDirectoryChildrenResponse{
		Children: []*pb.DirectoryChild{{Path: "/var/lib", Name: "lib", FileCount: 2, LastSeen: 1752400000, HasChildren: true}},
	}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories/children?parent_path=/var", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	child := data[0].(map[string]any)
	assert.Equal(t, "/var/lib", child["path"])
	assert.Equal(t, "lib", child["name"])
	assert.Equal(t, float64(2), child["file_count"])
	assert.Equal(t, true, child["has_children"])
}

func TestHandleListCatalogDirectoryChildren_PassesParentPathAndFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{childrenResp: &pb.ListDirectoryChildrenResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories/children?parent_path=/var/lib&received_after=1000&source_hosts=database&job_names=nightly-db", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastChildrenReq)
	assert.Equal(t, "/var/lib", fake.lastChildrenReq.GetParentPath())
	assert.Equal(t, int64(1000), fake.lastChildrenReq.GetReceivedAfter())
	assert.Equal(t, []string{"database"}, fake.lastChildrenReq.GetSourceHosts())
	assert.Equal(t, []string{"nightly-db"}, fake.lastChildrenReq.GetJobNames())
}

func TestHandleListCatalogDirectoryChildren_OmittedParentPathMeansRoot(t *testing.T) {
	fake := &fakeCatalogQueryClient{childrenResp: &pb.ListDirectoryChildrenResponse{}}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories/children", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastChildrenReq)
	assert.Equal(t, "", fake.lastChildrenReq.GetParentPath())
}

func TestHandleListCatalogDirectoryChildren_InvalidReceivedAfterReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/directories/children?received_after=not-a-number", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestParseDateRange_BothValid(t *testing.T) {
	w := httptest.NewRecorder()
	q := url.Values{"received_after": {"1000"}, "received_before": {"2000"}}

	after, before, ok := parseDateRange(w, q)

	require.True(t, ok)
	assert.Equal(t, int64(1000), after)
	assert.Equal(t, int64(2000), before)
	assert.Equal(t, http.StatusOK, w.Code) // nothing written on success
}

func TestParseDateRange_BothOmittedReturnsZeroBounds(t *testing.T) {
	w := httptest.NewRecorder()

	after, before, ok := parseDateRange(w, url.Values{})

	require.True(t, ok)
	assert.Equal(t, int64(0), after)
	assert.Equal(t, int64(0), before)
}

func TestParseDateRange_InvalidReceivedAfterWrites400(t *testing.T) {
	w := httptest.NewRecorder()
	q := url.Values{"received_after": {"not-a-number"}}

	_, _, ok := parseDateRange(w, q)

	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestParseDateRange_InvalidReceivedBeforeWrites400(t *testing.T) {
	w := httptest.NewRecorder()
	q := url.Values{"received_before": {"-5"}}

	_, _, ok := parseDateRange(w, q)

	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestParseDateRange_BothMalformedReturns400OnReceivedAfterFirst(t *testing.T) {
	w := httptest.NewRecorder()
	q := url.Values{"received_after": {"not-a-number"}, "received_before": {"also-not-a-number"}}

	_, _, ok := parseDateRange(w, q)

	// received_after is checked first; its error is the one written when both are malformed.
	assert.False(t, ok)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	body := w.Body.String()
	assert.Contains(t, body, "received_after")
}

func TestHandleListCatalogStores_ReturnsFacets(t *testing.T) {
	fake := &fakeCatalogQueryClient{
		facetsResp: &pb.ListFacetsResponse{
			Facets: []*pb.Facet{{Name: "bwfs-1", Count: 3, LastSeen: 100}},
		},
	}
	srv := newServer(nil, fake, nil, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog/stores?pattern=/var/www", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string][]facetDTO
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body["data"], 1)
	assert.Equal(t, "bwfs-1", body["data"][0].Name)
	assert.Equal(t, "/var/www", fake.lastFacetsReq.GetPattern())
}
