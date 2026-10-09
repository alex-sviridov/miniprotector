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
// only legitimate purpose is authenticating to issuer's RequestOperatingCert
// RPC. Never present on an operating-tier certificate. See
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

// cachedIdentity serves a parsed tls.Certificate from memory and reloads it
// only when the underlying files' mtimes change, replacing a full read and
// parse on every TLS handshake with two stat calls. It works the same whether
// the process that rewrites the files is this one (issuer's self-mint) or
// another (agent execing certclient operating-refresh): it only observes the
// filesystem.
//
// If a reload fails (e.g. the cert and key are momentarily mismatched), Get
// serves the last known-good identity while it has not expired, and -- since
// the stored mtimes are left alone -- retries on the very next call.
type cachedIdentity struct {
	crtPath, keyPath string

	mu             sync.Mutex
	loaded         bool
	cert           tls.Certificate
	crtMod, keyMod time.Time
}

func newCachedIdentity(certsDir, certFile, keyFile string) *cachedIdentity {
	return &cachedIdentity{
		crtPath: filepath.Join(certsDir, certFile),
		keyPath: filepath.Join(certsDir, keyFile),
	}
}

func (c *cachedIdentity) Get() (tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Stat before loading: if a file changes in between, the stored mtime is
	// older than the loaded content, which only causes one harmless extra
	// reload.
	crtMod, keyMod, err := modTimes(c.crtPath, c.keyPath)
	if err == nil && c.loaded && crtMod.Equal(c.crtMod) && keyMod.Equal(c.keyMod) {
		return c.cert, nil
	}
	if err == nil {
		var cert tls.Certificate
		if cert, err = tls.LoadX509KeyPair(c.crtPath, c.keyPath); err == nil {
			c.cert, c.crtMod, c.keyMod, c.loaded = cert, crtMod, keyMod, true
			return cert, nil
		}
	}

	if !c.loaded {
		return tls.Certificate{}, err
	}
	if c.cert.Leaf != nil && !time.Now().Before(c.cert.Leaf.NotAfter) {
		return tls.Certificate{}, fmt.Errorf("identity reload failed and the cached certificate has expired: %w", err)
	}
	slog.Default().Warn("mtls: identity reload failed, serving last known-good credential",
		"cert", c.crtPath, "key", c.keyPath, "error", err)
	return c.cert, nil
}

func modTimes(paths ...string) (a, b time.Time, err error) {
	var mods [2]time.Time
	for i, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return a, b, err
		}
		mods[i] = info.ModTime()
	}
	return mods[0], mods[1], nil
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

// serverTLSConfigForTier builds a server config requiring and verifying every
// client's certificate against certsDir/ca.crt, then applying tier.
func serverTLSConfigForTier(certsDir string, tier requiredTier) (*tls.Config, error) {
	cache := newCachedIdentity(certsDir, identCertFile, identKeyFile)
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

func clientTLSConfig(certsDir, certFile, keyFile, host string) (*tls.Config, error) {
	cache := newCachedIdentity(certsDir, certFile, keyFile)
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

// LoadServerCredentials builds gRPC transport credentials for a server that
// requires and verifies every client's certificate against certsDir/ca.crt.
// Any client cert signed by that CA is trusted, EXCEPT a bootstrap/
// issuer-caller credential (one carrying the oidEKUIssuerCaller EKU) --
// those are rejected here. issuer is the one exception; see
// LoadIssuerServerCredentials.
func LoadServerCredentials(certsDir string) (credentials.TransportCredentials, error) {
	cfg, err := ServerTLSConfig(certsDir)
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
	return clientTLSConfig(certsDir, identCertFile, identKeyFile, host)
}

// LoadClientCredentialsWithIdentity is LoadClientCredentials, parameterized
// on which cert/key filenames to load -- used by callers presenting an
// identity other than the standard client.crt/client.key pair (e.g.
// certclient's operating-refresh, authenticating with bootstrap.crt/
// bootstrap.key). Hostname/SAN verification rules are identical.
func LoadClientCredentialsWithIdentity(certsDir, certFile, keyFile, host string) (credentials.TransportCredentials, error) {
	cfg, err := clientTLSConfig(certsDir, certFile, keyFile, host)
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
