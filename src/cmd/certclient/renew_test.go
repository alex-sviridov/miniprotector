package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/smallstep/certificates/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRenewer struct {
	resp *api.SignResponse
	err  error
}

func (f *fakeRenewer) Renew(_ http.RoundTripper) (*api.SignResponse, error) {
	return f.resp, f.err
}

func setupExistingIdentity(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, pair := range []struct{ src, dst string }{
		{"ca.crt", "ca.crt"},
		{"client.crt", "bootstrap.crt"},
		{"client.key", "bootstrap.key"},
	} {
		data, err := os.ReadFile(filepath.Join(fixtureCertsDir, pair.src))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, pair.dst), data, 0o600))
	}
	return dir
}

func TestRenew_OverwritesClientCrt(t *testing.T) {
	certsDir := setupExistingIdentity(t)
	leaf := loadFixtureCert(t, "client.crt")

	keyBefore, err := os.ReadFile(filepath.Join(certsDir, "bootstrap.key"))
	require.NoError(t, err)

	renewer := &fakeRenewer{resp: &api.SignResponse{
		ServerPEM: api.Certificate{Certificate: leaf},
		CaPEM:     api.Certificate{Certificate: leaf},
	}}

	err = renew(renewer, certsDir)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(certsDir, "bootstrap.crt"))
	require.NoError(t, err)
	want := append(pemCert(leaf), pemCert(leaf)...)
	assert.Equal(t, want, got)

	keyAfter, err := os.ReadFile(filepath.Join(certsDir, "bootstrap.key"))
	require.NoError(t, err)
	assert.Equal(t, keyBefore, keyAfter, "bootstrap.key must be byte-for-byte unchanged after renew")
}

func TestRenew_ErrorPropagates(t *testing.T) {
	certsDir := setupExistingIdentity(t)
	renewer := &fakeRenewer{err: assert.AnError}

	err := renew(renewer, certsDir)
	assert.Error(t, err)
}

func TestRenew_MissingExistingCertErrors(t *testing.T) {
	certsDir := t.TempDir() // no existing identity files
	renewer := &fakeRenewer{}

	err := renew(renewer, certsDir)
	assert.Error(t, err)
}
