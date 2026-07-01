package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasExistingIdentity_AllFilesPresent(t *testing.T) {
	dir := setupExistingIdentity(t)
	assert.True(t, hasExistingIdentity(dir))
}

func TestHasExistingIdentity_MissingFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), []byte("x"), 0o644))
	// client.key intentionally missing
	assert.False(t, hasExistingIdentity(dir))
}

func TestResolveToken_FlagTakesPriority(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "env-token")
	got, err := resolveToken("flag-token", strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "flag-token", got)
}

func TestResolveToken_EnvVarUsedWhenFlagEmpty(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "env-token")
	got, err := resolveToken("", strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "env-token", got)
}

func TestResolveToken_FallsBackToStdin(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "")
	got, err := resolveToken("", strings.NewReader("stdin-token\n"))
	require.NoError(t, err)
	assert.Equal(t, "stdin-token", got)
}
