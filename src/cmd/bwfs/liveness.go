package main

import (
	"sync"
	"time"
)

// jobLiveness tracks, per backup job, when the server last saw any activity
// (a stream opening or any FileRequest received) and whether the job has
// already been finalized (success or failure). The stall watchdog uses
// StaleJobs to find jobs that have gone silent; the stream handler uses
// IsFinalized as a cheap in-memory check to reject further messages for a
// job whose outcome has already been decided, without hitting the database
// on every message.
type jobLiveness struct {
	mu        sync.Mutex
	lastSeen  map[string]time.Time
	finalized map[string]bool
}

func newJobLiveness() *jobLiveness {
	return &jobLiveness{
		lastSeen:  make(map[string]time.Time),
		finalized: make(map[string]bool),
	}
}

// Touch records activity for jobID now. Must not be called after Complete
// for the same jobID — callers check IsFinalized first.
func (l *jobLiveness) Touch(jobID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lastSeen[jobID] = time.Now()
}

// Complete marks jobID as finalized and stops tracking its liveness.
func (l *jobLiveness) Complete(jobID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.lastSeen, jobID)
	l.finalized[jobID] = true
}

// IsFinalized reports whether Complete has been called for jobID.
func (l *jobLiveness) IsFinalized(jobID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.finalized[jobID]
}

// StaleJobs returns the IDs of jobs whose last recorded activity is older
// than timeout.
func (l *jobLiveness) StaleJobs(timeout time.Duration) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := time.Now().Add(-timeout)
	var stale []string
	for jobID, seen := range l.lastSeen {
		if seen.Before(cutoff) {
			stale = append(stale, jobID)
		}
	}
	return stale
}
