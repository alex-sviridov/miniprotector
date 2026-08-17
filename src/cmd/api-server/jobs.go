// src/cmd/api-server/jobs.go
package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
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
	"verify":            true,
	"restore":           true,
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
	case "bootstrap-refresh", "operating-refresh", "policy-update", "verify", "restore":
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
// event value against loki, returning every matching (job_id, hostname,
// timestamp, status) line and whether the query hit its own line cap. A
// free function (not a *server method) so jobAggregator (Task 9) can call
// it against its own loki field too, not just handleListJobs.
func queryEvent(ctx context.Context, loki lokiQuerier, labelSelector, event string, since, until time.Time) ([]jobEventLine, bool, error) {
	query := fmt.Sprintf(`%s | event="%s"`, labelSelector, event)
	streams, err := loki.QueryRange(ctx, query, since, until, jobsQueryLineLimit)
	if err != nil {
		return nil, false, err
	}

	var lines []jobEventLine
	count := 0
	for _, stream := range streams {
		hostname := stream.Stream["hostname"]
		// job_id is unique per job invocation, so within one stream group
		// every line shares the same job_id/status -- real Loki (3.7.3,
		// confirmed against a live instance) hoists that homogeneous
		// structured metadata onto the stream object instead of repeating
		// it per value, leaving Values with no 3rd element at all. Metadata
		// is checked first since it's still the authoritative per-line
		// source on the rare stream that does carry it.
		streamJobID := stream.Stream["job_id"]
		streamStatus := stream.Stream["status"]
		for _, v := range stream.Values {
			count++
			jobID := v.Metadata["job_id"]
			if jobID == "" {
				jobID = streamJobID
			}
			if jobID == "" {
				continue
			}
			status := v.Metadata["status"]
			if status == "" {
				status = streamStatus
			}
			lines = append(lines, jobEventLine{
				JobID:     jobID,
				Hostname:  hostname,
				Timestamp: v.Timestamp / 1_000_000_000,
				Status:    status,
			})
		}
	}
	return lines, count >= jobsQueryLineLimit, nil
}

// jobEventAccumulator incrementally builds jobDTOs from start/finish event
// lines -- the same logic pairJobEvents runs once per query (batch, below)
// and jobAggregator (Task 9) runs one line at a time as they arrive from
// Loki's live tail. Kept as one implementation so the two call sites can't
// drift apart.
type jobEventAccumulator struct {
	byJobID map[string]*jobDTO
	order   []string
}

func newJobEventAccumulator() *jobEventAccumulator {
	return &jobEventAccumulator{byJobID: make(map[string]*jobDTO)}
}

// newJobEventAccumulatorSeeded starts from an already-known job summary
// (the aggregator's in-memory state for jobID) instead of from scratch --
// used when folding in one more live event for a job the aggregator has
// already seen.
func newJobEventAccumulatorSeeded(jobID string, dto jobDTO) *jobEventAccumulator {
	acc := newJobEventAccumulator()
	acc.byJobID[jobID] = &dto
	acc.order = []string{jobID}
	return acc
}

func (a *jobEventAccumulator) get(jobID string) *jobDTO {
	j, ok := a.byJobID[jobID]
	if !ok {
		j = &jobDTO{JobID: jobID, Kind: kindFromJobID(jobID), State: "in_progress"}
		a.byJobID[jobID] = j
		a.order = append(a.order, jobID)
	}
	return j
}

// ApplyStart folds one event=start line in, returning the affected job's
// current (possibly still-incomplete) summary.
func (a *jobEventAccumulator) ApplyStart(e jobEventLine) jobDTO {
	j := a.get(e.JobID)
	ts := e.Timestamp
	j.SourceHost = e.Hostname
	j.StartedAt = &ts
	return *j
}

// ApplyFinish folds one event=finish line in, returning the affected job's
// current summary. For kind=backup, StoreHost comes from the finish
// line's hostname (bwfs, the destination) -- every other kind leaves it
// nil, same rule pairJobEvents always applied.
func (a *jobEventAccumulator) ApplyFinish(e jobEventLine) jobDTO {
	j := a.get(e.JobID)
	ts := e.Timestamp
	j.FinishedAt = &ts
	j.State = e.Status
	if j.Kind == "backup" {
		host := e.Hostname
		j.StoreHost = &host
	}
	return *j
}

func (a *jobEventAccumulator) All() []jobDTO {
	out := make([]jobDTO, 0, len(a.order))
	for _, id := range a.order {
		out = append(out, *a.byJobID[id])
	}
	return out
}

// pairJobEvents groups start/finish lines by job_id into one jobDTO each.
// A job_id with only a start line is in_progress; one with only a finish
// line (its start fell outside the queried window) gets a nil StartedAt --
// never guessed.
func pairJobEvents(starts, finishes []jobEventLine) []jobDTO {
	acc := newJobEventAccumulator()
	for _, e := range starts {
		acc.ApplyStart(e)
	}
	for _, e := range finishes {
		acc.ApplyFinish(e)
	}
	return acc.All()
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
		writeJSONError(w, http.StatusBadRequest, "kind must be one of backup, bootstrap-refresh, operating-refresh, policy-update, verify, restore")
		return
	}
	sourceHost := q.Get("source_host")
	if sourceHost != "" && !jobHostnamePattern.MatchString(sourceHost) {
		writeJSONError(w, http.StatusBadRequest, "source_host contains invalid characters")
		return
	}
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

	starts, startsTruncated, err := queryEvent(r.Context(), s.loki, startLabelSelector, "start", since, until)
	if err != nil {
		s.logger.Error("handleListJobs: query start events failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}
	finishes, finishesTruncated, err := queryEvent(r.Context(), s.loki, finishLabelSelector, "finish", since, until)
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

var jobIDPattern = regexp.MustCompile(`^[a-zA-Z0-9:._-]+$`)

var jobHostnamePattern = regexp.MustCompile(`^[a-zA-Z0-9.-]+$`)

type logLineDTO struct {
	Timestamp int64  `json:"timestamp"`
	Hostname  string `json:"hostname"`
	Binary    string `json:"binary"`
	Line      string `json:"line"`
}

func (s *server) handleGetJobLogs(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job_id")
	if !jobIDPattern.MatchString(jobID) {
		writeJSONError(w, http.StatusBadRequest, "job_id contains invalid characters")
		return
	}

	q := r.URL.Query()
	until := time.Now()
	since := until.Add(-defaultJobsWindow)
	if raw := q.Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "since must be a unix-second integer")
			return
		}
		since = time.Unix(parsed, 0)
	}

	sourceHost := q.Get("source_host")
	if sourceHost != "" && !jobHostnamePattern.MatchString(sourceHost) {
		writeJSONError(w, http.StatusBadRequest, "source_host contains invalid characters")
		return
	}
	storeHost := q.Get("store_host")
	if storeHost != "" && !jobHostnamePattern.MatchString(storeHost) {
		writeJSONError(w, http.StatusBadRequest, "store_host contains invalid characters")
		return
	}
	// Unlike binariesForKind's default selector, this includes rwfs: this
	// endpoint returns every raw log line for a job_id verbatim (no
	// start/finish pairing), so rwfs's lines are useful signal here even
	// though handleListJobs excludes rwfs to avoid pairing noise (rwfs never
	// emits event=start/event=finish).
	labelSelector := `{binary=~"agent|brfs|bwfs|rwfs"}`
	switch {
	case sourceHost != "" && storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname=~"%s|%s"}`, sourceHost, storeHost)
	case sourceHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname="%s"}`, sourceHost)
	case storeHost != "":
		labelSelector = fmt.Sprintf(`{binary=~"agent|brfs|bwfs|rwfs", hostname="%s"}`, storeHost)
	}

	query := fmt.Sprintf(`%s | job_id="%s"`, labelSelector, jobID)
	streams, err := s.loki.QueryRange(r.Context(), query, since, until, jobsQueryLineLimit)
	if err != nil {
		s.logger.Error("handleGetJobLogs: query failed", "error", err)
		writeJSONError(w, http.StatusBadGateway, "query loki: "+err.Error())
		return
	}

	lines := []logLineDTO{}
	for _, stream := range streams {
		for _, v := range stream.Values {
			lines = append(lines, logLineDTO{
				Timestamp: v.Timestamp,
				Hostname:  stream.Stream["hostname"],
				Binary:    stream.Stream["binary"],
				Line:      v.Line,
			})
		}
	}
	sort.Slice(lines, func(i, k int) bool { return lines[i].Timestamp < lines[k].Timestamp })

	writeJSON(w, http.StatusOK, map[string]any{"data": lines})
}
