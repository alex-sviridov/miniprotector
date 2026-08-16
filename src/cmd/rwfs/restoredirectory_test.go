package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateRestoreDirectory_CreatesMissingDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "newdir")

	created, err := createRestoreDirectory(restoreDirectory{DestPath: target})
	require.NoError(t, err)
	assert.True(t, created)

	info, statErr := os.Stat(target)
	require.NoError(t, statErr)
	assert.True(t, info.IsDir())
}

func TestCreateRestoreDirectory_ReusesExistingDirectory(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "existing")
	require.NoError(t, os.Mkdir(target, 0o755))

	created, err := createRestoreDirectory(restoreDirectory{DestPath: target})
	require.NoError(t, err)
	assert.False(t, created)
}

func TestCreateRestoreDirectory_NonDirectoryAtPathIsHardError(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "actually-a-file")
	require.NoError(t, os.WriteFile(target, []byte("data"), 0o644))

	_, err := createRestoreDirectory(restoreDirectory{DestPath: target})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a directory")
}

func TestCreateRestoreDirectory_MissingParentReturnsError(t *testing.T) {
	base := t.TempDir()
	// "missing-parent" is never created, so "child" under it can't be
	// created either -- os.Mkdir is not recursive, and this pins that the
	// resulting error surfaces rather than being silently swallowed.
	target := filepath.Join(base, "missing-parent", "child")

	_, err := createRestoreDirectory(restoreDirectory{DestPath: target})
	assert.Error(t, err)
}
