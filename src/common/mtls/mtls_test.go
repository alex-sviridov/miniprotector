package mtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// startListener starts a raw TLS listener (not gRPC) using cfg, so handshake
// behavior can be tested directly without gRPC overhead. Every connection
// that completes a handshake gets "ok" written back; a rejected handshake
// simply never sees that write.
func startListener(t *testing.T, cfg *tls.Config) string {
	t.Helper()
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

// startTestServer starts a raw TLS listener using serverTLSConfig (the
// default, operating-tier-requiring config).
func startTestServer(t *testing.T, certsDir string) string {
	t.Helper()
	cfg, err := serverTLSConfig(certsDir)
	require.NoError(t, err)
	return startListener(t, cfg)
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

func copyCertsDir(t *testing.T, src string) string {
	t.Helper()
	dst := t.TempDir()
	for _, name := range []string{"ca.crt", "client.crt", "client.key"} {
		copyFile(t, filepath.Join(src, name), filepath.Join(dst, name))
	}
	return dst
}

func TestServerTLSConfig_ReloadsCertificateOnEachNewConnection(t *testing.T) {
	dir := copyCertsDir(t, fixtureCertsDir)
	addr := startTestServer(t, dir)

	clientCfg, err := clientTLSConfig(fixtureCertsDir, "bwfs.internal")
	require.NoError(t, err)

	// Baseline: valid cert on disk, handshake succeeds.
	require.NoError(t, dial(addr, clientCfg))

	// Corrupt the server's identity cert on disk without restarting the
	// listener. If GetCertificate were caching the cert captured when
	// serverTLSConfig was built instead of re-reading, this would still
	// succeed.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), []byte("not a cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), []byte("not a key"), 0o600))
	assert.Error(t, dial(addr, clientCfg))

	// Restore a valid cert — proves this is a live re-read, not a one-time
	// failure that got cached.
	copyFile(t, fixtureCertsDir+"/client.crt", filepath.Join(dir, "client.crt"))
	copyFile(t, fixtureCertsDir+"/client.key", filepath.Join(dir, "client.key"))
	assert.NoError(t, dial(addr, clientCfg))
}

func TestClientTLSConfig_ReloadsCertificateOnEachNewConnection(t *testing.T) {
	dir := copyCertsDir(t, fixtureCertsDir)
	cfg, err := clientTLSConfig(dir, "bwfs.internal")
	require.NoError(t, err)

	addr := startTestServer(t, fixtureCertsDir)

	// Baseline succeeds.
	require.NoError(t, dial(addr, cfg))

	// Corrupt the client's identity cert on disk. The test server requires
	// and verifies client certs, so a stale-cached client cert would still
	// dial successfully if GetClientCertificate weren't re-reading.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), []byte("not a cert"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), []byte("not a key"), 0o600))
	assert.Error(t, dial(addr, cfg))

	copyFile(t, fixtureCertsDir+"/client.crt", filepath.Join(dir, "client.crt"))
	copyFile(t, fixtureCertsDir+"/client.key", filepath.Join(dir, "client.key"))
	assert.NoError(t, dial(addr, cfg))
}

func TestLoadClientCredentialsWithIdentity_Success(t *testing.T) {
	creds, err := LoadClientCredentialsWithIdentity(fixtureCertsDir, "client.crt", "client.key", "bwfs.internal")
	require.NoError(t, err)
	assert.NotNil(t, creds)
}

func TestLoadClientCredentialsWithIdentity_MissingKeyFile(t *testing.T) {
	_, err := LoadClientCredentialsWithIdentity(fixtureCertsDir, "client.crt", "does-not-exist.key", "bwfs.internal")
	assert.Error(t, err)
}

func TestLoadClientCredentials_StillUsesDefaultFilenames(t *testing.T) {
	// LoadClientCredentials must keep resolving client.crt/client.key by
	// default -- this is what every existing caller (bwfs/brfs/rwfs/
	// catalogsync/catalog) depends on continuing to work unchanged.
	creds, err := LoadClientCredentials(fixtureCertsDir, "bwfs.internal")
	require.NoError(t, err)
	assert.NotNil(t, creds)
}

// generateTestCA creates a throwaway, in-memory CA keypair and self-signed
// certificate -- tests need to mint their own leaf certificates with
// specific ExtKeyUsage combinations, which the static fixtures in
// testdata/certs can't provide.
func generateTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert, key
}

// generateTestLeaf mints a leaf certificate signed by ca/caKey for hostname,
// carrying exactly the given ExtKeyUsage/UnknownExtKeyUsage combination.
func generateTestLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, hostname string, ekus []x509.ExtKeyUsage, unknownEKUs []asn1.ObjectIdentifier) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:       big.NewInt(2),
		Subject:            pkix.Name{CommonName: hostname},
		DNSNames:           []string{hostname},
		NotBefore:          time.Now().Add(-time.Hour),
		NotAfter:           time.Now().Add(time.Hour),
		KeyUsage:           x509.KeyUsageDigitalSignature,
		ExtKeyUsage:        ekus,
		UnknownExtKeyUsage: unknownEKUs,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	require.NoError(t, err)

	return tls.Certificate{
		Certificate: [][]byte{der, ca.Raw},
		PrivateKey:  key,
	}
}

// writeTestCertsDir writes ca.crt (from ca) and client.crt/client.key (from
// serverIdentity) into a fresh temp directory, matching the layout
// loadIdentityCert/loadCAPool expect.
func writeTestCertsDir(t *testing.T, ca *x509.Certificate, serverIdentity tls.Certificate) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}), 0o600))

	var chainPEM []byte
	for _, der := range serverIdentity.Certificate {
		chainPEM = append(chainPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), chainPEM, 0o600))

	ecKey, ok := serverIdentity.PrivateKey.(*ecdsa.PrivateKey)
	require.True(t, ok)
	keyDER, err := x509.MarshalECPrivateKey(ecKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))

	return dir
}

func peerConfig(caPool *x509.CertPool, peerCert tls.Certificate) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{peerCert},
		RootCAs:      caPool,
		ServerName:   "tier-test-server",
	}
}

func TestLoadServerCredentials_RejectsIssuerCallerPeerCert(t *testing.T) {
	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "tier-test-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	dir := writeTestCertsDir(t, ca, serverIdentity)

	cfg, err := serverTLSConfig(dir)
	require.NoError(t, err)
	addr := startListener(t, cfg)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	bootstrapLikeCert := generateTestLeaf(t, ca, caKey, "peer", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []asn1.ObjectIdentifier{oidEKUIssuerCaller})

	err = dial(addr, peerConfig(caPool, bootstrapLikeCert))
	assert.Error(t, err, "a peer cert carrying EKUIssuerCaller must be rejected by the default (operating-tier) server config")
}

func TestLoadServerCredentials_AcceptsOperatingPeerCert(t *testing.T) {
	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "tier-test-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	dir := writeTestCertsDir(t, ca, serverIdentity)

	cfg, err := serverTLSConfig(dir)
	require.NoError(t, err)
	addr := startListener(t, cfg)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	operatingCert := generateTestLeaf(t, ca, caKey, "peer", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)

	err = dial(addr, peerConfig(caPool, operatingCert))
	assert.NoError(t, err, "a peer cert with no EKUIssuerCaller marker must be accepted by the default (operating-tier) server config")
}

func TestLoadIssuerServerCredentials_AcceptsIssuerCallerPeerCert(t *testing.T) {
	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "tier-test-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	dir := writeTestCertsDir(t, ca, serverIdentity)

	cfg, err := serverTLSConfigForTier(dir, requireIssuerCallerTier)
	require.NoError(t, err)
	addr := startListener(t, cfg)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	bootstrapLikeCert := generateTestLeaf(t, ca, caKey, "peer", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []asn1.ObjectIdentifier{oidEKUIssuerCaller})

	err = dial(addr, peerConfig(caPool, bootstrapLikeCert))
	assert.NoError(t, err, "a peer cert carrying EKUIssuerCaller must be accepted by an issuer-caller-tier server config")
}

func TestLoadIssuerServerCredentials_RejectsOperatingPeerCert(t *testing.T) {
	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "tier-test-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	dir := writeTestCertsDir(t, ca, serverIdentity)

	cfg, err := serverTLSConfigForTier(dir, requireIssuerCallerTier)
	require.NoError(t, err)
	addr := startListener(t, cfg)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	operatingCert := generateTestLeaf(t, ca, caKey, "peer", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)

	err = dial(addr, peerConfig(caPool, operatingCert))
	assert.Error(t, err, "a peer cert with no EKUIssuerCaller marker must be rejected by an issuer-caller-tier server config")
}

func TestLoadIssuerServerCredentials_Success(t *testing.T) {
	creds, err := LoadIssuerServerCredentials(fixtureCertsDir)
	require.NoError(t, err)
	assert.NotNil(t, creds)
}

func TestServerTLSConfig_AcceptsOperatingPeerCert(t *testing.T) {
	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "tier-test-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	dir := writeTestCertsDir(t, ca, serverIdentity)

	cfg, err := ServerTLSConfig(dir)
	require.NoError(t, err)
	addr := startListener(t, cfg)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	operatingLikeCert := generateTestLeaf(t, ca, caKey, "peer", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, nil)

	err = dial(addr, peerConfig(caPool, operatingLikeCert))
	assert.NoError(t, err, "an operating-tier peer cert must be accepted")
}

func TestServerTLSConfig_RejectsIssuerCallerPeerCert(t *testing.T) {
	ca, caKey := generateTestCA(t)
	serverIdentity := generateTestLeaf(t, ca, caKey, "tier-test-server", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, nil)
	dir := writeTestCertsDir(t, ca, serverIdentity)

	cfg, err := ServerTLSConfig(dir)
	require.NoError(t, err)
	addr := startListener(t, cfg)

	caPool := x509.NewCertPool()
	caPool.AddCert(ca)
	bootstrapLikeCert := generateTestLeaf(t, ca, caKey, "peer", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, []asn1.ObjectIdentifier{oidEKUIssuerCaller})

	err = dial(addr, peerConfig(caPool, bootstrapLikeCert))
	assert.Error(t, err, "a peer cert carrying EKUIssuerCaller must be rejected by ServerTLSConfig, same as LoadServerCredentials")
}
