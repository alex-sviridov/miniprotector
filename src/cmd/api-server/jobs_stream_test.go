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
}

func (f *fakeLokiTailer) Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error {
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
