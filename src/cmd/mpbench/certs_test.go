package main

import (
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/mtls"
)

func TestWriteIdentity_LoadsAndCarriesClientRole(t *testing.T) {
	cfg := t.TempDir()
	require.NoError(t, WriteIdentity(cfg))
	certs := filepath.Join(cfg, "certs")

	_, err := mtls.LoadServerCredentials(certs)
	require.NoError(t, err)
	_, err = mtls.LoadClientCredentials(certs, "bwfs.internal")
	require.NoError(t, err)

	raw, err := os.ReadFile(filepath.Join(certs, "client.crt"))
	require.NoError(t, err)
	block, _ := pem.Decode(raw)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	assert.Contains(t, cert.DNSNames, "bwfs.internal")
	assert.Contains(t, cert.DNSNames, "localhost")
	var role string
	for _, ext := range cert.Extensions {
		if ext.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}) {
			role = string(ext.Value)
		}
	}
	assert.JSONEq(t, `{"authz-role":"client"}`, role)
}

func TestWriteLocalConf_ParsesAndCreatesLogDir(t *testing.T) {
	cfg := t.TempDir()
	logs := filepath.Join(t.TempDir(), "logs")
	require.NoError(t, WriteLocalConf(cfg, logs, 3))

	conf, err := config.ParseConfig(filepath.Join(cfg, "local.conf"))
	require.NoError(t, err)
	assert.Equal(t, 3, conf.DefaultStreams)
	assert.Equal(t, logs, conf.LogDir)
	st, err := os.Stat(logs)
	require.NoError(t, err)
	assert.True(t, st.IsDir())
}

func TestWriteLocalConf_AppendsExtraLines(t *testing.T) {
	cfg := t.TempDir()
	require.NoError(t, WriteLocalConf(cfg, filepath.Join(t.TempDir(), "logs"), 2, "grpc_window_bytes=4194304", "default_window=3"))

	conf, err := config.ParseConfig(filepath.Join(cfg, "local.conf"))
	require.NoError(t, err)
	assert.Equal(t, 4194304, conf.GrpcWindowBytes)
	assert.Equal(t, 3, conf.DefaultWindow)
}
