package main

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestJobTracker_FinishReturnsTrueOnlyForLastStream(t *testing.T) {
	tr := newJobTracker()
	tr.Start("job-1")
	tr.Start("job-1")

	assert.False(t, tr.Finish("job-1"), "first Finish: one stream still active")
	assert.True(t, tr.Finish("job-1"), "second Finish: last stream closing should report true")
}

func TestJobTracker_IndependentJobs(t *testing.T) {
	tr := newJobTracker()
	tr.Start("job-1")
	tr.Start("job-2")

	assert.True(t, tr.Finish("job-1"), "job-1 has no more active streams")
	assert.True(t, tr.Finish("job-2"), "job-2 has no more active streams")
}

func TestJobTracker_ConcurrentStartFinish(t *testing.T) {
	tr := newJobTracker()
	const streams = 50

	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tr.Start("job-1")
		}()
	}
	wg.Wait()

	var mu sync.Mutex
	lastCount := 0
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if tr.Finish("job-1") {
				mu.Lock()
				lastCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, lastCount, "exactly one Finish call should observe the last-stream transition")
}
