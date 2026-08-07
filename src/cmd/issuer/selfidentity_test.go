package main

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMintSelfIdentity_WritesAllThreeFilesWithMatchingCSR(t *testing.T) {
	certsDir := t.TempDir()
	rootFile := filepath.Join(t.TempDir(), "root.crt")
	require.NoError(t, os.WriteFile(rootFile, []byte("fake-root-pem"), 0o644))

	var gotHostname string
	var gotSANs []string
	var gotCSR *x509.CertificateRequest
	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		gotHostname = hostname
		gotSANs = sans
		gotCSR = csr
		return []byte("fake-chain"), nil
	}

	err := mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600)
	require.NoError(t, err)

	assert.Equal(t, "issuer", gotHostname)
	assert.Nil(t, gotSANs)
	require.NotNil(t, gotCSR)
	assert.Equal(t, "issuer", gotCSR.Subject.CommonName)
	assert.Equal(t, []string{"issuer"}, gotCSR.DNSNames,
		"CSR DNSNames must include the hostname explicitly -- an empty DNSNames CSR is accepted by step-ca but produces a SAN-less certificate that fails real TLS hostname verification")

	rootGot, err := os.ReadFile(filepath.Join(certsDir, "ca.crt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-root-pem"), rootGot)

	chainGot, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-chain"), chainGot)

	keyInfo, err := os.Stat(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())
}

func TestMintSelfIdentity_MintErrorPropagates_NoFilesWritten(t *testing.T) {
	certsDir := t.TempDir()
	rootFile := filepath.Join(t.TempDir(), "root.crt")
	require.NoError(t, os.WriteFile(rootFile, []byte("fake-root-pem"), 0o644))

	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return nil, assert.AnError
	}

	err := mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600)
	assert.Error(t, err)

	_, statErr := os.Stat(filepath.Join(certsDir, "client.crt"))
	assert.True(t, os.IsNotExist(statErr), "client.crt should not be written when mint fails")
}

func TestMintSelfIdentity_MissingRootFileErrors(t *testing.T) {
	certsDir := t.TempDir()
	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		t.Fatal("mint must not be called when the root file can't be read")
		return nil, nil
	}

	err := mintSelfIdentity("issuer", certsDir, filepath.Join(t.TempDir(), "does-not-exist.crt"), mint, 3600)
	assert.Error(t, err)
}

// TestMintSelfIdentity_KeyWriteFailure_LeavesLiveFilesConsistent reproduces
// the production incident: issuer's gRPC listener went silent ~24h into
// uptime, right at its daily self-cert-refresh tick, and only a full
// restart recovered it. mtls.go's GetCertificate caches client.crt/
// client.key in memory, reloading from disk only once its cache window
// elapses (see cachedIdentity), so
// if a refresh overwrites client.crt with a new cert but then fails before
// writing the matching client.key (disk full, permission error, kill
// signal -- anything interrupting the sequence), every subsequent
// handshake permanently fails to load a matching pair, until the files are
// rewritten together. This forces client.key's write to fail (by
// pre-occupying its path with a directory) after client.crt would already
// be overwritten, and asserts the live files must still form the
// previously-valid pair -- the refresh must not touch either live file
// unless it can commit both.
func TestMintSelfIdentity_KeyWriteFailure_LeavesLiveFilesConsistent(t *testing.T) {
	certsDir := t.TempDir()
	rootFile := filepath.Join(t.TempDir(), "root.crt")
	require.NoError(t, os.WriteFile(rootFile, []byte("fake-root-pem"), 0o644))

	call := 0
	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		call++
		return []byte(fmt.Sprintf("fake-chain-%d", call)), nil
	}

	require.NoError(t, mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600))
	crtBefore, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	keyBefore, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	// Occupy client.key's path with a directory so any write attempt to it
	// fails, simulating a refresh interrupted between writing client.crt
	// and client.key.
	require.NoError(t, os.Remove(filepath.Join(certsDir, "client.key")))
	require.NoError(t, os.Mkdir(filepath.Join(certsDir, "client.key"), 0o700))

	err = mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600)
	assert.Error(t, err, "refresh should report failure when it cannot commit client.key")

	crtAfter, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	assert.Equal(t, crtBefore, crtAfter,
		"client.crt must not be updated unless client.key can also be committed -- otherwise the live pair is permanently mismatched")

	keyInfo, err := os.Stat(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)
	if !keyInfo.IsDir() {
		keyAfter, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
		require.NoError(t, err)
		assert.Equal(t, keyBefore, keyAfter)
	}
}

func TestMintSelfIdentity_EachCallGeneratesAFreshKeypair(t *testing.T) {
	certsDir := t.TempDir()
	rootFile := filepath.Join(t.TempDir(), "root.crt")
	require.NoError(t, os.WriteFile(rootFile, []byte("fake-root-pem"), 0o644))
	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return []byte("fake-chain"), nil
	}

	require.NoError(t, mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600))
	keyAfterFirst, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	require.NoError(t, mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600))
	keyAfterSecond, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	assert.NotEqual(t, keyAfterFirst, keyAfterSecond,
		"unlike the operating credential's keypair (reused across refreshes), issuer's own self-mint has no external consistency requirement on a stable keypair -- a fresh one each call is simpler and correct")
}
