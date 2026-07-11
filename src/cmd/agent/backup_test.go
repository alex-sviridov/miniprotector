package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCachedPolicies(t *testing.T, dir, json string) string {
	t.Helper()
	path := filepath.Join(dir, "policies-cache.json")
	require.NoError(t, os.WriteFile(path, []byte(json), 0o644))
	return path
}

func TestWindowOpen_TriggerJustInsideGraceReportsOpen(t *testing.T) {
	sched, err := cron.ParseStandard("0 2 * * *") // fires 02:00 daily
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 2, 30, 0, 0, time.UTC) // 30 min after trigger
	assert.True(t, windowOpen([]cron.Schedule{sched}, now, time.Hour))
}

func TestWindowOpen_TriggerJustOutsideGraceReportsClosed(t *testing.T) {
	sched, err := cron.ParseStandard("0 2 * * *")
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 3, 30, 0, 0, time.UTC) // 90 min after trigger
	assert.False(t, windowOpen([]cron.Schedule{sched}, now, time.Hour))
}

func TestWindowOpen_OneOfMultipleSchedulesRecentlyTriggeredStillOpen(t *testing.T) {
	morning, err := cron.ParseStandard("0 2 * * *")
	require.NoError(t, err)
	evening, err := cron.ParseStandard("0 20 * * *")
	require.NoError(t, err)
	now := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC) // just after the morning slot only
	assert.True(t, windowOpen([]cron.Schedule{morning, evening}, now, time.Hour))
}

func TestRpoElapsed_NeverSucceededIsElapsed(t *testing.T) {
	assert.True(t, rpoElapsed(PolicyState{}, time.Now(), time.Hour))
}

func TestRpoElapsed_RecentSuccessIsNotElapsed(t *testing.T) {
	now := time.Now()
	last := now.Add(-10 * time.Minute)
	assert.False(t, rpoElapsed(PolicyState{LastSuccessAt: &last}, now, time.Hour))
}

func TestRpoElapsed_OldSuccessIsElapsed(t *testing.T) {
	now := time.Now()
	last := now.Add(-2 * time.Hour)
	assert.True(t, rpoElapsed(PolicyState{LastSuccessAt: &last}, now, time.Hour))
}

func TestBackupTasks_OnePolicyWithTwoPathsYieldsTwoTasksWithStableDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"object_filters": ["/var/lib/postgres", "/etc/postgres"],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks := backupTasks(path, conf)

	require.Len(t, tasks, 2)
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "backup:daily-db-backup:/var/lib/postgres")
	assert.Contains(t, ids, "backup:daily-db-backup:/etc/postgres")
	assert.NotEqual(t, tasks[0].ID, tasks[1].ID)
}

func TestBackupTasks_TaskArgsMatchBrfsShape(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"object_filters": ["/var/lib/postgres"],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks := backupTasks(path, conf)

	require.Len(t, tasks, 1)
	task := tasks[0]
	assert.Equal(t, "brfs", task.Binary)
	require.Len(t, task.Args, 5)
	assert.Equal(t, "/var/lib/postgres", task.Args[0])
	assert.Equal(t, "--destination", task.Args[1])
	assert.Equal(t, "bwfs-east:8080", task.Args[2])
	assert.Equal(t, "--job-id", task.Args[3])
	assert.Contains(t, task.Args[4], "backup:daily-db-backup:var-lib-postgres:")
	assert.True(t, task.Background)
}

func TestBackupTasks_DueRequiresBothWindowOpenAndRpoElapsed(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": ["/data"],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks := backupTasks(path, conf)
	require.Len(t, tasks, 1)
	task := tasks[0]

	windowOpenTime := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC)
	windowClosedTime := time.Date(2026, 7, 4, 10, 0, 0, 0, time.UTC)
	recent := windowOpenTime.Add(-10 * time.Minute)
	old := windowOpenTime.Add(-2 * time.Hour)

	assert.False(t, task.Due(PolicyState{LastSuccessAt: &recent}, windowOpenTime), "window open but RPO not elapsed: not due")
	assert.False(t, task.Due(PolicyState{LastSuccessAt: &old}, windowClosedTime), "RPO elapsed but window closed: not due")
	assert.True(t, task.Due(PolicyState{LastSuccessAt: &old}, windowOpenTime), "both true: due")
	assert.True(t, task.Due(PolicyState{}, windowOpenTime), "never run and window open: due")
}

func TestBackupTasks_PerPathIndependence(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": ["/a", "/b"],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks := backupTasks(path, conf)
	require.Len(t, tasks, 2)

	windowOpenTime := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC)
	recent := windowOpenTime.Add(-10 * time.Minute)

	var taskA, taskB Policy
	for _, task := range tasks {
		if task.ID == "backup:p:/a" {
			taskA = task
		} else {
			taskB = task
		}
	}
	// /a recently succeeded (not due); /b never ran (due) -- proves one
	// path's state has no effect on its sibling's due-check.
	assert.False(t, taskA.Due(PolicyState{LastSuccessAt: &recent}, windowOpenTime))
	assert.True(t, taskB.Due(PolicyState{}, windowOpenTime))
}

func TestBackupTasks_UnparseableRpoSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": ["/data"],
		"rpo": "not-a-duration",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	assert.Empty(t, backupTasks(path, conf))
}

func TestBackupTasks_NoValidBackupWindowSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": ["/data"],
		"rpo": "1h",
		"backup_window": ["not a cron expression"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	assert.Empty(t, backupTasks(path, conf))
}

func TestBackupTasks_MissingCacheFileYieldsNoTasks(t *testing.T) {
	conf := &config.Config{BackupWindowGraceSec: 3600}
	assert.Empty(t, backupTasks(filepath.Join(t.TempDir(), "does-not-exist.json"), conf))
}

func TestBackupTasks_RemovedPolicyStopsBeingDerived(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	conf := &config.Config{BackupWindowGraceSec: 3600}

	require.NoError(t, os.WriteFile(cachePath, []byte(`[{
		"name": "p", "object_filters": ["/data"], "rpo": "1h",
		"backup_window": ["0 2 * * *"], "destination": "bwfs:8080"
	}]`), 0o644))
	require.Len(t, backupTasks(cachePath, conf), 1)

	require.NoError(t, os.WriteFile(cachePath, []byte(`[]`), 0o644))
	assert.Empty(t, backupTasks(cachePath, conf))
}
