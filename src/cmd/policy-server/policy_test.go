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
		"object_filters": [{"path": "/var/www"}],
		"rpo": "24h",
		"backup_window": ["0 2 * * *", "0 20 * * *"],
		"destination": "bwfs-east.internal:8080"
	}`)

	p, err := parsePolicyFile(path)
	require.NoError(t, err)
	assert.Equal(t, "nightly-web-backup", p.Metadata.Name)
	assert.Equal(t, time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC), p.Metadata.CreatedAt)
	assert.Equal(t, []string{"web-*"}, p.ClientFilters.Hostnames)
	assert.Equal(t, map[string]string{"env": "prod"}, p.ClientFilters.Labels)
	assert.Equal(t, []ObjectFilter{{Path: "/var/www"}}, p.ObjectFilters)
	assert.Equal(t, "24h", p.RPO)
	assert.Equal(t, []string{"0 2 * * *", "0 20 * * *"}, p.BackupWindow)
	assert.Equal(t, "bwfs-east.internal:8080", p.Destination)
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
