package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJobAggregator_SubscribeReturnsCurrentSnapshot(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "a", "event": "start"},
				Values: []lokiValue{{Timestamp: 1752400500000000000}}},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "a", "event": "finish", "status": "success"},
				Values: []lokiValue{{Timestamp: 1752400501000000000}}},
		},
	}}
	agg := newJobAggregator(fake, &fakeLokiTailer{}, testLogger())

	snapshot, _, unsubscribe := agg.Subscribe(context.Background())
	defer unsubscribe()

	require.Len(t, snapshot, 1)
	assert.Equal(t, "a", snapshot[0].JobID)
}

func TestJobAggregator_IngestTailMessageUpsertsAndBroadcasts(t *testing.T) {
	agg := newJobAggregator(&fakeLokiClient{}, &fakeLokiTailer{}, testLogger())
	_, ch, unsubscribe := agg.Subscribe(context.Background())
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
	_, ch, unsubscribe := agg.Subscribe(context.Background())
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
	_, _, unsubscribe := agg.Subscribe(context.Background()) // never read from -- simulates a slow/stuck browser
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

func TestJobAggregator_ReconcileReplacesStateFromLoki(t *testing.T) {
	fakeLoki := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "start"},
				Values: []lokiValue{{Timestamp: 1752400500000000000}}},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "finish", "status": "success"},
				Values: []lokiValue{{Timestamp: 1752400501000000000}}},
		},
	}}
	agg := newJobAggregator(fakeLoki, &fakeLokiTailer{}, testLogger())

	require.NoError(t, agg.reconcile(context.Background()))

	agg.mu.Lock()
	job, ok := agg.jobs["operating-refresh:1"]
	agg.mu.Unlock()
	require.True(t, ok)
	assert.Equal(t, "success", job.State)
}

func TestJobAggregator_ReconcileBroadcastsSnapshot(t *testing.T) {
	fakeLoki := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}
	agg := newJobAggregator(fakeLoki, &fakeLokiTailer{}, testLogger())
	_, ch, unsubscribe := agg.Subscribe(context.Background())
	defer unsubscribe()

	require.NoError(t, agg.reconcile(context.Background()))

	select {
	case msg := <-ch:
		assert.Equal(t, "snapshot", msg.Type)
	case <-time.After(time.Second):
		t.Fatal("reconcile must broadcast a snapshot even when nothing changed")
	}
}

// countingReconcileLokiClient wraps a fakeLokiClient and counts QueryRange calls --
// lets these tests assert whether Loki was queried at all, not just what
// it returned.
type countingReconcileLokiClient struct {
	fakeLokiClient
	calls atomic.Int32
}

func (c *countingReconcileLokiClient) QueryRange(ctx context.Context, query string, start, end time.Time, limit int) ([]lokiStream, error) {
	c.calls.Add(1)
	return c.fakeLokiClient.QueryRange(ctx, query, start, end, limit)
}

func TestJobAggregator_ReconcileIfSubscribedSkipsWhenNoSubscribers(t *testing.T) {
	counting := &countingReconcileLokiClient{fakeLokiClient: fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}}
	agg := newJobAggregator(counting, &fakeLokiTailer{}, testLogger())

	require.NoError(t, agg.reconcileIfSubscribed(context.Background()))

	assert.EqualValues(t, 0, counting.calls.Load(), "must not query Loki when no subscriber is connected")
}

func TestJobAggregator_ReconcileIfSubscribedRunsWithSubscriber(t *testing.T) {
	counting := &countingReconcileLokiClient{fakeLokiClient: fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}}
	agg := newJobAggregator(counting, &fakeLokiTailer{}, testLogger())
	_, _, unsubscribe := agg.Subscribe(context.Background())
	defer unsubscribe()
	counting.calls.Store(0) // reset: Subscribe's own first-subscriber reconcile already ran once

	require.NoError(t, agg.reconcileIfSubscribed(context.Background()))

	// reconcile issues two QueryRange calls per run (one for event="start",
	// one for event="finish" -- see queryEvent/jobs.go), so one reconcile
	// here means calls == 2, not 1.
	assert.EqualValues(t, 2, counting.calls.Load(), "must query Loki when at least one subscriber is connected")
}

func TestJobAggregator_SubscribeTriggersReconcileOnFirstSubscriber(t *testing.T) {
	fake := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`: {
			{Stream: map[string]string{"hostname": "webserver", "job_id": "operating-refresh:1", "event": "start"},
				Values: []lokiValue{{Timestamp: 1752400500000000000}}},
		},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}
	agg := newJobAggregator(fake, &fakeLokiTailer{}, testLogger())

	snapshot, _, unsubscribe := agg.Subscribe(context.Background())
	defer unsubscribe()

	require.Len(t, snapshot, 1, "the first subscriber must see freshly reconciled state")
	assert.Equal(t, "operating-refresh:1", snapshot[0].JobID)
}

func TestJobAggregator_SecondSequentialSubscriberDoesNotTriggerExtraReconcile(t *testing.T) {
	counting := &countingReconcileLokiClient{fakeLokiClient: fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}}
	agg := newJobAggregator(counting, &fakeLokiTailer{}, testLogger())

	_, _, unsubscribe1 := agg.Subscribe(context.Background())
	defer unsubscribe1()
	// reconcile issues two QueryRange calls per run (event="start" and
	// event="finish" -- see queryEvent/jobs.go), so one reconcile here means
	// calls == 2, not 1.
	require.EqualValues(t, 2, counting.calls.Load())

	_, _, unsubscribe2 := agg.Subscribe(context.Background())
	defer unsubscribe2()

	// This second Subscribe call is sequential (issued only after the
	// first has already returned and registered its channel), not
	// concurrent -- it exercises the already-subscribed (subscriberCount()
	// > 0) path, not the 0->1 race. See Subscribe's doc comment for the
	// separate, genuinely-concurrent case this test does not cover.
	assert.EqualValues(t, 2, counting.calls.Load(), "a second sequential subscriber must not trigger another reconcile")
}

// blockingTailer's Tail call fails errCount times, then succeeds
// (delivering nothing, just blocking on ctx) -- lets the test observe
// jobAggregator.Start reconnecting with backoff and re-reconciling before
// each attempt, without a real Loki.
type blockingTailer struct {
	failuresBeforeSuccess int32
	attempts              atomic.Int32
}

func (b *blockingTailer) Tail(ctx context.Context, query string, start time.Time, onMessage func(lokiTailMessage) error) error {
	n := b.attempts.Add(1)
	if n <= b.failuresBeforeSuccess {
		return errors.New("simulated tail failure")
	}
	<-ctx.Done()
	return nil
}

func TestJobAggregator_StartReconnectsAfterTailFailure(t *testing.T) {
	fakeLoki := &fakeLokiClient{byQuery: map[string][]lokiStream{
		`{binary=~"agent|brfs|bwfs"} | event="start"`:  {},
		`{binary=~"agent|brfs|bwfs"} | event="finish"`: {},
	}}
	tailer := &blockingTailer{failuresBeforeSuccess: 2}
	agg := newJobAggregator(fakeLoki, tailer, testLogger())
	agg.backoffBase = time.Millisecond // keep the test fast

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go agg.Start(ctx)

	require.Eventually(t, func() bool {
		return tailer.attempts.Load() >= 3
	}, 2*time.Second, 10*time.Millisecond, "expected at least 2 failed attempts + 1 successful reconnect")
}
