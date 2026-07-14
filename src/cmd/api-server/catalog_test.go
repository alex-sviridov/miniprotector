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
	resp     *pb.ListEntriesResponse
	err      error
	lastReq  *pb.ListEntriesRequest
}

func (f *fakeCatalogQueryClient) ListEntries(ctx context.Context, in *pb.ListEntriesRequest, opts ...grpc.CallOption) (*pb.ListEntriesResponse, error) {
	f.lastReq = in
	return f.resp, f.err
}

func TestHandleListCatalog_ReturnsDataAndHasMore(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{
		Entries: []*pb.Entry{{Id: 1, SourceHost: "bwfs-a", Path: "/var/log/syslog"}},
		HasMore: true,
	}}
	srv := newServer(nil, fake, testLogger())
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
	assert.Equal(t, "/var/log/syslog", data[0].(map[string]any)["path"])
}

func TestHandleListCatalog_PassesFilterQueryParamsThrough(t *testing.T) {
	fake := &fakeCatalogQueryClient{resp: &pb.ListEntriesResponse{}}
	srv := newServer(nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?source_host=bwfs-a&pattern=/var/log&limit=10&starting_after=42", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, fake.lastReq)
	assert.Equal(t, "bwfs-a", fake.lastReq.GetSourceHost())
	assert.Equal(t, "/var/log", fake.lastReq.GetPattern())
	assert.Equal(t, int32(10), fake.lastReq.GetLimit())
	assert.Equal(t, int64(42), fake.lastReq.GetStartingAfter())
}

func TestHandleListCatalog_InvalidLimitReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?limit=not-a-number", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListCatalog_LimitOutOfRangeReturns400(t *testing.T) {
	fake := &fakeCatalogQueryClient{}
	srv := newServer(nil, fake, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/catalog?limit=501", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
