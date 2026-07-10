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

	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".changed"), []byte("1"), 0o644))

	require.Eventually(t, func() bool {
		return len(c.Policies()) == 1
	}, 2*time.Second, 10*time.Millisecond, "cache should reload after .changed is written")
}

func TestWatchForReload_IgnoresOtherFileWrites(t *testing.T) {
	dir := t.TempDir()
	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go watchForReload(ctx, dir, c, testLogger())

	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, c.Policies(), "reload must not fire without a write to .changed")
}
