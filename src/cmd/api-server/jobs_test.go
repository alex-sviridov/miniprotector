package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLokiClient struct {
	byQuery map[string][]lokiStream
	err     error
}

func (f *fakeLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byQuery[query], nil
}

func TestKindFromJobID(t *testing.T) {
	assert.Equal(t, "backup", kindFromJobID("backup:nightly:var-www:abcd1234:1752400000"))
	assert.Equal(t, "operating-refresh", kindFromJobID("operating-refresh:1752400500"))
	assert.Equal(t, "", kindFromJobID("no-colon-here"))
}

func TestHandleListJobs_PairsStartAndFinishByJobID(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver"}, Values: []lokiValue{
				{Timestamp: 1752400500000000000, Metadata: map[string]string{"job_id": "operating-refresh:1752400500"}},
			}},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "webserver"}, Values: []lokiValue{
				{Timestamp: 1752400501000000000, Metadata: map[string]string{"job_id": "operating-refresh:1752400500", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	job := data[0].(map[string]any)
	assert.Equal(t, "operating-refresh:1752400500", job["job_id"])
	assert.Equal(t, "operating-refresh", job["kind"])
	assert.Equal(t, "webserver", job["source_host"])
	assert.Nil(t, job["store_host"])
	assert.Equal(t, float64(1752400500), job["started_at"])
	assert.Equal(t, float64(1752400501), job["finished_at"])
	assert.Equal(t, "success", job["state"])
}

func TestHandleListJobs_NoFinishLineMeansInProgress(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver"}, Values: []lokiValue{
				{Timestamp: 1752400500000000000, Metadata: map[string]string{"job_id": "policy-update:1752400500"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	job := body["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "in_progress", job["state"])
	assert.Nil(t, job["finished_at"])
}

func TestHandleListJobs_BackupJobUsesFinishLineHostAsStoreHost(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "database"}, Values: []lokiValue{
				{Timestamp: 1752400000000000000, Metadata: map[string]string{"job_id": "backup:nightly:var-www:abcd1234:1752400000"}},
			}},
		},
		`{binary=~"brfs|bwfs"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "bwfs-east"}, Values: []lokiValue{
				{Timestamp: 1752400010000000000, Metadata: map[string]string{"job_id": "backup:nightly:var-www:abcd1234:1752400000", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=backup", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	job := body["data"].([]any)[0].(map[string]any)
	assert.Equal(t, "database", job["source_host"])
	assert.Equal(t, "bwfs-east", job["store_host"])
}

func TestHandleListJobs_InvalidKindReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=not-a-real-kind", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListJobs_WindowExceeding168hReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	now := time.Now()
	since := now.Add(-200 * time.Hour).Unix()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?since="+itoa(since), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleListJobs_LokiErrorReturns502(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{err: assert.AnError}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func itoa(n int64) string {
	return strconv.FormatInt(n, 10)
}
