// src/cmd/api-server/jobs_aggregator.go
package main

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

const (
	jobsAggregatorWindow         = 24 * time.Hour
	jobsAggregatorReconcileEvery = 60 * time.Second
)

// jobsAggregatorSubscriberBuffer bounds how many pending messages one
// connected browser can be behind before broadcast starts dropping
// updates for it -- a slow/stuck subscriber must never block delivery to
// every other one. A dropped upsert is never a permanent miss: the
// periodic reconciliation reconcile() runs (Task 9) resyncs every
// subscriber with a fresh full snapshot regardless.
const jobsAggregatorSubscriberBuffer = 32

// jobsStreamMsg is GET /api/v1/jobs/stream's wire message (Task 10):
// "snapshot" carries the full current job list (sent once, right after a
// browser connects, and again on every periodic reconcile), "upsert"
// carries one job whose summary just changed.
type jobsStreamMsg struct {
	Type string   `json:"type"`
	Jobs []jobDTO `json:"jobs,omitempty"`
	Job  *jobDTO  `json:"job,omitempty"`
}

// jobAggregator maintains one fleet-wide, in-memory job_id -> summary map
// fed by a single shared Loki tail (Task 9 adds the tail/reconcile
// lifecycle), fanning out snapshot+upsert messages to every connected
// browser (Task 10) -- rather than each browser tab opening its own
// fleet-wide tail, which would multiply Loki-side query cost with no
// benefit (spec Architecture).
type jobAggregator struct {
	loki   lokiQuerier
	tailer lokiTailer
	logger *slog.Logger

	// backoffBase/backoffMax are fields (not consts) so tests can shrink
	// backoffBase -- mirrors cmd/agent/reconcile.go's backoffBase/backoffMax
	// vars for the same reason. Same jittered-exponential idiom as that
	// file's backoff(), reimplemented here since this is a separate
	// package with no shared code between the two.
	backoffBase time.Duration
	backoffMax  time.Duration

	mu   sync.Mutex
	jobs map[string]jobDTO
	subs map[chan jobsStreamMsg]struct{}
}

func newJobAggregator(loki lokiQuerier, tailer lokiTailer, logger *slog.Logger) *jobAggregator {
	return &jobAggregator{
		loki:        loki,
		tailer:      tailer,
		logger:      logger,
		backoffBase: time.Second,
		backoffMax:  30 * time.Second,
		jobs:        make(map[string]jobDTO),
		subs:        make(map[chan jobsStreamMsg]struct{}),
	}
}

func (a *jobAggregator) backoff(failures int) time.Duration {
	exp := min(max(failures-1, 0), 8)
	d := a.backoffBase * time.Duration(1<<exp)
	if d > a.backoffMax {
		d = a.backoffMax
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// Subscribe registers a new listener and returns the current state as a
// snapshot, alongside the channel future upserts (and future full
// snapshots, from reconcile) will arrive on. Callers must call unsubscribe
// exactly once, typically via defer, when they stop reading.
func (a *jobAggregator) Subscribe() (snapshot []jobDTO, ch chan jobsStreamMsg, unsubscribe func()) {
	ch = make(chan jobsStreamMsg, jobsAggregatorSubscriberBuffer)

	a.mu.Lock()
	a.subs[ch] = struct{}{}
	snapshot = make([]jobDTO, 0, len(a.jobs))
	for _, j := range a.jobs {
		snapshot = append(snapshot, j)
	}
	a.mu.Unlock()

	unsubscribe = func() {
		a.mu.Lock()
		delete(a.subs, ch)
		a.mu.Unlock()
	}
	return snapshot, ch, unsubscribe
}

func (a *jobAggregator) broadcast(msg jobsStreamMsg) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- msg:
		default:
			// slow subscriber: drop rather than block every other one --
			// see jobsAggregatorSubscriberBuffer's comment above.
		}
	}
}

// ingestTailMessage folds one batch of live tail lines into the
// aggregator's state, using the exact same jobEventAccumulator logic (Task
// 7) GET /api/v1/jobs runs in batch -- applied here one job at a time,
// seeded from whatever this job_id's prior state already was (if any), so
// a finish line doesn't clobber the start line ingested moments earlier.
func (a *jobAggregator) ingestTailMessage(msg lokiTailMessage) {
	for _, stream := range msg.Streams {
		streamEvent := stream.Stream["event"]
		streamJobID := stream.Stream["job_id"]
		streamStatus := stream.Stream["status"]
		hostname := stream.Stream["hostname"]

		for _, v := range stream.Values {
			jobID := v.Metadata["job_id"]
			if jobID == "" {
				jobID = streamJobID
			}
			event := v.Metadata["event"]
			if event == "" {
				event = streamEvent
			}
			if jobID == "" || (event != "start" && event != "finish") {
				continue
			}
			status := v.Metadata["status"]
			if status == "" {
				status = streamStatus
			}
			line := jobEventLine{JobID: jobID, Hostname: hostname, Timestamp: v.Timestamp / 1_000_000_000, Status: status}

			a.mu.Lock()
			existing, ok := a.jobs[jobID]
			var acc *jobEventAccumulator
			if ok {
				acc = newJobEventAccumulatorSeeded(jobID, existing)
			} else {
				acc = newJobEventAccumulator()
			}
			var updated jobDTO
			if event == "start" {
				updated = acc.ApplyStart(line)
			} else {
				updated = acc.ApplyFinish(line)
			}
			a.jobs[jobID] = updated
			a.mu.Unlock()

			a.broadcast(jobsStreamMsg{Type: "upsert", Job: &updated})
		}
	}
}

// reconcile re-runs the same 24h fleet-wide query GET /api/v1/jobs already
// runs (via queryEvent/pairJobEvents, Task 7) and wholesale-replaces the
// aggregator's in-memory state, broadcasting the result as a fresh
// snapshot to every subscriber. Called on startup, every
// jobsAggregatorReconcileEvery, and once before every tail
// (re)attachment -- a correctness backstop independent of tail health,
// since Loki's tail is explicitly best-effort on delivery (spec
// Architecture), not a substitute for it.
func (a *jobAggregator) reconcile(ctx context.Context) error {
	until := time.Now()
	since := until.Add(-jobsAggregatorWindow)
	const selector = `{binary=~"agent|brfs|bwfs"}`

	starts, _, err := queryEvent(ctx, a.loki, selector, "start", since, until)
	if err != nil {
		return err
	}
	finishes, _, err := queryEvent(ctx, a.loki, selector, "finish", since, until)
	if err != nil {
		return err
	}
	jobs := pairJobEvents(starts, finishes)

	a.mu.Lock()
	a.jobs = make(map[string]jobDTO, len(jobs))
	for _, j := range jobs {
		a.jobs[j.JobID] = j
	}
	a.mu.Unlock()

	a.broadcast(jobsStreamMsg{Type: "snapshot", Jobs: jobs})
	return nil
}

// Start runs until ctx is cancelled: an initial reconcile, a background
// loop re-reconciling every jobsAggregatorReconcileEvery, and a
// foreground supervised tail that reconnects with jittered backoff on any
// unexpected error -- re-reconciling before every (re)attach so a dropped
// connection can never silently lose a job (spec Architecture). Intended
// to be run in its own goroutine by main.go (Task 10): `go agg.Start(ctx)`.
func (a *jobAggregator) Start(ctx context.Context) {
	if err := a.reconcile(ctx); err != nil {
		a.logger.Error("jobAggregator: initial reconcile failed", "error", err)
	}

	go a.reconcileLoop(ctx)
	a.tailLoop(ctx)
}

func (a *jobAggregator) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(jobsAggregatorReconcileEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.reconcile(ctx); err != nil {
				a.logger.Error("jobAggregator: periodic reconcile failed", "error", err)
			}
		}
	}
}

// tailLoop supervises the shared fleet-wide tail, reconnecting with
// jittered exponential backoff whenever Tail returns for any reason other
// than ctx cancellation. Each attempt gets its own cancellable child
// context (attemptCtx) rather than reusing the long-lived ctx passed into
// Start: lokiTailer.Tail spawns an internal goroutine that watches its
// input ctx and closes the underlying connection when it's Done, but that
// goroutine only exits when *that specific call's* ctx is cancelled -- it
// does not exit merely because Tail returned (e.g. on a read error). Using
// the outer ctx for every attempt would leave one such goroutine parked
// per failed attempt until Start's own ctx is eventually cancelled, i.e.
// for the rest of the process's life. Cancelling attemptCtx immediately
// after each attempt ends tears that goroutine down promptly instead.
func (a *jobAggregator) tailLoop(ctx context.Context) {
	failures := 0
	for ctx.Err() == nil {
		attemptCtx, cancelAttempt := context.WithCancel(ctx)
		// The trailing `| job_id=~".+"` filter doesn't narrow which lines
		// match (every agent/brfs/bwfs line already carries a non-empty
		// job_id) -- it's there so Loki actually attaches job_id/event/status
		// structured metadata to the response at all. Confirmed against a
		// live Loki 3.7.3 instance: a bare label selector with no
		// structured-metadata reference in the query returns values with no
		// per-line metadata and no job_id/event/status hoisted to the stream
		// object either, so ingestTailMessage's jobID/event extraction below
		// silently sees everything as empty and drops every line -- exactly
		// mirroring queryEvent's own `| event="%s"` filter (jobs.go), which
		// works today only because it always references a metadata field.
		err := a.tailer.Tail(attemptCtx, `{binary=~"agent|brfs|bwfs"} | job_id=~".+"`, time.Now(), func(msg lokiTailMessage) error {
			a.ingestTailMessage(msg)
			return nil
		})
		cancelAttempt()
		if ctx.Err() != nil {
			return
		}

		failures++
		a.logger.Error("jobAggregator: tail ended unexpectedly, reconnecting", "failures", failures, "error", err)
		if rerr := a.reconcile(ctx); rerr != nil {
			a.logger.Error("jobAggregator: reconnect reconcile failed", "error", rerr)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(a.backoff(failures)):
		}
	}
}
