package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallstep/certificates/api"
	"github.com/smallstep/cli-utils/token"
	"github.com/smallstep/cli-utils/token/provision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.step.sm/crypto/x509util"
)

const fixtureCertsDir = "../../common/testdata/certs"

// loadFixtureCert parses a PEM-encoded certificate file. Used to stand in
// for root/leaf/intermediate certs in tests — these fixtures don't need to
// chain to each other, since writeIdentity only re-serializes whatever
// *x509.Certificate values it's given.
func loadFixtureCert(t *testing.T, name string) *x509.Certificate {
	t.Helper()
	pemBytes, err := os.ReadFile(filepath.Join(fixtureCertsDir, name))
	require.NoError(t, err)
	block, _ := pem.Decode(pemBytes)
	require.NotNil(t, block, "no PEM block found in %s", name)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

// makeTestToken builds a real, validly-signed enrollment token using the same
// library client-manager uses, so ca.CreateSignRequest (a real, unmocked call)
// accepts it.
func makeTestToken(t *testing.T, subject string, sans []string, root *x509.Certificate) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tok, err := provision.New(subject,
		token.WithJWTID("test-jti"),
		token.WithIssuer("admin@backup.internal"),
		token.WithAudience("https://ca.internal/1.0/sign"),
		token.WithValidity(time.Now(), time.Now().Add(5*time.Minute)),
		token.WithSANS(sans),
		token.WithSHA(x509util.Fingerprint(root)),
	)
	require.NoError(t, err)

	signed, err := tok.SignedString("ES256", key)
	require.NoError(t, err)
	return signed
}

type fakeSigner struct {
	resp   *api.SignResponse
	err    error
	gotReq *api.SignRequest
}

func (f *fakeSigner) Sign(req *api.SignRequest) (*api.SignResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}

func fakeSignResponse(root, leaf, intermediate *x509.Certificate) *api.SignResponse {
	return &api.SignResponse{
		ServerPEM: api.Certificate{Certificate: leaf},
		CaPEM:     api.Certificate{Certificate: intermediate},
		TLS: &tls.ConnectionState{
			VerifiedChains: [][]*x509.Certificate{{leaf, intermediate, root}},
		},
	}
}

func TestBootstrap_WritesIdentityFiles(t *testing.T) {
	root := loadFixtureCert(t, "ca.crt")
	leaf := loadFixtureCert(t, "client.crt")

	tok := makeTestToken(t, "test-host", []string{"test-host"}, root)
	signer := &fakeSigner{resp: fakeSignResponse(root, leaf, leaf)}
	certsDir := t.TempDir()

	err := bootstrap(tok, signer, certsDir)
	require.NoError(t, err)

	for _, name := range []string{"ca.crt", "bootstrap.crt", "bootstrap.key"} {
		info, err := os.Stat(filepath.Join(certsDir, name))
		require.NoError(t, err, "expected %s to exist", name)
		if name == "bootstrap.key" {
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	}
}

func TestBootstrap_SignErrorPropagates(t *testing.T) {
	root := loadFixtureCert(t, "ca.crt")
	tok := makeTestToken(t, "test-host", []string{"test-host"}, root)
	signer := &fakeSigner{err: assert.AnError}
	certsDir := t.TempDir()

	err := bootstrap(tok, signer, certsDir)
	assert.Error(t, err)
	_, statErr := os.Stat(filepath.Join(certsDir, "bootstrap.crt"))
	assert.True(t, os.IsNotExist(statErr), "bootstrap.crt should not be written on sign failure")
}

func TestBootstrap_InvalidTokenErrors(t *testing.T) {
	certsDir := t.TempDir()
	err := bootstrap("not-a-real-token", &fakeSigner{}, certsDir)
	assert.Error(t, err)
}

func TestBootstrap_SetsBootstrapTierTemplateData(t *testing.T) {
	root := loadFixtureCert(t, "ca.crt")
	leaf := loadFixtureCert(t, "client.crt")

	tok := makeTestToken(t, "test-host", []string{"test-host"}, root)
	signer := &fakeSigner{resp: fakeSignResponse(root, leaf, leaf)}
	certsDir := t.TempDir()

	err := bootstrap(tok, signer, certsDir)
	require.NoError(t, err)

	require.NotNil(t, signer.gotReq)
	var got struct {
		Tier string `json:"tier"`
	}
	require.NoError(t, json.Unmarshal(signer.gotReq.TemplateData, &got))
	assert.Equal(t, "bootstrap", got.Tier)
}
