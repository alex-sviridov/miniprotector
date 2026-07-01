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
	for _, name := range []string{"ca.crt", "client.crt", "client.key"} {
		data, err := os.ReadFile(filepath.Join(fixtureCertsDir, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
	}
	return dir
}

func TestRenew_OverwritesClientCrt(t *testing.T) {
	certsDir := setupExistingIdentity(t)
	leaf := loadFixtureCert(t, "client.crt")

	renewer := &fakeRenewer{resp: &api.SignResponse{
		ServerPEM: api.Certificate{Certificate: leaf},
		CaPEM:     api.Certificate{Certificate: leaf},
	}}

	err := renew(renewer, certsDir)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	assert.NotEmpty(t, got)
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
