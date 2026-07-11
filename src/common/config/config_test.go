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

func TestParseConfig_CatalogSyncBatchSizeDefaultsTo500(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 500, conf.CatalogSyncBatchSize)
}

func TestParseConfig_CatalogSyncBatchSizeParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nCatalogSyncBatchSize=250\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 250, conf.CatalogSyncBatchSize)
}

func TestParseConfig_CatalogSyncPollIntervalSecDefaultsTo5(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 5, conf.CatalogSyncPollIntervalSec)
}

func TestParseConfig_CatalogSyncPollIntervalSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nCatalogSyncPollIntervalSec=15\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 15, conf.CatalogSyncPollIntervalSec)
}

func TestParseConfig_CatalogSyncMaxBackoffSecDefaultsTo60(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 60, conf.CatalogSyncMaxBackoffSec)
}

func TestParseConfig_CatalogSyncMaxBackoffSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nCatalogSyncMaxBackoffSec=120\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 120, conf.CatalogSyncMaxBackoffSec)
}

func TestParseConfig_CatalogHostOptional(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "", conf.CatalogHost)
}

func TestParseConfig_CatalogHostParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\ncatalog_host=catalog.backup.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "catalog.backup.internal", conf.CatalogHost)
}

func TestParseConfig_CatalogPortDefaultsTo15723(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 15723, conf.CatalogPort)
}

func TestParseConfig_CatalogPortParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\ncatalog_port=9443\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9443, conf.CatalogPort)
}

func TestParseConfig_VarPathOptional(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "", conf.VarPath)
}

func TestParseConfig_VarPathParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nvar_path=/var/lib/miniprotector\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "/var/lib/miniprotector", conf.VarPath)
}

func TestParseConfig_ReconcileIntervalSecDefaultsTo30(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 30, conf.ReconcileIntervalSec)
}

func TestParseConfig_ReconcileIntervalSecParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nReconcileIntervalSec=15\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 15, conf.ReconcileIntervalSec)
}

func TestResolveVarDir_ReturnsConfiguredPathWhenSet(t *testing.T) {
	got, err := ResolveVarDir(&Config{VarPath: "/var/lib/miniprotector"})
	require.NoError(t, err)
	assert.Equal(t, "/var/lib/miniprotector", got)
}

func TestResolveVarDir_DefaultsToExecutableDir(t *testing.T) {
	got, err := ResolveVarDir(&Config{})
	require.NoError(t, err)

	exePath, err := os.Executable()
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(exePath), got)
}

func TestParseConfig_IssuerHostParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nissuer_host=ca.backup.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "ca.backup.internal", conf.IssuerHost)
}

func TestParseConfig_IssuerPortDefaultsTo9200(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9200, conf.IssuerPort)
}

func TestParseConfig_IssuerPortParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nissuer_port=9300\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9300, conf.IssuerPort)
}

func TestParseConfig_OperatingCertTTLSecDefaultsTo3600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 3600, conf.OperatingCertTTLSec)
}

func TestParseConfig_OperatingCertTTLSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nOperatingCertTTLSec=1800\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 1800, conf.OperatingCertTTLSec)
}

func TestParseConfig_BootstrapCertRefreshIntervalSecDefaultsTo86400(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 86400, conf.BootstrapCertRefreshIntervalSec)
}

func TestParseConfig_BootstrapCertRefreshIntervalSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nBootstrapCertRefreshIntervalSec=43200\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 43200, conf.BootstrapCertRefreshIntervalSec)
}

func TestParseConfig_BootstrapCertTTLSecDefaultsTo7776000(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 7776000, conf.BootstrapCertTTLSec)
}

func TestParseConfig_BootstrapCertTTLSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nBootstrapCertTTLSec=2592000\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 2592000, conf.BootstrapCertTTLSec)
}

func TestParseConfig_OperatingCertFetchIntervalSecDefaultsTo900(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 900, conf.OperatingCertFetchIntervalSec)
}

func TestParseConfig_OperatingCertFetchIntervalSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nOperatingCertFetchIntervalSec=300\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 300, conf.OperatingCertFetchIntervalSec)
}

func TestParseConfig_IssuerSelfCertTTLSecDefaultsTo7776000(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 7776000, conf.IssuerSelfCertTTLSec)
}

func TestParseConfig_IssuerSelfCertTTLSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nIssuerSelfCertTTLSec=2592000\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 2592000, conf.IssuerSelfCertTTLSec)
}

func TestParseConfig_IssuerSelfCertRefreshIntervalSecDefaultsTo86400(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 86400, conf.IssuerSelfCertRefreshIntervalSec)
}

func TestParseConfig_IssuerSelfCertRefreshIntervalSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nIssuerSelfCertRefreshIntervalSec=43200\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 43200, conf.IssuerSelfCertRefreshIntervalSec)
}

func TestParseConfig_PolicyServerHostParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\npolicy_server_host=policy.backup.internal\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "policy.backup.internal", conf.PolicyServerHost)
}

func TestParseConfig_PolicyServerPortDefaultsTo9300(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9300, conf.PolicyServerPort)
}

func TestParseConfig_PolicyServerPortParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\npolicy_server_port=9301\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 9301, conf.PolicyServerPort)
}

func TestResolvePoliciesDir_JoinsBaseDirWithPolicies(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigPathEnvVar, dir)

	got, err := ResolvePoliciesDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "policies"), got)
}

func TestParseConfig_PolicyFetchIntervalSecDefaultsTo900(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 900, conf.PolicyFetchIntervalSec)
}

func TestParseConfig_PolicyFetchIntervalSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nPolicyFetchIntervalSec=300\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 300, conf.PolicyFetchIntervalSec)
}

func TestParseConfig_BackupWindowGraceSecDefaultsTo3600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 3600, conf.BackupWindowGraceSec)
}

func TestParseConfig_BackupWindowGraceSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nBackupWindowGraceSec=600\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 600, conf.BackupWindowGraceSec)
}

func TestParseConfig_MaxConcurrentBackupJobsDefaultsTo2(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 2, conf.MaxConcurrentBackupJobs)
}

func TestParseConfig_MaxConcurrentBackupJobsParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nMaxConcurrentBackupJobs=5\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 5, conf.MaxConcurrentBackupJobs)
}
