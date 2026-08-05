package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePolicyFile_ValidPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-10T00:00:00Z"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"],
		"storage_policy_id": "sp-1"
	}`)

	got, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p, ok := got.(*BackupPolicy)
	require.True(t, ok)
	assert.Equal(t, "nightly-web-backup", p.Metadata.Name)
	assert.NotEmpty(t, p.Metadata.ID)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.Metadata.CreatedAt)
	assert.Equal(t, []string{"web-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, p.ClientFilters.Labels)
	require.Len(t, p.ObjectFilters, 1)
	assert.Equal(t, "/var/www", p.ObjectFilters[0].Path)
	assert.Equal(t, []string{"*.html", "*.css"}, p.ObjectFilters[0].Include)
	assert.Equal(t, []string{"*.tmp"}, p.ObjectFilters[0].Exclude)
	assert.NotEmpty(t, p.ObjectFilters[0].ID)
	assert.Equal(t, "24h", p.RPO)
	assert.Equal(t, []string{"0 2 * * *", "0 20 * * *"}, p.BackupWindow)
	assert.Equal(t, "sp-1", p.StoragePolicyID)
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_ObjectFiltersAtDifferentIndicesGetDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "multi.json", `{
		"metadata": {"name": "multi"},
		"object_filters": [{"path": "/a"}, {"path": "/b"}],
		"storage_policy_id": "sp-1"
	}`)

	got, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p, ok := got.(*BackupPolicy)
	require.True(t, ok)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEmpty(t, p.ObjectFilters[0].ID)
	assert.NotEmpty(t, p.ObjectFilters[1].ID)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID)
}

func TestParsePolicyFile_ObjectFiltersWithIdenticalPathGetDistinctIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "duplicate-path.json", `{
		"metadata": {"name": "duplicate-path"},
		"object_filters": [
			{"path": "/var/www", "include": ["*.html"]},
			{"path": "/var/www", "exclude": ["*.log"]}
		],
		"storage_policy_id": "sp-1"
	}`)

	got, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p, ok := got.(*BackupPolicy)
	require.True(t, ok)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID, "two object filters sharing a path must still get distinct IDs")
}

func TestParsePolicyFile_ObjectFilterOmitsIncludeExclude(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "minimal.json", `{
		"metadata": {"name": "minimal"},
		"object_filters": [{"path": "/data"}],
		"storage_policy_id": "sp-1"
	}`)

	got, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p, ok := got.(*BackupPolicy)
	require.True(t, ok)
	require.Len(t, p.ObjectFilters, 1)
	assert.Equal(t, "/data", p.ObjectFilters[0].Path)
	assert.Empty(t, p.ObjectFilters[0].Include)
	assert.Empty(t, p.ObjectFilters[0].Exclude)
}

func TestParsePolicyFile_InvalidIncludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "include": ["["]}]
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidExcludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "exclude": ["["]}]
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_MissingNameFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{"metadata": {"name": ""}}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidHostnamePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"client_filters": {"hostnames": ["["]}
	}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_MissingStoragePolicyIdFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{"metadata": {"name": "broken"}}`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestBackupPolicy_ValidateValidPolicyReturnsNil(t *testing.T) {
	p := &BackupPolicy{
		PolicyBase: PolicyBase{
			Metadata:      Metadata{Name: "ok"},
			ClientFilters: ClientFilters{Hostnames: []string{"web-*"}},
		},
		ObjectFilters:   []ObjectFilter{{Path: "/data", Include: []string{"*.sql"}, Exclude: []string{"*.tmp"}}},
		StoragePolicyID: "sp-1",
	}
	assert.NoError(t, p.Validate())
}

func TestBackupPolicy_ValidateMissingNameFails(t *testing.T) {
	assert.Error(t, (&BackupPolicy{}).Validate())
}

func TestBackupPolicy_ValidateInvalidHostnamePatternFails(t *testing.T) {
	p := &BackupPolicy{PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}, ClientFilters: ClientFilters{Hostnames: []string{"["}}}}
	assert.Error(t, p.Validate())
}

func TestBackupPolicy_ValidateInvalidIncludePatternFails(t *testing.T) {
	p := &BackupPolicy{
		PolicyBase:    PolicyBase{Metadata: Metadata{Name: "x"}},
		ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"["}}},
	}
	assert.Error(t, p.Validate())
}

func TestBackupPolicy_ValidateInvalidExcludePatternFails(t *testing.T) {
	p := &BackupPolicy{
		PolicyBase:    PolicyBase{Metadata: Metadata{Name: "x"}},
		ObjectFilters: []ObjectFilter{{Path: "/data", Exclude: []string{"["}}},
	}
	assert.Error(t, p.Validate())
}

func TestBackupPolicy_ValidateMissingStoragePolicyIDFails(t *testing.T) {
	p := &BackupPolicy{PolicyBase: PolicyBase{Metadata: Metadata{Name: "x"}}}
	assert.Error(t, p.Validate())
}

func TestBackupPolicy_ToProtoSetsStoragePolicyIdAndLeavesDestinationsUnset(t *testing.T) {
	p := &BackupPolicy{
		PolicyBase:      PolicyBase{Metadata: Metadata{Name: "nightly"}, Type: "backup"},
		StoragePolicyID: "sp-1",
	}
	pp := p.ToProto(false)
	assert.Equal(t, "sp-1", pp.StoragePolicyId)
	assert.Empty(t, pp.Destinations, "Destinations is resolved elsewhere (attachDestination in server.go via checkinstore), never set directly by ToProto")
}
