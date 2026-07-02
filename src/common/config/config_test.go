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

func TestParseConfig_CAHostOptional(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "", conf.CAHost)
}

func TestParseConfig_CAHostParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nca_host=ca.backup.internal:9000\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "ca.backup.internal:9000", conf.CAHost)
}

func TestParseConfig_JobTimeoutSecDefaultsTo30(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 30, conf.JobTimeoutSec)
}

func TestParseConfig_JobTimeoutSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nJobTimeoutSec=90\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 90, conf.JobTimeoutSec)
}
