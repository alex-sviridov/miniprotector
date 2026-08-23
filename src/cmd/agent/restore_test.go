package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCachedPoliciesJSON mirrors backup_test.go's writeCachedPolicies, but
// takes structured []cachedPolicy values instead of a raw JSON string --
// named distinctly to avoid colliding with backup_test.go's existing
// helper of the same base name but a different signature.
func writeCachedPoliciesJSON(t *testing.T, path string, policies []cachedPolicy) {
	t.Helper()
	data, err := json.Marshal(policies)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o644))
}

func TestRestoreTasks_OneTaskPerRestorePolicy(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{
			Name: "web01-emergency", Type: "restore", JobID: "verify:web01-emergency:1700000000",
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		},
		{Name: "nightly", Type: "backup"}, // must contribute zero restore tasks
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, "verify:web01-emergency", tasks[0].ID)
	assert.Equal(t, "rwfs", tasks[0].Binary)
	assert.Equal(t, "verify:web01-emergency:1700000000", tasks[0].JobID, "job id must come from the cached policy, not be generated here")
	assert.Equal(t, []string{"verify", "bwfs-1:8080", "--rules-stdin", "--job-id", tasks[0].JobID}, tasks[0].Args)
	assert.True(t, tasks[0].Background)

	var payload struct {
		Rules []RestoreRule `json:"rules"`
	}
	require.NoError(t, json.Unmarshal(tasks[0].Stdin, &payload))
	assert.Equal(t, []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}}, payload.Rules)
}

func TestRestoreTasks_NoDestinationsSkipsWithNoTask(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{Name: "dangling", Type: "restore", Rules: []RestoreRule{{Path: "/x", Include: true}}},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	assert.Empty(t, tasks)
}

// A rules-less restore policy must contribute no task: `rwfs verify
// --rules-stdin` with zero rules selects zero files, so it would report
// success without verifying anything, and this one-shot task would record
// that as permanently done.
func TestRestoreTasks_NoRulesSkipsWithNoTask(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{Name: "rules-less", Type: "restore", Destinations: []string{"bwfs-1:8080"}},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	assert.Empty(t, tasks)
}

func TestRestoreTasks_DisabledPolicySkipped(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{
			Name: "old", Type: "restore",
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Path: "/x", Include: true}},
			DisabledAt:   time.Now().Add(-time.Hour),
		},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	assert.Empty(t, tasks)
}

func TestRestoreTasks_UnreadableCacheReturnsNotOK(t *testing.T) {
	_, ok := restoreTasks(filepath.Join(t.TempDir(), "missing.json"), testLogger())
	assert.False(t, ok)
}

func TestRestoreTasks_DueUntilFirstAttemptThenNeverAgain(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{Name: "x", Type: "restore", JobID: "verify:x:1700000000", Destinations: []string{"bwfs-1:8080"}, Rules: []RestoreRule{{Path: "/x", Include: true}}},
	})
	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)

	now := time.Now()
	assert.True(t, tasks[0].Due(PolicyState{}, now), "never attempted is due")
	attempted := now.Add(-time.Minute)
	assert.False(t, tasks[0].Due(PolicyState{LastAttemptAt: &attempted}, now), "attempted once (even if it failed) is never due again")
}

func TestRestoreTasks_RestoreModeUsesRestorePrefixAndRestoreSubcommand(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{
			Name: "web01-actual-restore", Type: "restore", Mode: "restore", Overwrite: true, JobID: "restore:web01-actual-restore:1700000000",
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, "restore:web01-actual-restore", tasks[0].ID)
	assert.Equal(t, "restore:web01-actual-restore:1700000000", tasks[0].JobID)
	assert.Equal(t, []string{"restore", "bwfs-1:8080", "--rules-stdin", "--job-id", tasks[0].JobID, "--overwrite"}, tasks[0].Args)
}

func TestRestoreTasks_RestoreModeWithoutOverwriteOmitsFlag(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{
			Name: "web01-actual-restore", Type: "restore", Mode: "restore", Overwrite: false, JobID: "restore:web01-actual-restore:1700000000",
			Destinations: []string{"bwfs-1:8080"},
			Rules:        []RestoreRule{{Host: "web-01", Path: "/var/www/index.html", Include: true}},
		},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	assert.Equal(t, []string{"restore", "bwfs-1:8080", "--rules-stdin", "--job-id", tasks[0].JobID}, tasks[0].Args)
}

func TestRestoreRule_TimeframeRoundTripsThroughJSON(t *testing.T) {
	rule := RestoreRule{Host: "h", Path: "/etc", Include: true, NotBefore: 100, NotAfter: 200}
	data, err := json.Marshal(rule)
	require.NoError(t, err)

	var decoded RestoreRule
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, int64(100), decoded.NotBefore)
	assert.Equal(t, int64(200), decoded.NotAfter)
}

func TestRestoreRule_DestPathRoundTripsThroughJSON(t *testing.T) {
	rule := RestoreRule{Host: "h", Path: "/etc/a", Include: true, DestPath: "/etc/a.bak"}
	data, err := json.Marshal(rule)
	require.NoError(t, err)

	var decoded RestoreRule
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "/etc/a.bak", decoded.DestPath)
}
