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
