package identity

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadOrCreateKey_CreatesThenReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "client.key")

	first, err := LoadOrCreateKey(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	second, err := LoadOrCreateKey(path)
	require.NoError(t, err)
	assert.True(t, first.Equal(second), "an existing key must be reused, not regenerated")
}

func TestLoadOrCreateKey_RejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.key")
	require.NoError(t, os.WriteFile(path, []byte("not pem"), 0o600))
	_, err := LoadOrCreateKey(path)
	assert.Error(t, err)
}

func TestWriteKey_LeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	key, err := LoadOrCreateKey(filepath.Join(dir, "client.key"))
	require.NoError(t, err)
	require.NoError(t, WriteKey(filepath.Join(dir, "other.key"), key))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2)
}

func TestNewCSR_SingleHostnameSAN(t *testing.T) {
	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "k"))
	require.NoError(t, err)
	der, err := NewCSR("node1", key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(der)
	require.NoError(t, err)
	require.NoError(t, csr.CheckSignature())
	assert.Equal(t, "node1", csr.Subject.CommonName)
	assert.Equal(t, []string{"node1"}, csr.DNSNames)
}
