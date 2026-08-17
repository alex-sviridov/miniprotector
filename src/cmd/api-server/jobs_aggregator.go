// src/cmd/api-server/jobs_aggregator.go
package main

import (
	"log/slog"
	"sync"
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

	mu   sync.Mutex
	jobs map[string]jobDTO
	subs map[chan jobsStreamMsg]struct{}
}

func newJobAggregator(loki lokiQuerier, tailer lokiTailer, logger *slog.Logger) *jobAggregator {
	return &jobAggregator{
		loki:   loki,
		tailer: tailer,
		logger: logger,
		jobs:   make(map[string]jobDTO),
		subs:   make(map[chan jobsStreamMsg]struct{}),
	}
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
