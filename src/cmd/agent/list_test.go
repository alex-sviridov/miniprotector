package main

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEstimatedNextRun_NeverRunReturnsZeroValue(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	got := estimatedNextRun(p, PolicyState{}, time.Now())
	assert.True(t, got.IsZero())
}

func TestEstimatedNextRun_HealthyUsesLastSuccessPlusInterval(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	last := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	now := last.Add(1 * time.Minute) // within Interval, so not due yet
	got := estimatedNextRun(p, PolicyState{LastSuccessAt: &last}, now)
	assert.Equal(t, last.Add(5*time.Minute), got)
}

func TestEstimatedNextRun_FailingUsesStoredNextRetryAt(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	retryAt := time.Date(2026, 7, 3, 12, 5, 0, 0, time.UTC)
	now := retryAt.Add(-1 * time.Minute) // before the retry threshold, so not due yet
	got := estimatedNextRun(p, PolicyState{ConsecutiveFailures: 2, NextRetryAt: &retryAt}, now)
	assert.Equal(t, retryAt, got)
}

func TestEstimatedNextRun_NotDueDefersToCustomNextRunFunc(t *testing.T) {
	fixed := time.Date(2026, 7, 4, 2, 0, 0, 0, time.UTC)
	p := Policy{
		Due:     func(PolicyState, time.Time) bool { return false },
		NextRun: func(PolicyState, time.Time) time.Time { return fixed },
	}
	got := estimatedNextRun(p, PolicyState{}, time.Now())
	assert.Equal(t, fixed, got)
}

func TestRenderPolicies_MissingCacheShowsNeverRunAndDueNow(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{{ID: "bootstrap-refresh", Binary: "certclient", Interval: 24 * time.Hour}}

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, time.Now(), testPolicies))

	out := buf.String()
	assert.Contains(t, out, "bootstrap-refresh")
	assert.Contains(t, out, "never run")
	assert.Contains(t, out, "due now")
}

func TestRenderPolicies_HealthyPolicyShowsOkAndNotNeverRun(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{{ID: "operating-refresh", Binary: "certclient", Interval: 15 * time.Minute}}

	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"operating-refresh": {LastSuccessAt: &now},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, testPolicies))

	out := buf.String()
	assert.Contains(t, out, "ok")
	assert.NotContains(t, out, "never run")
}

func TestRenderPolicies_FailingPolicyShowsRetryingWithCount(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{{ID: "operating-refresh", Binary: "certclient", Interval: 15 * time.Minute}}

	now := time.Now()
	retryAt := now.Add(time.Minute)
	require.NoError(t, writeCache(cachePath, Cache{
		"operating-refresh": {LastAttemptAt: &now, ConsecutiveFailures: 3, NextRetryAt: &retryAt},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, testPolicies))

	assert.Contains(t, buf.String(), "retrying (3 failures)")
}
