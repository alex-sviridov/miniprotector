package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

func TestParseArguments_MissingHostnameErrors(t *testing.T) {
	withArgs(t, []string{"certrequest", "--ca-url", "https://localhost:9000"}, func() {
		_, err := parseArguments()
		assert.Error(t, err)
	})
}

func TestParseArguments_ExplicitCAURLUsed(t *testing.T) {
	withArgs(t, []string{"certrequest", "node1", "--ca-url", "https://localhost:9000"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "node1", args.Hostname)
		assert.Equal(t, "https://localhost:9000", args.CAURL)
	})
}

func TestParseArguments_SANsAccumulate(t *testing.T) {
	withArgs(t, []string{"certrequest", "node1", "--ca-url", "https://localhost:9000", "--san", "a.internal", "--san", "b.internal"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, []string{"a.internal", "b.internal"}, args.SANs)
	})
}

func TestParseArguments_MissingCAURLFallsBackToDefaultsFile(t *testing.T) {
	dir := t.TempDir()
	defaultsPath := filepath.Join(dir, "defaults.json")
	require.NoError(t, os.WriteFile(defaultsPath, []byte(`{"ca-url": "https://ca.backup.internal:9000"}`), 0o644))

	withArgs(t, []string{"certrequest", "node1", "--defaults-file", defaultsPath}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "https://ca.backup.internal:9000", args.CAURL)
	})
}

func TestParseArguments_MissingCAURLAndDefaultsFileErrors(t *testing.T) {
	withArgs(t, []string{"certrequest", "node1", "--defaults-file", "/nonexistent/defaults.json"}, func() {
		_, err := parseArguments()
		assert.Error(t, err)
	})
}

func TestReadDefaultCAURL_ParsesField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "defaults.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"ca-url": "https://example:9000", "root": "/x"}`), 0o644))

	got, err := readDefaultCAURL(path)
	require.NoError(t, err)
	assert.Equal(t, "https://example:9000", got)
}
