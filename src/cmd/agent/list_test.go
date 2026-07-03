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
	got := estimatedNextRun(p, PolicyState{})
	assert.True(t, got.IsZero())
}

func TestEstimatedNextRun_HealthyUsesLastSuccessPlusInterval(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	last := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	got := estimatedNextRun(p, PolicyState{LastSuccessAt: &last})
	assert.Equal(t, last.Add(5*time.Minute), got)
}

func TestEstimatedNextRun_FailingUsesStoredNextRetryAt(t *testing.T) {
	p := Policy{Interval: 5 * time.Minute}
	retryAt := time.Date(2026, 7, 3, 12, 5, 0, 0, time.UTC)
	got := estimatedNextRun(p, PolicyState{ConsecutiveFailures: 2, NextRetryAt: &retryAt})
	assert.Equal(t, retryAt, got)
}

func TestRenderPolicies_MissingCacheShowsNeverRunAndDueNow(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, time.Now()))

	out := buf.String()
	assert.Contains(t, out, "cert-refresh")
	assert.Contains(t, out, "never run")
	assert.Contains(t, out, "due now")
}

func TestRenderPolicies_HealthyPolicyShowsOkAndNotNeverRun(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"cert-refresh": {LastSuccessAt: &now},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now))

	out := buf.String()
	assert.Contains(t, out, "ok")
	assert.NotContains(t, out, "never run")
}

func TestRenderPolicies_FailingPolicyShowsRetryingWithCount(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	now := time.Now()
	retryAt := now.Add(time.Minute)
	require.NoError(t, writeCache(cachePath, Cache{
		"cert-refresh": {LastAttemptAt: &now, ConsecutiveFailures: 3, NextRetryAt: &retryAt},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now))

	assert.Contains(t, buf.String(), "retrying (3 failures)")
}
