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

// writeSelfSignedIdentity writes a minimal, self-signed EC cert/key pair to
// certFile/keyFile inside dir, valid until notAfter. cachedIdentity's tests
// exercise Get() directly and never perform a real TLS handshake, so no CA
// chain is needed -- just a well-formed, parseable pair.
func writeSelfSignedIdentity(t *testing.T, dir, certFile, keyFile string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cache-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, certFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644))
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, keyFile), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))
}

func TestCachedIdentity_FirstGetLoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cert, err := cache.Get()
	require.NoError(t, err)
	assert.NotEmpty(t, cert.Certificate)
}

func TestCachedIdentity_FirstLoadFailurePropagates(t *testing.T) {
	dir := t.TempDir()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	_, err := cache.Get()
	assert.Error(t, err, "with no prior successful load, a load failure must propagate")
}

func TestCachedIdentity_WithinTTLServesFromMemoryWithoutDiskIO(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	fakeNow := time.Now()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	// Corrupt the files on disk. If Get() touched disk again, this would
	// either error (corrupt content) or return a different certificate.
	require.NoError(t, os.WriteFile(filepath.Join(dir, identCertFile), []byte("not a cert"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, identKeyFile), []byte("not a key"), 0o600))

	fakeNow = fakeNow.Add(30 * time.Second) // still within the 60s TTL
	second, err := cache.Get()
	require.NoError(t, err)
	assert.Equal(t, first.Certificate, second.Certificate, "within the TTL window, Get() must not re-read disk")
}

func TestCachedIdentity_TTLElapsedButMtimeUnchanged_SkipsReparsing(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	fakeNow := time.Now()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	crtPath := filepath.Join(dir, identCertFile)
	keyPath := filepath.Join(dir, identKeyFile)
	crtInfo, err := os.Stat(crtPath)
	require.NoError(t, err)
	keyInfo, err := os.Stat(keyPath)
	require.NoError(t, err)

	// Corrupt the content but restore the original mtimes -- proves the
	// unchanged-mtime path trusts mtime and genuinely skips reparsing,
	// rather than happening to reparse identical bytes back to the same
	// result. If it *did* reparse, this corrupted content would error.
	require.NoError(t, os.WriteFile(crtPath, []byte("not a cert"), 0o644))
	require.NoError(t, os.Chtimes(crtPath, crtInfo.ModTime(), crtInfo.ModTime()))
	require.NoError(t, os.WriteFile(keyPath, []byte("not a key"), 0o600))
	require.NoError(t, os.Chtimes(keyPath, keyInfo.ModTime(), keyInfo.ModTime()))

	fakeNow = fakeNow.Add(90 * time.Second) // past the 60s TTL
	second, err := cache.Get()
	require.NoError(t, err, "mtime unchanged, so Get() must not attempt to reparse the (corrupted) content")
	assert.Equal(t, first.Certificate, second.Certificate)
}

func TestCachedIdentity_MtimeChanged_Reloads(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	fakeNow := time.Now()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(2*time.Hour))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(dir, identCertFile), future, future))
	require.NoError(t, os.Chtimes(filepath.Join(dir, identKeyFile), future, future))

	fakeNow = fakeNow.Add(90 * time.Second)
	second, err := cache.Get()
	require.NoError(t, err)
	assert.NotEqual(t, first.Certificate, second.Certificate, "a genuinely rotated file must be picked up once the TTL has elapsed")
}

func TestCachedIdentity_TTLCappedByCertExpiration(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, start.Add(10*time.Second)) // expires well inside the 60s TTL

	fakeNow := start
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	// Rotate to a fresh, longer-lived cert, advancing the clock only 20s --
	// past the certificate's own 10s NotAfter, but well within a flat 60s
	// TTL. If validUntil were capped only at now()+60s, this would still
	// serve the already-expired cached cert.
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, start.Add(time.Hour))
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(dir, identCertFile), future, future))
	require.NoError(t, os.Chtimes(filepath.Join(dir, identKeyFile), future, future))

	fakeNow = start.Add(20 * time.Second)
	second, err := cache.Get()
	require.NoError(t, err)
	assert.NotEqual(t, first.Certificate, second.Certificate, "validUntil must be capped at the cached cert's own NotAfter, not just now()+60s")
}

func TestCachedIdentity_ReloadFailureWithExistingCache_FallsBackAndRetriesNextCall(t *testing.T) {
	dir := t.TempDir()
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(time.Hour))

	fakeNow := time.Now()
	cache := newCachedIdentity(dir, identCertFile, identKeyFile)
	cache.now = func() time.Time { return fakeNow }

	first, err := cache.Get()
	require.NoError(t, err)

	// Corrupt content AND change mtime, so a reload is actually attempted
	// and fails.
	future := time.Now().Add(time.Minute)
	require.NoError(t, os.WriteFile(filepath.Join(dir, identCertFile), []byte("not a cert"), 0o644))
	require.NoError(t, os.Chtimes(filepath.Join(dir, identCertFile), future, future))

	fakeNow = fakeNow.Add(90 * time.Second)
	second, err := cache.Get()
	require.NoError(t, err, "a reload failure must fall back to the last known-good identity, not fail the caller")
	assert.Equal(t, first.Certificate, second.Certificate)

	// Restore a valid, genuinely different cert. Because validUntil was
	// left unadvanced by the failed reload, the very next call must retry
	// immediately rather than waiting out another TTL window.
	writeSelfSignedIdentity(t, dir, identCertFile, identKeyFile, time.Now().Add(2*time.Hour))
	future2 := time.Now().Add(2 * time.Minute)
	require.NoError(t, os.Chtimes(filepath.Join(dir, identCertFile), future2, future2))
	require.NoError(t, os.Chtimes(filepath.Join(dir, identKeyFile), future2, future2))

	third, err := cache.Get() // fakeNow unchanged since the previous call
	require.NoError(t, err)
	assert.NotEqual(t, first.Certificate, third.Certificate, "a failed reload must not advance validUntil, so the next call retries immediately")
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

func TestClientTLSConfig_Success(t *testing.T) {
	cfg, err := ClientTLSConfig(fixtureCertsDir, "bwfs.internal")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.NotNil(t, cfg.GetClientCertificate, "must present this node's identity via GetClientCertificate for cert-reload-on-handshake, same as clientTLSConfig")
}

func TestClientTLSConfig_MissingCAFile(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, fixtureCertsDir+"/client.crt", dir+"/client.crt")
	copyFile(t, fixtureCertsDir+"/client.key", dir+"/client.key")
	// ca.crt intentionally omitted

	_, err := ClientTLSConfig(dir, "bwfs.internal")
	assert.Error(t, err)
}
