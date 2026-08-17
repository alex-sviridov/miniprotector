package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWSTicketStore_IssuedTicketConsumesOnce(t *testing.T) {
	store := newWSTicketStore()

	ticket, err := store.issue()
	require.NoError(t, err)
	assert.NotEmpty(t, ticket)

	assert.True(t, store.consume(ticket), "a freshly issued ticket must consume successfully")
	assert.False(t, store.consume(ticket), "a second consume of the same ticket must fail")
}

func TestWSTicketStore_UnknownTicketRejected(t *testing.T) {
	store := newWSTicketStore()
	assert.False(t, store.consume("never-issued"))
}

func TestWSTicketStore_ExpiredTicketRejected(t *testing.T) {
	store := newWSTicketStore()
	ticket, err := store.issue()
	require.NoError(t, err)

	store.mu.Lock()
	store.tickets[ticket] = time.Now().Add(-wsTicketTTL - time.Second)
	store.mu.Unlock()

	assert.False(t, store.consume(ticket))
}

func TestRequireWSTicket_MissingTicketRejected(t *testing.T) {
	store := newWSTicketStore()
	called := false
	h := requireWSTicket(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, called)
}

func TestRequireWSTicket_ValidTicketPassesThroughAndConsumes(t *testing.T) {
	store := newWSTicketStore()
	ticket, err := store.issue()
	require.NoError(t, err)
	called := false
	h := requireWSTicket(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/jobs/stream?ticket="+ticket, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	assert.True(t, called)
	assert.False(t, store.consume(ticket), "requireWSTicket must consume the ticket, not just check it")
}

func TestHandleIssueWSTicket_ReturnsConsumableTicket(t *testing.T) {
	srv := newServer(nil, nil, nil, testLogger())
	srv.wsTickets = newWSTicketStore()
	mux := http.NewServeMux()
	srv.registerRoutes(mux, "test-token")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ws-tickets", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Ticket string `json:"ticket"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.NotEmpty(t, body.Ticket)
	assert.True(t, srv.wsTickets.consume(body.Ticket))
}
