# Control-Plane Deployment for the Two-Tier Credential Model Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix `deploy/control-plane/`'s docker-compose demo, which phase 2c's certclient/agent changes broke (`catalog` crash-loops), by giving `issuer` a self-mint-and-refresh mechanism for its own mTLS identity, turning `catalog` into an ordinary `agent`-managed enrolled node, and wiring a persistent, shared `client-manager` database into the compose stack.

**Architecture:** `issuer` mints its own server certificate directly at startup (reusing its existing `mintAndSign` machinery with a self-built CSR, no enrollment token, no `certclient`) and re-mints on an internal ticker while running — no new binary, no second daemon on the CA host. `catalog` becomes a normal fleet node from a certificate-lifecycle standpoint: `certclient bootstrap`/`renew` for its bootstrap credential, then `agent serve` in the background running its existing, unmodified two policies against `issuer`.

**Tech Stack:** Go, `smallstep/certificates/{ca,api}` (already pinned, already used by `mintAndSign`), Docker Compose, shell (entrypoint scripts).

## Global Constraints

- `issuer`'s self-minted identity must have DNSNames that exactly match what `certmint.Mint` authorizes (`append([]string{hostname}, sans...)`, confirmed against a real CA in an earlier phase) — a CSR with empty `DNSNames` is accepted but produces a SAN-less certificate that fails real (non-loopback) TLS hostname verification, so the CSR must explicitly include the hostname in `DNSNames`, not just `CommonName`.
- `issuer`'s own hostname is supplied explicitly via a new `--hostname` flag — never inferred from the environment.
- A transient self-mint refresh failure while `issuer` is already running and serving must log and keep the existing, still-valid certificate in place — it must never crash or stop serving over a single failed refresh attempt.
- `catalog`'s two `agent` policies (`bootstrap-refresh`, `operating-refresh`) are reused completely unmodified from phase 2c — no special-casing.
- No changes to `bwfs`/`brfs`/`rwfs`'s own enrollment, and no changes to `docs/superpowers/specs/2026-07-03-demo-lab-environment-design.md` (a separate, already-flagged, out-of-scope follow-up).

---

### Task 1: `common/config` — issuer self-identity keys

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Produces: `Config.IssuerSelfCertTTLSec int` (default `7776000`, ~90 days — issuer's own identity is conceptually a bootstrap-tier credential, long-lived), `Config.IssuerSelfCertRefreshIntervalSec int` (default `86400`, daily — mirrors `BootstrapCertRefreshIntervalSec`'s existing pairing of a long TTL with a much more frequent refresh attempt).

- [ ] **Step 1: Write the failing tests**

Append to `src/common/config/config_test.go`:

```go
func TestParseConfig_IssuerSelfCertTTLSecDefaultsTo7776000(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 7776000, conf.IssuerSelfCertTTLSec)
}

func TestParseConfig_IssuerSelfCertTTLSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nIssuerSelfCertTTLSec=2592000\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 2592000, conf.IssuerSelfCertTTLSec)
}

func TestParseConfig_IssuerSelfCertRefreshIntervalSecDefaultsTo86400(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 86400, conf.IssuerSelfCertRefreshIntervalSec)
}

func TestParseConfig_IssuerSelfCertRefreshIntervalSecParsesCorrectly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nIssuerSelfCertRefreshIntervalSec=43200\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, 43200, conf.IssuerSelfCertRefreshIntervalSec)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./common/config/... -run 'TestParseConfig_IssuerSelfCert' -v`
Expected: FAIL — fields undefined (compile error).

- [ ] **Step 3: Implement**

In `src/common/config/config.go`, add two fields to the `Config` struct (after `OperatingCertFetchIntervalSec`):

```go
	IssuerSelfCertTTLSec             int
	IssuerSelfCertRefreshIntervalSec int
```

Add two defaults to the literal in `ParseConfig`:

```go
		IssuerSelfCertTTLSec:             7776000,
		IssuerSelfCertRefreshIntervalSec: 86400,
```

Add two `case`s to the `switch key` block:

```go
		case "IssuerSelfCertTTLSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid IssuerSelfCertTTLSec value at line %d: %s", lineNum, value)
			}
			config.IssuerSelfCertTTLSec = number
			foundFields["IssuerSelfCertTTLSec"] = true
		case "IssuerSelfCertRefreshIntervalSec":
			number, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid IssuerSelfCertRefreshIntervalSec value at line %d: %s", lineNum, value)
			}
			config.IssuerSelfCertRefreshIntervalSec = number
			foundFields["IssuerSelfCertRefreshIntervalSec"] = true
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: gofmt and commit**

Run: `cd src && gofmt -w common/config/config.go` (this file's struct/defaults blocks have grown long field names across several phases; keep it aligned as you go rather than leaving cleanup for later).

```bash
git add src/common/config/
git commit -m "feat(config): add IssuerSelfCertTTLSec, IssuerSelfCertRefreshIntervalSec"
```

---

### Task 2: `issuer` — self-mint own identity (testable core)

**Files:**
- Create: `src/cmd/issuer/selfidentity.go`
- Create: `src/cmd/issuer/selfidentity_test.go`

**Interfaces:**
- Consumes: `mintAndSignFunc` (existing, `src/cmd/issuer/server.go`).
- Produces: `mintSelfIdentity(hostname, certsDir, rootFile string, mint mintAndSignFunc, ttlSec int) error`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/issuer/selfidentity_test.go`:

```go
package main

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMintSelfIdentity_WritesAllThreeFilesWithMatchingCSR(t *testing.T) {
	certsDir := t.TempDir()
	rootFile := filepath.Join(t.TempDir(), "root.crt")
	require.NoError(t, os.WriteFile(rootFile, []byte("fake-root-pem"), 0o644))

	var gotHostname string
	var gotSANs []string
	var gotCSR *x509.CertificateRequest
	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		gotHostname = hostname
		gotSANs = sans
		gotCSR = csr
		return []byte("fake-chain"), nil
	}

	err := mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600)
	require.NoError(t, err)

	assert.Equal(t, "issuer", gotHostname)
	assert.Nil(t, gotSANs)
	require.NotNil(t, gotCSR)
	assert.Equal(t, "issuer", gotCSR.Subject.CommonName)
	assert.Equal(t, []string{"issuer"}, gotCSR.DNSNames,
		"CSR DNSNames must include the hostname explicitly -- an empty DNSNames CSR is accepted by step-ca but produces a SAN-less certificate that fails real TLS hostname verification")

	rootGot, err := os.ReadFile(filepath.Join(certsDir, "ca.crt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-root-pem"), rootGot)

	chainGot, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	assert.Equal(t, []byte("fake-chain"), chainGot)

	keyInfo, err := os.Stat(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), keyInfo.Mode().Perm())
}

func TestMintSelfIdentity_MintErrorPropagates_NoFilesWritten(t *testing.T) {
	certsDir := t.TempDir()
	rootFile := filepath.Join(t.TempDir(), "root.crt")
	require.NoError(t, os.WriteFile(rootFile, []byte("fake-root-pem"), 0o644))

	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return nil, assert.AnError
	}

	err := mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600)
	assert.Error(t, err)

	_, statErr := os.Stat(filepath.Join(certsDir, "client.crt"))
	assert.True(t, os.IsNotExist(statErr), "client.crt should not be written when mint fails")
}

func TestMintSelfIdentity_MissingRootFileErrors(t *testing.T) {
	certsDir := t.TempDir()
	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		t.Fatal("mint must not be called when the root file can't be read")
		return nil, nil
	}

	err := mintSelfIdentity("issuer", certsDir, filepath.Join(t.TempDir(), "does-not-exist.crt"), mint, 3600)
	assert.Error(t, err)
}

func TestMintSelfIdentity_EachCallGeneratesAFreshKeypair(t *testing.T) {
	certsDir := t.TempDir()
	rootFile := filepath.Join(t.TempDir(), "root.crt")
	require.NoError(t, os.WriteFile(rootFile, []byte("fake-root-pem"), 0o644))
	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return []byte("fake-chain"), nil
	}

	require.NoError(t, mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600))
	keyAfterFirst, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	require.NoError(t, mintSelfIdentity("issuer", certsDir, rootFile, mint, 3600))
	keyAfterSecond, err := os.ReadFile(filepath.Join(certsDir, "client.key"))
	require.NoError(t, err)

	assert.NotEqual(t, keyAfterFirst, keyAfterSecond,
		"unlike the operating credential's keypair (reused across refreshes), issuer's own self-mint has no external consistency requirement on a stable keypair -- a fresh one each call is simpler and correct")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd src && go test ./cmd/issuer/... -run TestMintSelfIdentity -v`
Expected: FAIL — `mintSelfIdentity` undefined (compile error).

- [ ] **Step 3: Implement**

Create `src/cmd/issuer/selfidentity.go`:

```go
// selfidentity.go: issuer mints its own mTLS server identity directly,
// using the CA provisioner access it already holds for RequestOperatingCert
// -- no enrollment token, no certclient, no dependency on a running issuer
// (it can't call itself). Safe to call repeatedly: each call generates a
// brand-new keypair and certificate; nothing else in the system depends on
// issuer's specific keypair staying stable across restarts or refreshes.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

func mintSelfIdentity(hostname, certsDir, rootFile string, mint mintAndSignFunc, ttlSec int) error {
	rootPEM, err := os.ReadFile(rootFile)
	if err != nil {
		return fmt.Errorf("read CA root %s: %w", rootFile, err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: []string{hostname},
	}, key)
	if err != nil {
		return fmt.Errorf("build CSR: %w", err)
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return fmt.Errorf("parse CSR: %w", err)
	}

	chainPEM, err := mint(hostname, nil, nil, csr)
	if err != nil {
		return fmt.Errorf("mint and sign self identity: %w", err)
	}

	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		return fmt.Errorf("create certs dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "ca.crt"), rootPEM, 0o644); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}
	if err := os.WriteFile(filepath.Join(certsDir, "client.crt"), chainPEM, 0o644); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(certsDir, "client.key"), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write client.key: %w", err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/issuer/... -v`
Expected: PASS (all tests, including every pre-existing one).

- [ ] **Step 5: Commit**

```bash
git add src/cmd/issuer/selfidentity.go src/cmd/issuer/selfidentity_test.go
git commit -m "feat(issuer): add self-mint logic for its own server identity"
```

---

### Task 3: `issuer` — wire self-mint into startup and a refresh loop

**Files:**
- Modify: `src/cmd/issuer/arguments.go`
- Modify: `src/cmd/issuer/main.go`

**Interfaces:**
- Consumes: `mintSelfIdentity` (Task 2), `config.Config.IssuerSelfCertTTLSec`/`IssuerSelfCertRefreshIntervalSec` (Task 1).
- Produces: `Arguments.Hostname string` (new required-in-practice flag, default empty).

- [ ] **Step 1: Add the `--hostname` flag**

In `src/cmd/issuer/arguments.go`, add a field to `Arguments`:

```go
	Hostname     string
```

Add a flag registration (after the `--password-file` flag):

```go
	cmd.Flags().StringVar(&args.Hostname, "hostname", "", "This issuer instance's own hostname, embedded as the CommonName/SAN of its self-minted server certificate (must match whatever issuer_host other nodes are configured to dial)")
```

- [ ] **Step 2: Wire self-mint into `main.go`**

In `src/cmd/issuer/main.go`, after the existing `mintSign := func(...) {...}` closure (the one used by `RequestOperatingCert`, unchanged) and before `srv := newIssuerServer(...)`, add:

```go
	if args.Hostname == "" {
		fmt.Fprintln(os.Stderr, "Arguments error: --hostname is required")
		os.Exit(1)
	}

	selfMintSign := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return mintAndSign(hostname, sans, attributes, csr, mintOpts, conf.IssuerSelfCertTTLSec)
	}

	logger.Info("minting own server identity", "hostname", args.Hostname)
	if err := mintSelfIdentity(args.Hostname, certsDir, args.RootFile, selfMintSign, conf.IssuerSelfCertTTLSec); err != nil {
		logger.Error("failed to mint own server identity", "error", err)
		os.Exit(1)
	}
```

Then, after `signalCtx, stop := signal.NotifyContext(...)` (before the `logger.Info("issuer started", ...)` line), start the refresh loop:

```go
	refreshInterval := time.Duration(conf.IssuerSelfCertRefreshIntervalSec) * time.Second
	go func() {
		ticker := time.NewTicker(refreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-signalCtx.Done():
				return
			case <-ticker.C:
				if err := mintSelfIdentity(args.Hostname, certsDir, args.RootFile, selfMintSign, conf.IssuerSelfCertTTLSec); err != nil {
					logger.Error("self-identity refresh failed, keeping existing certificate", "error", err)
				}
			}
		}
	}()
```

`logger`, `signalCtx`, `certsDir`, `mintOpts`, and `conf` are all already in scope at these points in the existing `main.go` — this step only inserts new lines, it does not restructure anything already there. Add `"time"` to the import block if not already present (it already is, via the existing `connection.StartServer`/context usage — verify before editing).

- [ ] **Step 3: Confirm it builds**

Run: `cd src && go build ./cmd/issuer/...`
Expected: no output, exit code 0.

- [ ] **Step 4: Confirm the whole repo still builds and vets cleanly**

Run: `cd src && go build ./... && go vet ./...`
Expected: only the pre-existing, unrelated `cmd/brfs` vet warning.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/issuer/arguments.go src/cmd/issuer/main.go
git commit -m "feat(issuer): mint and periodically refresh its own server identity at startup"
```

---

### Task 4: `issuer` — e2e proof of self-mint against a real CA

**Files:**
- Modify: `src/cmd/issuer/e2e_test.go`

**Interfaces:**
- Consumes: `mintSelfIdentity` (Task 2).

- [ ] **Step 1: Add the test**

Append to `src/cmd/issuer/e2e_test.go` (reuses the file's existing `requireDocker`/`repoRootDir`/`copyFile`/`copyComposeFileWithEphemeralPort`/`discoverHostPort`/`randomPassword`/`waitForCA` helpers):

```go
// TestE2E_MintSelfIdentityProducesAWorkingServerCertificate proves issuer
// can obtain its own mTLS server identity from nothing but direct CA
// provisioner access -- no enrollment token, no certclient -- against a
// real, throwaway step-ca, and that the resulting certificate's SAN
// actually matches its own hostname (the property that makes real,
// non-loopback TLS hostname verification succeed later).
func TestE2E_MintSelfIdentityProducesAWorkingServerCertificate(t *testing.T) {
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

	projectName := fmt.Sprintf("issuer-e2e-selfmint-%d", time.Now().UnixNano())
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
	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return mintAndSign(hostname, sans, attributes, csr, opts, 3600)
	}

	certsDir := filepath.Join(tempDir, "issuer-certs")
	require.NoError(t, mintSelfIdentity("e2e-issuer", certsDir, rootPath, mint, 3600))

	chainPEM, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	block, _ := pem.Decode(chainPEM)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "e2e-issuer", leaf.Subject.CommonName)
	assert.Equal(t, []string{"e2e-issuer"}, leaf.DNSNames,
		"issuer's own certificate must carry its hostname as a SAN, not just CommonName, for real (non-loopback) TLS hostname verification to succeed")
}
```

- [ ] **Step 2: Run the e2e test**

Run: `cd src && go test -tags=e2e -timeout=120s ./cmd/issuer/... -run TestE2E_MintSelfIdentityProducesAWorkingServerCertificate -v`
Expected: PASS (or a clear Docker-unavailable skip message).

- [ ] **Step 3: Commit**

```bash
git add src/cmd/issuer/e2e_test.go
git commit -m "test(issuer): prove self-mint produces a real, hostname-verifiable certificate"
```

---

### Task 5: `deploy/control-plane` — add the `issuer` service and a persistent `client-manager` volume

**Files:**
- Modify: `deploy/control-plane/docker-compose.yml`
- Create: `deploy/control-plane/issuer/Dockerfile`
- Create: `deploy/control-plane/issuer/local.conf`
- Create: `deploy/control-plane/client-manager/local.conf`

**Interfaces:**
- Consumes: `issuer serve --hostname` (Task 3), `IssuerSelfCertTTLSec`/`IssuerSelfCertRefreshIntervalSec` (Task 1).

- [ ] **Step 1: Create `issuer`'s Dockerfile**

`deploy/control-plane/issuer/Dockerfile`:

```dockerfile
FROM golang:1.26 AS builder

WORKDIR /build
COPY . .
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make issuer

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /build/bin/issuer ./
ENTRYPOINT ["./issuer", "serve", "--hostname", "issuer", \
            "--ca-url", "https://step-ca:9000", \
            "--root", "/data/root_ca.crt", \
            "--provisioner", "admin@backup.internal", \
            "--password-file", "/data/secrets/password"]
```

- [ ] **Step 2: Create `issuer`'s `local.conf`**

`deploy/control-plane/issuer/local.conf`:

```
# default_port/default_streams/logfolder are required by every miniprotector
# binary's shared config parser, even though issuer itself only uses
# issuer_port and the client-manager var_path below. Harmless placeholders.
default_port=15722
default_streams=4
logfolder=/data/log

# issuer_port is what agent-managed nodes' operating-refresh dials.
issuer_port=9200

# Points at the same directory client-manager's own enrollment commands
# write their SQLite database to (mounted as a shared volume in
# docker-compose.yml) -- issuer and client-manager share one database
# file, not a synced pair.
var_path=/data/client-manager
```

- [ ] **Step 3: Create `client-manager`'s `local.conf`**

`deploy/control-plane/client-manager/local.conf` (used by the throwaway `client-manager` container invocations documented in `README.md`, Task 7):

```
default_port=15722
default_streams=4
logfolder=/tmp

# Same directory issuer's own local.conf points its var_path at -- this is
# what makes client-manager's `add`/`revoke`/etc. commands and issuer's own
# reads of the same client list durable and shared, instead of each
# throwaway container writing to its own, discarded SQLite file.
var_path=/data
```

- [ ] **Step 4: Add `issuer` to `docker-compose.yml` and give `catalog`/client-manager a shared, persistent database volume**

In `deploy/control-plane/docker-compose.yml`, add a new `issuer` service (after `step-ca`, before `catalog`):

```yaml
  issuer:
    build:
      context: ../..
      dockerfile: deploy/control-plane/issuer/Dockerfile
    depends_on:
      - step-ca
    volumes:
      - ./issuer/data:/data
      - ./issuer/local.conf:/data/local.conf:ro
      - ./ca/data/certs/root_ca.crt:/data/root_ca.crt:ro
      - ./ca/data/secrets/password:/data/secrets/password:ro
      - ./client-manager/data:/data/client-manager
    environment:
      - MP_CONFIG_PATH=/data
    ports:
      - "9200:9200"
    restart: unless-stopped
```

Update `catalog`'s existing `volumes:`/`environment:` blocks to add (Task 6 relies on these being present):

```yaml
      - ./catalog/local.conf:/data/local.conf:ro
```
(already present — no change needed to this line itself) plus, in the same service block, add `depends_on: issuer` alongside the existing `step-ca` entry:

```yaml
    depends_on:
      - step-ca
      - issuer
```

- [ ] **Step 5: Confirm the compose file is syntactically valid**

Run: `cd deploy/control-plane && docker compose config --quiet`
Expected: no output, exit code 0 (this validates YAML/schema without starting anything).

- [ ] **Step 6: Commit**

```bash
git add deploy/control-plane/docker-compose.yml deploy/control-plane/issuer/ deploy/control-plane/client-manager/
git commit -m "feat(deploy): add issuer service and a persistent, shared client-manager volume"
```

---

### Task 6: `catalog` — bundle `agent`, rewrite entrypoint for the two-tier model

**Files:**
- Modify: `deploy/control-plane/catalog/Dockerfile`
- Modify: `deploy/control-plane/catalog/entrypoint.sh`
- Modify: `deploy/control-plane/catalog/local.conf`

**Interfaces:**
- Consumes: `certclient bootstrap`/`renew`/`operating-refresh` (phase 2c), `agent serve` (phase 2c), `issuer` service (Task 5).

- [ ] **Step 1: Bundle `agent` into the Dockerfile**

In `deploy/control-plane/catalog/Dockerfile`, change the build line:

```dockerfile
RUN CGO_ENABLED=1 GOOS=linux GOARCH=amd64 make catalog certclient agent
```

and the copy line:

```dockerfile
COPY --from=builder /build/bin/catalog /build/bin/certclient /build/bin/agent ./
```

- [ ] **Step 2: Rewrite the entrypoint**

Replace `deploy/control-plane/catalog/entrypoint.sh`:

```sh
#!/bin/sh
set -e

mkdir -p "$STORAGE_PATH"

# One-time bootstrap (first run, needs MP_CERT_TOKEN) or renew (every
# subsequent restart -- no expiry check, certclient always renews when an
# identity already exists) of the long-lived bootstrap credential.
if [ -f /data/certs/bootstrap.crt ]; then
	./certclient renew
else
	./certclient bootstrap --token "$MP_CERT_TOKEN"
fi

# agent keeps both the bootstrap credential (daily) and the operating
# credential (every 15 min, talking to issuer) fresh continuously, so
# catalog no longer needs a container restart to pick up a renewal --
# a real improvement over the old renew-on-restart-only behavior.
./agent serve &

exec ./catalog "$STORAGE_PATH" --debug="${DEBUG:-false}"
```

- [ ] **Step 3: Update `local.conf`**

Append to `deploy/control-plane/catalog/local.conf`:

```
# Where catalog's agent-managed operating-refresh policy dials issuer.
issuer_host=issuer
issuer_port=9200

# agent's own reconcile-loop tick cadence.
ReconcileIntervalSec=30

# How often agent refreshes each credential tier -- see docs/SECURITY.md
# for why these two are on such different cadences.
BootstrapCertRefreshIntervalSec=86400
OperatingCertFetchIntervalSec=900
```

- [ ] **Step 4: Confirm the compose file still validates**

Run: `cd deploy/control-plane && docker compose config --quiet`
Expected: no output, exit code 0.

- [ ] **Step 5: Commit**

```bash
git add deploy/control-plane/catalog/
git commit -m "feat(deploy): catalog bundles agent, entrypoint uses the two-tier credential model"
```

---

### Task 7: `deploy/control-plane/README.md` — rewritten enrollment walkthrough and smoke test

**Files:**
- Modify: `deploy/control-plane/README.md`

- [ ] **Step 1: Update the `catalog` enrollment section**

Replace the existing "`catalog` itself needs an mTLS identity..." paragraph and its `docker run` example with an updated version reflecting: (a) the throwaway `client-manager` container now mounts a persistent volume (`-v "$(pwd)/client-manager/data:/data" -v "$(pwd)/client-manager/local.conf:/data/local.conf:ro" -e MP_CONFIG_PATH=/data`, matching Task 5's new `client-manager/local.conf`), (b) `issuer` must be up (`docker compose up -d issuer`) before minting any client token, since it's the same shared database issuer reads from, (c) `catalog`'s own identity is now the bootstrap+operating two-tier pair, refreshed continuously by its bundled `agent`, not renewed only on restart.

- [ ] **Step 2: Add an enroll → connect → revoke smoke test section**

Add a new `## Smoke test: enroll, connect, revoke` section documenting the exact walkthrough used to verify this deployment end-to-end:

```markdown
## Smoke test: enroll, connect, revoke

A full walkthrough proving the two-tier credential model actually works in this deployment:

```bash
make control-plane-up   # step-ca, issuer

# Enroll catalog (writes to the shared, persistent client-manager volume)
docker run --rm --network control-plane_default \
  -v "$(pwd)/../..:/repo" -w /repo/src \
  -v "$(pwd)/client-manager/data:/data" \
  -v "$(pwd)/client-manager/local.conf:/data/local.conf:ro" \
  -e MP_CONFIG_PATH=/data \
  golang:1.26 \
  go run ./cmd/clientmanager add catalog-01 --ca-url https://step-ca:9000 \
    --root /repo/deploy/control-plane/ca/data/certs/root_ca.crt \
    --password-file /repo/deploy/control-plane/ca/data/secrets/password

MP_CERT_TOKEN=<printed-token> docker compose up -d catalog
docker compose logs -f catalog   # confirm it stays up (no more crash-loop)

# Revoke, then confirm catalog's next operating-refresh is refused
# (check catalog's logs after up to OperatingCertFetchIntervalSec has
# elapsed -- default 15 minutes)
docker run --rm ... go run ./cmd/clientmanager revoke catalog-01 ...
```

Teardown: `docker compose down` (stop/remove containers; named data volumes persist) or
`docker compose down --volumes` (full wipe, including `issuer`'s and `client-manager`'s own data)
— both already work generically, no new tooling needed.
```

- [ ] **Step 3: Update the "See Also" section**

Add: `- [issuer](../../docs/components/issuer.md)`.

- [ ] **Step 4: Commit**

```bash
git add deploy/control-plane/README.md
git commit -m "docs(deploy): rewrite enrollment walkthrough, add enroll/connect/revoke smoke test"
```

---

### Task 8: Documentation

**Files:**
- Modify: `docs/components/issuer.md`
- Modify: `docs/ARCHITECTURE.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Update `docs/components/issuer.md`**

Add a subsection describing self-mint + internal refresh: `issuer` mints its own server certificate directly at startup (`--hostname`, `IssuerSelfCertTTLSec` default ~90 days) using the same CA provisioner access it already holds for `RequestOperatingCert`, then re-mints on an internal ticker (`IssuerSelfCertRefreshIntervalSec`, default daily) while running — no `certclient`, no `agent`, no second process on the CA host. A transient refresh failure logs and keeps the existing certificate; only a failure at startup (before any certificate exists) is fatal.

- [ ] **Step 2: Update `docs/ARCHITECTURE.md`**

Correct the Control Plane vs. Agents table's "Agent images bundle `certclient` only" cell — `catalog`'s image now also bundles `agent`, since `catalog` is deployed as an ordinary `agent`-managed enrolled node (see `deploy/control-plane/README.md`). Add a note that `issuer` obtains its own identity by self-minting, not via `certclient`.

- [ ] **Step 3: Add the `CHANGELOG.md` entry**

Add a dated entry summarizing: fixed `deploy/control-plane`'s docker-compose demo, broken by phase 2c's certclient/agent changes (`catalog` crash-looped); `issuer` now self-mints and self-refreshes its own server identity; `catalog` is now an ordinary `agent`-managed enrolled node with continuous (not restart-only) credential refresh; `client-manager`'s demo enrollment commands now persist to a real, shared database.

- [ ] **Step 4: Commit**

```bash
git add docs/components/issuer.md docs/ARCHITECTURE.md CHANGELOG.md
git commit -m "docs: document issuer self-mint and catalog's agent-managed deployment"
```

---

## Self-Review

**Spec coverage:**
- `issuer` self-mints its own identity, no new binary/daemon on the CA host → Tasks 1–3.
- Real-CA proof of self-mint → Task 4.
- `docker-compose.yml` gains `issuer`, `client-manager` gets a persistent volume → Task 5.
- `catalog` becomes an ordinary `agent`-managed enrolled node → Task 6.
- Enroll/connect/revoke smoke test, documented → Task 7.
- Teardown explicitly confirmed (already-generic `docker compose down`) → Task 7, Step 2.
- Documentation impact (`issuer.md`, `ARCHITECTURE.md`, `CHANGELOG.md`) → Task 8.
- Non-Goals (`bwfs`/`brfs`/`rwfs` unchanged, no demo-lab-environment rewrite, no HA) → correctly not covered by any task above.

**Placeholder scan:** No "TBD"/"TODO". Every code and config block is complete. Task 5/6/7's YAML/shell/Dockerfile content is given in full, not described abstractly.

**Type consistency:** `mintSelfIdentity`'s signature (Task 2: `mintSelfIdentity(hostname, certsDir, rootFile string, mint mintAndSignFunc, ttlSec int) error`) is used identically in Task 3's `main.go` wiring and Task 4's e2e test. `mintAndSignFunc` is the existing type from `server.go` (phase 2b), not redefined. The CSR's `DNSNames: []string{hostname}` requirement (established during phase 2c's own SAN-matching discovery) is applied consistently in Task 2's implementation, its own test assertions, and Task 4's e2e assertion — no call site omits it.

No gaps found.
