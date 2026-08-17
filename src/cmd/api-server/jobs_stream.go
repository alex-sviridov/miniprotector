// src/cmd/api-server/jobs_stream.go
package main

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
)

// browserUpgrader is shared by every browser-facing WS endpoint this
// binary serves. CheckOrigin always returns true: the ticket
// (requireWSTicket) is the real auth boundary, and in production the
// browser only ever reaches this same-origin via web's nginx reverse
// proxy (web/nginx.conf) anyway.
var browserUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// handleJobLogsStream upgrades to a WebSocket and tails job_id's log lines
// live, in the same logLineDTO shape GET /api/v1/jobs/{job_id}/logs
// already returns (jobs.go) -- LogLine.vue's parser doesn't need a second
// format. A stateless per-connection proxy: each call dials its own
// job_id-filtered Loki tail (unlike handleJobsStream/jobAggregator, Tasks
// 9-10, which share one fleet-wide tail across every connected browser).
// Gated by requireWSTicket, registered in server.go.
func (s *server) handleJobLogsStream(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job_id")
	if !jobIDPattern.MatchString(jobID) {
		http.Error(w, "job_id contains invalid characters", http.StatusBadRequest)
		return
	}

	start := time.Now()
	if raw := r.URL.Query().Get("start"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			start = time.Unix(parsed, 0)
		}
	}

	conn, err := browserUpgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("handleJobLogsStream: upgrade failed", "job_id", jobID, "error", err)
		return
	}
	defer conn.Close()

	// Includes rwfs, same as handleGetJobLogs (jobs.go) -- this endpoint
	// returns every raw line for job_id verbatim, no start/finish pairing,
	// so rwfs's lines (which never carry event/status) are still useful
	// signal here.
	query := fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs"} | job_id="%s"`, jobID)
	err = s.lokiTail.Tail(r.Context(), query, start, func(msg lokiTailMessage) error {
		for _, stream := range msg.Streams {
			for _, v := range stream.Values {
				line := logLineDTO{
					Timestamp: v.Timestamp,
					Hostname:  stream.Stream["hostname"],
					Binary:    stream.Stream["binary"],
					Line:      v.Line,
				}
				if err := conn.WriteJSON(line); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		s.logger.Error("handleJobLogsStream: tail ended", "job_id", jobID, "error", err)
	}
}
