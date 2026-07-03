package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCache_MissingFileReturnsEmptyCache(t *testing.T) {
	dir := t.TempDir()
	c, err := readCache(filepath.Join(dir, "agent-state.json"))
	require.NoError(t, err)
	assert.Empty(t, c)
}

func TestReadCache_CorruptFileReturnsEmptyCache(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))

	c, err := readCache(path)
	require.NoError(t, err)
	assert.Empty(t, c)
}

func TestWriteCacheThenReadCache_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")

	now := time.Now().UTC().Truncate(time.Second)
	c := Cache{
		"cert-refresh": {LastSuccessAt: &now, ConsecutiveFailures: 0},
	}
	require.NoError(t, writeCache(path, c))

	got, err := readCache(path)
	require.NoError(t, err)
	require.Contains(t, got, "cert-refresh")
	assert.True(t, got["cert-refresh"].LastSuccessAt.Equal(now))
	assert.Equal(t, 0, got["cert-refresh"].ConsecutiveFailures)
}

func TestWriteCache_CreatesParentDirectoryIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "agent-state.json")

	require.NoError(t, writeCache(path, Cache{}))

	_, err := os.Stat(path)
	assert.NoError(t, err)
}

func TestWriteCache_OverwritesPreviousValueAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-state.json")

	require.NoError(t, writeCache(path, Cache{"a": {ConsecutiveFailures: 1}}))
	require.NoError(t, writeCache(path, Cache{"a": {ConsecutiveFailures: 2}}))

	got, err := readCache(path)
	require.NoError(t, err)
	assert.Equal(t, 2, got["a"].ConsecutiveFailures)

	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err), "no leftover temp file after a successful write")
}
