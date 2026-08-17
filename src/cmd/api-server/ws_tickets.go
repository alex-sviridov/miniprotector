// src/cmd/api-server/ws_tickets.go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// wsTicketTTL bounds how long an issued-but-unused ticket stays valid.
// Short on purpose -- a ticket authenticates exactly one WS connection
// attempt, made immediately after it's issued, not a session.
const wsTicketTTL = 30 * time.Second

// wsTicketStore issues short-lived, single-use tickets that authenticate a
// browser's WebSocket upgrade. A WS handshake can't carry an Authorization
// header the way every other api-server call does (see
// docs/superpowers/specs/2026-08-17-live-job-updates-design.md), so a
// ticket -- minted only from an already-bearer-authenticated REST call,
// handleIssueWSTicket below -- stands in for it on the two WS routes
// (Tasks 6, 10) only. The long-lived shared bearer token itself never has
// to appear in a URL.
type wsTicketStore struct {
	mu      sync.Mutex
	tickets map[string]time.Time
}

func newWSTicketStore() *wsTicketStore {
	return &wsTicketStore{tickets: make(map[string]time.Time)}
}

func (s *wsTicketStore) issue() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	ticket := hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	for t, at := range s.tickets {
		if time.Since(at) >= wsTicketTTL {
			delete(s.tickets, t) // opportunistic sweep, mirrors loki_cache.go's cachingLokiClient
		}
	}
	s.tickets[ticket] = time.Now()
	return ticket, nil
}

// consume reports whether ticket is a valid, unexpired, not-yet-used
// ticket -- and if so, invalidates it immediately, so a replayed URL
// (e.g. from browser history or a proxy log) can't open a second
// connection.
func (s *wsTicketStore) consume(ticket string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.tickets[ticket]
	if !ok {
		return false
	}
	delete(s.tickets, ticket)
	return time.Since(at) < wsTicketTTL
}

// requireWSTicket guards a WS-upgrade handler with a ticket passed as a
// query parameter, in place of requireBearerToken's Authorization-header
// check (auth.go), which a browser's WS handshake can't provide.
func requireWSTicket(store *wsTicketStore, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ticket := r.URL.Query().Get("ticket")
		if ticket == "" || !store.consume(ticket) {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) handleIssueWSTicket(w http.ResponseWriter, r *http.Request) {
	ticket, err := s.wsTickets.issue()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "generate ticket: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"ticket": ticket})
}
