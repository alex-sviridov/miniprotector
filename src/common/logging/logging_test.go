package logging

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testContext(logDir, appName string) context.Context {
	ctx := context.WithValue(context.Background(), "appName", appName)
	ctx = context.WithValue(ctx, config.ContextKey, &config.Config{LogDir: logDir})
	ctx = context.WithValue(ctx, "debugMode", false)
	ctx = context.WithValue(ctx, "quietMode", true)
	return ctx
}

func TestNewLogger_WritesToStableBinaryNamedFile(t *testing.T) {
	dir := t.TempDir()
	logger, closer := NewLogger(testContext(dir, "testbinary"))
	defer closer.Close()

	logger.Info("hello")

	data, err := os.ReadFile(filepath.Join(dir, "testbinary.log"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "hello")
}

func TestNewLogger_WritesValidJSONLines(t *testing.T) {
	dir := t.TempDir()
	logger, closer := NewLogger(testContext(dir, "testbinary"))
	logger.Info("structured", "key", "value")
	require.NoError(t, closer.Close())

	data, err := os.ReadFile(filepath.Join(dir, "testbinary.log"))
	require.NoError(t, err)

	var entry map[string]any
	require.NoError(t, json.Unmarshal(data, &entry))
	assert.Equal(t, "structured", entry["msg"])
	assert.Equal(t, "value", entry["key"])
}

func TestNewLogger_TwoLoggersSameBinaryAppendSameFile(t *testing.T) {
	dir := t.TempDir()

	logger1, closer1 := NewLogger(testContext(dir, "testbinary"))
	logger1.Info("first")
	require.NoError(t, closer1.Close())

	logger2, closer2 := NewLogger(testContext(dir, "testbinary"))
	logger2.Info("second")
	require.NoError(t, closer2.Close())

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "both loggers must write to one stable file, not one per invocation")

	data, err := os.ReadFile(filepath.Join(dir, "testbinary.log"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "first")
	assert.Contains(t, string(data), "second")
}

func TestNewLogger_CloserIsNilSafeWhenLogDirEmpty(t *testing.T) {
	_, closer := NewLogger(testContext("", "testbinary"))
	assert.NoError(t, closer.Close(), "Close must be safe to call even when no file handler was created")
}

func TestNewLogger_DifferentBinariesGetDifferentFiles(t *testing.T) {
	dir := t.TempDir()

	logger1, closer1 := NewLogger(testContext(dir, "binary-a"))
	logger1.Info("from a")
	require.NoError(t, closer1.Close())

	logger2, closer2 := NewLogger(testContext(dir, "binary-b"))
	logger2.Info("from b")
	require.NoError(t, closer2.Close())

	_, err := os.Stat(filepath.Join(dir, "binary-a.log"))
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, "binary-b.log"))
	assert.NoError(t, err)
}
