# mTLS for gRPC Transport Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every gRPC connection between `brfs`/`rwfs` (clients) and `bwfs` (server) mutually authenticated with TLS, using certs read from a configurable base directory, with no plaintext fallback.

**Architecture:** A new `src/common/mtls` package owns cert loading and `tls.Config` construction; the two existing shared chokepoints (`connection.StartServer`, `connection.Connect`) call it internally given a certs directory. A reworked `src/common/config` resolves a single `MP_CONFIG_PATH` base directory (replacing `MP_CONFIGFILE`) under which both `local.conf` and `certs/` live as siblings.

**Tech Stack:** Go 1.26, `google.golang.org/grpc` v1.81.1, stdlib `crypto/tls`/`crypto/x509`, `github.com/stretchr/testify` for tests. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-07-01-mtls-integration-design.md`

## Global Constraints

- Every node (bwfs, brfs, rwfs) loads the **same three files** — `ca.crt`, `client.crt`, `client.key` — as its identity, regardless of client/server role. No separate "server cert."
- mTLS is **mandatory**: missing/unreadable/unparseable cert files are hard errors, no plaintext fallback path.
- Server trust policy: **any client cert signed by `ca.crt` is trusted** — no CN/SAN allowlist, no per-identity authorization.
- Client hostname verification: standard SAN/hostname check against the dialed `host`, **except** loopback hosts (`host == "localhost"` or `net.ParseIP(host).IsLoopback()`), where hostname verification is skipped but the cert must still chain to the trusted CA.
- `MP_CONFIG_PATH` env var (directory, default: the running binary's own directory) replaces `MP_CONFIGFILE` entirely. `<base>/local.conf` is the config file; `<base>/certs/` holds the three cert files.
- Cert issuance/reissuance/rotation is out of scope (separate future tool). No CN/SAN allowlisting. No Unix-socket transport (none exists in code today).

---

### Task 1: Fixture certs for tests

**Files:**
- Create: `src/common/testdata/certs/ca.crt`
- Create: `src/common/testdata/certs/client.crt`
- Create: `src/common/testdata/certs/client.key`
- Create: `src/common/testdata/certs-untrusted/ca.crt`
- Create: `src/common/testdata/certs-untrusted/client.crt`
- Create: `src/common/testdata/certs-untrusted/client.key`

**Interfaces:**
- Produces: two independent CA+leaf cert sets on disk, reused by every later task's tests via relative paths `../testdata/certs` (from `src/common/mtls/` and `src/common/connection/`) and by the e2e Dockerfile (`src/common/testdata/certs`, Task 7). `certs/client.crt`'s SAN is `DNS:bwfs.internal` — deliberately not `localhost`, so tests can distinguish the loopback-skip code path from normal SAN matching. `certs-untrusted` is signed by a **different** CA, used only to prove rejection of untrusted client certs.

- [ ] **Step 1: Generate the main fixture CA + leaf cert**

```bash
cd /tmp && rm -rf mtls-fixtures && mkdir mtls-fixtures && cd mtls-fixtures

openssl genrsa -out ca.key 4096
openssl req -x509 -new -key ca.key -sha256 -days 3650 \
  -subj "/CN=miniprotector-test-ca" -out ca.crt

openssl genrsa -out client.key 2048
openssl req -new -key client.key -subj "/CN=miniprotector-test-node" -out client.csr
openssl x509 -req -in client.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
  -out client.crt -days 3650 -sha256 \
  -extfile <(printf "subjectAltName=DNS:bwfs.internal")
```

- [ ] **Step 2: Verify the chain and SAN**

```bash
openssl verify -CAfile ca.crt client.crt
openssl x509 -in client.crt -noout -ext subjectAltName
```

Expected: `client.crt: OK` and `DNS:bwfs.internal`.

- [ ] **Step 3: Generate the second, independent "untrusted" CA + leaf cert**

```bash
openssl genrsa -out ca-untrusted.key 4096
openssl req -x509 -new -key ca-untrusted.key -sha256 -days 3650 \
  -subj "/CN=miniprotector-untrusted-ca" -out ca-untrusted.crt

openssl genrsa -out client-untrusted.key 2048
openssl req -new -key client-untrusted.key -subj "/CN=miniprotector-untrusted-node" -out client-untrusted.csr
openssl x509 -req -in client-untrusted.csr -CA ca-untrusted.crt -CAkey ca-untrusted.key -CAcreateserial \
  -out client-untrusted.crt -days 3650 -sha256 \
  -extfile <(printf "subjectAltName=DNS:bwfs.internal")
```

- [ ] **Step 4: Confirm the untrusted cert does NOT verify against the main CA**

```bash
openssl verify -CAfile ca.crt client-untrusted.crt
```

Expected: fails with something like `error 20 at 0 depth lookup: unable to get local issuer certificate`. If this instead prints `OK`, the two CAs aren't actually independent — stop and regenerate.

- [ ] **Step 5: Copy into the repo and commit**

```bash
mkdir -p /home/alex/miniprotector/src/common/testdata/certs
cp ca.crt client.crt client.key /home/alex/miniprotector/src/common/testdata/certs/

mkdir -p /home/alex/miniprotector/src/common/testdata/certs-untrusted
cp ca-untrusted.crt /home/alex/miniprotector/src/common/testdata/certs-untrusted/ca.crt
cp client-untrusted.crt /home/alex/miniprotector/src/common/testdata/certs-untrusted/client.crt
cp client-untrusted.key /home/alex/miniprotector/src/common/testdata/certs-untrusted/client.key

cd /home/alex/miniprotector
git add src/common/testdata/certs src/common/testdata/certs-untrusted
git commit -m "test: add fixture CA + client certs for mTLS tests"
```

---

### Task 2: `MP_CONFIG_PATH` base-dir resolution in `config` package

**Files:**
- Modify: `src/common/config/config.go`
- Create: `src/common/config/config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `config.ConfigPathEnvVar = "MP_CONFIG_PATH"`, `config.ResolveBaseDir() (string, error)`, `config.ResolveConfigPath() (string, error)` (now `<base>/local.conf`), `config.ResolveCertsDir() (string, error)` (now `<base>/certs`). `config.ConfigFileEnvVar`/`MP_CONFIGFILE` and the old two-candidate search are removed. `config.ParseConfig` and `config.Config` are unchanged.

- [ ] **Step 1: Write the failing tests**

Create `src/common/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBaseDir_EnvVarSet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigPathEnvVar, dir)

	got, err := ResolveBaseDir()
	require.NoError(t, err)
	assert.Equal(t, dir, got)
}

func TestResolveBaseDir_DefaultsToExecutableDir(t *testing.T) {
	// Setting to "" (rather than leaving whatever the test process inherited)
	// makes this deterministic: os.Getenv returns "" either way, which is what
	// ResolveBaseDir treats as "unset".
	t.Setenv(ConfigPathEnvVar, "")

	got, err := ResolveBaseDir()
	require.NoError(t, err)

	exePath, err := os.Executable()
	require.NoError(t, err)
	assert.Equal(t, filepath.Dir(exePath), got)
}

func TestResolveConfigPath_JoinsBaseDirWithLocalConf(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigPathEnvVar, dir)

	got, err := ResolveConfigPath()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "local.conf"), got)
}

func TestResolveCertsDir_JoinsBaseDirWithCerts(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigPathEnvVar, dir)

	got, err := ResolveCertsDir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "certs"), got)
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./common/config/... -v`
Expected: FAIL — `undefined: ConfigPathEnvVar` (or similar) since none of the new symbols exist yet.

- [ ] **Step 3: Replace the env var and resolution logic**

In `src/common/config/config.go`, replace lines 13-51 (the `ConfigFileEnvVar` const and `ResolveConfigPath` function) with:

```go
// ConfigPathEnvVar is the environment variable used to override the base
// configuration directory. If unset, ResolveBaseDir defaults to the running
// binary's own directory. Both the config file (<base>/local.conf) and the
// mTLS certs directory (<base>/certs) are resolved relative to this base.
const ConfigPathEnvVar = "MP_CONFIG_PATH"

// ResolveBaseDir returns MP_CONFIG_PATH if set, otherwise the directory
// containing the running binary.
func ResolveBaseDir() (string, error) {
	if envPath := os.Getenv(ConfigPathEnvVar); envPath != "" {
		return envPath, nil
	}

	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to determine executable path: %w", err)
	}
	return filepath.Dir(exePath), nil
}

// ResolveConfigPath determines the configuration file path: <base>/local.conf,
// where base comes from ResolveBaseDir.
func ResolveConfigPath() (string, error) {
	baseDir, err := ResolveBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "local.conf"), nil
}

// ResolveCertsDir determines the mTLS certs directory: <base>/certs, where
// base comes from ResolveBaseDir. The directory is expected to contain
// ca.crt, client.crt, and client.key (see common/mtls).
func ResolveCertsDir() (string, error) {
	baseDir, err := ResolveBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, "certs"), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS — all four new tests green.

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): replace MP_CONFIGFILE with MP_CONFIG_PATH base directory"
```

---

### Task 3: `mtls` package — cert loading and TLS config

**Files:**
- Create: `src/common/mtls/mtls.go`
- Create: `src/common/mtls/mtls_test.go`

**Interfaces:**
- Consumes: fixture certs at `../testdata/certs` and `../testdata/certs-untrusted` (Task 1).
- Produces: `mtls.LoadServerCredentials(certsDir string) (credentials.TransportCredentials, error)`, `mtls.LoadClientCredentials(certsDir, host string) (credentials.TransportCredentials, error)` — these are what Task 4 calls.

- [ ] **Step 1: Write the failing tests**

Create `src/common/mtls/mtls_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./common/mtls/... -v`
Expected: FAIL — `undefined: LoadServerCredentials` (package doesn't exist yet).

- [ ] **Step 3: Implement `src/common/mtls/mtls.go`**

```go
// Package mtls loads mutual-TLS credentials for miniprotector's gRPC
// transport. Every node (bwfs, brfs, rwfs) presents the same identity cert
// regardless of its client/server role: ca.crt, client.crt, client.key in a
// single directory.
package mtls

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc/credentials"
)

const (
	caCertFile    = "ca.crt"
	identCertFile = "client.crt"
	identKeyFile  = "client.key"
)

func loadCertAndPool(certsDir string) (tls.Certificate, *x509.CertPool, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, identCertFile),
		filepath.Join(certsDir, identKeyFile),
	)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load identity cert/key from %s: %w", certsDir, err)
	}

	caPEM, err := os.ReadFile(filepath.Join(certsDir, caCertFile))
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read CA cert from %s: %w", certsDir, err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return tls.Certificate{}, nil, fmt.Errorf("parse CA cert from %s: no valid certificates found", certsDir)
	}

	return cert, caPool, nil
}

func serverTLSConfig(certsDir string) (*tls.Config, error) {
	cert, caPool, err := loadCertAndPool(certsDir)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
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

func clientTLSConfig(certsDir, host string) (*tls.Config, error) {
	cert, caPool, err := loadCertAndPool(certsDir)
	if err != nil {
		return nil, err
	}

	if isLoopbackHost(host) {
		return &tls.Config{
			Certificates:          []tls.Certificate{cert},
			InsecureSkipVerify:    true, // hostname check disabled; chain is still verified below
			VerifyPeerCertificate: verifyChainOnly(caPool),
		}, nil
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      caPool,
		ServerName:   host,
	}, nil
}

// LoadServerCredentials builds gRPC transport credentials for a server that
// requires and verifies every client's certificate against certsDir/ca.crt.
// Any client cert signed by that CA is trusted; there is no CN/SAN allowlist.
func LoadServerCredentials(certsDir string) (credentials.TransportCredentials, error) {
	cfg, err := serverTLSConfig(certsDir)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}

// LoadClientCredentials builds gRPC transport credentials for dialing host.
// Hostname/SAN verification is skipped for loopback hosts (localhost,
// 127.0.0.0/8, ::1); every other host must match a SAN on the server's
// presented certificate.
func LoadClientCredentials(certsDir, host string) (credentials.TransportCredentials, error) {
	cfg, err := clientTLSConfig(certsDir, host)
	if err != nil {
		return nil, err
	}
	return credentials.NewTLS(cfg), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/mtls/... -v`
Expected: PASS — all 8 tests green.

- [ ] **Step 5: Commit**

```bash
git add src/common/mtls
git commit -m "feat(mtls): add cert loading and TLS config for gRPC transport"
```

---

### Task 4: Wire `mtls` into `connection.StartServer` / `Connect`

**Files:**
- Modify: `src/common/connection/server.go`
- Modify: `src/common/connection/client.go`
- Create: `src/common/connection/mtls_wiring_test.go`

**Interfaces:**
- Consumes: `mtls.LoadServerCredentials`, `mtls.LoadClientCredentials` (Task 3); fixture certs at `../testdata/certs` and `../testdata/certs-untrusted` (Task 1).
- Produces: `StartServer(ctx context.Context, logger *slog.Logger, port int, certsDir string, register func(*grpc.Server)) error` and `Connect(host string, port, timeout int, certsDir string) (*grpc.ClientConn, error)` — both gain a `certsDir` parameter. Task 5's call sites depend on these exact signatures.

- [ ] **Step 1: Write the failing tests**

Create `src/common/connection/mtls_wiring_test.go`:

```go
package connection

import (
	"context"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

const (
	fixtureCertsDir   = "../testdata/certs"
	untrustedCertsDir = "../testdata/certs-untrusted"
)

func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestStartServerConnect_RoundTripSucceeds(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(ctx, testLogger(), port, fixtureCertsDir, func(s *grpc.Server) {})
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
		t.Fatal("StartServer did not shut down in time")
	}
}

func TestStartServerConnect_UntrustedClientCertRejected(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(ctx, testLogger(), port, fixtureCertsDir, func(s *grpc.Server) {})
	}()
	time.Sleep(100 * time.Millisecond)

	_, err := Connect("127.0.0.1", port, 2, untrustedCertsDir)
	assert.Error(t, err)

	cancel()
	<-errCh
}

func TestStartServer_MissingCertsDirFailsFast(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := StartServer(ctx, testLogger(), port, "does-not-exist", func(s *grpc.Server) {})
	assert.Error(t, err)
}

func TestConnect_MissingCertsDirFailsFast(t *testing.T) {
	_, err := Connect("127.0.0.1", 1, 1, "does-not-exist")
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./common/connection/... -v`
Expected: FAIL — `too many arguments in call to StartServer` / `Connect` (signatures don't have `certsDir` yet).

- [ ] **Step 3: Update `src/common/connection/server.go`**

Replace the full file with:

```go
package connection

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
)

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

- [ ] **Step 4: Update `src/common/connection/client.go`**

Replace the `Connect` function (lines 17-42) with:

```go
func Connect(host string, port, timeout int, certsDir string) (*grpc.ClientConn, error) {
	creds, err := mtls.LoadClientCredentials(certsDir, host)
	if err != nil {
		return nil, fmt.Errorf("failed to load client credentials: %w", err)
	}

	// Configure keepalive for connection health monitoring
	keepaliveParams := keepalive.ClientParameters{
		Time:                10 * time.Second, // Send ping every 10 seconds
		Timeout:             3 * time.Second,  // Wait 3 seconds for pong response
		PermitWithoutStream: true,             // Send pings even when no active streams
	}

	// Connect to server with keepalive
	conn, err := grpc.NewClient(
		fmt.Sprintf("%s:%d", host, port),
		grpc.WithTransportCredentials(creds),
		grpc.WithKeepaliveParams(keepaliveParams),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}

	if err := checkConnection(conn, timeout); err != nil {
		conn.Close() // Close only on connection failure
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	// Connection remains open; caller wraps it with the generated client it needs.
	return conn, nil
}
```

And replace the import block at the top of `client.go` (lines 1-15) with:

```go
package connection

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/keepalive"
)
```

(this drops `"google.golang.org/grpc/credentials/insecure"`, no longer used, and adds the new `mtls` import)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd src && go test ./common/connection/... -v`
Expected: PASS — all 4 tests green.

- [ ] **Step 6: Commit**

```bash
git add src/common/connection
git commit -m "feat(connection): require mutual TLS in StartServer and Connect"
```

---

### Task 5: Wire `certsDir` through `bwfs`/`brfs`/`rwfs` call sites

**Files:**
- Modify: `src/cmd/bwfs/main.go:88`
- Modify: `src/cmd/brfs/main.go:87`
- Modify: `src/cmd/rwfs/main.go:47,52`
- Modify: `src/cmd/rwfs/list.go`
- Modify: `src/cmd/rwfs/verify.go`

**Interfaces:**
- Consumes: `config.ResolveCertsDir() (string, error)` (Task 2), `connection.StartServer(..., certsDir string, ...)`, `connection.Connect(..., certsDir string)` (Task 4).
- Produces: nothing consumed by later tasks — this is the last code-wiring task; from here the binaries are fully wired for mTLS.

- [ ] **Step 1: `src/cmd/bwfs/main.go`** — resolve certs dir right before `connection.StartServer`, and pass it through:

```go
		certsDir, err := config.ResolveCertsDir()
		if err != nil {
			logger.Error("Certs directory resolution failed", "error", err)
			os.Exit(1)
		}

		if err := connection.StartServer(ctx, logger, arguments.Port, certsDir, func(s *grpc.Server) {
			pb.RegisterBackupServiceServer(s, backupServer)
			pb.RegisterListServiceServer(s, listSrv)
			pb.RegisterRestoreServiceServer(s, restoreSrv)
		}); err != nil {
			logger.Error("Server failed", "error", err)
			os.Exit(1)
		}
```

This block replaces the existing `if err := connection.StartServer(ctx, logger, arguments.Port, func(s *grpc.Server) {` call (current lines 88-95); insert the new `certsDir, err := ...` block immediately before it.

- [ ] **Step 2: `src/cmd/brfs/main.go`** — resolve certs dir right before `connection.Connect`:

Replace:
```go
	// Create gRPC connection
	conn, err := connection.Connect(arguments.WriterHost, arguments.WriterPort, 5)
	if err != nil {
		logger.Error("Error connecting to server", "error", err)
		return
	}
```
with:
```go
	// Create gRPC connection
	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("Certs directory resolution failed", "error", err)
		return
	}
	conn, err := connection.Connect(arguments.WriterHost, arguments.WriterPort, 5, certsDir)
	if err != nil {
		logger.Error("Error connecting to server", "error", err)
		return
	}
```

- [ ] **Step 3: `src/cmd/rwfs/main.go`** — resolve certs dir once, pass to both actions:

Replace:
```go
	switch arguments.Action {
	case "list":
		if err := runList(arguments.BwfsHost, arguments.BwfsPort, arguments.ServerName, arguments.PathFilter, arguments.Filter, arguments.Output); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.ServerName, arguments.PathFilter, arguments.Filter, arguments.Streams, arguments.Retries, arguments.Quiet); err != nil {
			logger.Error("Verify failed", "error", err)
			os.Exit(1)
		}
	}
```
with:
```go
	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		logger.Error("Certs directory resolution failed", "error", err)
		os.Exit(1)
	}

	switch arguments.Action {
	case "list":
		if err := runList(arguments.BwfsHost, arguments.BwfsPort, arguments.ServerName, arguments.PathFilter, arguments.Filter, arguments.Output, certsDir); err != nil {
			logger.Error("List failed", "error", err)
			os.Exit(1)
		}
	case "verify":
		if err := runVerify(logger, arguments.BwfsHost, arguments.BwfsPort, arguments.ServerName, arguments.PathFilter, arguments.Filter, arguments.Streams, arguments.Retries, arguments.Quiet, certsDir); err != nil {
			logger.Error("Verify failed", "error", err)
			os.Exit(1)
		}
	}
```

- [ ] **Step 4: `src/cmd/rwfs/list.go`** — add `certsDir` parameter:

```go
func runList(host string, port int, serverName, pathFilter, filter, output, certsDir string) error {
	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()
```
(rest of the function body unchanged)

- [ ] **Step 5: `src/cmd/rwfs/verify.go`** — add `certsDir` parameter:

```go
func runVerify(logger *slog.Logger, host string, port int, serverName, pathFilter, filter string, streams, retries int, quiet bool, certsDir string) error {
	conn, err := connection.Connect(host, port, 5, certsDir)
	if err != nil {
		return fmt.Errorf("connect to bwfs: %w", err)
	}
	defer conn.Close()
```
(rest of the function body unchanged)

- [ ] **Step 6: Build all three binaries to confirm the wiring compiles**

Run: `cd src && go build ./...`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/bwfs/main.go src/cmd/brfs/main.go src/cmd/rwfs/main.go src/cmd/rwfs/list.go src/cmd/rwfs/verify.go
git commit -m "feat(cmd): resolve certs dir and pass through to StartServer/Connect"
```

---

### Task 6: Repo layout migration — `MP_CONFIG_PATH` default directory

**Files:**
- Modify: `.gitignore`
- Move: `.config/local.conf` → `bin/local.conf`

**Interfaces:**
- Consumes: nothing.
- Produces: a tracked `bin/local.conf` matching what `config.ResolveConfigPath()` (Task 2) now looks for by default (binary's own directory, since dev binaries build to `bin/`).

- [ ] **Step 1: Un-ignore `bin/local.conf` specifically**

In `/home/alex/miniprotector/.gitignore`, change:
```
# Binaries
*.exe
*.exe~
*.dll
*.so
*.dylib
bin/
```
to:
```
# Binaries
*.exe
*.exe~
*.dll
*.so
*.dylib
bin/
!bin/local.conf
```

- [ ] **Step 2: Move the tracked config file**

```bash
mkdir -p /home/alex/miniprotector/bin
git -C /home/alex/miniprotector mv .config/local.conf bin/local.conf
```

- [ ] **Step 3: Verify git sees the move and the new ignore exception**

```bash
git -C /home/alex/miniprotector status
```
Expected: `renamed: .config/local.conf -> bin/local.conf` (or shown as delete+add — either is fine) and `.gitignore` shown as modified. No untracked `bin/local.conf`.

- [ ] **Step 4: Sanity-check `bin/local.conf` still parses**

```bash
cd /home/alex/miniprotector && MP_CONFIG_PATH=bin go run ./src/cmd/rwfs 2>&1 | head -5
```
Expected: doesn't fail with a "config file not found" or "unknown configuration key" error (it will fail later for lack of arguments — that's fine, this step only checks config resolution/parsing succeeds).

- [ ] **Step 5: Commit**

```bash
rmdir /home/alex/miniprotector/.config 2>/dev/null; true
git add .gitignore bin/local.conf
git commit -m "chore: move local.conf under bin/, matching MP_CONFIG_PATH default"
```

(`git mv` in Step 2 already staged the rename; the `rmdir` just clears the now-empty `.config/` directory from the working tree if the filesystem left it behind — it's a no-op if `git mv` already removed it.)

---

### Task 7: e2e Docker updates

**Files:**
- Modify: `src/e2e/Dockerfile`
- Modify: `src/e2e/docker.go:258` (`waitForBwfs`)

**Interfaces:**
- Consumes: `src/common/testdata/certs` (Task 1) baked into the container image at `/app/certs`.
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Update `src/e2e/Dockerfile`**

Change:
```dockerfile
WORKDIR /app
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs ./
COPY src/e2e/config.conf .config/local.conf
```
to:
```dockerfile
WORKDIR /app
COPY --from=builder /build/bin/brfs /build/bin/bwfs /build/bin/rwfs ./
COPY src/e2e/config.conf local.conf
COPY src/common/testdata/certs certs
```

(`MP_CONFIG_PATH` needs no explicit env var in the container — it defaults to `/app`, which is exactly where `local.conf` and `certs/` now live.)

- [ ] **Step 2: Update `waitForBwfs` in `src/e2e/docker.go`** — it only needs to confirm the port accepts TCP connections, not complete a TLS handshake, so switch from a plaintext gRPC dial to a plain TCP dial:

Replace:
```go
// waitForBwfs polls gRPC dial until bwfs is ready or timeout expires.
func waitForBwfs(ctx context.Context, hostPort string) error {
	deadline := time.Now().Add(15 * time.Second)
	addr := "127.0.0.1:" + hostPort
	for time.Now().Before(deadline) {
		conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("bwfs at %s did not become ready within 15s", addr)
}
```
with:
```go
// waitForBwfs polls a plain TCP connection until bwfs's port accepts
// connections or the timeout expires. It doesn't need a TLS handshake —
// it's just confirming the listener is up before the harness issues real
// commands against it.
func waitForBwfs(ctx context.Context, hostPort string) error {
	deadline := time.Now().Add(15 * time.Second)
	addr := "127.0.0.1:" + hostPort
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return fmt.Errorf("bwfs at %s did not become ready within 15s", addr)
}
```

Then remove the now-unused imports `"google.golang.org/grpc"` and `"google.golang.org/grpc/credentials/insecure"` from `src/e2e/docker.go`'s import block (`net` is already imported).

- [ ] **Step 3: Confirm the e2e package still compiles**

Run: `cd src && go vet -tags=e2e ./e2e/...`
Expected: no errors (this doesn't require Docker — it's just a compile/vet check).

- [ ] **Step 4: Run the e2e suite if Docker is available**

Run: `make test-e2e`
Expected: PASS. If Docker isn't available in this environment, note that and skip — this is the one step in the plan that requires a Docker daemon.

- [ ] **Step 5: Commit**

```bash
git add src/e2e/Dockerfile src/e2e/docker.go
git commit -m "test(e2e): bake mTLS fixture certs into container image, drop gRPC from readiness poll"
```

---

### Task 8: Documentation

**Files:**
- Modify: `docs/components/bwfs.md`
- Modify: `docs/components/brfs.md`
- Modify: `docs/components/rwfs.md`
- Modify: `docs/ARCHITECTURE.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: Add a "Transport Security" section to `docs/components/bwfs.md`**, inserted immediately before the existing `## Building` section:

```markdown
## Transport Security

All gRPC connections (`BackupService`, `ListService`, `RestoreService`) require mutual TLS.
`bwfs` loads its identity cert and the trusted CA from `MP_CONFIG_PATH/certs/{ca.crt,client.crt,client.key}`
(`MP_CONFIG_PATH` defaults to the binary's own directory). Any client presenting a cert signed
by that CA is trusted — there's no additional per-client allowlist. Missing or invalid certs
are a fatal startup error; there is no plaintext fallback. Cert issuance itself is out of scope
for `bwfs` — see the `ca/` step-ca setup for how certs are provisioned today.
```

- [ ] **Step 2: Add the same section to `docs/components/brfs.md`**, inserted immediately before its existing `## Building` section:

```markdown
## Transport Security

The connection to `bwfs` is mutually authenticated TLS. `brfs` loads its identity cert and the
trusted CA from `MP_CONFIG_PATH/certs/{ca.crt,client.crt,client.key}` (`MP_CONFIG_PATH` defaults
to the binary's own directory). Missing or invalid certs are a fatal error before any backup
traffic is sent. When `--destination` is a loopback address (`localhost`, `127.0.0.1`, `::1`),
hostname verification against the server cert's SAN is skipped — the cert must still chain to
the trusted CA.
```

- [ ] **Step 3: Add the same section to `docs/components/rwfs.md`**, inserted immediately before its existing `## Building` section:

```markdown
## Transport Security

Connections to `bwfs` (both `list` and `verify`) are mutually authenticated TLS. `rwfs` loads
its identity cert and the trusted CA from `MP_CONFIG_PATH/certs/{ca.crt,client.crt,client.key}`
(`MP_CONFIG_PATH` defaults to the binary's own directory). Missing or invalid certs are a fatal
error before any query is sent. When the `bwfs_host:port` target's host is loopback (`localhost`,
`127.0.0.1`, `::1`), hostname verification against the server cert's SAN is skipped — the cert
must still chain to the trusted CA.
```

- [ ] **Step 4: Update `docs/ARCHITECTURE.md`**

In the "Backup Process" section, change:
```markdown
- Connects to **bwfs** via network or Unix socket
```
to:
```markdown
- Connects to **bwfs** via network or Unix socket, authenticated with mutual TLS
```

In the "Restore/Verify Process" section, change:
```markdown
- **rwfs** connects to **bwfs** via network or Unix socket using the list/restore protocol
```
to:
```markdown
- **rwfs** connects to **bwfs** via network or Unix socket using the list/restore protocol, authenticated with mutual TLS
```

In the mermaid diagram, change:
```
    brfs -->|backup protocol<br/>network/unix socket| bwfs
```
to:
```
    brfs -->|backup protocol<br/>network/unix socket, mTLS| bwfs
```
and change:
```
    bwfs -->|list/restore protocol<br/>network/unix socket| rwfs
```
to:
```
    bwfs -->|list/restore protocol<br/>network/unix socket, mTLS| rwfs
```

- [ ] **Step 5: Commit**

```bash
git add docs/components/bwfs.md docs/components/brfs.md docs/components/rwfs.md docs/ARCHITECTURE.md
git commit -m "docs: document mandatory mTLS transport across bwfs/brfs/rwfs"
```

---

### Task 9: Full-repo verification

**Files:** none modified — verification only.

**Interfaces:** none.

- [ ] **Step 1: Build everything**

Run: `cd src && go build ./...`
Expected: no errors.

- [ ] **Step 2: Vet everything**

Run: `cd src && go vet ./...`
Expected: no errors.

- [ ] **Step 3: Run the full unit/integration test suite**

Run: `cd src && go test ./...`
Expected: PASS across all packages.

- [ ] **Step 4: Run the integration-tagged tests separately**

Run: `cd src && go test -tags integration ./...`
Expected: PASS. These tests (`src/cmd/bwfs/{integration_test,restore_test}.go`) use `bufconn` + `grpc.NewServer()`/`grpc.NewClient()` directly, bypassing `StartServer`/`Connect` entirely — they should be completely unaffected by this change, and this step confirms that.

- [ ] **Step 5: Manual smoke test using the already-provisioned dev certs**

The repo already has real step-ca-issued certs at `bin/certs/{ca.crt,client.crt,client.key}` (provisioned outside this plan's scope). Use them to confirm the actual binaries work end-to-end:

```bash
cd /home/alex/miniprotector
make build
MP_CONFIG_PATH=bin ./bin/bwfs /tmp/mtls-smoke-storage server --port 18080 &
sleep 1
MP_CONFIG_PATH=bin ./bin/rwfs list localhost:18080
kill %1
```

Expected: `rwfs list` connects and prints an (empty) table without any TLS/connection error — proving the loopback-skip path and real cert loading work against genuinely-issued certs, not just the test fixtures. If this fails with a handshake or cert error, do not consider the plan complete — that's a real signal something in Tasks 3-5 doesn't match how these certs were actually issued (e.g. their SAN, or key usage extensions).

- [ ] **Step 6: Run e2e suite if Docker is available**

Run: `make test-e2e`
Expected: PASS (already covered in Task 7 Step 4 — re-run here only if it was skipped there due to Docker being unavailable at the time).
