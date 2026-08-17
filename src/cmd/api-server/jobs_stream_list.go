// src/cmd/api-server/jobs_stream_list.go
package main

import "net/http"

// handleJobsStream upgrades to a WebSocket, sends the aggregator's current
// state as one "snapshot" message, then relays every subsequent "upsert"/
// "snapshot" (from a periodic reconcile) until the client disconnects.
// Gated by requireWSTicket, registered in server.go. Unlike
// handleJobLogsStream (Task 6), this never dials Loki itself -- it only
// subscribes to the one shared aggregator every connected browser reads
// from (Tasks 8-9).
func (s *server) handleJobsStream(w http.ResponseWriter, r *http.Request) {
	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("handleJobsStream: upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	snapshot, ch, unsubscribe := s.aggregator.Subscribe()
	defer unsubscribe()

	if err := conn.WriteJSON(jobsStreamMsg{Type: "snapshot", Jobs: snapshot}); err != nil {
		return
	}

	clientClosed := make(chan struct{})
	go func() {
		defer close(clientClosed)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	for {
		select {
		case <-clientClosed:
			return
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if err := conn.WriteJSON(msg); err != nil {
				return
			}
		}
	}
}
