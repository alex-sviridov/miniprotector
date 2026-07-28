package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicyFile_StoragePolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "east-1.json", `{
		"metadata": {"name": "east-1-storage"},
		"client_filters": {"hostnames": ["storage-east-*"]},
		"hostname": "storage-east-1.internal",
		"port": 9400,
		"config": {"backend": "filesystem", "root": "/data/storage"}
	}`)

	got, err := parsePolicyFile(path, "storage")
	require.NoError(t, err)
	p, ok := got.(*StoragePolicy)
	require.True(t, ok)
	assert.Equal(t, "east-1-storage", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, []string{"storage-east-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, "storage-east-1.internal", p.Hostname)
	assert.Equal(t, 9400, p.Port)
	assert.JSONEq(t, `{"backend": "filesystem", "root": "/data/storage"}`, string(p.Config))
	assert.Equal(t, "storage", p.Kind())
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_SameBasenameInDifferentTypeSubfoldersYieldsDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	pathBackup := writePolicyFile(t, filepath.Join(dir, "backup"), "nightly.json", `{"metadata": {"name": "nightly"}}`)
	pathStorage := writePolicyFile(t, filepath.Join(dir, "storage"), "nightly.json", `{
		"metadata": {"name": "nightly"}, "hostname": "h", "port": 1, "config": {}
	}`)

	pBackup, err := parsePolicyFile(pathBackup, "backup")
	require.NoError(t, err)
	pStorage, err := parsePolicyFile(pathStorage, "storage")
	require.NoError(t, err)

	assert.NotEqual(t, pBackup.Meta().ID, pStorage.Meta().ID, "same basename in different type subfolders must not collide")
}

func TestStoragePolicy_ValidateValidPolicyReturnsNil(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "ok"}},
		Hostname:   "storage-1.internal",
		Port:       9400,
		Config:     []byte(`{"backend": "filesystem"}`),
	}
	assert.NoError(t, p.Validate())
}

func TestStoragePolicy_ValidateMissingNameFails(t *testing.T) {
	p := &StoragePolicy{Hostname: "h", Port: 1, Config: []byte(`{}`)}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidateMissingHostnameFails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Port:       9400,
		Config:     []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidatePortZeroFails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       0,
		Config:     []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidatePortAbove65535Fails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       70000,
		Config:     []byte(`{}`),
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidateEmptyConfigFails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       9400,
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_ValidateMalformedConfigJSONFails(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       9400,
		Config:     []byte(`not json`),
	}
	assert.Error(t, p.Validate())
}

func TestStoragePolicy_CloneDeepCopiesConfig(t *testing.T) {
	p := &StoragePolicy{
		PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}},
		Hostname:   "h",
		Port:       9400,
		Config:     []byte(`{"a":1}`),
	}
	cloned := p.Clone().(*StoragePolicy)
	cloned.Config[2] = 'X'
	assert.Equal(t, `{"a":1}`, string(p.Config), "mutating the clone's Config must not affect the original")
}
