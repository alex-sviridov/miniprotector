package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatchForReload_ReloadsOnChangedFileWrite(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Empty(t, c.Policies())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchForReload(ctx, dir, c, testLogger())
	time.Sleep(50 * time.Millisecond)

	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".changed"), []byte("1"), 0o644))

	require.Eventually(t, func() bool {
		return len(c.Policies()) == 1
	}, 2*time.Second, 10*time.Millisecond, "cache should reload after .changed is written")
}

func TestWatchForReload_ReloadsOnTouchOfExistingChangedFile(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Empty(t, c.Policies())

	changedPath := filepath.Join(dir, ".changed")
	require.NoError(t, os.WriteFile(changedPath, []byte("1"), 0o644))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchForReload(ctx, dir, c, testLogger())
	time.Sleep(50 * time.Millisecond)

	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	// Simulate `touch` on an already-existing file: an mtime-only update,
	// which Linux inotify reports as IN_ATTRIB (fsnotify's Chmod op), not
	// Write or Create.
	now := time.Now()
	require.NoError(t, os.Chtimes(changedPath, now, now))

	require.Eventually(t, func() bool {
		return len(c.Policies()) == 1
	}, 2*time.Second, 10*time.Millisecond, "cache should reload after touch of an already-existing .changed file")
}

func TestWatchForReload_IgnoresOtherFileWrites(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchForReload(ctx, dir, c, testLogger())

	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, c.Policies(), "reload must not fire without a write to .changed")
}
