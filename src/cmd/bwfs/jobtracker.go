package main

import "sync"

// jobTracker counts the number of currently active streams per backup job,
// so the server can detect when a job's last stream has closed.
type jobTracker struct {
	mu     sync.Mutex
	active map[string]int
}

func newJobTracker() *jobTracker {
	return &jobTracker{active: make(map[string]int)}
}

// Start records a new active stream for jobID.
func (t *jobTracker) Start(jobID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[jobID]++
}

// Finish records that a stream for jobID has ended. It returns true when
// this was the last active stream for that job (the count reached zero).
func (t *jobTracker) Finish(jobID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active[jobID]--
	if t.active[jobID] <= 0 {
		delete(t.active, jobID)
		return true
	}
	return false
}
