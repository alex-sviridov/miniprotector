# Credential Tier Enforcement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Cryptographically confine bootstrap credentials to only ever authenticating to `issuer`, closing the gap `docs/SECURITY.md` already discloses by name: today a leaked `bootstrap.crt` authenticates to `bwfs`/`catalog` exactly as well as an operating credential does, since `common/mtls` trusts any CA-signed certificate regardless of tier.

**Architecture:** Bootstrap certificates gain `extKeyUsage: ["clientAuth"]` only (dropping `serverAuth`, since they never run a server) plus a new custom Extended Key Usage OID, `1.3.6.1.4.1.61183.1.3` (named `EKUIssuerCaller`), set by a branch in the CA's leaf template keyed on a new `"tier"` field in `TemplateData`. `common/mtls.LoadServerCredentials` (used by `bwfs`/`catalog`, unchanged signature) now additionally rejects any peer certificate carrying that marker; a new `mtls.LoadIssuerServerCredentials` — used only by `issuer` — enforces the opposite. `bwfs`/`catalog`'s `main.go` files and `connection.StartServer`'s signature are untouched; only `issuer`'s `main.go` and the two Sign call sites (`certclient/bootstrap.go`, `issuer/mintsign.go`) change.

**Tech Stack:** Go, `crypto/x509`/`crypto/tls` (standard library `ExtKeyUsage`/`UnknownExtKeyUsage`), gRPC (`google.golang.org/grpc/credentials`), `smallstep/certificates/{ca,api}` v0.30.2 (pinned, already used throughout), `go.step.sm/crypto/x509util`'s CA template (`unknownExtKeyUsage` field).

## Global Constraints

- `mtls.LoadServerCredentials(certsDir)` keeps its exact existing signature — every current caller (`bwfs`, `catalog`, and their tests) needs zero changes.
- `connection.StartServer(...)` keeps its exact existing signature and behavior.
- Only `cmd/issuer/main.go` switches to the new `LoadIssuerServerCredentials` + `StartServerWithCredentials` path. No other `main.go` changes.
- The custom EKU OID is `1.3.6.1.4.1.61183.1.3`, named `EKUIssuerCaller` in code and docs — never described using the words "server"/"client" (already overloaded in this codebase for backup roles and RPC roles).
- This ships against the demo-lab environment (`deploy/control-plane`) only. No live-migration path for certificates issued before this change — a clean re-provision (wipe the CA/client-manager volumes, re-run the enroll walkthrough) is expected and sufficient. Do not build migration/grace-period logic.
- No new error type, status code, or structured log signal for a tier mismatch — it surfaces as a plain TLS/gRPC handshake failure, consistent with every other existing mTLS rejection in this codebase.
- Full design rationale: `docs/superpowers/specs/2026-07-05-credential-tier-enforcement-design.md`.

---

### Task 1: `common/mtls` — EKU-based credential tier enforcement

**Files:**
- Modify: `src/common/mtls/mtls.go`
- Modify: `src/common/mtls/mtls_test.go`

**Interfaces:**
- Produces: `LoadIssuerServerCredentials(certsDir string) (credentials.TransportCredentials, error)` — new. `LoadServerCredentials(certsDir string) (credentials.TransportCredentials, error)` — signature unchanged, behavior additionally rejects peer certs carrying the `EKUIssuerCaller` marker.
- Produces (unexported, used by this task's own tests and Task 1 only): `requiredTier` type, `requireOperatingTier`/`requireIssuerCallerTier` constants, `serverTLSConfigForTier(certsDir string, tier requiredTier) (*tls.Config, error)`, `oidEKUIssuerCaller`.

- [ ] **Step 1: Write the failing tests**

Open `src/common/mtls/mtls_test.go`. First, replace the existing `startTestServer` helper (it currently builds both the listener config *and* the accept loop inline) with a lower-level `startListener` helper plus a `startTestServer` that calls it — this lets the new tier tests reuse the same accept loop without duplicating it:

Replace:
```go
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
```

With:
```go
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
```

Now append the following to the end of the file — helpers to build in-memory CA+leaf certs with controllable `ExtKeyUsage`/`UnknownExtKeyUsage` (the static fixtures in `testdata/certs` carry no `ExtKeyUsage` extension at all, so they can't exercise the tier check), plus five new tests:

```go
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
```

Finally, add the new imports these tests need at the top of the file:
```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/mtls/... -run 'TestLoadServerCredentials_RejectsIssuerCallerPeerCert|TestLoadServerCredentials_AcceptsOperatingPeerCert|TestLoadIssuerServerCredentials' -v`
Expected: build failure — `oidEKUIssuerCaller`, `requireIssuerCallerTier`, `serverTLSConfigForTier`, and `LoadIssuerServerCredentials` are undefined.

- [ ] **Step 3: Implement the tier marker and enforcement**

In `src/common/mtls/mtls.go`, add `"encoding/asn1"` to the import block, then add after the existing `const` block (`caCertFile`/`identCertFile`/`identKeyFile`):

```go
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
```

Then replace the existing `serverTLSConfig` function:

```go
func serverTLSConfig(certsDir string) (*tls.Config, error) {
	// Fail fast at build time if certsDir is missing/broken, rather than
	// only on the first handshake.
	if _, err := loadIdentityCert(certsDir); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := loadIdentityCert(certsDir)
			if err != nil {
				return nil, err
			}
			return &cert, nil
		},
		ClientCAs:  caPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}, nil
}
```

With:

```go
func serverTLSConfig(certsDir string) (*tls.Config, error) {
	return serverTLSConfigForTier(certsDir, requireOperatingTier)
}

// serverTLSConfigForTier is serverTLSConfig, parameterized on which
// credential tier the listener accepts from its peers.
func serverTLSConfigForTier(certsDir string, tier requiredTier) (*tls.Config, error) {
	// Fail fast at build time if certsDir is missing/broken, rather than
	// only on the first handshake.
	if _, err := loadIdentityCert(certsDir); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			cert, err := loadIdentityCert(certsDir)
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
```

Finally, update the doc comment on `LoadServerCredentials` and add `LoadIssuerServerCredentials` right after it:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/mtls/... -v`
Expected: PASS (all tests in the package, including every pre-existing one — confirms zero regression).

- [ ] **Step 5: Commit**

```bash
git add src/common/mtls/mtls.go src/common/mtls/mtls_test.go
git commit -m "$(cat <<'EOF'
feat(mtls): enforce credential tier via a custom Extended Key Usage

Bootstrap certificates will soon carry a custom EKU (EKUIssuerCaller,
OID 1.3.6.1.4.1.61183.1.3) marking them as issuer-only credentials.
LoadServerCredentials now rejects any peer cert carrying that marker
(the default, used by bwfs/catalog); the new LoadIssuerServerCredentials
enforces the opposite, for issuer's own listener.
EOF
)"
```

---

### Task 2: `common/connection` — `StartServerWithCredentials`

**Files:**
- Modify: `src/common/connection/server.go`
- Modify: `src/common/connection/mtls_wiring_test.go`

**Interfaces:**
- Consumes: nothing new from Task 1 directly (uses `mtls.LoadServerCredentials`, already existing, in its own test).
- Produces: `StartServerWithCredentials(ctx context.Context, logger *slog.Logger, port int, creds credentials.TransportCredentials, register func(*grpc.Server)) error`. `StartServer(...)` keeps its exact existing signature and behavior, now implemented in terms of this.

- [ ] **Step 1: Write the failing test**

Append to `src/common/connection/mtls_wiring_test.go`:

```go
func TestStartServerWithCredentials_RoundTripSucceeds(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	creds, err := mtls.LoadServerCredentials(fixtureCertsDir)
	require.NoError(t, err)

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServerWithCredentials(ctx, testLogger(), port, creds, func(s *grpc.Server) {})
	}()
	time.Sleep(100 * time.Millisecond)

	conn, err := Connect("127.0.0.1", port, 5, fixtureCertsDir)
	require.NoError(t, err)
	conn.Close()

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("StartServerWithCredentials did not shut down in time")
	}
}
```

Add `"github.com/alex-sviridov/miniprotector/common/mtls"` to this file's import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./common/connection/... -run TestStartServerWithCredentials_RoundTripSucceeds -v`
Expected: build failure — `StartServerWithCredentials` is undefined.

- [ ] **Step 3: Implement**

In `src/common/connection/server.go`, add `"google.golang.org/grpc/credentials"` to the import block, then replace the existing `StartServer` function:

```go
func StartServer(ctx context.Context, logger *slog.Logger, port int, certsDir string, register func(*grpc.Server)) error {
	creds, err := mtls.LoadServerCredentials(certsDir)
	if err != nil {
		return fmt.Errorf("failed to load server credentials: %w", err)
	}

	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	grpcServer := grpc.NewServer(grpc.Creds(creds))
	register(grpcServer)

	logger.Info("Server ready, accepting connections")

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	return grpcServer.Serve(listener)
}
```

With:

```go
// StartServer creates and starts a gRPC server on the specified port,
// requiring mutual TLS using the certs in certsDir (ca.crt, client.crt,
// client.key). The register callback receives the bare *grpc.Server so
// callers can register any service (backup, restore, …) without this
// package importing service-specific proto packages.
func StartServer(ctx context.Context, logger *slog.Logger, port int, certsDir string, register func(*grpc.Server)) error {
	creds, err := mtls.LoadServerCredentials(certsDir)
	if err != nil {
		return fmt.Errorf("failed to load server credentials: %w", err)
	}
	return StartServerWithCredentials(ctx, logger, port, creds, register)
}

// StartServerWithCredentials is StartServer, parameterized on already-built
// transport credentials instead of loading the default certsDir/client.crt
// identity -- used by callers presenting a different credential requirement
// (issuer, which requires bootstrap/issuer-caller peer certs rather than the
// default operating-tier check; see mtls.LoadIssuerServerCredentials).
func StartServerWithCredentials(ctx context.Context, logger *slog.Logger, port int, creds credentials.TransportCredentials, register func(*grpc.Server)) error {
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return fmt.Errorf("failed to listen on port %d: %w", port, err)
	}

	logger.Info("Server starting", "port", port)

	grpcServer := grpc.NewServer(grpc.Creds(creds))
	register(grpcServer)

	logger.Info("Server ready, accepting connections")

	go func() {
		<-ctx.Done()
		logger.Info("Shutting down server...")
		grpcServer.GracefulStop()
	}()

	return grpcServer.Serve(listener)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/connection/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/common/connection/server.go src/common/connection/mtls_wiring_test.go
git commit -m "$(cat <<'EOF'
feat(connection): add StartServerWithCredentials

StartServer now delegates to it after loading the default operating-
tier credentials. issuer will use StartServerWithCredentials directly,
passing mtls.LoadIssuerServerCredentials's inverted-tier credentials
instead -- the one listener in this codebase that needs to.
EOF
)"
```

---

### Task 3: `issuer` — enforce issuer-caller tier on its own listener, tag its own mints as `operating`

**Files:**
- Modify: `src/cmd/issuer/main.go`
- Modify: `src/cmd/issuer/mintsign.go`

**Interfaces:**
- Consumes: `mtls.LoadIssuerServerCredentials` (Task 1), `connection.StartServerWithCredentials` (Task 2).
- Produces: nothing new for later tasks — `mintAndSign`'s exported signature is unchanged; only its internal `TemplateData` shape changes, which Task 6's e2e tests read.

- [ ] **Step 1: Update `mintsign.go`'s `TemplateData` shape**

In `src/cmd/issuer/mintsign.go`, replace:

```go
	templateData, err := json.Marshal(attributes)
	if err != nil {
		return nil, fmt.Errorf("marshal attributes: %w", err)
	}
```

With:

```go
	templateData, err := json.Marshal(struct {
		Tier       string            `json:"tier"`
		Attributes map[string]string `json:"attributes,omitempty"`
	}{
		Tier:       "operating",
		Attributes: attributes,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal template data: %w", err)
	}
```

`omitempty` on `Attributes` omits the field for both `nil` and empty (non-nil) maps — the same two cases `TestE2E_MintAndSignEmbedsAttributesAsCertificateExtension` (already passing, unmodified) already covers, so its assertions about when the attribute extension does/doesn't appear keep passing unchanged once Task 4's template reads `.Insecure.User.attributes` instead of `.Insecure.User`.

- [ ] **Step 2: Update `main.go` to use the issuer-caller-tier listener**

In `src/cmd/issuer/main.go`, add `"github.com/alex-sviridov/miniprotector/common/mtls"` to the import block, then replace:

```go
	if err := connection.StartServer(signalCtx, logger, conf.IssuerPort, certsDir, func(s *grpc.Server) {
		pb.RegisterIssuerServiceServer(s, srv)
	}); err != nil {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
```

With:

```go
	creds, err := mtls.LoadIssuerServerCredentials(certsDir)
	if err != nil {
		logger.Error("failed to load server credentials", "error", err)
		os.Exit(1)
	}

	if err := connection.StartServerWithCredentials(signalCtx, logger, conf.IssuerPort, creds, func(s *grpc.Server) {
		pb.RegisterIssuerServiceServer(s, srv)
	}); err != nil {
		logger.Error("serve failed", "error", err)
		os.Exit(1)
	}
```

- [ ] **Step 3: Confirm it builds**

Run: `cd src && go build ./cmd/issuer/...`
Expected: no output, exit code 0.

- [ ] **Step 4: Run existing issuer unit tests to confirm no regression**

Run: `cd src && go test ./cmd/issuer/... -v`
Expected: PASS (this runs `server_test.go`/`selfidentity_test.go`; `e2e_test.go` is skipped since it's behind the `e2e` build tag and no `-tags=e2e` was passed).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/issuer/main.go src/cmd/issuer/mintsign.go
git commit -m "$(cat <<'EOF'
feat(issuer): require issuer-caller-tier peer certs on its own listener

issuer now loads mtls.LoadIssuerServerCredentials instead of the
default mtls.LoadServerCredentials -- the one listener in this codebase
that should accept bootstrap credentials and reject operating ones,
the reverse of every other server. mintAndSign's TemplateData now
declares "tier":"operating" alongside attributes, which the next task's
CA template branches on.
EOF
)"
```

---

### Task 4: CA leaf template — branch `extKeyUsage` on tier

**Files:**
- Modify: `deploy/control-plane/ca/templates/leaf.tpl`

**Interfaces:**
- Consumes: `TemplateData` shape `{"tier": "operating"|"bootstrap", "attributes": {...}}` (operating, from Task 3) or `{"tier": "bootstrap"}` (bootstrap, from Task 5).
- Produces: nothing new for later tasks by name — Task 5 and Task 6 depend on this file's *rendered output*, not a Go symbol.

There is no non-Docker unit harness for this file — `x509util`'s CA template is only ever exercised through a real step-ca (see `docs/superpowers/specs/2026-07-05-issuer-attribute-template-design.md` for why the existing attribute-extension work tested it the same way). Task 6's e2e tests are what prove this renders correctly; there's no separate "run the test, watch it fail" step for a template file with no direct test runner.

- [ ] **Step 1: Edit the template**

Replace the full contents of `deploy/control-plane/ca/templates/leaf.tpl`:

```
{
	"subject": {{ toJson .Subject }},
	"sans": {{ toJson .SANs }},
{{- if typeIs "*rsa.PublicKey" .Insecure.CR.PublicKey }}
	"keyUsage": ["keyEncipherment", "digitalSignature"],
{{- else }}
	"keyUsage": ["digitalSignature"],
{{- end }}
	"extKeyUsage": ["serverAuth", "clientAuth"]
{{- if .Insecure.User }},
	"extensions": [{
		"id": "1.3.6.1.4.1.61183.1.1",
		"critical": false,
		"value": "{{ toJson .Insecure.User | b64enc }}"
	}]
{{- end }}
}
```

With:

```
{
	"subject": {{ toJson .Subject }},
	"sans": {{ toJson .SANs }},
{{- if typeIs "*rsa.PublicKey" .Insecure.CR.PublicKey }}
	"keyUsage": ["keyEncipherment", "digitalSignature"],
{{- else }}
	"keyUsage": ["digitalSignature"],
{{- end }}
{{- if eq .Insecure.User.tier "bootstrap" }}
	"extKeyUsage": ["clientAuth"],
	"unknownExtKeyUsage": ["1.3.6.1.4.1.61183.1.3"]
{{- else }}
	"extKeyUsage": ["serverAuth", "clientAuth"]
{{- end }}
{{- if .Insecure.User.attributes }},
	"extensions": [{
		"id": "1.3.6.1.4.1.61183.1.1",
		"critical": false,
		"value": "{{ toJson .Insecure.User.attributes | b64enc }}"
	}]
{{- end }}
}
```

- [ ] **Step 2: Commit**

```bash
git add deploy/control-plane/ca/templates/leaf.tpl
git commit -m "$(cat <<'EOF'
feat(deploy): branch CA leaf template's extKeyUsage on credential tier

A "tier":"bootstrap" TemplateData value now produces extKeyUsage:
["clientAuth"] plus the custom EKUIssuerCaller marker (OID
1.3.6.1.4.1.61183.1.3); anything else (including today's only other
caller, issuer's "tier":"operating") keeps the existing serverAuth+
clientAuth pair unchanged. The attribute extension now reads
.Insecure.User.attributes instead of the whole object, matching the
new nested TemplateData shape.
EOF
)"
```

---

### Task 5: `certclient bootstrap` — tag its Sign request `"tier":"bootstrap"`

**Files:**
- Modify: `src/cmd/certclient/bootstrap.go`
- Modify: `src/cmd/certclient/bootstrap_test.go`

**Interfaces:**
- Consumes: nothing new by name (uses the existing `signer` interface and `ca.CreateSignRequest`).
- Produces: nothing new for later tasks — Task 6's e2e test re-implements this same logic directly against a real CA rather than importing this package (a different `main` package can't be imported).

- [ ] **Step 1: Write the failing test**

Append to `src/cmd/certclient/bootstrap_test.go`, and change `fakeSigner` to capture the request it received (needed so the new test can inspect `TemplateData`):

Replace:
```go
type fakeSigner struct {
	resp *api.SignResponse
	err  error
}

func (f *fakeSigner) Sign(_ *api.SignRequest) (*api.SignResponse, error) {
	return f.resp, f.err
}
```

With:
```go
type fakeSigner struct {
	resp   *api.SignResponse
	err    error
	gotReq *api.SignRequest
}

func (f *fakeSigner) Sign(req *api.SignRequest) (*api.SignResponse, error) {
	f.gotReq = req
	return f.resp, f.err
}
```

Then append this new test at the end of the file:

```go
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
```

Add `"encoding/json"` to this file's import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./cmd/certclient/... -run TestBootstrap_SetsBootstrapTierTemplateData -v`
Expected: FAIL — `signer.gotReq` is `nil` (production code doesn't set `TemplateData` yet, and the pre-change `Sign` didn't capture the request either — this step's `fakeSigner` change plus the new test together are what fail first).

- [ ] **Step 3: Implement**

In `src/cmd/certclient/bootstrap.go`, add `"encoding/json"` to the import block, then replace:

```go
func bootstrap(token string, client signer, certsDir string) error {
	req, pk, err := ca.CreateSignRequest(token)
	if err != nil {
		return fmt.Errorf("create sign request: %w", err)
	}

	sign, err := client.Sign(req)
	if err != nil {
		return fmt.Errorf("sign request: %w", err)
	}

	return writeIdentity(certsDir, sign, pk)
}
```

With:

```go
func bootstrap(token string, client signer, certsDir string) error {
	req, pk, err := ca.CreateSignRequest(token)
	if err != nil {
		return fmt.Errorf("create sign request: %w", err)
	}

	templateData, err := json.Marshal(struct {
		Tier string `json:"tier"`
	}{Tier: "bootstrap"})
	if err != nil {
		return fmt.Errorf("marshal template data: %w", err)
	}
	req.TemplateData = templateData

	sign, err := client.Sign(req)
	if err != nil {
		return fmt.Errorf("sign request: %w", err)
	}

	return writeIdentity(certsDir, sign, pk)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/certclient/... -v`
Expected: PASS (all tests in the package, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/certclient/bootstrap.go src/cmd/certclient/bootstrap_test.go
git commit -m "$(cat <<'EOF'
feat(certclient): tag bootstrap redemption's sign request tier=bootstrap

The CA's leaf template (previous commit) reads this to mark the
issued certificate as a bootstrap/issuer-caller credential instead of
an operating one -- the half of the tier split certclient is
responsible for; issuer's own mint path already sends tier=operating.
EOF
)"
```

---

### Task 6: Real-CA e2e proof — tier markers land correctly and are actually enforced at handshake

**Files:**
- Modify: `src/cmd/issuer/e2e_test.go`

**Interfaces:**
- Consumes: `mintAndSign` (existing), `mtls.LoadServerCredentials`/`LoadIssuerServerCredentials` (Task 1), `connection.StartServerWithCredentials`/`ConnectWithIdentity` (Task 2, existing), `certmint.Mint` (existing), `ca.CreateSignRequest`/`ca.NewClient`/`ca.Certificate` (existing smallstep SDK, already used elsewhere in this file/package).
- Produces: nothing for later tasks — this is the terminal proof task.

- [ ] **Step 1: Write the new e2e tests**

Append to `src/cmd/issuer/e2e_test.go`. First, add the new imports this needs to the existing import block: `"crypto"`, `"log/slog"`, `"net"`.

Then add these helpers and tests:

```go
// issuerCallerExtKeyUsageOID is the custom Extended Key Usage that marks a
// bootstrap-tier certificate -- see common/mtls and
// docs/superpowers/specs/2026-07-05-credential-tier-enforcement-design.md.
var issuerCallerExtKeyUsageOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 3}

// hasEKU reports whether cert's ExtKeyUsage list contains want.
func hasEKU(cert *x509.Certificate, want x509.ExtKeyUsage) bool {
	for _, eku := range cert.ExtKeyUsage {
		if eku == want {
			return true
		}
	}
	return false
}

// hasUnknownEKU reports whether cert's UnknownExtKeyUsage list contains oid.
func hasUnknownEKU(cert *x509.Certificate, oid asn1.ObjectIdentifier) bool {
	for _, got := range cert.UnknownExtKeyUsage {
		if got.Equal(oid) {
			return true
		}
	}
	return false
}

// signBootstrapTierCert mints a real enrollment token and redeems it against
// the CA exactly as certclient/bootstrap.go's bootstrap() does -- including
// setting TemplateData{"tier":"bootstrap"} -- so this proves the same code
// path production uses, not a hand-simplified stand-in. Returns the parsed
// leaf certificate and the private key generated for it.
func signBootstrapTierCert(t *testing.T, opts certmint.Options, hostname string) (*x509.Certificate, crypto.PrivateKey) {
	t.Helper()
	token, err := certmint.Mint(hostname, nil, opts)
	require.NoError(t, err)

	req, pk, err := ca.CreateSignRequest(token)
	require.NoError(t, err)

	templateData, err := json.Marshal(struct {
		Tier string `json:"tier"`
	}{Tier: "bootstrap"})
	require.NoError(t, err)
	req.TemplateData = templateData

	client, err := ca.NewClient(opts.CAURL, ca.WithRootFile(opts.RootFile))
	require.NoError(t, err)

	signResp, err := client.Sign(req)
	require.NoError(t, err)

	leaf, err := ca.Certificate(signResp)
	require.NoError(t, err)
	return leaf, pk
}

// writeCertsDir writes ca.crt (a copy of opts.RootFile) and leaf/key as
// client.crt/client.key into a fresh temp directory, matching the layout
// common/mtls expects.
func writeCertsDir(t *testing.T, opts certmint.Options, leaf *x509.Certificate, key crypto.PrivateKey) string {
	t.Helper()
	dir := t.TempDir()

	rootPEM, err := os.ReadFile(opts.RootFile)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), rootPEM, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}), 0o644))

	ecKey, ok := key.(*ecdsa.PrivateKey)
	require.True(t, ok, "expected an ECDSA private key")
	keyDER, err := x509.MarshalECPrivateKey(ecKey)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.key"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600))

	return dir
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// TestE2E_MintAndSignOperatingCertHasNoIssuerCallerEKU proves the operating
// tier issued by mintAndSign (used for every real RequestOperatingCert call
// and issuer's own self-mint) carries the full serverAuth+clientAuth
// ExtKeyUsage and never the bootstrap-only EKUIssuerCaller marker -- the
// property common/mtls's default (operating-tier) server config relies on
// to accept it.
func TestE2E_MintAndSignOperatingCertHasNoIssuerCallerEKU(t *testing.T) {
	opts := startTestCA(t, "issuer-e2e-operating-eku")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "e2e-operating-eku-host"},
	}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(csrDER)
	require.NoError(t, err)

	chainPEM, err := mintAndSign("e2e-operating-eku-host", nil, nil, csr, opts, 3600)
	require.NoError(t, err, "mintAndSign")

	block, _ := pem.Decode(chainPEM)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	assert.True(t, hasEKU(leaf, x509.ExtKeyUsageServerAuth), "operating cert must carry serverAuth")
	assert.True(t, hasEKU(leaf, x509.ExtKeyUsageClientAuth), "operating cert must carry clientAuth")
	assert.False(t, hasUnknownEKU(leaf, issuerCallerExtKeyUsageOID), "operating cert must not carry the bootstrap-only EKUIssuerCaller marker")
}

// TestE2E_BootstrapTierCertHasIssuerCallerEKU proves the bootstrap tier
// certclient/bootstrap.go's real redemption flow produces carries only
// clientAuth (never serverAuth) plus the custom EKUIssuerCaller marker --
// the property mtls.LoadIssuerServerCredentials relies on to accept it and
// mtls.LoadServerCredentials relies on to reject it.
func TestE2E_BootstrapTierCertHasIssuerCallerEKU(t *testing.T) {
	opts := startTestCA(t, "issuer-e2e-bootstrap-eku")

	leaf, _ := signBootstrapTierCert(t, opts, "e2e-bootstrap-eku-host")

	assert.False(t, hasEKU(leaf, x509.ExtKeyUsageServerAuth), "bootstrap cert must not carry serverAuth")
	assert.True(t, hasEKU(leaf, x509.ExtKeyUsageClientAuth), "bootstrap cert must carry clientAuth")
	assert.True(t, hasUnknownEKU(leaf, issuerCallerExtKeyUsageOID), "bootstrap cert must carry the EKUIssuerCaller marker")
}

// TestE2E_CredentialTierEnforcedAtHandshake proves the whole pipeline this
// design built actually closes the gap docs/SECURITY.md flagged: a real
// bootstrap-tier certificate (mirroring certclient bootstrap's redemption
// flow) is accepted by an issuer-tier listener and rejected by an
// operating-tier listener, and a real operating-tier certificate (minted via
// mintAndSign, the same path RequestOperatingCert/self-mint use) is accepted
// by an operating-tier listener and rejected by an issuer-tier listener.
func TestE2E_CredentialTierEnforcedAtHandshake(t *testing.T) {
	opts := startTestCA(t, "issuer-e2e-tier-enforce")

	operatingKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	operatingCSRDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "e2e-tier-operating-host"},
	}, operatingKey)
	require.NoError(t, err)
	operatingCSR, err := x509.ParseCertificateRequest(operatingCSRDER)
	require.NoError(t, err)
	operatingChainPEM, err := mintAndSign("e2e-tier-operating-host", nil, nil, operatingCSR, opts, 3600)
	require.NoError(t, err)
	operatingBlock, _ := pem.Decode(operatingChainPEM)
	require.NotNil(t, operatingBlock)
	operatingLeaf, err := x509.ParseCertificate(operatingBlock.Bytes)
	require.NoError(t, err)
	operatingCertsDir := writeCertsDir(t, opts, operatingLeaf, operatingKey)

	bootstrapLeaf, bootstrapKey := signBootstrapTierCert(t, opts, "e2e-tier-bootstrap-host")
	bootstrapCertsDir := writeCertsDir(t, opts, bootstrapLeaf, bootstrapKey)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	operatingListenerCreds, err := mtls.LoadServerCredentials(operatingCertsDir)
	require.NoError(t, err)
	operatingPort := freeTCPPort(t)
	go func() {
		_ = connection.StartServerWithCredentials(ctx, logger, operatingPort, operatingListenerCreds, func(s *grpc.Server) {})
	}()

	issuerListenerCreds, err := mtls.LoadIssuerServerCredentials(operatingCertsDir)
	require.NoError(t, err)
	issuerPort := freeTCPPort(t)
	go func() {
		_ = connection.StartServerWithCredentials(ctx, logger, issuerPort, issuerListenerCreds, func(s *grpc.Server) {})
	}()

	time.Sleep(200 * time.Millisecond)

	_, err = connection.ConnectWithIdentity("127.0.0.1", operatingPort, 2, operatingCertsDir, "client.crt", "client.key")
	assert.NoError(t, err, "operating cert must be accepted by the operating-tier listener")

	_, err = connection.ConnectWithIdentity("127.0.0.1", operatingPort, 2, bootstrapCertsDir, "client.crt", "client.key")
	assert.Error(t, err, "bootstrap cert must be rejected by the operating-tier listener")

	_, err = connection.ConnectWithIdentity("127.0.0.1", issuerPort, 2, bootstrapCertsDir, "client.crt", "client.key")
	assert.NoError(t, err, "bootstrap cert must be accepted by the issuer-tier listener")

	_, err = connection.ConnectWithIdentity("127.0.0.1", issuerPort, 2, operatingCertsDir, "client.crt", "client.key")
	assert.Error(t, err, "operating cert must be rejected by the issuer-tier listener")
}
```

Add `"github.com/alex-sviridov/miniprotector/common/connection"` and `"github.com/alex-sviridov/miniprotector/common/mtls"` and `"google.golang.org/grpc"` to the import block (`"github.com/smallstep/certificates/ca"` and `"github.com/smallstep/certificates/api"` are not directly needed beyond what `signBootstrapTierCert` uses — add `"github.com/smallstep/certificates/ca"` too, since this file doesn't import it yet; `mintsign.go`'s own import of `api` isn't needed here since this file never constructs an `api.SignRequest` literal directly — `ca.CreateSignRequest` returns one already built).

- [ ] **Step 2: Run the e2e tests**

Run: `cd src && go test -tags=e2e -timeout=300s ./cmd/issuer/... -run 'TestE2E_MintAndSignOperatingCertHasNoIssuerCallerEKU|TestE2E_BootstrapTierCertHasIssuerCallerEKU|TestE2E_CredentialTierEnforcedAtHandshake' -v`
Expected: PASS for all three (requires a working Docker daemon; skips loudly via `requireDocker` if unavailable rather than failing).

- [ ] **Step 3: Run the full existing e2e suite in this package to confirm no regression**

Run: `cd src && go test -tags=e2e -timeout=300s ./cmd/issuer/... -v`
Expected: PASS for every test in the file, including the pre-existing attribute/SAN/self-mint e2e tests (Task 3/4's `TemplateData`/template changes must not have broken them).

- [ ] **Step 4: Commit**

```bash
git add src/cmd/issuer/e2e_test.go
git commit -m "$(cat <<'EOF'
test(issuer): prove credential tier lands in real certs and is enforced

Three new Docker-backed e2e tests: an operating cert never carries the
EKUIssuerCaller marker, a bootstrap cert always does (and drops
serverAuth), and -- the actual enforcement proof -- a real bootstrap
cert is accepted only by an issuer-tier listener while a real
operating cert is accepted only by an operating-tier listener.
EOF
)"
```

---

### Task 7: Documentation

**Files:**
- Modify: `docs/SECURITY.md`
- Modify: `docs/components/issuer.md`
- Modify: `docs/components/certclient.md`
- Modify: `docs/components/client-manager.md`
- Modify: `docs/protocols/issuer.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update `docs/SECURITY.md`**

Replace the bullet at (currently) lines 102-110:

```
- **The bootstrap credential is not yet cryptographically confined to only reaching `issuer`.**
  In principle it should be impossible for a bootstrap credential to authenticate to anything
  other than `issuer` (it grants no other legitimate access, and a leaked bootstrap credential
  should ideally be useless against `bwfs`/`brfs`/`rwfs`/`catalog`). Today, `common/mtls`'s
  server-side check trusts any certificate signed by the CA, regardless of which credential tier
  issued it — so this boundary is an operational expectation (nothing is deployed that would
  accept a bootstrap credential as if it were an operating one), not an enforced one. Closing this
  would mean a custom certificate extension distinguishing the two tiers plus one shared
  `common/mtls` check; it hasn't been built.
```

With:

```
- **The bootstrap credential is now cryptographically confined to only reaching `issuer`.** A
  bootstrap certificate carries `extKeyUsage: ["clientAuth"]` only (never `serverAuth`) plus a
  custom Extended Key Usage OID, `1.3.6.1.4.1.61183.1.3` (named `EKUIssuerCaller` in code —
  deliberately not named around "server"/"client", already overloaded elsewhere in this codebase),
  identifying it as a bootstrap/issuer-caller credential. `common/mtls.LoadServerCredentials` —
  used by `bwfs` and `catalog` — rejects any peer certificate carrying that marker;
  `mtls.LoadIssuerServerCredentials` — used only by `issuer`'s own listener — rejects any peer
  certificate that *doesn't* carry it. A leaked bootstrap credential can now only ever authenticate
  to `issuer`, exactly as intended. See
  [Design: Credential Tier Enforcement](superpowers/specs/2026-07-05-credential-tier-enforcement-design.md).
```

- [ ] **Step 2: Update `docs/components/issuer.md`**

Insert a new paragraph after the existing "Attribute extension" paragraph (currently ending `...and why nothing in this codebase yet reads or enforces the extension it embeds.` at line 84), before `## Configuration Keys`:

```
**Credential tier enforcement:** `issuer`'s own listener uses `mtls.LoadIssuerServerCredentials`
instead of the default `mtls.LoadServerCredentials` every other server uses — it accepts only
bootstrap/issuer-caller credentials (certificates carrying the custom `EKUIssuerCaller` Extended
Key Usage, OID `1.3.6.1.4.1.61183.1.3`) and rejects operating credentials outright. This is the one
exception to every other server's default behavior, which is the reverse: reject bootstrap/
issuer-caller credentials, accept everything else. See
[Security Model](../SECURITY.md#the-two-tier-credential-model) and
[Design: Credential Tier Enforcement](../superpowers/specs/2026-07-05-credential-tier-enforcement-design.md).
```

Add one line to the `## See Also` list:

```
- [Design: Credential Tier Enforcement](../superpowers/specs/2026-07-05-credential-tier-enforcement-design.md)
```

- [ ] **Step 3: Update `docs/components/certclient.md`**

Replace the `bootstrap` bullet in `## Behavior`:

```
- **`bootstrap`**: redeems a one-time enrollment token for a long-lived bootstrap credential,
  writing `ca.crt` and `bootstrap.crt`/`bootstrap.key`. Gets the token from `--token`, then
  `MP_CERT_TOKEN`, then an interactive stdin prompt, in that order. Trust in the CA is established
  from the token's embedded root fingerprint claim (no separately-distributed root cert needed for
  this step).
```

With:

```
- **`bootstrap`**: redeems a one-time enrollment token for a long-lived bootstrap credential,
  writing `ca.crt` and `bootstrap.crt`/`bootstrap.key`. Gets the token from `--token`, then
  `MP_CERT_TOKEN`, then an interactive stdin prompt, in that order. Trust in the CA is established
  from the token's embedded root fingerprint claim (no separately-distributed root cert needed for
  this step). The redemption's sign request carries `TemplateData {"tier": "bootstrap"}`, which the
  CA's custom leaf template turns into a certificate with `extKeyUsage: ["clientAuth"]` only plus
  the custom `EKUIssuerCaller` marker — see
  [Security Model](../SECURITY.md#the-two-tier-credential-model).
```

Add one line to the `## See Also` list:

```
- [Design: Credential Tier Enforcement](../superpowers/specs/2026-07-05-credential-tier-enforcement-design.md)
```

- [ ] **Step 4: Update `docs/protocols/issuer.md`**

Append a sentence to the end of the `## Authorization` section (after `...it reveals nothing the caller isn't already entitled to know about itself and mints/signs nothing.`):

```
Beneath this RPC-level check, the transport itself now also enforces credential tier: `issuer`'s
listener (`mtls.LoadIssuerServerCredentials`) accepts only bootstrap/issuer-caller certificates,
rejecting an operating certificate before any RPC-level logic runs. See
[Security Model](../SECURITY.md#the-two-tier-credential-model).
```

- [ ] **Step 5: Update `docs/components/client-manager.md`**

Add one line to the `## See Also` list, after the existing `[Design: Client Manager Phase 2]` line:

```
- [Design: Credential Tier Enforcement](../superpowers/specs/2026-07-05-credential-tier-enforcement-design.md)
```

- [ ] **Step 6: Add a `CHANGELOG.md` entry**

Insert immediately after the `All notable changes...` line (before the existing top entry):

```
## 2026-07-05 — Bootstrap credentials can no longer reach bwfs/catalog

`common/mtls` trusted any CA-signed certificate regardless of which of the two credential tiers
issued it — a leaked bootstrap credential (whose only intended use is authenticating to `issuer`)
could authenticate to `bwfs`/`catalog` exactly as well as an operating credential, something
`docs/SECURITY.md` already flagged as a known, unenforced gap. Bootstrap certificates now carry
`extKeyUsage: ["clientAuth"]` only plus a custom Extended Key Usage marker (`EKUIssuerCaller`, OID
`1.3.6.1.4.1.61183.1.3`); `common/mtls.LoadServerCredentials` (used by `bwfs`/`catalog`) rejects any
peer certificate carrying that marker, and a new `mtls.LoadIssuerServerCredentials` (used only by
`issuer`) rejects any peer certificate that doesn't. Certificates issued before this change lack
the marker and won't pass either check — the demo lab (`deploy/control-plane`) needs its CA and
client-manager volumes wiped and the enroll walkthrough re-run after upgrading.
```

- [ ] **Step 7: Commit**

```bash
git add docs/SECURITY.md docs/components/issuer.md docs/components/certclient.md docs/components/client-manager.md docs/protocols/issuer.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
docs: document credential tier enforcement and its clean-slate requirement

Updates SECURITY.md's bootstrap-confinement gap to describe it as
closed, adds the EKUIssuerCaller mechanism to issuer/certclient/
protocol docs, and adds a CHANGELOG entry noting existing demo-lab
deployments need their CA/client-manager volumes wiped and re-enrolled.
EOF
)"
```

---

## Final Verification

- [ ] Run the full unit test suite: `cd src && go test ./...` — expect PASS across every package, zero regressions.
- [ ] Run `go vet`: `cd src && go vet ./...` and `cd src && go vet -tags=e2e ./...` — expect no output.
- [ ] Run the full e2e suite for this package: `cd src && go test -tags=e2e -timeout=300s ./cmd/issuer/... -v` — expect PASS for all tests, old and new.
- [ ] Confirm `bwfs`, `catalog`, and `connection.StartServer`'s call sites truly needed no changes: `git diff main --stat` (or `git diff <base-commit> --stat` for this branch) should show no changes under `src/cmd/bwfs/` or `src/cmd/catalog/`.
