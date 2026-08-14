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

func TestBinariesForKind(t *testing.T) {
	assert.Equal(t, "brfs|bwfs", binariesForKind("backup"))
	assert.Equal(t, "agent", binariesForKind("bootstrap-refresh"))
	assert.Equal(t, "agent", binariesForKind("operating-refresh"))
	assert.Equal(t, "agent", binariesForKind("policy-update"))
	assert.Equal(t, "agent", binariesForKind("restore"))
	assert.Equal(t, "agent|brfs|bwfs", binariesForKind(""))
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

// TestHandleListJobs_ReadsJobIDFromStreamLevelStructuredMetadata reproduces
// real Loki 3.7.3's actual query_range wire shape for this query pattern:
// since job_id is unique per invocation, each matching line's structured
// metadata is homogeneous within its stream group, so Loki hoists
// job_id/event/status onto the stream object and returns bare [timestamp,
// line] values -- no 3rd per-value metadata element. Confirmed against a
// real demo Loki instance: api-server's /api/v1/jobs came back empty despite
// brfs/bwfs/agent and Vector all correctly producing and shipping the data.
func TestHandleListJobs_ReadsJobIDFromStreamLevelStructuredMetadata(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{
				Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1752400500", "event": "start"},
				Values: []lokiValue{{Timestamp: 1752400500000000000}},
			},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {
			{
				Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1752400500", "event": "finish", "status": "success"},
				Values: []lokiValue{{Timestamp: 1752400501000000000}},
			},
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
	require.Len(t, data, 1, "job dropped -- job_id must be read from stream-level fields when per-value Metadata is absent")
	job := data[0].(map[string]any)
	assert.Equal(t, "operating-refresh:1752400500", job["job_id"])
	assert.Equal(t, "success", job["state"])
	assert.Equal(t, float64(1752400501), job["finished_at"])
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

func TestHandleListJobs_SourceHostFilterDoesNotExcludeBackupFinishLine(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"brfs|bwfs", hostname="database"} | event="start"`: {
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=backup&source_host=database", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1, "the completed backup job must still appear, not be dropped")
	job := data[0].(map[string]any)
	assert.Equal(t, "success", job["state"], "must reflect the real terminal state, not silently show in_progress")
	assert.NotNil(t, job["finished_at"])
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

func TestHandleListJobs_KindRestoreIsAccepted(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=restore", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleListJobs_RestoreKindUsesAgentBinaryLabel(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent"} | event="start"`: {
			{Stream: map[string]string{"hostname": "database"}, Values: []lokiValue{
				{Timestamp: 1752400500000000000, Metadata: map[string]string{"job_id": "restore:e2e-restore-verify:1752400500"}},
			}},
		},
		`{binary=~"agent"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "database"}, Values: []lokiValue{
				{Timestamp: 1752400501000000000, Metadata: map[string]string{"job_id": "restore:e2e-restore-verify:1752400500", "status": "success"}},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs?kind=restore", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1)
	job := data[0].(map[string]any)
	assert.Equal(t, "restore", job["kind"])
	assert.Equal(t, "success", job["state"])
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

func TestHandleGetJobLogs_ReturnsLinesSortedByTimestamp(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="operating-refresh:1752400500"`: {
			{Stream: map[string]string{"hostname": "webserver", "binary": "agent"}, Values: []lokiValue{
				{Timestamp: 1752400501000000000, Line: "policy execution completed"},
				{Timestamp: 1752400500000000000, Line: "policy execution started"},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/operating-refresh:1752400500/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 2)
	assert.Equal(t, "policy execution started", data[0].(map[string]any)["line"])
	assert.Equal(t, "policy execution completed", data[1].(map[string]any)["line"])
	assert.Equal(t, "webserver", data[0].(map[string]any)["hostname"])
	assert.Equal(t, "agent", data[0].(map[string]any)["binary"])
}

func TestHandleGetJobLogs_InvalidJobIDCharacterReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/not%20valid;job/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleGetJobLogs_JobIDWithDotIsAccepted(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="restore:restore-2026-08-13T14:30:00.123Z-store-a:1755094200"`: {
			{Stream: map[string]string{"hostname": "database", "binary": "agent"}, Values: []lokiValue{
				{Timestamp: 1755094200000000000, Line: "policy execution started"},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/restore:restore-2026-08-13T14:30:00.123Z-store-a:1755094200/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
}

func TestHandleGetJobLogs_SourceAndStoreHostNarrowLabelSelector(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs", hostname=~"database|bwfs-east"} | job_id="backup:nightly:var-www:abcd1234:1752400000"`: {
			{Stream: map[string]string{"hostname": "database", "binary": "brfs"}, Values: []lokiValue{
				{Timestamp: 1752400000000000000, Line: "Backup reader started"},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/backup:nightly:var-www:abcd1234:1752400000/logs?source_host=database&store_host=bwfs-east", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Len(t, body["data"].([]any), 1)
}

func TestHandleGetJobLogs_IncludesRwfsBinaryLines(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="restore:e2e-restore-verify:1755094200"`: {
			{Stream: map[string]string{"hostname": "database", "binary": "rwfs"}, Values: []lokiValue{
				{Timestamp: 1755094201000000000, Line: `{"msg":"verified","path":"/var/lib/dbdata/dump.sql"}`},
			}},
		},
	}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/restore:e2e-restore-verify:1755094200/logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	data := body["data"].([]any)
	require.Len(t, data, 1, "rwfs-emitted log lines must be included, not filtered out")
	assert.Equal(t, "rwfs", data[0].(map[string]any)["binary"])
}

func TestHandleListJobs_InvalidSourceHostCharacterReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, `/api/v1/jobs?source_host=x%22%7D+%7C+line_format`, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleGetJobLogs_InvalidSourceHostCharacterReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, `/api/v1/jobs/operating-refresh:1752400500/logs?source_host=x%22%7D+%7C+line_format`, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleGetJobLogs_InvalidStoreHostCharacterReturns400(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.loki = &fakeLokiClient{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest(http.MethodGet, `/api/v1/jobs/operating-refresh:1752400500/logs?store_host=x%22%7D+%7C+line_format`, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
