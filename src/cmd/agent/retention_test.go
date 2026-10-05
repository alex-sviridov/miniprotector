package main

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/retention"
)

func TestPrepareRetention_WritesFileLogsMatrixAndReturnsFlag(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	dir := t.TempDir()
	conf := &config.Config{RetentionDefaultDays: 7}

	args, err := prepareRetention(logger, conf, dir, "backup:p:/data:abcd1234", "job-1", "/data", nil)
	require.NoError(t, err)

	require.Len(t, args, 2)
	assert.Equal(t, "--retention-file", args[0])
	assert.Equal(t, dir, filepath.Dir(args[1]))

	m, err := retention.LoadFile(args[1])
	require.NoError(t, err)
	assert.Equal(t, int64(1_000_000+7*86400), m.ExpireAt("x", time.Unix(1_000_000, 0)))

	assert.Contains(t, buf.String(), `"event":"retention_matrix"`)
	assert.Contains(t, buf.String(), `"job_id":"job-1"`)
	assert.Contains(t, buf.String(), `"keep_seconds":604800`)
}

func TestPrepareRetention_UnwritableDirIsAnError(t *testing.T) {
	file := filepath.Join(t.TempDir(), "afile")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	_, err := prepareRetention(slog.Default(), &config.Config{RetentionDefaultDays: 7}, filepath.Join(file, "sub"), "t", "j", "/data", nil)
	assert.Error(t, err)
}

func TestSafeFileName(t *testing.T) {
	assert.Equal(t, "backup_p__data_abcd1234", safeFileName("backup:p:/data:abcd1234"))
}

func TestRetentionRulesFrom_FiltersSortsAndMaps(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cached := []cachedPolicy{
		{Name: "backup-1", Type: "backup"},
		{Name: "late", Type: "retention", Retention: &cachedRetention{BackupType: "filesystem", Path: "/late", KeepSeconds: 30, Priority: 5}},
		{Name: "early", Type: "retention", Retention: &cachedRetention{BackupType: "filesystem", Path: "/early", Include: []string{"*.log"}, KeepSeconds: 10, Priority: 1}},
		{Name: "tie-b", Type: "retention", Retention: &cachedRetention{BackupType: "filesystem", Path: "/tb", KeepSeconds: 1, Priority: 3}},
		{Name: "tie-a", Type: "retention", Retention: &cachedRetention{BackupType: "filesystem", Path: "/ta", KeepSeconds: 2, Priority: 3}},
		{Name: "db", Type: "retention", Retention: &cachedRetention{BackupType: "database", Path: "/db", KeepSeconds: 9, Priority: 2}},
		{Name: "disabled", Type: "retention", DisabledAt: past, Retention: &cachedRetention{BackupType: "filesystem", Path: "/off", KeepSeconds: 9, Priority: 2}},
		{Name: "no-rule", Type: "retention"},
	}

	got := retentionRulesFrom(cached)

	assert.Equal(t, []retention.Rule{
		{Prefix: "/early", Include: []string{"*.log"}, KeepSeconds: 10},
		{Prefix: "/ta", KeepSeconds: 2},
		{Prefix: "/tb", KeepSeconds: 1},
		{Prefix: "/late", KeepSeconds: 30},
	}, got)
}

func TestRetentionRulesFrom_NoRetentionPoliciesIsNil(t *testing.T) {
	assert.Nil(t, retentionRulesFrom(nil))
	assert.Nil(t, retentionRulesFrom([]cachedPolicy{{Name: "b", Type: "backup"}}))
}

func TestPrepareRetention_UsesRulesInPriorityOrderThenDefault(t *testing.T) {
	dir := t.TempDir()
	conf := &config.Config{RetentionDefaultDays: 7}
	rules := retentionRulesFrom([]cachedPolicy{
		{Name: "logs", Type: "retention", Retention: &cachedRetention{BackupType: "filesystem", Path: "/data/app/logs", Include: []string{"*.log"}, KeepSeconds: 3 * 86400, Priority: 1}},
		{Name: "app", Type: "retention", Retention: &cachedRetention{BackupType: "filesystem", Path: "/data/app", KeepSeconds: 30 * 86400, Priority: 2}},
		{Name: "elsewhere", Type: "retention", Retention: &cachedRetention{BackupType: "filesystem", Path: "/etc", KeepSeconds: 1, Priority: 3}},
	})

	args, err := prepareRetention(slog.Default(), conf, dir, "backup:p:/data/app:abcd1234", "job-1", "/data/app", rules)
	require.NoError(t, err)
	m, err := retention.LoadFile(args[1])
	require.NoError(t, err)

	now := time.Unix(1_000_000, 0)
	assert.Equal(t, now.Unix()+3*86400, m.ExpireAt("logs/x.log", now), "first matching rule wins")
	assert.Equal(t, now.Unix()+30*86400, m.ExpireAt("logs/x.txt", now), "falls through to the next matching rule")
	assert.Equal(t, now.Unix()+30*86400, m.ExpireAt("other.bin", now))
}
