package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestJobLiveness_TouchThenStaleJobsExcludesFreshJob(t *testing.T) {
	l := newJobLiveness()
	l.Touch("job-1")

	stale := l.StaleJobs(time.Hour)
	assert.Empty(t, stale, "a job touched moments ago must not be stale")
}

func TestJobLiveness_StaleJobsIncludesOldEntry(t *testing.T) {
	l := newJobLiveness()
	l.mu.Lock()
	l.lastSeen["job-old"] = time.Now().Add(-2 * time.Hour)
	l.mu.Unlock()

	stale := l.StaleJobs(time.Hour)
	assert.Equal(t, []string{"job-old"}, stale)
}

func TestJobLiveness_CompleteMarksFinalizedAndRemovesFromLastSeen(t *testing.T) {
	l := newJobLiveness()
	l.Touch("job-1")
	l.Complete("job-1")

	assert.True(t, l.IsFinalized("job-1"))
	l.mu.Lock()
	_, stillTracked := l.lastSeen["job-1"]
	l.mu.Unlock()
	assert.False(t, stillTracked, "a completed job must not still count as active for staleness checks")
}

func TestJobLiveness_IsFinalizedFalseForUnknownJob(t *testing.T) {
	l := newJobLiveness()
	assert.False(t, l.IsFinalized("never-seen"))
}
