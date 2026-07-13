package main

import (
	"encoding/json"
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

func TestShortID_TruncatesToEightHexCharsAfterStrippingDashes(t *testing.T) {
	assert.Equal(t, "aaaaaaaa", shortID("aaaaaaaa-1111-1111-1111-111111111111"))
}

func TestShortID_EmptyInputReturnsEmpty(t *testing.T) {
	assert.Equal(t, "", shortID(""))
}

func TestShortID_ShorterThanEightCharsReturnedUnchanged(t *testing.T) {
	assert.Equal(t, "abcd", shortID("ab-cd"))
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

func TestReadCachedPolicies_MissingFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	policies, ok := readCachedPolicies(filepath.Join(dir, "does-not-exist.json"))
	assert.False(t, ok)
	assert.Empty(t, policies)
}

func TestReadCachedPolicies_CorruptFileReturnsOkFalse(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	policies, ok := readCachedPolicies(path)
	assert.False(t, ok)
	assert.Empty(t, policies)
}

func TestReadCachedPolicies_ValidEmptyListReturnsOkTrue(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[]`)
	policies, ok := readCachedPolicies(path)
	assert.True(t, ok)
	assert.Empty(t, policies)
}

func TestBackupTasks_OnePolicyWithTwoPathsYieldsTwoTasksWithStableDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/var/lib/postgres"},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/etc/postgres"}
		],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 2)
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "backup:daily-db-backup:/var/lib/postgres:aaaaaaaa")
	assert.Contains(t, ids, "backup:daily-db-backup:/etc/postgres:bbbbbbbb")
	assert.NotEqual(t, tasks[0].ID, tasks[1].ID)
}

func TestBackupTasks_ObjectFiltersSharingPathGetDistinctTaskIDs(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "web-policy",
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/var/www", "include": ["*.html"]},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/var/www", "exclude": ["*.log"]}
		],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 2)
	assert.NotEqual(t, tasks[0].ID, tasks[1].ID, "two object filters sharing a path must get distinct task IDs")
	ids := []string{tasks[0].ID, tasks[1].ID}
	assert.Contains(t, ids, "backup:web-policy:/var/www:aaaaaaaa")
	assert.Contains(t, ids, "backup:web-policy:/var/www:bbbbbbbb")
}

func TestBackupTasks_TaskArgsMatchBrfsShape(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "daily-db-backup",
		"object_filters": [{"path": "/var/lib/postgres"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs-east:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
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
		"object_filters": [{"path": "/data"}],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	require.True(t, ok)
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
		"object_filters": [
			{"id": "aaaaaaaa-1111-1111-1111-111111111111", "path": "/a"},
			{"id": "bbbbbbbb-2222-2222-2222-222222222222", "path": "/b"}
		],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	require.True(t, ok)
	require.Len(t, tasks, 2)

	windowOpenTime := time.Date(2026, 7, 4, 2, 10, 0, 0, time.UTC)
	recent := windowOpenTime.Add(-10 * time.Minute)

	var taskA, taskB Policy
	for _, task := range tasks {
		if task.ID == "backup:p:/a:aaaaaaaa" {
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
		"object_filters": [{"path": "/data"}],
		"rpo": "not-a-duration",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	assert.True(t, ok, "the file itself was still validly read")
	assert.Empty(t, tasks)
}

func TestBackupTasks_NoValidBackupWindowSkipsPolicyEntirely(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "p",
		"object_filters": [{"path": "/data"}],
		"rpo": "1h",
		"backup_window": ["not a cron expression"],
		"destination": "bwfs:8080"
	}]`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	assert.True(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_MissingCacheFileReturnsOkFalseWithNoTasks(t *testing.T) {
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(filepath.Join(t.TempDir(), "does-not-exist.json"), conf)
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_CorruptCacheFileReturnsOkFalseWithNoTasks(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `not json`)
	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)
	assert.False(t, ok)
	assert.Empty(t, tasks)
}

func TestBackupTasks_JobIDFieldMatchesArgsFlag(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	cached := []cachedPolicy{{
		Name:          "web-policy",
		ObjectFilters: []ObjectFilter{{Path: "/srv/web"}},
		RPO:           "1h",
		BackupWindow:  []string{"* * * * *"},
		Destination:   "bwfs:9000",
	}}
	data, err := json.Marshal(cached)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cachePath, data, 0o644))

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(cachePath, conf)
	require.True(t, ok)
	require.Len(t, tasks, 1)

	task := tasks[0]
	assert.NotEmpty(t, task.JobID)
	assert.Contains(t, task.Args, "--job-id")
	assert.Contains(t, task.Args, task.JobID)
}

func TestBackupTasks_RemovedPolicyStopsBeingDerived(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	conf := &config.Config{BackupWindowGraceSec: 3600}

	require.NoError(t, os.WriteFile(cachePath, []byte(`[{
		"name": "p", "object_filters": [{"path": "/data"}], "rpo": "1h",
		"backup_window": ["0 2 * * *"], "destination": "bwfs:8080"
	}]`), 0o644))
	tasks, ok := backupTasks(cachePath, conf)
	require.True(t, ok)
	require.Len(t, tasks, 1)

	require.NoError(t, os.WriteFile(cachePath, []byte(`[]`), 0o644))
	tasks, ok = backupTasks(cachePath, conf)
	assert.True(t, ok, "an empty-but-valid file is still a confirmed-good read")
	assert.Empty(t, tasks)
}

func TestBackupTasks_TaskArgsIncludeIncludeExcludeFlagsWhenPresent(t *testing.T) {
	dir := t.TempDir()
	path := writeCachedPolicies(t, dir, `[{
		"name": "web-policy",
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "1h",
		"backup_window": ["0 2 * * *"],
		"destination": "bwfs:8080"
	}]`)

	conf := &config.Config{BackupWindowGraceSec: 3600}
	tasks, ok := backupTasks(path, conf)

	require.True(t, ok)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.Len(t, task.Args, 9)
	assert.Equal(t, "/var/www", task.Args[0])
	assert.Equal(t, "--destination", task.Args[1])
	assert.Equal(t, "bwfs:8080", task.Args[2])
	assert.Equal(t, "--job-id", task.Args[3])
	assert.Equal(t, "--include", task.Args[5])
	assert.Equal(t, "*.html,*.css", task.Args[6])
	assert.Equal(t, "--exclude", task.Args[7])
	assert.Equal(t, "*.tmp", task.Args[8])
}
