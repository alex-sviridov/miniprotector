# Client Manager Phase 2c: Agent/Issuer Wiring Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `agent` to actually obtain and refresh a node's operating certificate through `issuer`, replacing agent v1's single phase-1 `certclient`-exec policy with a two-tier bootstrap/operating credential model, making phase 2's revocation/live-attribute/live-SAN goals real end-to-end instead of just architected.

**Architecture:** `certclient` narrows to managing the long-lived bootstrap credential (renamed `bootstrap.crt`/`bootstrap.key`) and gains a new `operating-refresh` subcommand that dials `issuer`, learns the node's current SAN aliases via a new `DescribeSANs` RPC, builds a matching CSR, and writes the resulting operating certificate to the unchanged `client.crt`/`client.key` path every other component already expects. `agent` runs both credentials' refresh as two independently-scheduled embedded policies using its existing generic reconcile machinery.

**Tech Stack:** Go, gRPC + protobuf (one new RPC on the existing `issuer.proto`), `smallstep/certificates/{ca,api}` (pinned v0.30.2, already used throughout), `common/mtls`/`common/connection` (additive changes only), cobra (already used for every CLI in this repo).

## Global Constraints

- Every existing caller of `common/mtls.LoadClientCredentials` and `common/connection.Connect` (`bwfs`, `brfs`, `rwfs`, `catalogsync`, `catalog`) must keep its exact current signature and behavior — all new capability is additive.
- The caller's hostname is always derived from `mtls.PeerHostname(ctx)` server-side (never a request field) for every RPC, including the new `DescribeSANs` — same trust model as `RequestOperatingCert` and every other authenticated RPC in this project.
- No atomic temp-file/rename writes for any cert/key file — `bootstrap.go`/`renew.go` already write plain `os.WriteFile`; new code matches that existing convention rather than introducing a new one.
- A CSR's requested DNS SANs must be an **exact set match** against `issuer`'s authorized SAN list for the JWK/OTT provisioner path, confirmed against the pinned `smallstep/certificates@v0.30.2` source (`authority/provisioner/sign_options.go`'s `dnsNamesValidator`, enforced in `authority/tls.go`) — not a subset, and an empty-`DNSNames` CSR silently yields a SAN-less certificate rather than an error. This is why `DescribeSANs` must be called, and its result used verbatim, before building any operating-cert CSR.
- `certclient` gains a persistent `--debug` flag wired through `common/logging.NewLogger(ctx)`, matching `agent`/`issuer`'s existing convention.
- New `local.conf` keys (`BootstrapCertRefreshIntervalSec` default `86400`, `BootstrapCertTTLSec` default `7776000`, `OperatingCertFetchIntervalSec` default `900`) follow the existing `_host`/`_port`/`*Sec` naming convention in `common/config`.

---

### Task 1: `common/mtls` — additive explicit-filename client credentials

**Files:**
- Modify: `src/common/mtls/mtls.go`
- Modify: `src/common/mtls/mtls_test.go`

**Interfaces:**
- Produces: `LoadClientCredentialsWithIdentity(certsDir, certFile, keyFile, host string) (credentials.TransportCredentials, error)`. `LoadClientCredentials(certsDir, host string) (credentials.TransportCredentials, error)` keeps its exact existing signature and behavior.

- [ ] **Step 1: Write the failing test**

Append to `src/common/mtls/mtls_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/mtls/... -run 'TestLoadClientCredentialsWithIdentity|TestLoadClientCredentials_StillUsesDefaultFilenames' -v`
Expected: FAIL — `LoadClientCredentialsWithIdentity` undefined (compile error).

- [ ] **Step 3: Implement**

In `src/common/mtls/mtls.go`, replace `loadIdentityCert` and `clientTLSConfig` with parameterized versions, keeping the old names as thin wrappers:

```go
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
```

Replace `clientTLSConfig` with:

```go
func clientTLSConfigWithIdentity(certsDir, certFile, keyFile, host string) (*tls.Config, error) {
	if _, err := loadIdentityCertFiles(certsDir, certFile, keyFile); err != nil {
		return nil, err
	}
	caPool, err := loadCAPool(certsDir)
	if err != nil {
		return nil, err
	}

	getClientCert := func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		cert, err := loadIdentityCertFiles(certsDir, certFile, keyFile)
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
```

Replace `LoadClientCredentials` with:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/mtls/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/common/mtls/
git commit -m "feat(mtls): add explicit-filename client credentials for non-default identities"
```

---

### Task 2: `common/connection` — additive `ConnectWithIdentity`

**Files:**
- Modify: `src/common/connection/client.go`
- Modify: `src/common/connection/mtls_wiring_test.go`

**Interfaces:**
- Consumes: `mtls.LoadClientCredentialsWithIdentity` (Task 1).
- Produces: `ConnectWithIdentity(host string, port, timeout int, certsDir, certFile, keyFile string) (*grpc.ClientConn, error)`. `Connect(host string, port, timeout int, certsDir string) (*grpc.ClientConn, error)` keeps its exact existing signature and behavior.

- [ ] **Step 1: Write the failing test**

Append to `src/common/connection/mtls_wiring_test.go`:

```go
func TestConnectWithIdentity_RoundTripSucceeds(t *testing.T) {
	port := freeTCPPort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartServer(ctx, testLogger(), port, fixtureCertsDir, func(s *grpc.Server) {})
	}()
	time.Sleep(100 * time.Millisecond)

	conn, err := ConnectWithIdentity("127.0.0.1", port, 5, fixtureCertsDir, "client.crt", "client.key")
	require.NoError(t, err)
	conn.Close()

	cancel()
	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("StartServer did not shut down in time")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd src && go test ./common/connection/... -run TestConnectWithIdentity_RoundTripSucceeds -v`
Expected: FAIL — `ConnectWithIdentity` undefined (compile error).

- [ ] **Step 3: Implement**

Replace the body of `src/common/connection/client.go` (keep the `package connection` line and existing imports, adding `"google.golang.org/grpc/credentials"`):

```go
package connection

import (
	"context"
	"fmt"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/mtls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

func dialWithCredentials(creds credentials.TransportCredentials, host string, port, timeout int) (*grpc.ClientConn, error) {
	keepaliveParams := keepalive.ClientParameters{
		Time:                10 * time.Second, // Send ping every 10 seconds
		Timeout:             3 * time.Second,  // Wait 3 seconds for pong response
		PermitWithoutStream: true,             // Send pings even when no active streams
	}

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

// Connect dials host:port, presenting certsDir/client.crt and
// certsDir/client.key as this node's mTLS identity.
func Connect(host string, port, timeout int, certsDir string) (*grpc.ClientConn, error) {
	creds, err := mtls.LoadClientCredentials(certsDir, host)
	if err != nil {
		return nil, fmt.Errorf("failed to load client credentials: %w", err)
	}
	return dialWithCredentials(creds, host, port, timeout)
}

// ConnectWithIdentity is Connect, parameterized on which cert/key filenames
// to present -- used by callers authenticating with an identity other than
// the standard client.crt/client.key pair.
func ConnectWithIdentity(host string, port, timeout int, certsDir, certFile, keyFile string) (*grpc.ClientConn, error) {
	creds, err := mtls.LoadClientCredentialsWithIdentity(certsDir, certFile, keyFile, host)
	if err != nil {
		return nil, fmt.Errorf("failed to load client credentials: %w", err)
	}
	return dialWithCredentials(creds, host, port, timeout)
}

func checkConnection(conn *grpc.ClientConn, timeoutSec int) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	conn.Connect()

	for {
		state := conn.GetState()

		switch state {
		case connectivity.Ready:
			return nil
		case connectivity.TransientFailure, connectivity.Shutdown:
			// fall through to wait for a state change, same as today
		}

		if !conn.WaitForStateChange(ctx, state) {
			return fmt.Errorf("timed out waiting for connection to be ready (last state: %s)", state)
		}
	}
}
```

Note: `checkConnection`'s body above must match the existing implementation exactly (read the current function before pasting — this plan reproduces its known shape from `common/connection/client.go`; if the live switch/loop body differs in some way not shown here, keep the existing logic unchanged and only add the two new functions above it).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/connection/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/common/connection/
git commit -m "feat(connection): add ConnectWithIdentity for non-default identity filenames"
```

---

### Task 3: `common/config` — new keys

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.BootstrapCertRefreshIntervalSec int` (default `86400`), `Config.BootstrapCertTTLSec int` (default `7776000`), `Config.OperatingCertFetchIntervalSec int` (default `900`), parsed from `BootstrapCertRefreshIntervalSec`, `BootstrapCertTTLSec`, `OperatingCertFetchIntervalSec` keys.

- [ ] **Step 1: Write the failing tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_BootstrapCertRefreshIntervalSecDefaultsTo86400(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 86400, conf.BootstrapCertRefreshIntervalSec)
}

func TestParseConfig_BootstrapCertRefreshIntervalSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nBootstrapCertRefreshIntervalSec=43200\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 43200, conf.BootstrapCertRefreshIntervalSec)
}

func TestParseConfig_BootstrapCertTTLSecDefaultsTo7776000(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 7776000, conf.BootstrapCertTTLSec)
}

func TestParseConfig_BootstrapCertTTLSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nBootstrapCertTTLSec=2592000\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 2592000, conf.BootstrapCertTTLSec)
}

func TestParseConfig_OperatingCertFetchIntervalSecDefaultsTo900(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 900, conf.OperatingCertFetchIntervalSec)
}

func TestParseConfig_OperatingCertFetchIntervalSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nOperatingCertFetchIntervalSec=300\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 300, conf.OperatingCertFetchIntervalSec)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run 'TestParseConfig_BootstrapCert|TestParseConfig_OperatingCertFetchIntervalSec' -v`
Expected: FAIL — fields undefined (compile error).

- [ ] **Step 3: Implement**

In `src/common/config/config.go`, add three fields to the `Config` struct (after `OperatingCertTTLSec`):

```go
	BootstrapCertRefreshIntervalSec int
	BootstrapCertTTLSec             int
	OperatingCertFetchIntervalSec   int
```

Add three defaults to the literal in `ParseConfig` (after `OperatingCertTTLSec: 3600,`):

```go
		BootstrapCertRefreshIntervalSec: 86400,
		BootstrapCertTTLSec:             7776000,
		OperatingCertFetchIntervalSec:   900,
```

Add three `case`s to the `switch key` block (after the `OperatingCertTTLSec` case):

```go
		case "BootstrapCertRefreshIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid BootstrapCertRefreshIntervalSec value at line %d: %s", lineNum, value)
			}
			config.BootstrapCertRefreshIntervalSec = number
			foundFields["BootstrapCertRefreshIntervalSec"] = true
		case "BootstrapCertTTLSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid BootstrapCertTTLSec value at line %d: %s", lineNum, value)
			}
			config.BootstrapCertTTLSec = number
			foundFields["BootstrapCertTTLSec"] = true
		case "OperatingCertFetchIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid OperatingCertFetchIntervalSec value at line %d: %s", lineNum, value)
			}
			config.OperatingCertFetchIntervalSec = number
			foundFields["OperatingCertFetchIntervalSec"] = true
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/common/config/
git commit -m "feat(config): add BootstrapCertRefreshIntervalSec, BootstrapCertTTLSec, OperatingCertFetchIntervalSec"
```

---

### Task 4: `issuer.proto` — add `DescribeSANs`

**Files:**
- Modify: `src/api/issuer.proto`
- Generated (via `make proto`): `src/api/issuer.pb.go`, `src/api/issuer_grpc.pb.go`

**Interfaces:**
- Produces: `pb.DescribeSANsRequest{}`, `pb.DescribeSANsResponse{Sans []string}`, `pb.IssuerServiceServer.DescribeSANs`, `pb.IssuerServiceClient.DescribeSANs`.

- [ ] **Step 1: Update the proto file**

In `src/api/issuer.proto`, add the RPC to the service and the two new messages:

```proto
syntax = "proto3";

package issuerservice;

option go_package = "./proto";

// IssuerService is the sole RPC surface a bootstrapped node calls to keep
// its operating certificate fresh. The caller's hostname is never a field
// on any of these messages -- it is always derived from the verified mTLS
// peer identity, exactly like every other authenticated RPC in this
// project.
service IssuerService {
  rpc RequestOperatingCert(RequestOperatingCertRequest) returns (RequestOperatingCertResponse);
  rpc DescribeSANs(DescribeSANsRequest) returns (DescribeSANsResponse);
}

message RequestOperatingCertRequest {
  // DER-encoded PKCS#10 certificate signing request. The caller's private
  // key never leaves the caller -- only the CSR crosses the wire.
  bytes csr_der = 1;
}

message RequestOperatingCertResponse {
  // Full certificate chain (leaf + any intermediates), PEM-encoded,
  // concatenated in order -- ready to write directly to client.crt.
  bytes cert_chain_pem = 1;
}

message DescribeSANsRequest {}

message DescribeSANsResponse {
  // Current SAN aliases for the caller's own hostname (never including the
  // hostname itself, which the caller always places in the CSR's own
  // Subject.CommonName). Must be requested and used verbatim as a CSR's
  // DNSNames before calling RequestOperatingCert: step-ca's OTT provisioner
  // validates a CSR's requested DNS SANs against the signing token's
  // authorized set with an exact match, not a subset (confirmed against
  // smallstep/certificates@v0.30.2's authority/provisioner/sign_options.go
  // dnsNamesValidator) -- there is no way to learn or guess this list
  // without asking.
  repeated string sans = 1;
}
```

- [ ] **Step 2: Generate the Go code**

Run: `make proto`
Expected output: `Protobuf code generated in src/api/`; `src/api/issuer.pb.go` and `src/api/issuer_grpc.pb.go` now also define `DescribeSANsRequest`/`DescribeSANsResponse`/the `DescribeSANs` method on both the client and server interfaces.

- [ ] **Step 3: Confirm it compiles**

Run: `cd src && go build ./api/...`
Expected: no output, exit code 0.

- [ ] **Step 4: Confirm `cmd/issuer` still compiles**

Run: `cd src && go build ./cmd/issuer/...`
Expected: **build failure** — `issuerServer` no longer satisfies `pb.IssuerServiceServer` (missing `DescribeSANs`). This is expected; Task 5 fixes it. Do not skip generating the proto code just because this build fails — that failure is the point (it's what makes Task 5's test-first step meaningful).

- [ ] **Step 5: Commit**

```bash
git add src/api/issuer.proto src/api/issuer.pb.go src/api/issuer_grpc.pb.go
git commit -m "feat(api): add issuer DescribeSANs RPC"
```

---

### Task 5: `issuer` — `DescribeSANs` handler

**Files:**
- Modify: `src/cmd/issuer/server.go`
- Modify: `src/cmd/issuer/server_test.go`

**Interfaces:**
- Consumes: `pb.DescribeSANsRequest`/`pb.DescribeSANsResponse` (Task 4), `clientmanagerstore.Store.GetClient` (existing), `ClientRecord.SANsList()` (existing).
- Produces: `(*issuerServer).DescribeSANs(ctx context.Context, req *pb.DescribeSANsRequest) (*pb.DescribeSANsResponse, error)`.

- [ ] **Step 1: Write the failing tests**

Append to `src/cmd/issuer/server_test.go` (reuses the existing `fakeAuthContext`/`newTestIssuerStore`/`testLogger` helpers already in this file):

```go
func TestDescribeSANs_KnownHostReturnsCurrentSANs(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"node-1.internal", "node-1.alt"}, time.Now()))

	srv := newIssuerServer(store, nil, testLogger())
	resp, err := srv.DescribeSANs(fakeAuthContext(t, "node-1"), &pb.DescribeSANsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1.internal", "node-1.alt"}, resp.Sans)
}

func TestDescribeSANs_HostWithNoSANsReturnsEmpty(t *testing.T) {
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", nil, time.Now()))

	srv := newIssuerServer(store, nil, testLogger())
	resp, err := srv.DescribeSANs(fakeAuthContext(t, "node-1"), &pb.DescribeSANsRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.Sans)
}

func TestDescribeSANs_RevokedHostStillReturnsSANs(t *testing.T) {
	// DescribeSANs reveals nothing the caller isn't already entitled to know
	// about itself and issues nothing -- only RequestOperatingCert enforces
	// revocation.
	store := newTestIssuerStore(t)
	require.NoError(t, store.AddClient("node-1", []string{"node-1.internal"}, time.Now()))
	require.NoError(t, store.SetRevoked("node-1", true, time.Now()))

	srv := newIssuerServer(store, nil, testLogger())
	resp, err := srv.DescribeSANs(fakeAuthContext(t, "node-1"), &pb.DescribeSANsRequest{})
	require.NoError(t, err)
	assert.Equal(t, []string{"node-1.internal"}, resp.Sans)
}

func TestDescribeSANs_UnknownHostErrors(t *testing.T) {
	store := newTestIssuerStore(t)

	srv := newIssuerServer(store, nil, testLogger())
	_, err := srv.DescribeSANs(fakeAuthContext(t, "ghost"), &pb.DescribeSANsRequest{})
	assert.Error(t, err)
}

func TestDescribeSANs_NoPeerIdentityRejected(t *testing.T) {
	store := newTestIssuerStore(t)

	srv := newIssuerServer(store, nil, testLogger())
	_, err := srv.DescribeSANs(context.Background(), &pb.DescribeSANsRequest{})
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/issuer/... -run TestDescribeSANs -v`
Expected: FAIL — `(*issuerServer).DescribeSANs` undefined (compile error).

- [ ] **Step 3: Implement**

Append to `src/cmd/issuer/server.go`:

```go
// DescribeSANs returns the caller's own current SAN alias list, read live
// from the same database RequestOperatingCert consults -- the only
// unauthenticated-adjacent-looking read in this service, but it reveals
// nothing the caller isn't already entitled to know about itself, and it
// mints/signs nothing. No revoked check: a revoked host's SANs are still
// readable; only issuance (RequestOperatingCert) is refused.
func (s *issuerServer) DescribeSANs(ctx context.Context, _ *pb.DescribeSANsRequest) (*pb.DescribeSANsResponse, error) {
	hostname, err := mtls.PeerHostname(ctx)
	if err != nil {
		return nil, fmt.Errorf("determine caller identity: %w", err)
	}

	client, err := s.store.GetClient(hostname)
	if err != nil {
		return nil, fmt.Errorf("hostname %s not tracked: %w", hostname, err)
	}

	return &pb.DescribeSANsResponse{Sans: client.SANsList()}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/issuer/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Confirm the whole build is clean again**

Run: `cd src && go build ./... && go vet ./...`
Expected: exit code 0 (the deliberate Task 4 build failure is now resolved).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/issuer/server.go src/cmd/issuer/server_test.go
git commit -m "feat(issuer): add DescribeSANs handler"
```

---

### Task 6: `issuer` — prove SAN propagation against a real CA

**Files:**
- Modify: `src/cmd/issuer/e2e_test.go`

**Interfaces:**
- Consumes: `mintAndSign` (existing, `src/cmd/issuer/mintsign.go`).

This task does not touch `certclient` — it closes the loop on the specific, previously-unverified property this whole phase turned on: that a CSR whose `DNSNames` were built from the exact SAN list `issuer` would authorize is actually accepted by real `step-ca`, and the resulting certificate really carries those SANs. `certclient`'s own unit tests (Task 8) prove its CSR always matches whatever `DescribeSANs` returns; this test proves that when it does, the real CA honors it.

- [ ] **Step 1: Add the test**

Append to `src/cmd/issuer/e2e_test.go` (reuses the file's existing `requireDocker`/`repoRootDir`/`copyFile`/`copyComposeFileWithEphemeralPort`/`discoverHostPort`/`randomPassword`/`waitForCA` helpers; add `"github.com/stretchr/testify/assert"` to the import block):

```go
// TestE2E_MintAndSignEmbedsSANsInCertificate proves the exact-match SAN
// constraint this phase's design turned on: a CSR whose DNSNames were built
// from the same SAN list passed into mintAndSign is accepted by a real
// step-ca, and the resulting leaf certificate's DNSNames match exactly.
// certclient's own unit tests (see cmd/certclient) prove its CSR always
// matches whatever issuer's DescribeSANs returns; this test proves that
// when it does, the real CA actually honors it.
func TestE2E_MintAndSignEmbedsSANsInCertificate(t *testing.T) {
	requireDocker(t)

	repoRoot := repoRootDir(t)
	tempDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "ca"), 0o755))
	copyComposeFileWithEphemeralPort(t, filepath.Join(repoRoot, "deploy", "control-plane", "docker-compose.yml"), filepath.Join(tempDir, "docker-compose.yml"))
	copyFile(t, filepath.Join(repoRoot, "deploy", "control-plane", "ca", "entrypoint.sh"), filepath.Join(tempDir, "ca", "entrypoint.sh"))
	require.NoError(t, os.Chmod(filepath.Join(tempDir, "ca", "entrypoint.sh"), 0o755))

	secretsDir := filepath.Join(tempDir, "ca", "data", "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0o700))
	password := randomPassword(t)
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "password"), []byte(password), 0o600))

	projectName := fmt.Sprintf("issuer-e2e-sans-%d", time.Now().UnixNano())
	compose := func(args ...string) *exec.Cmd {
		cmd := exec.Command("docker", append([]string{"compose", "-p", projectName}, args...)...)
		cmd.Dir = tempDir
		return cmd
	}
	t.Cleanup(func() {
		downCmd := compose("down", "--volumes", "--remove-orphans")
		if out, err := downCmd.CombinedOutput(); err != nil {
			t.Logf("docker compose down failed: %v\n%s", err, out)
		}
	})
	upCmd := compose("up", "-d", "step-ca")
	out, err := upCmd.CombinedOutput()
	require.NoError(t, err, "docker compose up failed: %s", out)

	hostPort := discoverHostPort(t, compose)
	caURL := fmt.Sprintf("https://localhost:%s", hostPort)
	rootPath := filepath.Join(tempDir, "ca", "data", "certs", "root_ca.crt")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, waitForCA(ctx, caURL, rootPath), "step-ca never became ready")

	opts := certmint.Options{
		CAURL:        caURL,
		RootFile:     rootPath,
		Provisioner:  "admin@backup.internal",
		PasswordFile: filepath.Join(secretsDir, "password"),
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	wantSANs := []string{"e2e-sans-host.internal"}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: "e2e-sans-host"},
		DNSNames: wantSANs,
	}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(csrDER)
	require.NoError(t, err)

	chainPEM, err := mintAndSign("e2e-sans-host", wantSANs, nil, csr, opts)
	require.NoError(t, err, "mintAndSign")
	require.NotEmpty(t, chainPEM)

	block, _ := pem.Decode(chainPEM)
	require.NotNil(t, block, "expected at least one PEM block in the chain")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, wantSANs, leaf.DNSNames, "issued certificate's SANs must exactly match the CSR's requested DNSNames")
}
```

- [ ] **Step 2: Run the e2e test**

Run: `cd src && go test -tags=e2e -timeout=120s ./cmd/issuer/... -run TestE2E_MintAndSignEmbedsSANsInCertificate -v`
Expected: PASS (or a clear Docker-unavailable skip message).

- [ ] **Step 3: Commit**

```bash
git add src/cmd/issuer/e2e_test.go
git commit -m "test(issuer): prove exact-match SAN propagation against a real CA"
```

---

### Task 7: `certclient` — subcommand split, `--debug`/logging, bootstrap-credential rename

**Files:**
- Modify: `src/cmd/certclient/arguments.go`
- Modify: `src/cmd/certclient/main.go`
- Modify: `src/cmd/certclient/bootstrap.go`
- Modify: `src/cmd/certclient/renew.go`
- Modify: `src/cmd/certclient/bootstrap_test.go`
- Modify: `src/cmd/certclient/renew_test.go`

**Interfaces:**
- Produces: `Arguments{Action, Token, Debug}`, `parseArguments() (*Arguments, error)` with `Action` one of `"bootstrap"`/`"renew"`/`"operating-refresh"`. `bootstrap`/`renew`'s exported-within-package signatures are unchanged (`bootstrap(token string, client signer, certsDir string) error`, `renew(client renewer, certsDir string) error`) — only their target filenames change.

- [ ] **Step 1: Update `bootstrap.go`'s target filenames**

In `src/cmd/certclient/bootstrap.go`, in `writeIdentity`, change the two identity-file writes (leave `ca.crt`'s write and everything else unchanged):

```go
	chain := append(pemCert(leaf), pemCert(intermediate)...)
	if err := os.WriteFile(filepath.Join(certsDir, "bootstrap.crt"), chain, 0o644); err != nil {
		return fmt.Errorf("write bootstrap.crt: %w", err)
	}

	rootPEM := pemCert(root)
	if err := os.WriteFile(filepath.Join(certsDir, "ca.crt"), rootPEM, 0o644); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(certsDir, "bootstrap.key"), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write bootstrap.key: %w", err)
	}
```

- [ ] **Step 2: Update `renew.go`'s target filenames**

In `src/cmd/certclient/renew.go`, in `renew`, change the load path:

```go
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, "bootstrap.crt"),
		filepath.Join(certsDir, "bootstrap.key"),
	)
	if err != nil {
		return fmt.Errorf("load existing identity: %w", err)
	}
```

And in `writeRenewedCert`:

```go
	if err := os.WriteFile(filepath.Join(certsDir, "bootstrap.crt"), chain, 0o644); err != nil {
		return fmt.Errorf("write bootstrap.crt: %w", err)
	}
```

- [ ] **Step 3: Update the existing tests' filenames**

In `src/cmd/certclient/bootstrap_test.go`, `TestBootstrap_WritesIdentityFiles` currently checks `for _, name := range []string{"ca.crt", "client.crt", "client.key"}`. Change to:

```go
	for _, name := range []string{"ca.crt", "bootstrap.crt", "bootstrap.key"} {
		info, err := os.Stat(filepath.Join(certsDir, name))
		require.NoError(t, err, "expected %s to exist", name)
		if name == "bootstrap.key" {
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	}
```

`TestBootstrap_SignErrorPropagates` currently checks `os.Stat(filepath.Join(certsDir, "client.crt"))` — change to `os.Stat(filepath.Join(certsDir, "bootstrap.crt"))`.

In `src/cmd/certclient/renew_test.go`, `setupExistingIdentity` currently writes `[]string{"ca.crt", "client.crt", "client.key"}` from `fixtureCertsDir` — change to:

```go
func setupExistingIdentity(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, pair := range []struct{ src, dst string }{
		{"ca.crt", "ca.crt"},
		{"client.crt", "bootstrap.crt"},
		{"client.key", "bootstrap.key"},
	} {
		data, err := os.ReadFile(filepath.Join(fixtureCertsDir, pair.src))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, pair.dst), data, 0o600))
	}
	return dir
}
```

(The fixture files themselves keep their existing on-disk names, `client.crt`/`client.key` — only what they're copied *to* in the temp test dir changes, since that's what `renew`'s new `bootstrap.crt`/`bootstrap.key` load path expects.)

`TestRenew_OverwritesClientCrt` reads back `filepath.Join(certsDir, "client.crt")` and `"client.key"` — change both to `"bootstrap.crt"`/`"bootstrap.key"`.

- [ ] **Step 4: Run the updated tests to verify they pass**

Run: `cd src && go test ./cmd/certclient/... -run 'TestBootstrap|TestRenew' -v`
Expected: PASS.

- [ ] **Step 5: Rewrite `arguments.go` for subcommands and `--debug`**

Replace `src/cmd/certclient/arguments.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	Action string // "bootstrap" | "renew" | "operating-refresh"
	Token  string
	Debug  bool
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}

	rootCmd := &cobra.Command{
		Use:   "certclient <command>",
		Short: "Manage this node's mTLS bootstrap credential and operating certificate",
	}
	rootCmd.PersistentFlags().BoolVar(&args.Debug, "debug", false, "Enable debug logging")

	bootstrapCmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Redeem a one-time enrollment token for a long-lived bootstrap credential",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "bootstrap" },
	}
	bootstrapCmd.Flags().StringVar(&args.Token, "token", "",
		"Enrollment token for first-time bootstrap (prefer MP_CERT_TOKEN or the stdin prompt over this flag on shared hosts)")

	renewCmd := &cobra.Command{
		Use:   "renew",
		Short: "Renew the existing bootstrap credential via step-ca's /renew",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "renew" },
	}

	operatingRefreshCmd := &cobra.Command{
		Use:   "operating-refresh",
		Short: "Obtain a fresh operating certificate from issuer",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) { args.Action = "operating-refresh" },
	}

	rootCmd.AddCommand(bootstrapCmd, renewCmd, operatingRefreshCmd)

	if err := rootCmd.Execute(); err != nil {
		return nil, err
	}
	if args.Action == "" {
		return nil, fmt.Errorf("a subcommand is required: bootstrap, renew, operating-refresh")
	}
	return args, nil
}
```

- [ ] **Step 6: Rewrite `main.go` for dispatch, `--debug`/logging, and the bootstrap.crt path**

Replace `src/cmd/certclient/main.go`:

```go
// certclient manages a node's mTLS bootstrap credential (bootstrap.crt/
// bootstrap.key) and, via operating-refresh, its short-lived operating
// certificate (client.crt/client.key) obtained from issuer.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/common/logging"
	"github.com/smallstep/certificates/ca"
)

func main() {
	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	configPath, err := config.ResolveConfigPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}
	conf, err := config.ParseConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	ctx := context.WithValue(context.Background(), "appName", "certclient")
	ctx = context.WithValue(ctx, config.ContextKey, conf)
	ctx = context.WithValue(ctx, "debugMode", args.Debug)
	ctx = context.WithValue(ctx, "quietMode", false)
	logger, logfile := logging.NewLogger(ctx)
	defer logfile.Close()

	switch args.Action {
	case "bootstrap":
		tok, err := resolveToken(args.Token, os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Token error: %v\n", err)
			os.Exit(1)
		}
		logger.Debug("bootstrapping identity")
		client, err := ca.Bootstrap(tok)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
			os.Exit(1)
		}
		if err := bootstrap(tok, client, certsDir); err != nil {
			logger.Error("bootstrap failed", "error", err)
			fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
			os.Exit(1)
		}
		logger.Info("bootstrap succeeded", "certs_dir", certsDir)
		fmt.Println("Identity bootstrapped in", certsDir)

	case "renew":
		if conf.CAHost == "" {
			fmt.Fprintln(os.Stderr, "Configuration error: ca_host not set in local.conf")
			os.Exit(1)
		}
		client, err := ca.NewClient(fmt.Sprintf("https://%s", conf.CAHost), ca.WithRootFile(filepath.Join(certsDir, "ca.crt")))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create CA client: %v\n", err)
			os.Exit(1)
		}
		logger.Debug("renewing bootstrap credential")
		if err := renew(client, certsDir); err != nil {
			logger.Error("renew failed", "error", err)
			fmt.Fprintf(os.Stderr, "Renew failed: %v\n", err)
			os.Exit(1)
		}
		logger.Info("renew succeeded", "certs_dir", certsDir)
		fmt.Println("Identity renewed in", certsDir)

	case "operating-refresh":
		if conf.IssuerHost == "" {
			fmt.Fprintln(os.Stderr, "Configuration error: issuer_host not set in local.conf")
			os.Exit(1)
		}
		if err := operatingRefresh(certsDir, conf.IssuerHost, conf.IssuerPort, conf.ConnectionTimeOutSec, logger); err != nil {
			logger.Error("operating refresh failed", "error", err)
			fmt.Fprintf(os.Stderr, "Operating refresh failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Operating certificate refreshed in", certsDir)
	}
}
```

(`operatingRefresh` is defined in Task 8; this file will not compile until that task is done — that's expected, same as Task 4/5's split.)

- [ ] **Step 7: Confirm what's expected to fail**

Run: `cd src && go build ./cmd/certclient/...`
Expected: **build failure** — `operatingRefresh` undefined. Expected; Task 8 defines it. Do not add a stub — proceed directly to Task 8.

- [ ] **Step 8: Commit**

```bash
git add src/cmd/certclient/arguments.go src/cmd/certclient/main.go src/cmd/certclient/bootstrap.go src/cmd/certclient/renew.go src/cmd/certclient/bootstrap_test.go src/cmd/certclient/renew_test.go
git commit -m "feat(certclient): split into bootstrap/renew/operating-refresh subcommands, rename to bootstrap.crt/bootstrap.key, add --debug logging"
```

---

### Task 8: `certclient operating-refresh`

**Files:**
- Create: `src/cmd/certclient/operatingrefresh.go`
- Create: `src/cmd/certclient/operatingrefresh_test.go`

**Interfaces:**
- Consumes: `pb.DescribeSANsRequest`/`Response`, `pb.RequestOperatingCertRequest`/`Response` (Task 4), `connection.ConnectWithIdentity` (Task 2).
- Produces: `operatingRefresh(certsDir, issuerHost string, issuerPort, timeoutSec int, logger *slog.Logger) error` (the real, network-dialing entry point `main.go` calls), `runOperatingRefresh(ctx context.Context, certsDir string, client issuerClient, logger *slog.Logger) error` (the testable core).

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/certclient/operatingrefresh_test.go`:

```go
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	pb "github.com/alex-sviridov/miniprotector/api"
)

func operatingRefreshTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeTestBootstrapCred writes a self-signed bootstrap.crt/bootstrap.key
// pair with the given CommonName into certsDir -- runOperatingRefresh only
// ever reads the CommonName back out of it, so a self-signed fixture is
// sufficient; it never needs to chain to a real CA for this test.
func writeTestBootstrapCred(t *testing.T, certsDir, hostname string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	require.NoError(t, os.WriteFile(filepath.Join(certsDir, "bootstrap.crt"), certPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(certsDir, "bootstrap.key"), keyPEM, 0o600))
}

type fakeIssuerClient struct {
	sans     []string
	sansErr  error
	certResp *pb.RequestOperatingCertResponse
	certErr  error
	gotCSR   *x509.CertificateRequest
}

func (f *fakeIssuerClient) DescribeSANs(_ context.Context, _ *pb.DescribeSANsRequest, _ ...grpc.CallOption) (*pb.DescribeSANsResponse, error) {
	if f.sansErr != nil {
		return nil, f.sansErr
	}
	return &pb.DescribeSANsResponse{Sans: f.sans}, nil
}

func (f *fakeIssuerClient) RequestOperatingCert(_ context.Context, req *pb.RequestOperatingCertRequest, _ ...grpc.CallOption) (*pb.RequestOperatingCertResponse, error) {
	if f.certErr != nil {
		return nil, f.certErr
	}
	csr, err := x509.ParseCertificateRequest(req.GetCsrDer())
	if err != nil {
		return nil, err
	}
	f.gotCSR = csr
	return f.certResp, nil
}

func TestRunOperatingRefresh_Success_WritesClientCrtWithMatchingCSR(t *testing.T) {
	certsDir := t.TempDir()
	writeTestBootstrapCred(t, certsDir, "node-1")

	fake := &fakeIssuerClient{
		sans:     []string{"node-1.internal"},
		certResp: &pb.RequestOperatingCertResponse{CertChainPem: []byte("fake-chain")},
	}

	err := runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger())
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-chain"), got)

	require.NotNil(t, fake.gotCSR)
	assert.Equal(t, "node-1", fake.gotCSR.Subject.CommonName)
	// DNSNames must be hostname+sans, matching what certmint.Mint actually
	// authorizes (append([]string{hostname}, sans...)) -- not sans alone.
	assert.Equal(t, []string{"node-1", "node-1.internal"}, fake.gotCSR.DNSNames)

	_, err = os.Stat(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err, "client.key should have been generated")
}

func TestRunOperatingRefresh_ReusesExistingOperatingKey(t *testing.T) {
	certsDir := t.TempDir()
	writeTestBootstrapCred(t, certsDir, "node-1")
	fake := &fakeIssuerClient{certResp: &pb.RequestOperatingCertResponse{CertChainPem: []byte("chain-1")}}

	require.NoError(t, runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger()))
	keyAfterFirst, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	fake2 := &fakeIssuerClient{certResp: &pb.RequestOperatingCertResponse{CertChainPem: []byte("chain-2")}}
	require.NoError(t, runOperatingRefresh(context.Background(), certsDir, fake2, operatingRefreshTestLogger()))
	keyAfterSecond, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	assert.Equal(t, keyAfterFirst, keyAfterSecond, "client.key must be byte-for-byte unchanged across refreshes")
}

func TestRunOperatingRefresh_DescribeSANsErrorPropagates_NoClientCrtWritten(t *testing.T) {
	certsDir := t.TempDir()
	writeTestBootstrapCred(t, certsDir, "node-1")
	fake := &fakeIssuerClient{sansErr: assert.AnError}

	err := runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger())
	assert.Error(t, err)
	_, statErr := os.Stat(filepath.Join(certsDir, "client.crt"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRunOperatingRefresh_RequestOperatingCertErrorPropagates_NoClientCrtWritten(t *testing.T) {
	certsDir := t.TempDir()
	writeTestBootstrapCred(t, certsDir, "node-1")
	fake := &fakeIssuerClient{certErr: assert.AnError}

	err := runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger())
	assert.Error(t, err)
	_, statErr := os.Stat(filepath.Join(certsDir, "client.crt"))
	assert.True(t, os.IsNotExist(statErr))
}

func TestRunOperatingRefresh_MissingBootstrapCredErrors(t *testing.T) {
	certsDir := t.TempDir() // no bootstrap.crt/bootstrap.key written
	fake := &fakeIssuerClient{}

	err := runOperatingRefresh(context.Background(), certsDir, fake, operatingRefreshTestLogger())
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/certclient/... -run TestRunOperatingRefresh -v`
Expected: FAIL — `runOperatingRefresh` undefined (compile error).

- [ ] **Step 3: Implement**

Create `src/cmd/certclient/operatingrefresh.go`:

```go
// operatingrefresh.go implements certclient operating-refresh: obtaining a
// fresh, short-lived operating certificate from issuer using the node's
// long-lived bootstrap credential, and writing it to the standard
// client.crt/client.key path every other component already expects.
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	pb "github.com/alex-sviridov/miniprotector/api"
	"github.com/alex-sviridov/miniprotector/common/connection"
	"google.golang.org/grpc"
)

// issuerClient is the subset of pb.IssuerServiceClient runOperatingRefresh
// needs -- satisfied directly by the real generated client, and by a fake
// in tests, mirroring this package's existing signer/renewer pattern.
type issuerClient interface {
	DescribeSANs(ctx context.Context, in *pb.DescribeSANsRequest, opts ...grpc.CallOption) (*pb.DescribeSANsResponse, error)
	RequestOperatingCert(ctx context.Context, in *pb.RequestOperatingCertRequest, opts ...grpc.CallOption) (*pb.RequestOperatingCertResponse, error)
}

// operatingRefresh is the real, network-dialing entry point main.go calls:
// it authenticates to issuer with the bootstrap credential and delegates
// to runOperatingRefresh.
func operatingRefresh(certsDir, issuerHost string, issuerPort, timeoutSec int, logger *slog.Logger) error {
	conn, err := connection.ConnectWithIdentity(issuerHost, issuerPort, timeoutSec, certsDir, "bootstrap.crt", "bootstrap.key")
	if err != nil {
		return fmt.Errorf("connect to issuer: %w", err)
	}
	defer conn.Close()

	client := pb.NewIssuerServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()

	return runOperatingRefresh(ctx, certsDir, client, logger)
}

// runOperatingRefresh is the testable core: given an already-connected
// issuerClient, it determines this node's hostname and current SAN list,
// builds a matching CSR against a load-or-generate operating keypair, and
// writes the resulting certificate chain to client.crt.
func runOperatingRefresh(ctx context.Context, certsDir string, client issuerClient, logger *slog.Logger) error {
	hostname, err := hostnameFromBootstrapCert(certsDir)
	if err != nil {
		return fmt.Errorf("determine hostname from bootstrap credential: %w", err)
	}

	logger.Debug("fetching current SAN list", "hostname", hostname)
	sansResp, err := client.DescribeSANs(ctx, &pb.DescribeSANsRequest{})
	if err != nil {
		return fmt.Errorf("describe SANs: %w", err)
	}

	key, err := loadOrGenerateOperatingKey(certsDir)
	if err != nil {
		return fmt.Errorf("load or generate operating key: %w", err)
	}

	csrDER, err := buildOperatingCSR(hostname, sansResp.GetSans(), key)
	if err != nil {
		return fmt.Errorf("build CSR: %w", err)
	}

	logger.Debug("requesting operating certificate", "hostname", hostname, "sans", sansResp.GetSans())
	certResp, err := client.RequestOperatingCert(ctx, &pb.RequestOperatingCertRequest{CsrDer: csrDER})
	if err != nil {
		return fmt.Errorf("request operating cert: %w", err)
	}

	if err := os.WriteFile(filepath.Join(certsDir, "client.crt"), certResp.GetCertChainPem(), 0o644); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}
	logger.Info("operating certificate refreshed", "hostname", hostname)
	return nil
}

// hostnameFromBootstrapCert parses this node's own hostname from its
// bootstrap credential's Subject.CommonName -- safe and coordination-free,
// since hostnames don't change post-enrollment.
func hostnameFromBootstrapCert(certsDir string) (string, error) {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, "bootstrap.crt"),
		filepath.Join(certsDir, "bootstrap.key"),
	)
	if err != nil {
		return "", fmt.Errorf("load bootstrap credential: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("parse bootstrap certificate: %w", err)
	}
	if leaf.Subject.CommonName == "" {
		return "", fmt.Errorf("bootstrap certificate has no CommonName")
	}
	return leaf.Subject.CommonName, nil
}

// loadOrGenerateOperatingKey loads certsDir/client.key if it already
// exists, else generates a fresh ECDSA keypair and persists it. The
// operating credential's keypair is generated once and reused across every
// subsequent refresh -- only the certificate itself is re-obtained each
// cycle.
func loadOrGenerateOperatingKey(certsDir string) (*ecdsa.PrivateKey, error) {
	keyPath := filepath.Join(certsDir, "client.key")

	data, err := os.ReadFile(keyPath)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("parse %s: no PEM block found", keyPath)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", keyPath, err)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read %s: %w", keyPath, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, fmt.Errorf("write %s: %w", keyPath, err)
	}
	return key, nil
}

// buildOperatingCSR builds a CSR whose DNSNames exactly match what
// certmint.Mint will authorize: hostname plus sans, in that order
// (certmint.Mint builds its token's SAN claim as
// append([]string{hostname}, sans...) -- confirmed against a real CA by
// this phase's e2e test, cmd/issuer/e2e_test.go's
// TestE2E_MintAndSignEmbedsSANsInCertificate). A CSR omitting hostname
// from DNSNames does NOT satisfy the exact-match validator even though
// hostname is also the CSR's CommonName -- CommonName and DNSNames are
// validated independently.
func buildOperatingCSR(hostname string, sans []string, key *ecdsa.PrivateKey) ([]byte, error) {
	template := &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: append([]string{hostname}, sans...),
	}
	return x509.CreateCertificateRequest(rand.Reader, template, key)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/certclient/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Confirm the whole build is clean**

Run: `cd src && go build ./... && go vet ./...`
Expected: exit code 0 (the deliberate Task 7 build failure is now resolved).

- [ ] **Step 6: Commit**

```bash
git add src/cmd/certclient/operatingrefresh.go src/cmd/certclient/operatingrefresh_test.go
git commit -m "feat(certclient): add operating-refresh subcommand"
```

---

### Task 9: `agent` — two policies, config-driven intervals

**Files:**
- Modify: `src/cmd/agent/policy.go`
- Modify: `src/cmd/agent/reconcile.go`
- Modify: `src/cmd/agent/list.go`
- Modify: `src/cmd/agent/main.go`
- Modify: `src/cmd/agent/reconcile_test.go`
- Modify: `src/cmd/agent/list_test.go`

**Interfaces:**
- Consumes: `config.Config.BootstrapCertRefreshIntervalSec`/`OperatingCertFetchIntervalSec` (Task 3).
- Produces: `policies(conf *config.Config) []Policy` (replaces the package-level `policies` var). `run` and `renderPolicies` both gain a `policies []Policy` parameter.

- [ ] **Step 1: Replace the compiled-in policy list**

Replace `src/cmd/agent/policy.go`:

```go
package main

import (
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
)

// Policy is a single reconcilable unit: run Binary with Args whenever more
// than Interval has elapsed since the last successful run.
type Policy struct {
	ID       string
	Binary   string
	Args     []string
	Interval time.Duration
}

// policies returns agent's two embedded policies, their intervals read from
// conf rather than compiled in -- bootstrap-refresh (long-lived credential,
// infrequent) and operating-refresh (short-lived credential, frequent).
func policies(conf *config.Config) []Policy {
	return []Policy{
		{ID: "bootstrap-refresh", Binary: "certclient", Args: []string{"renew"},
			Interval: time.Duration(conf.BootstrapCertRefreshIntervalSec) * time.Second},
		{ID: "operating-refresh", Binary: "certclient", Args: []string{"operating-refresh"},
			Interval: time.Duration(conf.OperatingCertFetchIntervalSec) * time.Second},
	}
}
```

- [ ] **Step 2: Thread `policies` through `run`**

In `src/cmd/agent/reconcile.go`, change the `run` signature and its one reference to the removed package var:

```go
func run(ctx context.Context, logger *slog.Logger, cachePath string, reconcileInterval time.Duration, execute runner, policies []Policy) error {
```

(the body's `for _, p := range policies` loop is unchanged — it now reads the parameter instead of a package var).

- [ ] **Step 3: Thread `policies` through `renderPolicies`**

In `src/cmd/agent/list.go`, change the signature:

```go
func renderPolicies(w io.Writer, cachePath string, now time.Time, policies []Policy) error {
```

(the body's `for _, p := range policies` loop is unchanged).

- [ ] **Step 4: Update `main.go`'s call sites**

In `src/cmd/agent/main.go`, compute the policy list once and pass it to both call sites:

```go
	pols := policies(conf)

	switch arguments.Action {
	case "serve":
		if err := os.MkdirAll(varDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create var directory %s: %v\n", varDir, err)
			os.Exit(1)
		}

		ctx := context.WithValue(context.Background(), "appName", appName)
		ctx = context.WithValue(ctx, config.ContextKey, conf)
		ctx = context.WithValue(ctx, "debugMode", arguments.Debug)
		ctx = context.WithValue(ctx, "quietMode", false)

		logger, logfile := logging.NewLogger(ctx)
		defer logfile.Close()

		reconcileInterval := time.Duration(conf.ReconcileIntervalSec) * time.Second
		signalCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()

		logger.Info("agent started", "reconcile_interval", reconcileInterval, "cache_path", cachePath)
		if err := run(signalCtx, logger, cachePath, reconcileInterval, realExec, pols); err != nil {
			logger.Error("agent exited with error", "error", err)
			os.Exit(1)
		}

	case "list-policies":
		if err := renderPolicies(os.Stdout, cachePath, time.Now(), pols); err != nil {
			fmt.Fprintf(os.Stderr, "list-policies failed: %v\n", err)
			os.Exit(1)
		}
	}
```

(Insert the `pols := policies(conf)` line right before the `switch arguments.Action {` that already exists in this file — everything else in `main.go` is unchanged.)

- [ ] **Step 5: Update `reconcile_test.go`'s call sites**

In `src/cmd/agent/reconcile_test.go`, the two tests that currently swap the package-level `policies` var no longer need to (there is no package var to swap). Replace:

```go
	origPolicies := policies
	policies = []Policy{{ID: "test-policy", Binary: "true", Interval: time.Hour}}
	defer func() { policies = origPolicies }()
	...
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run)
```

with:

```go
	testPolicies := []Policy{{ID: "test-policy", Binary: "true", Interval: time.Hour}}
	...
	err := run(ctx, testLogger(), cachePath, 10*time.Millisecond, fr.run, testPolicies)
```

and the second occurrence (currently `Binary: "false"`) analogously:

```go
	testPolicies := []Policy{{ID: "test-policy", Binary: "false", Interval: time.Hour}}
	...
	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr.run, testPolicies)
```

- [ ] **Step 6: Update `list_test.go`'s call sites**

In `src/cmd/agent/list_test.go`, each of the three `renderPolicies(&buf, cachePath, ...)` calls needs a `policies` argument. Since these tests currently rely on the package-level `policies` var (one entry, `cert-refresh`) to know what to assert against, give each an explicit local policy list matching what that test already asserts on (check each test's own assertions for the policy ID/interval it expects, and pass a `[]Policy{...}` literal reproducing it as the fourth argument) — e.g. if a test currently expects a row for `cert-refresh`, replace it with a `bootstrap-refresh` or `operating-refresh` row and adjust its assertions to match, since `cert-refresh` no longer exists as a policy ID anywhere in this package. Read the three tests in full before editing so each one's expected table output is updated consistently with its new explicit policy list, not just its function call.

- [ ] **Step 7: Run tests to verify they pass**

Run: `cd src && go test ./cmd/agent/... -v`
Expected: PASS (all tests, including every pre-existing one, now referencing explicit policy lists instead of the removed package var).

- [ ] **Step 8: Full build verification**

Run: `cd src && go build ./... && go vet ./... && go test ./... 2>&1 | tail -40`
Expected: `ok` for every package (the pre-existing `cmd/brfs` vet warning, if checked, remains the only `go vet` output — not introduced by this task).

- [ ] **Step 9: Commit**

```bash
git add src/cmd/agent/
git commit -m "feat(agent): run bootstrap-refresh and operating-refresh as two config-driven policies"
```

---

### Task 10: Documentation

**Files:**
- Modify: `docs/components/certclient.md`
- Modify: `docs/components/agent.md`
- Modify: `docs/components/issuer.md`
- Modify: `docs/components/client-manager.md`
- Modify: `docs/protocols/issuer.md`
- Create: `docs/SECURITY.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `README.md`
- Modify: `CHANGELOG.md`

Per `.claude/CLAUDE.md`'s documentation rules (gRPC protocol change, feature change, and changelog sections all apply to this phase).

- [ ] **Step 1: Update `docs/protocols/issuer.md`**

Add the `DescribeSANs` RPC to the `## RPC` proto block (mirror `issuer.proto`'s final content from Task 4), and add a subsection explaining why it exists:

```markdown
## Why `DescribeSANs` exists

step-ca's OTT/JWK provisioner validates a presented CSR's requested DNS SANs against the signing
token's authorized set with an **exact match**, not a subset (confirmed against
`smallstep/certificates@v0.30.2`'s `authority/provisioner/sign_options.go` `dnsNamesValidator`,
enforced in `authority/tls.go`). A CSR with no DNSNames is silently accepted but yields a SAN-less
certificate; a CSR with the wrong DNSNames is rejected outright. Since only `client-manager`'s
database (which the calling node cannot read) knows a hostname's current SAN alias list,
`certclient operating-refresh` calls `DescribeSANs` first and uses its result verbatim as the CSR's
`DNSNames`, before calling `RequestOperatingCert`.
```

- [ ] **Step 2: Rewrite `docs/components/certclient.md`**

Update for: the `bootstrap`/`renew`/`operating-refresh` subcommand split; the `bootstrap.crt`/
`bootstrap.key` filename rename (and that `client.crt`/`client.key` are now written by
`operating-refresh`, not `bootstrap`/`renew`); the new `--debug` flag; a "See Also" link to
[issuer](./issuer.md).

- [ ] **Step 3: Update `docs/components/agent.md`**

Update for: the two-policy list (`bootstrap-refresh`, `operating-refresh`) replacing the single
`cert-refresh` policy; the three new config keys and what each governs; that `agent list-policies`
now shows two rows.

- [ ] **Step 4: Update `docs/components/issuer.md`**

Add the `DescribeSANs` RPC to its Behavior section; remove the "not yet wired" caveat about agent
integration, replacing it with a cross-link to `certclient.md`'s `operating-refresh`.

- [ ] **Step 5: Update `docs/components/client-manager.md`**

Correct the phase 2b-added note stating agent integration for revoke enforcement is "not yet
built" — after this phase it is; cross-link to `certclient.md`.

- [ ] **Step 6: Write `docs/SECURITY.md`**

New file. Consolidate, as a canonical living document (not scattered across dated specs):

- The two-tier bootstrap/operating credential model and why it exists (renewal can't carry new
  content in step-ca; revocation is keyed by serial, not subject; `/renew` never re-checks
  authorization — the three constraints phase 2's design worked through).
- mTLS everywhere; hostname always derived from the verified peer certificate, never a
  client-supplied request field (`RequestOperatingCert`, `DescribeSANs`, and every other
  authenticated RPC in this project).
- The revocation trust model and its costs: `issuer` becomes a hard dependency for the entire
  fleet's mesh access; no HA for it yet; the bootstrap credential is not yet cryptographically
  confined to only reaching `issuer` (today an operational expectation, not an enforced boundary).
- Cross-link from `docs/ARCHITECTURE.md` and `README.md`.

- [ ] **Step 7: Update `docs/ARCHITECTURE.md`**

Add a cross-reference to `docs/SECURITY.md` near the credentials discussion. Update the `agent`
row's Status column (currently "Implemented (v1: cert renewal only)") and the `issuer` row's
Status column (currently "Implemented (agent integration and a CA-side custom template are
separate, later work)") — agent integration is done; only the CA-side custom template for
attribute embedding remains separate, later work.

- [ ] **Step 8: Update `README.md`**

Add `docs/SECURITY.md` to the Documentation list. Update the one-line `agent`/`certclient`
component descriptions if they reference the retired single-credential/single-policy shape.

- [ ] **Step 9: Add the `CHANGELOG.md` entry**

Add a dated entry (most recent first) summarizing: agent now obtains and refreshes operating
certificates through issuer using a two-tier bootstrap/operating credential model, completing
phase 2's revocation/attribute/SAN goals end-to-end; includes a new `issuer.DescribeSANs` RPC
discovered to be necessary during implementation for SAN propagation to actually work.

- [ ] **Step 10: Commit**

```bash
git add docs/ README.md CHANGELOG.md
git commit -m "docs: document phase 2c (agent/issuer wiring, DescribeSANs, SECURITY.md)"
```

---

## Self-Review

**Spec coverage:**
- Two-tier bootstrap/operating credential model, `common/mtls`/`common/connection` additive changes → Tasks 1–2.
- New config keys → Task 3.
- `DescribeSANs` RPC (the corrected design's fix for SAN propagation) → Task 4.
- `issuer` server-side `DescribeSANs` handler → Task 5.
- Real-CA proof that SAN propagation actually works → Task 6.
- `certclient` subcommand split, bootstrap-credential rename, `--debug`/logging → Task 7.
- `certclient operating-refresh` (the phase 2c core) → Task 8.
- `agent`'s two-policy, config-driven reconcile → Task 9.
- All documentation impact from the design (including the corrected proto/protocol-doc
  requirement and the new `docs/SECURITY.md`) → Task 10.
- Every Non-Goal from the design (no `issuer` `RequestOperatingCert` handler changes beyond the
  additive `DescribeSANs`, no crypto isolation of the bootstrap credential, no HA, no migration
  tooling, no policy-server) is correctly *not* covered by any task above.

**Placeholder scan:** Task 9 Step 6 intentionally describes a pattern ("read each test's own
assertions... reproduce them") rather than literal code, because `list_test.go`'s exact current
assertions weren't fully transcribed into this plan — this is flagged explicitly in that step
with concrete instructions for what to look for and how to adapt it consistently, not left as a
bare "add tests" placeholder. Every other step has complete, runnable code.

**Type consistency:** `issuerClient` (Task 8) matches `pb.IssuerServiceClient`'s generated method
signatures exactly (`ctx, *Request, ...grpc.CallOption) (*Response, error)`), confirmed against the
existing `catalogsync` client-call pattern using the same generated-client shape. `Policy`'s fields
(Task 9) are unchanged from agent v1; only how the slice is constructed (`policies(conf)` instead
of a package var) and threaded (`run`/`renderPolicies` parameters) changes, consistently across
`policy.go`/`reconcile.go`/`list.go`/`main.go`/both test files. `bootstrap`/`renew`'s signatures
(Task 7) are explicitly unchanged from their current form — only string literals inside their
bodies change, verified against the actual current file contents read before this plan was written.

No gaps found.
