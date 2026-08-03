package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRunCheckinCleanup_RemovesRecordsOlderThanRetention(t *testing.T) {
	store := newTestCheckinStore(t)
	require.NoError(t, store.RecordCheckin("policy-1", "stale-host", time.Now().Add(-time.Hour)))
	require.NoError(t, store.RecordCheckin("policy-1", "fresh-host", time.Now()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runCheckinCleanup(ctx, store, 5*time.Millisecond, 10*time.Minute, testLogger())

	require.Eventually(t, func() bool {
		records, err := store.CheckinsForPolicy("policy-1")
		return err == nil && len(records) == 1 && records[0].Hostname == "fresh-host"
	}, 2*time.Second, 10*time.Millisecond, "cleanup should remove only the stale record")
}

func TestRunCheckinCleanup_StopsWhenContextCancelled(t *testing.T) {
	store := newTestCheckinStore(t)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		runCheckinCleanup(ctx, store, 5*time.Millisecond, time.Minute, testLogger())
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runCheckinCleanup did not return after context cancellation")
	}
}
