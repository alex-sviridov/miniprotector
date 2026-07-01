package mtls

import (
	"crypto/tls"
	"net"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fixtureCertsDir   = "../testdata/certs"
	untrustedCertsDir = "../testdata/certs-untrusted"
)

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(dst, data, 0o600))
}

func TestLoadServerCredentials_Success(t *testing.T) {
	creds, err := LoadServerCredentials(fixtureCertsDir)
	require.NoError(t, err)
	assert.NotNil(t, creds)
}

func TestLoadServerCredentials_MissingDir(t *testing.T) {
	_, err := LoadServerCredentials("does-not-exist")
	assert.Error(t, err)
}

func TestLoadClientCredentials_Success(t *testing.T) {
	creds, err := LoadClientCredentials(fixtureCertsDir, "bwfs.internal")
	require.NoError(t, err)
	assert.NotNil(t, creds)
}

func TestLoadClientCredentials_MissingCAFile(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, fixtureCertsDir+"/client.crt", dir+"/client.crt")
	copyFile(t, fixtureCertsDir+"/client.key", dir+"/client.key")
	// ca.crt intentionally omitted

	_, err := LoadClientCredentials(dir, "bwfs.internal")
	assert.Error(t, err)
}

// startTestServer starts a raw TLS listener (not gRPC) using serverTLSConfig,
// so handshake behavior can be tested directly without gRPC overhead.
func startTestServer(t *testing.T, certsDir string) string {
	t.Helper()
	cfg, err := serverTLSConfig(certsDir)
	require.NoError(t, err)

	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	require.NoError(t, err)
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tlsConn := c.(*tls.Conn)
				if err := tlsConn.Handshake(); err == nil {
					c.Write([]byte("ok"))
				}
			}(conn)
		}
	}()
	return ln.Addr().String()
}

func dial(addr string, cfg *tls.Config) error {
	conn, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	buf := make([]byte, 2)
	_, err = conn.Read(buf)
	return err
}

func TestHandshake_LoopbackHostSkipsHostnameCheck(t *testing.T) {
	addr := startTestServer(t, fixtureCertsDir)
	for _, host := range []string{"localhost", "127.0.0.1"} {
		cfg, err := clientTLSConfig(fixtureCertsDir, host)
		require.NoError(t, err)
		assert.NoError(t, dial(addr, cfg), "host=%s", host)
	}
}

func TestHandshake_NonLoopbackHostMatchingSAN(t *testing.T) {
	addr := startTestServer(t, fixtureCertsDir)
	cfg, err := clientTLSConfig(fixtureCertsDir, "bwfs.internal")
	require.NoError(t, err)
	assert.NoError(t, dial(addr, cfg))
}

func TestHandshake_NonLoopbackHostMismatchedSAN(t *testing.T) {
	addr := startTestServer(t, fixtureCertsDir)
	cfg, err := clientTLSConfig(fixtureCertsDir, "wrong.internal")
	require.NoError(t, err)
	assert.Error(t, dial(addr, cfg))
}

func TestHandshake_ServerRejectsUntrustedClientCert(t *testing.T) {
	addr := startTestServer(t, fixtureCertsDir)

	untrustedCert, _, err := loadCertAndPool(untrustedCertsDir)
	require.NoError(t, err)
	_, trustedPool, err := loadCertAndPool(fixtureCertsDir)
	require.NoError(t, err)

	cfg := &tls.Config{
		Certificates: []tls.Certificate{untrustedCert},
		RootCAs:      trustedPool,
		ServerName:   "bwfs.internal",
	}
	assert.Error(t, dial(addr, cfg))
}
