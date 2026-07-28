package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writePolicyFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestParsePolicyFile_SetsTypeFromArgument(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)

	p, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	assert.Equal(t, "backup", p.Kind())
}

func TestParsePolicyFile_ComputesDeterministicPolicyID(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{
		"metadata": {"name": "nightly-web-backup"},
		"object_filters": [{"path": "/var/www"}]
	}`)

	p1, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)
	p2, err := parsePolicyFile(path, "backup")
	require.NoError(t, err)

	assert.NotEmpty(t, p1.Meta().ID)
	assert.Equal(t, p1.Meta().ID, p2.Meta().ID, "same filename must yield the same policy ID every parse")
}

func TestParsePolicyFile_DifferentFilenamesYieldDifferentPolicyIDs(t *testing.T) {
	dir := t.TempDir()
	pathA := writePolicyFile(t, dir, "a.json", `{"metadata": {"name": "same-name"}}`)
	pathB := writePolicyFile(t, dir, "b.json", `{"metadata": {"name": "same-name"}}`)

	pa, err := parsePolicyFile(pathA, "backup")
	require.NoError(t, err)
	pb, err := parsePolicyFile(pathB, "backup")
	require.NoError(t, err)

	assert.NotEqual(t, pa.Meta().ID, pb.Meta().ID, "identical metadata.name in different files must not collide")
}

func TestParsePolicyFile_MissingFileFails(t *testing.T) {
	_, err := parsePolicyFile(filepath.Join(t.TempDir(), "does-not-exist.json"), "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_InvalidJSONFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "bad.json", `not json`)

	_, err := parsePolicyFile(path, "backup")
	assert.Error(t, err)
}

func TestParsePolicyFile_UnrecognizedTypeFails(t *testing.T) {
	dir := t.TempDir()
	path := writePolicyFile(t, dir, "nightly.json", `{"metadata": {"name": "nightly"}}`)

	_, err := parsePolicyFile(path, "quux")
	assert.Error(t, err)
}
