package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCursor_MissingFileReturnsZero(t *testing.T) {
	dir := t.TempDir()
	seq, err := readCursor(filepath.Join(dir, "catalogsync.cursor"))
	require.NoError(t, err)
	assert.Equal(t, int64(0), seq)
}

func TestReadCursor_CorruptFileReturnsZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalogsync.cursor")
	require.NoError(t, os.WriteFile(path, []byte("not-a-number"), 0o644))

	seq, err := readCursor(path)
	require.NoError(t, err)
	assert.Equal(t, int64(0), seq)
}

func TestWriteCursorThenReadCursor_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalogsync.cursor")

	require.NoError(t, writeCursor(path, 42))

	seq, err := readCursor(path)
	require.NoError(t, err)
	assert.Equal(t, int64(42), seq)
}

func TestWriteCursor_OverwritesPreviousValueAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "catalogsync.cursor")

	require.NoError(t, writeCursor(path, 1))
	require.NoError(t, writeCursor(path, 2))

	seq, err := readCursor(path)
	require.NoError(t, err)
	assert.Equal(t, int64(2), seq)

	_, err = os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(err), "no leftover temp file after a successful write")
}
