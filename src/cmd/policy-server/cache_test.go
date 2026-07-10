package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestCache_ReloadLoadsValidPolicies(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "policy-b"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	assert.Len(t, got, 2)
}

func TestCache_ReloadSkipsMalformedFileKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "good.json", `{"metadata": {"name": "policy-good"}}`)
	writePolicyFile(t, dir, "bad.json", `not json`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1)
	assert.Equal(t, "policy-good", got[0].Metadata.Name)
}

func TestCache_ReloadAllFilesFailKeepsPreviousCache(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "good.json", `{"metadata": {"name": "policy-good"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Len(t, c.Policies(), 1)

	require.NoError(t, os.Remove(filepath.Join(dir, "good.json")))
	writePolicyFile(t, dir, "bad.json", `not json`)

	err := c.Reload(dir, testLogger())
	assert.Error(t, err)
	got := c.Policies()
	require.Len(t, got, 1, "previous good cache must be kept")
	assert.Equal(t, "policy-good", got[0].Metadata.Name)
}

func TestCache_ReloadEmptyDirectoryYieldsEmptyPolicies(t *testing.T) {
	dir := t.TempDir()

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	assert.Empty(t, c.Policies())
}

func TestCache_PoliciesReturnsSnapshotCopy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	got[0].Metadata.Name = "mutated"

	got2 := c.Policies()
	assert.Equal(t, "policy-a", got2[0].Metadata.Name, "mutating a returned snapshot must not affect the cache")
}
