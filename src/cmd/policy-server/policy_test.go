package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePolicyFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestParsePolicyFile_ValidPolicyParsesAllFields(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup", "created_at": "2026-07-10T00:00:00Z", "updated_at": "2026-07-10T00:00:00Z"},
		"client_filters": {"hostnames": ["web-*"], "labels": {"env": "prod"}},
		"object_filters": [{"path": "/var/www", "include": ["*.html", "*.css"], "exclude": ["*.tmp"]}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
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
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
	assert.Equal(t, path, p.SourcePath)
}

func TestParsePolicyFile_ComputesDeterministicPolicyID(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup"},
		"object_filters": [{"path": "/var/www"}]
	}`)

	p1, err := parsePolicyFile(path)
	require.NoError(t, err)
	p2, err := parsePolicyFile(path)
	require.NoError(t, err)

	assert.NotEmpty(t, p1.Metadata.ID)
	assert.Equal(t, p1.Metadata.ID, p2.Metadata.ID, "same filename must yield the same policy ID every parse")
}

func TestParsePolicyFile_DifferentFilenamesYieldDifferentPolicyIDs(t *testing.T) {
	dir := t.TempDir()
	pathA := writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "same-name"}}`)
	pathB := writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "same-name"}}`)

	pa, err := parsePolicyFile(pathA)
	require.NoError(t, err)
	pb, err := parsePolicyFile(pathB)
	require.NoError(t, err)

	assert.NotEqual(t, pa.Metadata.ID, pb.Metadata.ID, "identical metadata.name in different files must not collide")
}

func TestParsePolicyFile_ObjectFiltersAtDifferentIndicesGetDifferentIDs(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "multi.json", `{
		"metadata": {"name": "multi"},
		"object_filters": [{"path": "/a"}, {"path": "/b"}]
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
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
		]
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	require.Len(t, p.ObjectFilters, 2)
	assert.NotEqual(t, p.ObjectFilters[0].ID, p.ObjectFilters[1].ID, "two object filters sharing a path must still get distinct IDs")
}

func TestParsePolicyFile_ObjectFilterOmitsIncludeExclude(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "minimal.json", `{
		"metadata": {"name": "minimal"},
		"object_filters": [{"path": "/data"}]
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
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

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidExcludePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"object_filters": [{"path": "/data", "exclude": ["["]}]
	}`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_MissingNameFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{"metadata": {"name": ""}}`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidJSONFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `not json`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidHostnamePatternFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `{
		"metadata": {"name": "broken"},
		"client_filters": {"hostnames": ["["]}
	}`)

	_, err := parsePolicyFile(path)
	assert.Error(t, err)
}

func TestParsePolicyFile_MissingFileFails(t *testing.T) {
	_, err := parsePolicyFile(filepath.Join(t.TempDir(), "does-not-exist.json"))
	assert.Error(t, err)
}

func TestValidatePolicy_ValidPolicyReturnsNil(t *testing.T) {
	p := Policy{
		Metadata:      Metadata{Name: "ok"},
		ClientFilters: ClientFilters{Hostnames: []string{"web-*"}},
		ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"*.sql"}, Exclude: []string{"*.tmp"}}},
	}
	assert.NoError(t, validatePolicy(p))
}

func TestValidatePolicy_MissingNameFails(t *testing.T) {
	assert.Error(t, validatePolicy(Policy{}))
}

func TestValidatePolicy_InvalidHostnamePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ClientFilters: ClientFilters{Hostnames: []string{"["}}}
	assert.Error(t, validatePolicy(p))
}

func TestValidatePolicy_InvalidIncludePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ObjectFilters: []ObjectFilter{{Path: "/data", Include: []string{"["}}}}
	assert.Error(t, validatePolicy(p))
}

func TestValidatePolicy_InvalidExcludePatternFails(t *testing.T) {
	p := Policy{Metadata: Metadata{Name: "x"}, ObjectFilters: []ObjectFilter{{Path: "/data", Exclude: []string{"["}}}}
	assert.Error(t, validatePolicy(p))
}
