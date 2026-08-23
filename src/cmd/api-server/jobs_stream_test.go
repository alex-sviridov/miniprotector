package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeLokiTailer struct {
	messages []lokiTailMessage

	lastQuery string
}

func (f *fakeLokiTailer) Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error {
	f.lastQuery = query
	for _, m := range f.messages {
		if err := onMessage(m); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

func TestHandleJobLogsStream_RelaysMatchingLinesToClient(t *testing.T) {
	fake := &fakeLokiTailer{messages: []lokiTailMessage{{
		Streams: []lokiStream{{
			Stream: map[string]string{"hostname": "database", "binary": "brfs"},
			Values: []lokiValue{{Timestamp: 1752400000123456789, Line: `{"msg":"done","event":"finish"}`}},
		}},
	}}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.lokiTail = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	gatewayStub := httptest.NewServer(mux)
	defer gatewayStub.Close()

	ticket, err := srv.wsTickets.issue()
	require.NoError(t, err)

	wsURL := "ws" + strings.TrimPrefix(gatewayStub.URL, "http") + "/api/v1/jobs/backup%3Anightly%3A1/logs/stream?ticket=" + ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	var got logLineDTO
	require.NoError(t, conn.ReadJSON(&got))
	assert.Equal(t, "database", got.Hostname)
	assert.Equal(t, "brfs", got.Binary)
	assert.Contains(t, got.Line, "finish")
}

func TestHandleJobLogsStream_SelectorIncludesPolicyServer(t *testing.T) {
	fake := &fakeLokiTailer{messages: []lokiTailMessage{{
		Streams: []lokiStream{{
			Stream: map[string]string{"hostname": "database", "binary": "brfs"},
			Values: []lokiValue{{Timestamp: 1752400000123456789, Line: `{"msg":"done","event":"finish"}`}},
		}},
	}}}
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.lokiTail = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	gatewayStub := httptest.NewServer(mux)
	defer gatewayStub.Close()

	ticket, err := srv.wsTickets.issue()
	require.NoError(t, err)

	wsURL := "ws" + strings.TrimPrefix(gatewayStub.URL, "http") + "/api/v1/jobs/restore%3Ax%3A1/logs/stream?ticket=" + ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	var got logLineDTO
	require.NoError(t, conn.ReadJSON(&got))

	assert.Equal(t, `{binary=~"agent|brfs|bwfs|rwfs|policy-server"} | job_id="restore:x:1"`, fake.lastQuery)
}

// signalingTailer blocks until ctx is cancelled -- mirroring a real tail
// that has nothing left to deliver (the common case: the browser is
// viewing an already-finished job) -- then closes done. Lets a test observe
// whether the server actually propagates a client disconnect to the
// upstream Loki tail call, rather than leaking it forever.
type signalingTailer struct {
	done chan struct{}
}

func (s *signalingTailer) Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error {
	<-ctx.Done()
	close(s.done)
	return ctx.Err()
}

// TestHandleJobLogsStream_ClientDisconnectCancelsUpstreamTail guards
// against leaking an upstream Loki tail connection (and its goroutine)
// every time a browser navigates away from a job detail page whose tail
// has nothing left to send. r.Context() alone does not reliably become
// Done for a hijacked WebSocket connection (see handleJobsStream's own
// clientClosed goroutine and log-gateway's relayTail for the established
// pattern this mirrors) -- without an explicit read loop on the client
// conn, the server has no way to notice the browser is gone.
func TestHandleJobLogsStream_ClientDisconnectCancelsUpstreamTail(t *testing.T) {
	fake := &signalingTailer{done: make(chan struct{})}
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.lokiTail = fake
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	gatewayStub := httptest.NewServer(mux)
	defer gatewayStub.Close()

	ticket, err := srv.wsTickets.issue()
	require.NoError(t, err)

	wsURL := "ws" + strings.TrimPrefix(gatewayStub.URL, "http") + "/api/v1/jobs/backup%3Anightly%3A1/logs/stream?ticket=" + ticket
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err)

	// Client disconnects without the tail ever having anything to deliver.
	require.NoError(t, conn.Close())

	select {
	case <-fake.done:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream Loki tail was never cancelled after the client disconnected -- this leaks the tail connection")
	}
}

func TestHandleJobLogsStream_InvalidJobIDRejectedBeforeUpgrade(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.lokiTail = &fakeLokiTailer{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	ticket, err := srv.wsTickets.issue()
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/bad%20id/logs/stream?ticket="+ticket, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandleJobLogsStream_MissingTicketRejected(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	srv.lokiTail = &fakeLokiTailer{}
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/backup%3Anightly%3A1/logs/stream", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
