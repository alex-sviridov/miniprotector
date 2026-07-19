// src/cmd/api-server/jobs.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultJobsWindow  = 24 * time.Hour
	maxJobsWindow      = 168 * time.Hour
	defaultJobsLimit   = 100
	maxJobsLimit       = 500
	jobsQueryLineLimit = 5000
)

var validJobKinds = map[string]bool{
	"backup":            true,
	"bootstrap-refresh": true,
	"operating-refresh": true,
	"policy-update":     true,
}

// kindFromJobID derives a job's kind from its own id, per the prefix
// convention agent/policy.go and agent/backup.go already established
// (e.g. "backup:nightly:var-www:abcd1234:1752400000",
// "operating-refresh:1752400500") -- no separate field needed anywhere.
func kindFromJobID(jobID string) string {
	if idx := strings.Index(jobID, ":"); idx >= 0 {
		return jobID[:idx]
	}
	return ""
}

// binariesForKind returns the Loki `binary` label regex to scope a query
// to, for a given (possibly empty) kind filter. kind=backup deliberately
// excludes "agent" -- see reconcile.go's isBackupPolicy and this repo's
// design doc for why agent never tags a scheduled backup dispatch with
// event/status.
func binariesForKind(kind string) string {
	switch kind {
	case "backup":
		return "brfs|bwfs"
	case "bootstrap-refresh", "operating-refresh", "policy-update":
		return "agent"
	default:
		return "agent|brfs|bwfs"
	}
}

type jobDTO struct {
	JobID      string  `json:"job_id"`
	Kind       string  `json:"kind"`
	SourceHost string  `json:"source_host"`
	StoreHost  *string `json:"store_host"`
	StartedAt  *int64  `json:"started_at"`
	FinishedAt *int64  `json:"finished_at"`
	State      string  `json:"state"`
}

// jobEventLine is one event=start or event=finish line, reduced to the
// fields pairJobEvents needs. Timestamp is unix seconds (Loki's own
// nanosecond wire unit, truncated -- sub-second precision is not
// meaningful for a job's started_at/finished_at).
type jobEventLine struct {
	JobID     string
	Hostname  string
	Timestamp int64
	Status    string
}

// queryEvent runs one Loki query scoped to labelSelector and the given
// event value, returning every matching (job_id, hostname, timestamp,
// status) line and whether the query hit its own line cap (in which case
// the window should be narrowed -- see the truncated flag on
// handleListJobs' response).
func (s *server) queryEvent(ctx context.Context, labelSelector, event string, since, until time.Time) ([]jobEventLine, bool, error) {
	query := fmt.Sprintf(`%s | event="%s"`, labelSelector, event)
	streams, err := s.loki.QueryRange(ctx, query, since, until, jobsQueryLineLimit)
	if err != nil {
		return nil, false, err
	}

	var lines []jobEventLine
	count := 0
	for _, stream := range streams {
		hostname := stream.Stream["hostname"]
		for _, v := range stream.Values {
			count++
			jobID := v.Metadata["job_id"]
			if jobID == "" {
				continue
			}
			lines = append(lines, jobEventLine{
				JobID:     jobID,
				Hostname:  hostname,
				Timestamp: v.Timestamp / 1_000_000_000,
				Status:    v.Metadata["status"],
			})
		}
	}
	return lines, count >= jobsQueryLineLimit, nil
}

// pairJobEvents groups start/finish lines by job_id into one jobDTO each.
// A job_id with only a start line is in_progress; one with only a finish
// line (its start fell outside the queried window) gets a nil StartedAt --
// never guessed. For kind=backup, StoreHost comes from the finish line's
// hostname (bwfs, the destination) while SourceHost comes from the start
// line's hostname (brfs, the real source) -- every other kind has a single
// SourceHost and a nil StoreHost.
func pairJobEvents(starts, finishes []jobEventLine) []jobDTO {
	byJobID := make(map[string]*jobDTO)
	var order []string

	get := func(jobID string) *jobDTO {
		j, ok := byJobID[jobID]
		if !ok {
			j = &jobDTO{JobID: jobID, Kind: kindFromJobID(jobID), State: "in_progress"}
			byJobID[jobID] = j
			order = append(order, jobID)
		}
		return j
	}

	for _, e := range starts {
		j := get(e.JobID)
		ts := e.Timestamp
		j.SourceHost = e.Hostname
		j.StartedAt = &ts
	}
	for _, e := range finishes {
		j := get(e.JobID)
		ts := e.Timestamp
		j.FinishedAt = &ts
		j.State = e.Status
		if j.Kind == "backup" {
			host := e.Hostname
			j.StoreHost = &host
		}
	}

	out := make([]jobDTO, 0, len(order))
	for _, id := range order {
		out = append(out, *byJobID[id])
	}
	return out
}

func sortKey(j jobDTO) int64 {
	if j.StartedAt != nil {
		return *j.StartedAt
	}
	if j.FinishedAt != nil {
		return *j.FinishedAt
	}
	return 0
}

func (s *server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	until := time.Now()
	if raw := q.Get("until"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "until must be a unix-second integer")
			return
		}
		until = time.Unix(parsed, 0)
	}
	since := until.Add(-defaultJobsWindow)
	if raw := q.Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "since must be a unix-second integer")
			return
		}
		since = time.Unix(parsed, 0)
	}
	if until.Before(since) {
		writeJSONError(w, http.StatusBadRequest, "until must not be before since")
		return
	}
	if until.Sub(since) > maxJobsWindow {
		writeJSONError(w, http.StatusBadRequest, "until-since must not exceed 168h")
		return
	}

	kind := q.Get("kind")
	if kind != "" && !validJobKinds[kind] {
		writeJSONError(w, http.StatusBadRequest, "kind must be one of backup, bootstrap-refresh, operating-refresh, policy-update")
		return
	}
	sourceHost := q.Get("source_host")
	stateFilter := q.Get("state")

	limit := defaultJobsLimit
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxJobsLimit {
			writeJSONError(w, http.StatusBadRequest, "limit must be an integer between 1 and 500")
			return
		}
		limit = parsed
	}

	binarySelector := binariesForKind(kind)
	startLabelSelector := fmt.Sprintf(`{binary=~"%s"}`, binarySelector)
	if sourceHost != "" {
		startLabelSelector = fmt.Sprintf(`{binary=~"%s", hostname="%s"}`, binarySelector, sourceHost)
	}
	// The finish query is deliberately never narrowed by hostname, even
	// when source_host is set: a backup job's finish line is labeled with
	// the store host (bwfs), not the source host (brfs) that source_host
	// filters on -- narrowing this query by hostname="<source_host>" would
	// silently exclude every backup job's finish line, making a completed
	// job incorrectly render as in_progress. Correctness for source_host
	// is guaranteed by the post-pairing filter below (line ~238); this
	// selector is a pure Loki-side performance narrowing, not required for
	// correctness, so it only applies where it can't cause data loss.
	finishLabelSelector := fmt.Sprintf(`{binary=~"%s"}`, binarySelector)

	starts, startsTruncated, err := s.queryEvent(r.Context(), startLabelSelector, "start", since, until)
	if err != nil {
		s.logger.Error("handleListJobs: query start events failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}
	finishes, finishesTruncated, err := s.queryEvent(r.Context(), finishLabelSelector, "finish", since, until)
	if err != nil {
		s.logger.Error("handleListJobs: query finish events failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}

	jobs := pairJobEvents(starts, finishes)

	filtered := make([]jobDTO, 0, len(jobs))
	for _, j := range jobs {
		if kind != "" && j.Kind != kind {
			continue
		}
		if sourceHost != "" && j.SourceHost != sourceHost {
			continue
		}
		if stateFilter != "" && j.State != stateFilter {
			continue
		}
		filtered = append(filtered, j)
	}
	sort.Slice(filtered, func(i, k int) bool { return sortKey(filtered[i]) > sortKey(filtered[k]) })
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": filtered, "truncated": startsTruncated || finishesTruncated})
}

// handleGetJobLogs is a placeholder: Task 11 registers this route (see
// server.go's registerRoutes) so the route table lives in one place, but
// the real implementation lands in Task 12. This stub exists only so the
// package compiles and the pre-existing test suite keeps passing in the
// interim -- it is not part of Task 11's brief and Task 12 replaces it
// wholesale.
func (s *server) handleGetJobLogs(w http.ResponseWriter, r *http.Request) {
	writeJSONError(w, http.StatusNotImplemented, "not implemented")
}
