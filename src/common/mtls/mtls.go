// Package mtls loads mutual-TLS credentials for miniprotector's gRPC
// transport. Every node (bwfs, brfs, rwfs) presents the same identity cert
// regardless of its client/server role: ca.crt, client.crt, client.key in a
// single directory.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc/credentials"
)

const (
	caCertFile    = "ca.crt"
	identCertFile = "client.crt"
	identKeyFile  = "client.key"
)

// oidEKUIssuerCaller marks a bootstrap-tier credential: a certificate whose
// only legitimate purpose is authenticating to issuer's RequestOperatingCert/
// DescribeSANs RPCs. Never present on an operating-tier certificate. See
// docs/SECURITY.md and
// docs/superpowers/specs/2026-07-05-credential-tier-enforcement-design.md.
var oidEKUIssuerCaller = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 3}

// requiredTier selects which credential tier a server's mTLS listener
// accepts from its peers.
type requiredTier int

const (
	// requireOperatingTier rejects any peer certificate carrying
	// oidEKUIssuerCaller -- the default for every server except issuer.
	requireOperatingTier requiredTier = iota
	// requireIssuerCallerTier rejects any peer certificate that does not
	// carry oidEKUIssuerCaller -- issuer's own listener uses this, since
	// its only legitimate caller presents a bootstrap credential.
	requireIssuerCallerTier
)

func hasIssuerCallerEKU(cert *x509.Certificate) bool {
	for _, oid := range cert.UnknownExtKeyUsage {
		if oid.Equal(oidEKUIssuerCaller) {
			return true
		}
	}
	return false
}

// verifyPeerTier returns a VerifyPeerCertificate callback enforcing tier on
// the peer's leaf certificate, in addition to (not instead of) the normal
// chain verification already performed via ClientCAs/ClientAuth.
func verifyPeerTier(tier requiredTier) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("no certificate presented by peer")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parse peer certificate: %w", err)
		}
		isIssuerCaller := hasIssuerCallerEKU(leaf)
		switch tier {
		case requireOperatingTier:
			if isIssuerCaller {
				return fmt.Errorf("peer presented a bootstrap/issuer-caller credential, not accepted on this listener")
			}
		case requireIssuerCallerTier:
			if !isIssuerCaller {
				return fmt.Errorf("peer presented an operating credential; this listener only accepts bootstrap/issuer-caller credentials")
			}
		}
		return nil
	}
}

func loadIdentityCertFiles(certsDir, certFile, keyFile string) (tls.Certificate, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, certFile),
		filepath.Join(certsDir, keyFile),
	)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("load identity cert/key from %s: %w", certsDir, err)
	}
	return cert, nil
}

func loadIdentityCert(certsDir string) (tls.Certificate, error) {
	return loadIdentityCertFiles(certsDir, identCertFile, identKeyFile)
}

// identityCacheTTL bounds how long cachedIdentity trusts an in-memory
// identity before re-checking disk. See
// docs/superpowers/specs/2026-08-07-mtls-credential-caching-design.md.
const identityCacheTTL = 60 * time.Second

// cachedIdentity caches a parsed tls.Certificate in memory, re-reading from
// disk only once its validity window has elapsed -- capped at
// identityCacheTTL or the certificate's own NotAfter, whichever is sooner --
// and even then only re-parsing if the underlying files' mtimes actually
// changed. This replaces a full disk read-and-parse on every single TLS
// handshake with, in the common case, a single mutex-guarded memory read.
//
// Works identically whether the process that rewrites certFile/keyFile is
// this same process (issuer's self-mint) or a different one (agent execing
// certclient operating-refresh) -- it only ever observes the filesystem,
// never assumes a push from the writer.
type cachedIdentity struct {
	certsDir, certFile, keyFile string
	now                         func() time.Time // time.Now in production; injectable for tests

	mu         sync.Mutex
	loaded     bool
	cert       tls.Certificate
	crtModTime time.Time
	keyModTime time.Time
	validUntil time.Time
}

func newCachedIdentity(certsDir, certFile, keyFile string) *cachedIdentity {
	return &cachedIdentity{
		certsDir: certsDir,
		certFile: certFile,
		keyFile:  keyFile,
		now:      time.Now,
	}
}

// Get returns the cached identity, refreshing it from disk first if the
// cache's validity window has elapsed. On a reload failure, it falls back
// to the last known-good identity (if any) rather than failing the caller,
// and deliberately leaves validUntil unadvanced so the very next call
// retries -- self-healing without ever failing a live handshake on a
// transient disk error.
func (c *cachedIdentity) Get() (tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	if c.loaded && now.Before(c.validUntil) {
		return c.cert, nil
	}

	crtPath := filepath.Join(c.certsDir, c.certFile)
	keyPath := filepath.Join(c.certsDir, c.keyFile)

	if c.loaded {
		crtInfo, crtErr := os.Stat(crtPath)
		keyInfo, keyErr := os.Stat(keyPath)
		if crtErr == nil && keyErr == nil &&
			crtInfo.ModTime().Equal(c.crtModTime) && keyInfo.ModTime().Equal(c.keyModTime) {
			c.validUntil = c.nextValidUntil(now)
			return c.cert, nil
		}
	}

	cert, err := tls.LoadX509KeyPair(crtPath, keyPath)
	if err != nil {
		if c.loaded {
			return c.fallbackOrError(now, err)
		}
		return tls.Certificate{}, err
	}
	crtInfo, err := os.Stat(crtPath)
	if err != nil {
		if c.loaded {
			return c.fallbackOrError(now, err)
		}
		return tls.Certificate{}, err
	}
	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		if c.loaded {
			return c.fallbackOrError(now, err)
		}
		return tls.Certificate{}, err
	}

	c.cert = cert
	c.crtModTime = crtInfo.ModTime()
	c.keyModTime = keyInfo.ModTime()
	c.loaded = true
	c.validUntil = c.nextValidUntil(now)
	return c.cert, nil
}

// fallbackOrError is called when a reload attempt fails and c.loaded is
// true. If the cached identity has already expired, it propagates the
// error rather than serving an expired certificate. Otherwise it logs the
// reload failure and serves the last known-good identity -- validUntil is
// deliberately left unadvanced by the caller, so the very next call
// retries.
func (c *cachedIdentity) fallbackOrError(now time.Time, err error) (tls.Certificate, error) {
	if c.cert.Leaf != nil && !now.Before(c.cert.Leaf.NotAfter) {
		return tls.Certificate{}, fmt.Errorf("identity reload failed and the cached certificate has expired: %w", err)
	}
	slog.Default().Warn("mtls: identity reload failed, serving last known-good credential",
		"certsDir", c.certsDir, "certFile", c.certFile, "keyFile", c.keyFile, "error", err)
	return c.cert, nil
}

// nextValidUntil caps the cache's validity window at c.cert's own
// expiration, so a near-expiry certificate gets re-checked sooner than a
// fresh one -- never serving a certificate past its own NotAfter.
func (c *cachedIdentity) nextValidUntil(now time.Time) time.Time {
	ttl := now.Add(identityCacheTTL)
	if c.cert.Leaf != nil && c.cert.Leaf.NotAfter.Before(ttl) {
		return c.cert.Leaf.NotAfter
	}
	return ttl
}

func loadCAPool(certsDir string) (*x509.CertPool, error) {
	caPEM, err := os.ReadFile(filepath.Join(certsDir, caCertFile))
	if err != nil {
		return nil, fmt.Errorf("read CA cert from %s: %w", certsDir, err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse CA cert from %s: no valid certificates found", certsDir)
	}
	return caPool, nil
}

func loadCertAndPool(certsDir string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := loadIdentityCert(certsDir)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return cert, caPool, nil
}

func serverTLSConfig(certsDir string) (*tls.Config, error) {
	return serverTLSConfigForTier(certsDir, requireOperatingTier)
}

// serverTLSConfigForTier is serverTLSConfig, parameterized on which
// credential tier the listener accepts from its peers.
func serverTLSConfigForTier(certsDir string, tier requiredTier) (*tls.Config, error) {
	return serverTLSConfigForTierWithClock(certsDir, tier, time.Now)
}

// serverTLSConfigForTierWithClock is serverTLSConfigForTier, parameterized
// on the clock cachedIdentity uses -- production always calls through
// serverTLSConfigForTier with time.Now; tests use this directly to advance
// past the cache TTL without a real sleep.
func serverTLSConfigForTierWithClock(certsDir string, tier requiredTier, now func() time.Time) (*tls.Config, error) {
	cache := newCachedIdentity(certsDir, identCertFile, identKeyFile)
	cache.now = now
	// Fail fast at build time if certsDir is missing/broken, rather than
	// only on the first handshake. This also warms the cache.
	if _, err := cache.Get(); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := cache.Get()
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
		ClientCAs:             caPool,
		ClientAuth:            tls.RequireAndVerifyClientCert,
		VerifyPeerCertificate: verifyPeerTier(tier),
	}, nil
}

// isLoopbackHost reports whether host is a loopback address/name where
// hostname verification against a cert's SAN would be an artificial
// provisioning burden (anything reachable via loopback is already running on
// the same trusted machine).
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func verifyChainOnly(caPool *x509.CertPool) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("no certificate presented by peer")
		}
		leaf, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("parse peer certificate: %w", err)
		}
		intermediates := x509.NewCertPool()
		for _, raw := range rawCerts[1:] {
			c, err := x509.ParseCertificate(raw)
			if err != nil {
				return fmt.Errorf("parse peer intermediate certificate: %w", err)
			}
			intermediates.AddCert(c)
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: caPool, Intermediates: intermediates}); err != nil {
			return fmt.Errorf("verify peer certificate chain: %w", err)
		}
		return nil
	}
}

func clientTLSConfigWithIdentity(certsDir, certFile, keyFile, host string) (*tls.Config, error) {
	return clientTLSConfigWithIdentityAndClock(certsDir, certFile, keyFile, host, time.Now)
}

// clientTLSConfigWithIdentityAndClock is clientTLSConfigWithIdentity,
// parameterized on the clock cachedIdentity uses -- same rationale as
// serverTLSConfigForTierWithClock.
func clientTLSConfigWithIdentityAndClock(certsDir, certFile, keyFile, host string, now func() time.Time) (*tls.Config, error) {
	cache := newCachedIdentity(certsDir, certFile, keyFile)
	cache.now = now
	// Fail fast at build time if certsDir is missing/broken, rather than
	// only on the first dial. This also warms the cache.
	if _, err := cache.Get(); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}

	getClientCert := func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		cert, err := cache.Get()
		if err != nil {
			return nil, err
		}
		return &cert, nil
	}

	if isLoopbackHost(host) {
		return &tls.Config{
			GetClientCertificate:  getClientCert,
			InsecureSkipVerify:    true, // hostname check disabled; chain is still verified below
			VerifyPeerCertificate: verifyChainOnly(caPool),
		}, nil
	}

	return &tls.Config{
		GetClientCertificate: getClientCert,
		RootCAs:              caPool,
		ServerName:           host,
	}, nil
}

func clientTLSConfig(certsDir, host string) (*tls.Config, error) {
	return clientTLSConfigWithIdentity(certsDir, identCertFile, identKeyFile, host)
}

// LoadServerCredentials builds gRPC transport credentials for a server that
// requires and verifies every client's certificate against certsDir/ca.crt.
// Any client cert signed by that CA is trusted, EXCEPT a bootstrap/
// issuer-caller credential (one carrying the oidEKUIssuerCaller EKU) --
// those are rejected here. issuer is the one exception; see
// LoadIssuerServerCredentials.
func LoadServerCredentials(certsDir string) (credentials.TransportCredentials, error) {
	cfg, err := serverTLSConfig(certsDir)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

// LoadIssuerServerCredentials is LoadServerCredentials with the tier check
// inverted: it accepts only bootstrap/issuer-caller credentials, rejecting
// any operating credential. Used solely by issuer's own listener, since
// issuer's only legitimate caller (certclient operating-refresh) always
// presents a bootstrap credential.
func LoadIssuerServerCredentials(certsDir string) (credentials.TransportCredentials, error) {
	cfg, err := serverTLSConfigForTier(certsDir, requireIssuerCallerTier)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

// ServerTLSConfig returns the raw operating-tier *tls.Config
// LoadServerCredentials wraps into gRPC transport credentials -- for a
// server built directly on net/http.Server (like log-gateway) instead of
// gRPC. Same tier enforcement (rejects a bootstrap/issuer-caller peer
// cert) and the same cached, TTL-and-expiry-bounded certificate reload
// every gRPC server's credentials already get from serverTLSConfigForTier
// -- see cachedIdentity.
func ServerTLSConfig(certsDir string) (*tls.Config, error) {
	return serverTLSConfigForTier(certsDir, requireOperatingTier)
}

// ClientTLSConfig returns the raw operating-tier *tls.Config
// LoadClientCredentials wraps into gRPC transport credentials -- for an
// HTTP client built directly on net/http (e.g. api-server dialing
// log-gateway's query_range proxy route) instead of gRPC. Presents the
// standard client.crt/client.key identity; same hostname/chain
// verification rules as LoadClientCredentials, including the same cached,
// TTL-and-expiry-bounded certificate reload via GetClientCertificate -- see
// cachedIdentity.
func ClientTLSConfig(certsDir, host string) (*tls.Config, error) {
	return clientTLSConfig(certsDir, host)
}

// LoadClientCredentialsWithIdentity is LoadClientCredentials, parameterized
// on which cert/key filenames to load -- used by callers presenting an
// identity other than the standard client.crt/client.key pair (e.g.
// certclient's operating-refresh, authenticating with bootstrap.crt/
// bootstrap.key). Hostname/SAN verification rules are identical.
func LoadClientCredentialsWithIdentity(certsDir, certFile, keyFile, host string) (credentials.TransportCredentials, error) {
	cfg, err := clientTLSConfigWithIdentity(certsDir, certFile, keyFile, host)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

// LoadClientCredentials builds gRPC transport credentials for dialing host,
// presenting certsDir/client.crt and certsDir/client.key. Hostname/SAN
// verification is skipped for loopback hosts (localhost, 127.0.0.0/8, ::1);
// every other host must match a SAN on the server's presented certificate.
func LoadClientCredentials(certsDir, host string) (credentials.TransportCredentials, error) {
	return LoadClientCredentialsWithIdentity(certsDir, identCertFile, identKeyFile, host)
}
