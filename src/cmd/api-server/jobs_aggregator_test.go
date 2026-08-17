package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobAggregator_SubscribeReturnsCurrentSnapshot(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	agg.jobs["a"] = jobDTO{JobID: "a", Kind: "backup", State: "success"}

	snapshot, _, unsubscribe := agg.Subscribe()
	defer unsubscribe()

	require.Len(t, snapshot, 1)
	assert.Equal(t, "a", snapshot[0].JobID)
}

func TestJobAggregator_IngestTailMessageUpsertsAndBroadcasts(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	_, ch, unsubscribe := agg.Subscribe()
	defer unsubscribe()

	agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
		Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "start"},
		Values: []lokiValue{{Timestamp: 1752400500000000000}},
	}}})

	select {
	case msg := <-ch:
		assert.Equal(t, "upsert", msg.Type)
		require.NotNil(t, msg.Job)
		assert.Equal(t, "operating-refresh:1", msg.Job.JobID)
		assert.Equal(t, "in_progress", msg.Job.State)
	case <-time.After(time.Second):
		t.Fatal("no upsert broadcast")
	}

	agg.mu.Lock()
	stored, ok := agg.jobs["operating-refresh:1"]
	agg.mu.Unlock()
	require.True(t, ok, "ingested job must be stored in the aggregator's own state")
	assert.Equal(t, "in_progress", stored.State)
}

func TestJobAggregator_IngestTailMessageAppliesFinishOnTopOfStart(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	_, ch, unsubscribe := agg.Subscribe()
	defer unsubscribe()

	agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
		Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "start"},
		Values: []lokiValue{{Timestamp: 1752400500000000000}},
	}}})
	<-ch

	agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
		Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "finish", "status": "success"},
		Values: []lokiValue{{Timestamp: 1752400501000000000}},
	}}})

	select {
	case msg := <-ch:
		require.NotNil(t, msg.Job)
		assert.Equal(t, "success", msg.Job.State)
		require.NotNil(t, msg.Job.StartedAt, "the finish upsert must not lose the start line already ingested")
	case <-time.After(time.Second):
		t.Fatal("no upsert broadcast")
	}
}

func TestJobAggregator_SlowSubscriberDoesNotBlockBroadcast(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	_, _, unsubscribe := agg.Subscribe() // never read from -- simulates a slow/stuck browser
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		for i := 0; i < jobsAggregatorSubscriberBuffer+5; i++ {
			agg.ingestTailMessage(lokiTailMessage{Streams: []lokiStream{{
				Stream: map[string]string{"hostname": "h", "job_id": "policy-update:1", "event": "start"},
				Values: []lokiValue{{Timestamp: int64(i) * 1_000_000_000}},
			}}})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcast blocked on a full, unread subscriber channel")
	}
}
