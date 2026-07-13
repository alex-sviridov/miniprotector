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
	writePolicyFile(t, dir, "a.json", `{
		"metadata": {"name": "policy-a"},
		"client_filters": {
			"hostnames": ["host1", "host2"],
			"labels": {"env": "prod", "team": "platform"}
		},
		"object_filters": [{"path": "/data/*", "include": ["*.sql"], "exclude": ["*.tmp"]}],
		"rpo": "1h",
		"backup_window": ["08:00", "12:00"]
	}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	// Test mutation of plain value field (should not affect cache)
	got := c.Policies()
	got[0].Metadata.Name = "mutated-name"

	// Test mutation of nested slice field
	got[0].ClientFilters.Hostnames[0] = "mutated-host"

	// Test mutation of nested map field
	got[0].ClientFilters.Labels["env"] = "dev"

	// Test mutation of ObjectFilters slice
	got[0].ObjectFilters[0].Path = "/mutated/*"
	got[0].ObjectFilters[0].Include[0] = "mutated"
	got[0].ObjectFilters[0].Exclude[0] = "mutated"

	// Test mutation of BackupWindow slice
	got[0].BackupWindow[0] = "23:00"

	// Verify that a fresh call to Policies() returns the original values
	got2 := c.Policies()
	assert.Equal(t, "policy-a", got2[0].Metadata.Name, "mutating Metadata.Name in returned snapshot must not affect cache")
	assert.Equal(t, "host1", got2[0].ClientFilters.Hostnames[0], "mutating Hostnames in returned snapshot must not affect cache")
	assert.Equal(t, "prod", got2[0].ClientFilters.Labels["env"], "mutating Labels in returned snapshot must not affect cache")
	assert.Equal(t, "/data/*", got2[0].ObjectFilters[0].Path, "mutating ObjectFilters in returned snapshot must not affect cache")
	assert.Equal(t, "*.sql", got2[0].ObjectFilters[0].Include[0], "mutating ObjectFilters[].Include in returned snapshot must not affect cache")
	assert.Equal(t, "*.tmp", got2[0].ObjectFilters[0].Exclude[0], "mutating ObjectFilters[].Exclude in returned snapshot must not affect cache")
	assert.Equal(t, "08:00", got2[0].BackupWindow[0], "mutating BackupWindow in returned snapshot must not affect cache")
}
