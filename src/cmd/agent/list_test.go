package main

import (
	"bytes"
	"path/filepath"
	"strings"
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
	require.NoError(t, renderPolicies(&buf, cachePath, time.Now(), testPolicies, nil))

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
	require.NoError(t, renderPolicies(&buf, cachePath, now, testPolicies, nil))

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
	require.NoError(t, renderPolicies(&buf, cachePath, now, testPolicies, nil))

	assert.Contains(t, buf.String(), "retrying (3 failures)")
}

func TestFormatError_EmptyReturnsDash(t *testing.T) {
	assert.Equal(t, "-", formatError(""))
}

func TestFormatError_ShortReturnsUnchanged(t *testing.T) {
	assert.Equal(t, "boom", formatError("boom"))
}

func TestFormatError_LongIsTruncatedWithEllipsis(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := formatError(long)
	// maxErrorColumnWidth is a rune-count cap, not a byte-count cap -- "…"
	// is one rune but three UTF-8 bytes, so the check must count runes.
	assert.LessOrEqual(t, len([]rune(got)), maxErrorColumnWidth)
	assert.True(t, strings.HasSuffix(got, "…"))
}

func TestRenderPolicies_FailingPolicyShowsLastError(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{{ID: "operating-refresh", Binary: "certclient", Interval: 15 * time.Minute}}

	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"operating-refresh": {LastAttemptAt: &now, ConsecutiveFailures: 1, LastError: "connection refused"},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, testPolicies, nil))

	assert.Contains(t, buf.String(), "connection refused")
}

func TestRenderPolicies_LongLastErrorIsTruncatedInTable(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	testPolicies := []Policy{{ID: "p", Binary: "b", Interval: time.Minute}}

	longErr := strings.Repeat("x", 200)
	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"p": {LastAttemptAt: &now, ConsecutiveFailures: 1, LastError: longErr},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, testPolicies, nil))

	out := buf.String()
	assert.NotContains(t, out, longErr, "the full 200-char error must not appear verbatim in table output")
}

func TestRenderPolicies_StorageTaskShowsOkAndDashForNextRun(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	now := time.Now()
	require.NoError(t, writeCache(cachePath, Cache{
		"storage:east-1-storage": {LastSuccessAt: &now},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, nil, []storageTask{{ID: "storage:east-1-storage"}}))

	out := buf.String()
	assert.Contains(t, out, "storage:east-1-storage")
	assert.Contains(t, out, "ok")
}

func TestRenderPolicies_StorageTaskCrashLoopingShowsRetryingWithCount(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")

	now := time.Now()
	retryAt := now.Add(time.Minute)
	require.NoError(t, writeCache(cachePath, Cache{
		"storage:east-1-storage": {LastAttemptAt: &now, ConsecutiveFailures: 4, NextRetryAt: &retryAt, LastError: "bind: address already in use"},
	}))

	var buf bytes.Buffer
	require.NoError(t, renderPolicies(&buf, cachePath, now, nil, []storageTask{{ID: "storage:east-1-storage"}}))

	out := buf.String()
	assert.Contains(t, out, "retrying (4 failures)")
	assert.Contains(t, out, "bind: address already in use")
}
