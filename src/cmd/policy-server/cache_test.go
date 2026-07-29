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
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "b.json", `{"metadata": {"name": "policy-b"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	assert.Len(t, got, 2)
}

func TestCache_ReloadSkipsMalformedFileKeepsGoodOnes(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "good.json", `{"metadata": {"name": "policy-good"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "bad.json", `not json`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1)
	assert.Equal(t, "policy-good", got[0].Meta().Name)
}

func TestCache_ReloadAllFilesFailKeepsPreviousCache(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "good.json", `{"metadata": {"name": "policy-good"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	require.Len(t, c.Policies(), 1)

	require.NoError(t, os.Remove(filepath.Join(dir, "backup", "good.json")))
	writePolicyFile(t, filepath.Join(dir, "backup"), "bad.json", `not json`)

	err := c.Reload(dir, testLogger())
	assert.Error(t, err)
	got := c.Policies()
	require.Len(t, got, 1, "previous good cache must be kept")
	assert.Equal(t, "policy-good", got[0].Meta().Name)
}

func TestCache_ReloadEmptyDirectoryYieldsEmptyPolicies(t *testing.T) {
	dir := t.TempDir()

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))
	assert.Empty(t, c.Policies())
}

func TestCache_ReloadSkipsUnrecognizedTypeSubfolder(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "other"), "b.json", `{"metadata": {"name": "policy-b"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1, "a subfolder name absent from policyParsers must be skipped, not loaded")
	assert.Equal(t, "policy-a", got[0].Meta().Name)
	assert.Equal(t, "backup", got[0].Kind())
}

func TestCache_ReloadSkipsFileDirectlyUnderPoliciesDir(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, dir, "stray.json", `{"metadata": {"name": "stray"}}`)
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 1, "a *.json file with no type subfolder must not be loaded")
	assert.Equal(t, "policy-a", got[0].Meta().Name)
}

func TestCache_PoliciesReturnsSnapshotCopy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{
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

	got := c.Policies()
	bp, ok := got[0].(*BackupPolicy)
	require.True(t, ok)

	bp.Metadata.Name = "mutated-name"
	bp.ClientFilters.Hostnames[0] = "mutated-host"
	bp.ClientFilters.Labels["env"] = "dev"
	bp.ObjectFilters[0].Path = "/mutated/*"
	bp.ObjectFilters[0].Include[0] = "mutated"
	bp.ObjectFilters[0].Exclude[0] = "mutated"
	bp.BackupWindow[0] = "23:00"

	got2 := c.Policies()
	bp2, ok := got2[0].(*BackupPolicy)
	require.True(t, ok)
	assert.Equal(t, "policy-a", bp2.Metadata.Name, "mutating Metadata.Name in returned snapshot must not affect cache")
	assert.Equal(t, "host1", bp2.ClientFilters.Hostnames[0], "mutating Hostnames in returned snapshot must not affect cache")
	assert.Equal(t, "prod", bp2.ClientFilters.Labels["env"], "mutating Labels in returned snapshot must not affect cache")
	assert.Equal(t, "/data/*", bp2.ObjectFilters[0].Path, "mutating ObjectFilters in returned snapshot must not affect cache")
	assert.Equal(t, "*.sql", bp2.ObjectFilters[0].Include[0], "mutating ObjectFilters[].Include in returned snapshot must not affect cache")
	assert.Equal(t, "*.tmp", bp2.ObjectFilters[0].Exclude[0], "mutating ObjectFilters[].Exclude in returned snapshot must not affect cache")
	assert.Equal(t, "08:00", bp2.BackupWindow[0], "mutating BackupWindow in returned snapshot must not affect cache")
	assert.NotEmpty(t, bp2.ObjectFilters[0].ID, "ObjectFilter.ID must survive the snapshot copy")
	assert.Equal(t, "backup", bp2.Type, "Type must survive the snapshot copy")
}

func TestCache_FindByIDReturnsMatchingPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	want := c.Policies()[0]
	got, ok := c.FindByID(want.Meta().ID)
	require.True(t, ok)
	assert.Equal(t, "policy-a", got.Meta().Name)
	assert.Equal(t, filepath.Join(dir, "backup", "a.json"), got.Path())
}

func TestCache_FindByIDUnknownIDReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.FindByID("does-not-exist")
	assert.False(t, ok)
}

func TestCache_FindBySourcePathReturnsMatchingPolicy(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got, ok := c.FindBySourcePath(filepath.Join(dir, "backup", "a.json"))
	require.True(t, ok)
	assert.Equal(t, "policy-a", got.Meta().Name)
}

func TestCache_FindBySourcePathUnknownPathReturnsFalse(t *testing.T) {
	c := NewCache()
	_, ok := c.FindBySourcePath("/does/not/exist.json")
	assert.False(t, ok)
}

func TestCache_ReloadLoadsBackupAndStoragePoliciesTogether(t *testing.T) {
	dir := t.TempDir()
	writePolicyFile(t, filepath.Join(dir, "backup"), "a.json", `{"metadata": {"name": "policy-a"}}`)
	writePolicyFile(t, filepath.Join(dir, "storage"), "b.json", `{
		"metadata": {"name": "policy-b"}, "port": 9400, "config": {}
	}`)

	c := NewCache()
	require.NoError(t, c.Reload(dir, testLogger()))

	got := c.Policies()
	require.Len(t, got, 2)
	kinds := map[string]string{}
	for _, p := range got {
		kinds[p.Meta().Name] = p.Kind()
	}
	assert.Equal(t, "backup", kinds["policy-a"])
	assert.Equal(t, "storage", kinds["policy-b"])
}
