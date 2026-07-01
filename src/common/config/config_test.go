package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBaseDir_EnvVarSet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigPathEnvVar, dir)

	got, err := ResolveBaseDir()
	require.NoError(t, err)
	assert.Equal(t, dir, got)
}

func TestResolveBaseDir_DefaultsToExecutableDir(t *testing.T) {
	// Setting to "" (rather than leaving whatever the test process inherited)
	// makes this deterministic: os.Getenv returns "" either way, which is what
	// ResolveBaseDir treats as "unset".
	t.Setenv(ConfigPathEnvVar, "")

	got, err := ResolveBaseDir()
	require.NoError(t, err)

	exePath, err := os.Executable()
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(exePath), got)
}

func TestResolveConfigPath_JoinsBaseDirWithLocalConf(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigPathEnvVar, dir)

	got, err := ResolveConfigPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "local.conf"), got)
}

func TestResolveCertsDir_JoinsBaseDirWithCerts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigPathEnvVar, dir)

	got, err := ResolveCertsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "certs"), got)
}
