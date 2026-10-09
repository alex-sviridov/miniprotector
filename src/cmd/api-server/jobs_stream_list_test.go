package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleJobsStream_SendsSnapshotThenUpsert(t *testing.T) {
	// jobAggregator.Subscribe now runs a synchronous reconcile on the 0->1
	// subscriber transition (Task 2), so the fake Loki client must be
	// seeded to return job "a" rather than relying on agg.jobs being
	// manually pre-populated and left untouched -- see
	// TestJobAggregator_SubscribeReturnsCurrentSnapshot in
	// jobs_aggregator_test.go for the same fix.
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs|policy-server"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "a", "event": "start"},
				Values: []lokiValue{{Timestamp: 1752400500000000000}}},
		},
		`{binary=~"agent|brfs|bwfs|policy-server"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "a", "event": "finish", "status": "success"},
				Values: []lokiValue{{Timestamp: 1752400501000000000}}},
		},
	}}
	agg := newJobAggregator(fake, &fakeLokiTailer{}, testLogger())

	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.aggregator = agg
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	gatewayStub := httptest.NewServer(mux)
	defer gatewayStub.Close()

	ticket, err := srv.wsTickets.issue()
	require.NoError(t, err)

	wsURL := "ws" + strings.TrimPrefix(gatewayStub.URL, "http") + "/api/v1/jobs/stream?ticket=" + ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	var snapshot jobsStreamMsg
	require.NoError(t, conn.ReadJSON(&snapshot))
	assert.Equal(t, "snapshot", snapshot.Type)
	require.Len(t, snapshot.Jobs, 1)
	assert.Equal(t, "a", snapshot.Jobs[0].JobID)

	agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
		Stream: map[string]string{"hostname": "h", "job_id": "b", "event": "start"},
		Values: []lokiValue{{Timestamp: time.Now().UnixNano()}},
	}}})

	var upsert jobsStreamMsg
	require.NoError(t, conn.ReadJSON(&upsert))
	assert.Equal(t, "upsert", upsert.Type)
	require.NotNil(t, upsert.Job)
	assert.Equal(t, "b", upsert.Job.JobID)
}

func TestHandleJobsStream_MissingTicketRejected(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.aggregator = newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
