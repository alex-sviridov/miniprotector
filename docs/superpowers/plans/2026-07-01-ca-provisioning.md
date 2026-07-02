# CA Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the manual `step-ca` setup under `ca/` with a Docker Compose service, and add two new binaries — `certrequest` (admin-side token minting) and `certclient` (agent-side bootstrap/renew) — so nodes can obtain and refresh the mTLS identity that `common/mtls` already expects, instead of hand-provisioning certs via `docker exec`.

**Architecture:** `certrequest` is a control-plane CLI that calls `github.com/smallstep/certificates/ca`'s `NewProvisioner`/`Provisioner.Token` against a running CA to mint a one-time enrollment token. `certclient` is an agent-side CLI: if no identity exists in the certs directory it bootstraps one from a token (`ca.Bootstrap` + `ca.CreateSignRequest` + `Client.Sign`), otherwise it renews the existing one (`Client.Renew` over a hand-built mTLS `http.RoundTripper`, reusing the existing key). Both binaries' network-touching calls are hidden behind small `signer`/`renewer` interfaces so the file-writing and orchestration logic is unit-testable without a live CA.

**Tech Stack:** Go 1.26, `github.com/smallstep/certificates/ca` v0.30.2 (+ transitive `go.step.sm/crypto`, `github.com/smallstep/cli-utils`), `github.com/spf13/cobra` (already a dependency), `github.com/stretchr/testify`, Docker Compose for `ca/`.

**Spec:** `docs/superpowers/specs/2026-07-01-ca-provisioning-design.md`

## Global Constraints

- Every node's identity is still exactly the three files `common/mtls` already reads: `ca.crt`, `client.crt`, `client.key` in the certs directory from `config.ResolveCertsDir()`.
- `certclient` is an **agent** tool — bundled onto every `bwfs`/`brfs`/`rwfs` host. `certrequest` is a **control-plane** tool — run only on/near the CA host, never on an agent host or in an agent Docker image.
- Token sources for `certclient` bootstrap, in preference order: `--token` flag (least safe — visible via `ps`), `MP_CERT_TOKEN` env var, stdin prompt.
- `certclient` renew always renews when invoked (no expiry check) and reuses the existing private key — only `client.crt` is rewritten on renew.
- `certrequest` mints tokens by calling the live CA's stock `/provisioners` endpoints (not by parsing `ca.json` directly) — this requires `--ca-url` and network reachability to the CA, corrected from an earlier draft of the spec that assumed a fully offline flow.
- No CN/SAN allowlist, no expiry-based renewal policy — out of scope, per the spec.
- `client.key` is written with `0600` permissions. The provisioner's decrypted private key inside `certrequest` is held only in memory, never written to disk.

---

### Task 1: `ca/` Docker Compose packaging

**Files:**
- Create: `ca/docker-compose.yml`
- Create: `ca/entrypoint.sh`
- Delete: `ca/init.sh`
- Delete: `ca/start.sh`
- Modify: `ca/README.md`

**Interfaces:**
- Consumes: nothing (this task has no Go code dependencies).
- Produces: a running `step-ca` container reachable at `localhost:9000` once `ca/data/secrets/password` exists and `docker compose up -d` has been run. Later tasks (`certrequest`, `certclient`) assume this is how the CA is deployed, and that `ca/data/certs/root_ca.crt` and `ca/data/config/defaults.json` exist on the host running `certrequest`.

- [ ] **Step 1: Write `ca/entrypoint.sh`**

```bash
#!/bin/sh
set -e
if [ ! -f /home/step/config/ca.json ]; then
  step ca init --deployment-type=standalone \
    --name="Enterprise Backup Cluster CA" \
    --dns="ca.backup.internal,localhost" \
    --address=":9000" \
    --provisioner="admin@backup.internal" \
    --password-file=/home/step/secrets/password
fi
exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
```

```bash
chmod +x /home/alex/miniprotector/ca/entrypoint.sh
```

- [ ] **Step 2: Write `ca/docker-compose.yml`**

`entrypoint.sh` lives at the top of `ca/` (not inside the gitignored `ca/data/`), so it needs its
own bind mount alongside the data volume:

```yaml
services:
  step-ca:
    image: smallstep/step-ca
    volumes:
      - ./data:/home/step
      - ./entrypoint.sh:/home/step/entrypoint.sh:ro
    ports:
      - "9000:9000"
    entrypoint: ["/home/step/entrypoint.sh"]
    restart: unless-stopped
```

- [ ] **Step 3: Remove the old scripts**

```bash
git -C /home/alex/miniprotector rm ca/init.sh ca/start.sh
```

- [ ] **Step 4: Rewrite `ca/README.md`**

Replace its contents entirely with:

```markdown
# Enterprise Backup Cluster CA

A `step-ca` instance issuing mTLS identities for miniprotector nodes.

## First-time setup

Generate the provisioner password once, before the first `docker compose up` (it can't be
automated away without either committing a secret or inventing a new secret-distribution
mechanism):

```bash
mkdir -p data/secrets
openssl rand -base64 32 > data/secrets/password
```

## Running

```bash
docker compose up -d
```

Idempotent: the entrypoint only runs `step ca init` if `data/config/ca.json` doesn't already
exist, so re-running `docker compose up -d` after the first time just (re)starts the server.

## Enrolling a node

On (or near) the CA host, using the `ca/data/certs/root_ca.crt` and `ca/data/secrets/password`
this compose setup produces:

```bash
certrequest node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
```

This prints a one-time enrollment token. Relay it to the target node out-of-band (SSH, etc.) as
the `MP_CERT_TOKEN` environment variable, then on that node:

```bash
MP_CERT_TOKEN=<token> certclient
```

Re-running `certclient` on a node that already has an identity renews it instead (no token
needed — renewal is authenticated with the existing certificate).

## Viewing an issued certificate

```bash
openssl x509 -in <certs-dir>/client.crt -text -noout
```

## See Also

- [certrequest](../docs/components/certrequest.md)
- [certclient](../docs/components/certclient.md)
- [Architecture](../docs/ARCHITECTURE.md)
```

- [ ] **Step 5: Validate the compose file syntax**

Run: `cd /home/alex/miniprotector/ca && docker compose config`
Expected: prints the resolved compose configuration with no errors. If Docker isn't available in
this environment, note that and skip — this step only validates YAML/schema, it doesn't require
pulling the image or a running daemon.

- [ ] **Step 6: Commit**

```bash
cd /home/alex/miniprotector
git add ca/docker-compose.yml ca/entrypoint.sh ca/README.md
git add ca/init.sh ca/start.sh  # stages the deletions
git commit -m "chore(ca): replace manual init.sh/start.sh with docker compose"
```

---

### Task 2: `config` package — `ca_host`

**Files:**
- Modify: `src/common/config/config.go`
- Modify: `src/common/config/config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Config.CAHost string`, populated from a new `ca_host=...` key in `local.conf`. Consumed by `certclient` (Task 7) via `config.GetConfigFromContext`/`ParseConfig`. Not required — absent from `requiredFields`.

- [ ] **Step 1: Write the failing test**

Add to `src/common/config/config_test.go`:

```go
func TestParseConfig_CAHostOptional(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	require.NoError(t, os.WriteFile(path, []byte("default_port=8080\ndefault_streams=4\nlogfolder=/tmp\n"), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "", conf.CAHost)
}

func TestParseConfig_CAHostParsed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local.conf")
	content := "default_port=8080\ndefault_streams=4\nlogfolder=/tmp\nca_host=ca.backup.internal:9000\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))

	conf, err := ParseConfig(path)
	require.NoError(t, err)
	assert.Equal(t, "ca.backup.internal:9000", conf.CAHost)
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./common/config/... -v -run TestParseConfig_CAHost`
Expected: FAIL — `conf.CAHost undefined` (field doesn't exist yet).

- [ ] **Step 3: Add the field and parsing case**

In `src/common/config/config.go`, add `CAHost` to the `Config` struct:

```go
// Config holds configuration from /etc/btool/local.conf
type Config struct {
	DefaultPort              int
	DefaultStreams           int
	LogFolder                string
	ClientHashQueryBatchSize int
	ConnectionTimeOutSec     int
	FileLockTimeoutSec       int
	StopStreamOnFileError    bool
	CAHost                   string
}
```

Add a case to the `switch key` block in `ParseConfig`, alongside the existing `case "logfolder":`:

```go
		case "ca_host":
			config.CAHost = value
			foundFields["ca_host"] = true
```

Do **not** add `"ca_host"` to `requiredFields` — it stays optional.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./common/config/... -v`
Expected: PASS, including the two new tests and all pre-existing ones.

- [ ] **Step 5: Commit**

```bash
git add src/common/config/config.go src/common/config/config_test.go
git commit -m "feat(config): add optional ca_host setting"
```

---

### Task 3: Add the smallstep client dependency

**Files:**
- Modify: `src/go.mod`
- Modify: `src/go.sum`

**Interfaces:**
- Consumes: nothing.
- Produces: `github.com/smallstep/certificates/ca` and `github.com/smallstep/certificates/api` importable from `src/cmd/certclient` and `src/cmd/certrequest` (Tasks 4-9), and `github.com/smallstep/cli-utils/token`/`token/provision` + `go.step.sm/crypto/x509util` importable from `certclient`'s tests (Task 4).

- [ ] **Step 1: Add the dependency**

```bash
cd /home/alex/miniprotector/src
go get github.com/smallstep/certificates/ca@v0.30.2
go get github.com/smallstep/cli-utils/token@v0.12.2
go get go.step.sm/crypto/x509util@v0.77.1
go mod tidy
```

- [ ] **Step 2: Confirm the module builds**

Run: `cd src && go build ./...`
Expected: no errors (nothing imports the new packages yet, so this just confirms `go.mod`/`go.sum`
resolved cleanly).

- [ ] **Step 3: Commit**

```bash
git add src/go.mod src/go.sum
git commit -m "chore: add github.com/smallstep/certificates/ca dependency"
```

---

### Task 4: `certclient` — bootstrap (identity writer + orchestration)

**Files:**
- Create: `src/cmd/certclient/bootstrap.go`
- Create: `src/cmd/certclient/bootstrap_test.go`

**Interfaces:**
- Consumes: `github.com/smallstep/certificates/ca` (`ca.CreateSignRequest`, `ca.Certificate`, `ca.IntermediateCertificate`, `ca.RootCertificate`), `github.com/smallstep/certificates/api` (`api.SignRequest`, `api.SignResponse`) (Task 3).
- Produces: `type signer interface { Sign(req *api.SignRequest) (*api.SignResponse, error) }` (satisfied structurally by `*ca.Client`), `func bootstrap(token string, client signer, certsDir string) error`. Task 7's `main.go` calls `bootstrap(tok, realClient, certsDir)` where `realClient` comes from `ca.Bootstrap(tok)`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/certclient/bootstrap_test.go`:

```go
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/smallstep/certificates/api"
	"github.com/smallstep/cli-utils/token"
	"github.com/smallstep/cli-utils/token/provision"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.step.sm/crypto/x509util"
)

const fixtureCertsDir = "../../common/testdata/certs"

// loadFixtureCert parses a PEM-encoded certificate file. Used to stand in
// for root/leaf/intermediate certs in tests — these fixtures don't need to
// chain to each other, since writeIdentity only re-serializes whatever
// *x509.Certificate values it's given.
func loadFixtureCert(t *testing.T, name string) *x509.Certificate {
	t.Helper()
	pemBytes, err := os.ReadFile(filepath.Join(fixtureCertsDir, name))
	require.NoError(t, err)
	block, _ := pem.Decode(pemBytes)
	require.NotNil(t, block, "no PEM block found in %s", name)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	return cert
}

// makeTestToken builds a real, validly-signed enrollment token using the same
// library certrequest uses, so ca.CreateSignRequest (a real, unmocked call)
// accepts it.
func makeTestToken(t *testing.T, subject string, sans []string, root *x509.Certificate) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tok, err := provision.New(subject,
		token.WithJWTID("test-jti"),
		token.WithIssuer("admin@backup.internal"),
		token.WithAudience("https://ca.internal/1.0/sign"),
		token.WithValidity(time.Now(), time.Now().Add(5*time.Minute)),
		token.WithSANS(sans),
		token.WithSHA(x509util.Fingerprint(root)),
	)
	require.NoError(t, err)

	signed, err := tok.SignedString("ES256", key)
	require.NoError(t, err)
	return signed
}

type fakeSigner struct {
	resp *api.SignResponse
	err  error
}

func (f *fakeSigner) Sign(_ *api.SignRequest) (*api.SignResponse, error) {
	return f.resp, f.err
}

func fakeSignResponse(root, leaf, intermediate *x509.Certificate) *api.SignResponse {
	return &api.SignResponse{
		ServerPEM: api.Certificate{Certificate: leaf},
		CaPEM:     api.Certificate{Certificate: intermediate},
		TLS: &tls.ConnectionState{
			VerifiedChains: [][]*x509.Certificate{{leaf, intermediate, root}},
		},
	}
}

func TestBootstrap_WritesIdentityFiles(t *testing.T) {
	root := loadFixtureCert(t, "ca.crt")
	leaf := loadFixtureCert(t, "client.crt")

	tok := makeTestToken(t, "test-host", []string{"test-host"}, root)
	signer := &fakeSigner{resp: fakeSignResponse(root, leaf, leaf)}
	certsDir := t.TempDir()

	err := bootstrap(tok, signer, certsDir)
	require.NoError(t, err)

	for _, name := range []string{"ca.crt", "client.crt", "client.key"} {
		info, err := os.Stat(filepath.Join(certsDir, name))
		require.NoError(t, err, "expected %s to exist", name)
		if name == "client.key" {
			assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		}
	}
}

func TestBootstrap_SignErrorPropagates(t *testing.T) {
	root := loadFixtureCert(t, "ca.crt")
	tok := makeTestToken(t, "test-host", []string{"test-host"}, root)
	signer := &fakeSigner{err: assert.AnError}
	certsDir := t.TempDir()

	err := bootstrap(tok, signer, certsDir)
	assert.Error(t, err)
	_, statErr := os.Stat(filepath.Join(certsDir, "client.crt"))
	assert.True(t, os.IsNotExist(statErr), "client.crt should not be written on sign failure")
}

func TestBootstrap_InvalidTokenErrors(t *testing.T) {
	certsDir := t.TempDir()
	err := bootstrap("not-a-real-token", &fakeSigner{}, certsDir)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./cmd/certclient/... -v`
Expected: FAIL — `undefined: bootstrap` (package doesn't have the implementation yet).

- [ ] **Step 3: Implement `src/cmd/certclient/bootstrap.go`**

```go
// Package main implements certclient, which bootstraps or renews this
// node's mTLS identity from the CA.
package main

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"

	"github.com/smallstep/certificates/api"
	"github.com/smallstep/certificates/ca"
)

// signer is satisfied by *ca.Client. Isolating it lets bootstrap be unit
// tested without a live CA connection.
type signer interface {
	Sign(req *api.SignRequest) (*api.SignResponse, error)
}

// bootstrap exchanges an enrollment token for a signed identity via client,
// writing ca.crt, client.crt, and client.key into certsDir.
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

// writeIdentity writes the root, leaf+intermediate chain, and private key
// from a sign response to certsDir. Pure and independently testable — no
// network calls.
func writeIdentity(certsDir string, sign *api.SignResponse, pk crypto.PrivateKey) error {
	root, err := ca.RootCertificate(sign)
	if err != nil {
		return fmt.Errorf("extract root certificate: %w", err)
	}
	leaf, err := ca.Certificate(sign)
	if err != nil {
		return fmt.Errorf("extract leaf certificate: %w", err)
	}
	intermediate, err := ca.IntermediateCertificate(sign)
	if err != nil {
		return fmt.Errorf("extract intermediate certificate: %w", err)
	}
	ecdsaKey, ok := pk.(*ecdsa.PrivateKey)
	if !ok {
		return fmt.Errorf("unexpected private key type %T", pk)
	}
	keyDER, err := x509.MarshalECPrivateKey(ecdsaKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	if err := os.MkdirAll(certsDir, 0o700); err != nil {
		return fmt.Errorf("create certs dir: %w", err)
	}

	chain := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leaf.Raw}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: intermediate.Raw})...,
	)
	if err := os.WriteFile(filepath.Join(certsDir, "client.crt"), chain, 0o644); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}

	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: root.Raw})
	if err := os.WriteFile(filepath.Join(certsDir, "ca.crt"), rootPEM, 0o644); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(filepath.Join(certsDir, "client.key"), keyPEM, 0o600); err != nil {
		return fmt.Errorf("write client.key: %w", err)
	}

	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/certclient/... -v`
Expected: PASS — all three tests green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/certclient/bootstrap.go src/cmd/certclient/bootstrap_test.go
git commit -m "feat(certclient): bootstrap identity from an enrollment token"
```

---

### Task 5: `certclient` — renew

**Files:**
- Create: `src/cmd/certclient/renew.go`
- Create: `src/cmd/certclient/renew_test.go`

**Interfaces:**
- Consumes: fixture certs at `../../common/testdata/certs` (already committed, used by the mTLS package's own tests).
- Produces: `type renewer interface { Renew(tr http.RoundTripper) (*api.SignResponse, error) }` (satisfied structurally by `*ca.Client`), `func renew(client renewer, certsDir string) error`. Task 7's `main.go` calls `renew(realClient, certsDir)` where `realClient` comes from `ca.NewClient(...)`.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/certclient/renew_test.go`:

```go
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/smallstep/certificates/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeRenewer struct {
	resp *api.SignResponse
	err  error
}

func (f *fakeRenewer) Renew(_ http.RoundTripper) (*api.SignResponse, error) {
	return f.resp, f.err
}

func setupExistingIdentity(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"ca.crt", "client.crt", "client.key"} {
		data, err := os.ReadFile(filepath.Join(fixtureCertsDir, name))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
	}
	return dir
}

func TestRenew_OverwritesClientCrt(t *testing.T) {
	certsDir := setupExistingIdentity(t)
	leaf := loadFixtureCert(t, "client.crt")

	renewer := &fakeRenewer{resp: &api.SignResponse{
		ServerPEM: api.Certificate{Certificate: leaf},
		CaPEM:     api.Certificate{Certificate: leaf},
	}}

	err := renew(renewer, certsDir)
	require.NoError(t, err)

	got, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	assert.NotEmpty(t, got)
}

func TestRenew_ErrorPropagates(t *testing.T) {
	certsDir := setupExistingIdentity(t)
	renewer := &fakeRenewer{err: assert.AnError}

	err := renew(renewer, certsDir)
	assert.Error(t, err)
}

func TestRenew_MissingExistingCertErrors(t *testing.T) {
	certsDir := t.TempDir() // no existing identity files
	renewer := &fakeRenewer{}

	err := renew(renewer, certsDir)
	assert.Error(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./cmd/certclient/... -v -run TestRenew`
Expected: FAIL — `undefined: renew`.

- [ ] **Step 3: Implement `src/cmd/certclient/renew.go`**

```go
package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/smallstep/certificates/api"
	"github.com/smallstep/certificates/ca"
)

// renewer is satisfied by *ca.Client. Isolating it lets renew be unit
// tested without a live CA connection.
type renewer interface {
	Renew(tr http.RoundTripper) (*api.SignResponse, error)
}

// renew re-authenticates with the existing identity in certsDir and
// overwrites client.crt with a freshly renewed certificate for the same
// key pair. ca.crt and client.key are left untouched — step-ca's renewal
// semantics re-sign the same key, and root rotation is out of scope here
// (a fresh bootstrap handles that rare case).
func renew(client renewer, certsDir string) error {
	cert, err := tls.LoadX509KeyPair(
		filepath.Join(certsDir, "client.crt"),
		filepath.Join(certsDir, "client.key"),
	)
	if err != nil {
		return fmt.Errorf("load existing identity: %w", err)
	}

	caPEM, err := os.ReadFile(filepath.Join(certsDir, "ca.crt"))
	if err != nil {
		return fmt.Errorf("read ca.crt: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return fmt.Errorf("parse ca.crt: no valid certificates found")
	}

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      pool,
		},
	}

	sign, err := client.Renew(tr)
	if err != nil {
		return fmt.Errorf("renew request: %w", err)
	}

	return writeRenewedCert(certsDir, sign)
}

func writeRenewedCert(certsDir string, sign *api.SignResponse) error {
	leaf, err := ca.Certificate(sign)
	if err != nil {
		return fmt.Errorf("extract leaf certificate: %w", err)
	}
	intermediate, err := ca.IntermediateCertificate(sign)
	if err != nil {
		return fmt.Errorf("extract intermediate certificate: %w", err)
	}

	chain := append(
		pemCert(leaf),
		pemCert(intermediate)...,
	)
	if err := os.WriteFile(filepath.Join(certsDir, "client.crt"), chain, 0o644); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}
	return nil
}
```

`pemCert` is a small helper shared by both files — add it to `bootstrap.go` (replacing the two
inline `pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", ...})` calls there with calls to it) so
it isn't duplicated:

```go
func pemCert(cert *x509.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
}
```

Update `writeIdentity` in `bootstrap.go` to use it:

```go
	chain := append(pemCert(leaf), pemCert(intermediate)...)
	if err := os.WriteFile(filepath.Join(certsDir, "client.crt"), chain, 0o644); err != nil {
		return fmt.Errorf("write client.crt: %w", err)
	}

	rootPEM := pemCert(root)
	if err := os.WriteFile(filepath.Join(certsDir, "ca.crt"), rootPEM, 0o644); err != nil {
		return fmt.Errorf("write ca.crt: %w", err)
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/certclient/... -v`
Expected: PASS — all tests in the package green, including Task 4's.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/certclient/renew.go src/cmd/certclient/renew_test.go src/cmd/certclient/bootstrap.go
git commit -m "feat(certclient): renew an existing identity via mTLS"
```

---

### Task 6: `certclient` — token resolution and identity-detection helpers

**Files:**
- Create: `src/cmd/certclient/token.go`
- Create: `src/cmd/certclient/token_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func hasExistingIdentity(certsDir string) bool`, `func resolveToken(flagValue string, stdin io.Reader) (string, error)`. Task 7's `main.go` calls both.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/certclient/token_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHasExistingIdentity_AllFilesPresent(t *testing.T) {
	dir := setupExistingIdentity(t)
	assert.True(t, hasExistingIdentity(dir))
}

func TestHasExistingIdentity_MissingFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "client.crt"), []byte("x"), 0o644))
	// client.key intentionally missing
	assert.False(t, hasExistingIdentity(dir))
}

func TestResolveToken_FlagTakesPriority(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "env-token")
	got, err := resolveToken("flag-token", strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "flag-token", got)
}

func TestResolveToken_EnvVarUsedWhenFlagEmpty(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "env-token")
	got, err := resolveToken("", strings.NewReader(""))
	require.NoError(t, err)
	assert.Equal(t, "env-token", got)
}

func TestResolveToken_FallsBackToStdin(t *testing.T) {
	t.Setenv("MP_CERT_TOKEN", "")
	got, err := resolveToken("", strings.NewReader("stdin-token\n"))
	require.NoError(t, err)
	assert.Equal(t, "stdin-token", got)
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./cmd/certclient/... -v -run 'TestHasExistingIdentity|TestResolveToken'`
Expected: FAIL — `undefined: hasExistingIdentity`.

- [ ] **Step 3: Implement `src/cmd/certclient/token.go`**

```go
package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// hasExistingIdentity reports whether all three identity files are already
// present in certsDir.
func hasExistingIdentity(certsDir string) bool {
	for _, name := range []string{"ca.crt", "client.crt", "client.key"} {
		if _, err := os.Stat(filepath.Join(certsDir, name)); err != nil {
			return false
		}
	}
	return true
}

// resolveToken returns the enrollment token from, in preference order: the
// --token flag (least safe — visible via process listings), MP_CERT_TOKEN,
// or a line read from stdin.
func resolveToken(flagValue string, stdin io.Reader) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if env := os.Getenv("MP_CERT_TOKEN"); env != "" {
		return env, nil
	}
	fmt.Fprint(os.Stderr, "Enter enrollment token: ")
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read token from stdin: %w", err)
	}
	token := strings.TrimSpace(line)
	if token == "" {
		return "", fmt.Errorf("no token provided via --token, MP_CERT_TOKEN, or stdin")
	}
	return token, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/certclient/... -v`
Expected: PASS — all tests in the package green.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/certclient/token.go src/cmd/certclient/token_test.go
git commit -m "feat(certclient): resolve enrollment token from flag, env, or stdin"
```

---

### Task 7: `certclient` — main wiring

**Files:**
- Create: `src/cmd/certclient/arguments.go`
- Create: `src/cmd/certclient/main.go`

**Interfaces:**
- Consumes: `config.ResolveConfigPath`, `config.ParseConfig`, `config.ResolveCertsDir`, `Config.CAHost` (Task 2); `bootstrap`, `signer` (Task 4); `renew`, `renewer` (Task 5); `hasExistingIdentity`, `resolveToken` (Task 6); `ca.Bootstrap`, `ca.NewClient` (`github.com/smallstep/certificates/ca`, Task 3).
- Produces: the `certclient` binary. Nothing later depends on this task's internals.

- [ ] **Step 1: Implement `src/cmd/certclient/arguments.go`**

```go
package main

import "github.com/spf13/cobra"

// Arguments holds parsed command line arguments.
type Arguments struct {
	Token string
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}
	cmd := &cobra.Command{
		Use:   "certclient",
		Short: "Bootstrap or renew this node's mTLS identity from the CA",
		Args:  cobra.NoArgs,
		Run:   func(cmd *cobra.Command, _ []string) {},
	}
	cmd.Flags().StringVar(&args.Token, "token", "",
		"Enrollment token for first-time bootstrap (prefer MP_CERT_TOKEN or the stdin prompt over this flag on shared hosts)")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	return args, nil
}
```

- [ ] **Step 2: Implement `src/cmd/certclient/main.go`**

```go
// certclient bootstraps or renews this node's mTLS identity from the CA.
package main

import (
	"fmt"
	"os"

	"github.com/alex-sviridov/miniprotector/common/config"
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
	if conf.CAHost == "" {
		fmt.Fprintln(os.Stderr, "Configuration error: ca_host not set in local.conf")
		os.Exit(1)
	}

	certsDir, err := config.ResolveCertsDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Certs directory resolution failed: %v\n", err)
		os.Exit(1)
	}

	if hasExistingIdentity(certsDir) {
		client, err := ca.NewClient(fmt.Sprintf("https://%s", conf.CAHost))
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create CA client: %v\n", err)
			os.Exit(1)
		}
		if err := renew(client, certsDir); err != nil {
			fmt.Fprintf(os.Stderr, "Renew failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Identity renewed in", certsDir)
		return
	}

	tok, err := resolveToken(args.Token, os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Token error: %v\n", err)
		os.Exit(1)
	}

	client, err := ca.Bootstrap(tok)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	if err := bootstrap(tok, client, certsDir); err != nil {
		fmt.Fprintf(os.Stderr, "Bootstrap failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Identity bootstrapped in", certsDir)
}
```

- [ ] **Step 3: Build to confirm it compiles**

Run: `cd src && go build ./cmd/certclient/...`
Expected: no errors. (`*ca.Client` from both `ca.NewClient` and `ca.Bootstrap` must satisfy the
`renewer`/`signer` interfaces respectively — a compile failure here means one of those interfaces'
method signatures doesn't match `*ca.Client`'s actual methods; re-check against
`$(go env GOMODCACHE)/github.com/smallstep/certificates@v0.30.2/ca/client.go`.)

- [ ] **Step 4: Run the full package test suite**

Run: `cd src && go test ./cmd/certclient/... -v`
Expected: PASS — everything from Tasks 4-6 plus this task's compile check.

- [ ] **Step 5: Commit**

```bash
git add src/cmd/certclient/arguments.go src/cmd/certclient/main.go
git commit -m "feat(certclient): wire bootstrap/renew into a CLI"
```

---

### Task 8: `certrequest`

**Files:**
- Create: `src/cmd/certrequest/arguments.go`
- Create: `src/cmd/certrequest/arguments_test.go`
- Create: `src/cmd/certrequest/main.go`

**Interfaces:**
- Consumes: `ca.NewProvisioner`, `Provisioner.Token` (`github.com/smallstep/certificates/ca`, Task 3).
- Produces: the `certrequest` binary. Nothing later depends on this task's internals.

- [ ] **Step 1: Write the failing tests**

Create `src/cmd/certrequest/arguments_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

func TestParseArguments_MissingHostnameErrors(t *testing.T) {
	withArgs(t, []string{"certrequest", "--ca-url", "https://localhost:9000"}, func() {
		_, err := parseArguments()
		assert.Error(t, err)
	})
}

func TestParseArguments_ExplicitCAURLUsed(t *testing.T) {
	withArgs(t, []string{"certrequest", "node1", "--ca-url", "https://localhost:9000"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "node1", args.Hostname)
		assert.Equal(t, "https://localhost:9000", args.CAURL)
	})
}

func TestParseArguments_SANsAccumulate(t *testing.T) {
	withArgs(t, []string{"certrequest", "node1", "--ca-url", "https://localhost:9000", "--san", "a.internal", "--san", "b.internal"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, []string{"a.internal", "b.internal"}, args.SANs)
	})
}

func TestParseArguments_MissingCAURLFallsBackToDefaultsFile(t *testing.T) {
	dir := t.TempDir()
	defaultsPath := filepath.Join(dir, "defaults.json")
	require.NoError(t, os.WriteFile(defaultsPath, []byte(`{"ca-url": "https://ca.backup.internal:9000"}`), 0o644))

	withArgs(t, []string{"certrequest", "node1", "--defaults-file", defaultsPath}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "https://ca.backup.internal:9000", args.CAURL)
	})
}

func TestParseArguments_MissingCAURLAndDefaultsFileErrors(t *testing.T) {
	withArgs(t, []string{"certrequest", "node1", "--defaults-file", "/nonexistent/defaults.json"}, func() {
		_, err := parseArguments()
		assert.Error(t, err)
	})
}

func TestReadDefaultCAURL_ParsesField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "defaults.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"ca-url": "https://example:9000", "root": "/x"}`), 0o644))

	got, err := readDefaultCAURL(path)
	require.NoError(t, err)
	assert.Equal(t, "https://example:9000", got)
}
```

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `cd src && go test ./cmd/certrequest/... -v`
Expected: FAIL — `undefined: parseArguments` (package doesn't exist yet).

- [ ] **Step 3: Implement `src/cmd/certrequest/arguments.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Arguments holds parsed command line arguments.
type Arguments struct {
	Hostname     string
	SANs         []string
	CAURL        string
	RootFile     string
	Provisioner  string
	PasswordFile string
}

func parseArguments() (*Arguments, error) {
	args := &Arguments{}
	var caURLFlag, defaultsFile string

	cmd := &cobra.Command{
		Use:   "certrequest <hostname>",
		Short: "Mint a one-time enrollment token for a node",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, cliArgs []string) {
			args.Hostname = cliArgs[0]
		},
	}
	cmd.Flags().StringArrayVar(&args.SANs, "san", nil, "Additional SAN alias for the token (repeatable)")
	cmd.Flags().StringVar(&caURLFlag, "ca-url", "", "CA URL, e.g. https://localhost:9000 (default: read from --defaults-file)")
	cmd.Flags().StringVar(&defaultsFile, "defaults-file", "ca/data/config/defaults.json", "Path to step-ca's defaults.json, used to default --ca-url")
	cmd.Flags().StringVar(&args.RootFile, "root", "ca/data/certs/root_ca.crt", "Path to the CA's root certificate")
	cmd.Flags().StringVar(&args.Provisioner, "provisioner", "admin@backup.internal", "Provisioner name")
	cmd.Flags().StringVar(&args.PasswordFile, "password-file", "ca/data/secrets/password", "Path to the provisioner password file")

	if err := cmd.Execute(); err != nil {
		return nil, err
	}
	if args.Hostname == "" {
		return nil, fmt.Errorf("hostname is required")
	}

	args.CAURL = caURLFlag
	if args.CAURL == "" {
		defaultURL, err := readDefaultCAURL(defaultsFile)
		if err != nil {
			return nil, fmt.Errorf("--ca-url not given and could not be read from %s: %w", defaultsFile, err)
		}
		args.CAURL = defaultURL
	}

	return args, nil
}

// readDefaultCAURL reads the "ca-url" field out of step-ca's defaults.json.
func readDefaultCAURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var defaults struct {
		CAURL string `json:"ca-url"`
	}
	if err := json.Unmarshal(data, &defaults); err != nil {
		return "", err
	}
	if defaults.CAURL == "" {
		return "", fmt.Errorf("%s has no ca-url field", path)
	}
	return defaults.CAURL, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd src && go test ./cmd/certrequest/... -v`
Expected: PASS — all six tests green.

- [ ] **Step 5: Implement `src/cmd/certrequest/main.go`**

```go
// certrequest mints a one-time enrollment token for a node, run on or near
// the CA host.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/smallstep/certificates/ca"
)

func main() {
	args, err := parseArguments()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Arguments error: %v\n", err)
		os.Exit(1)
	}

	passwordBytes, err := os.ReadFile(args.PasswordFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read password file: %v\n", err)
		os.Exit(1)
	}
	password := []byte(strings.TrimSpace(string(passwordBytes)))

	provisioner, err := ca.NewProvisioner(args.Provisioner, "", args.CAURL, password, ca.WithRootFile(args.RootFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load provisioner: %v\n", err)
		os.Exit(1)
	}

	sans := append([]string{args.Hostname}, args.SANs...)
	token, err := provisioner.Token(args.Hostname, sans...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to mint token: %v\n", err)
		os.Exit(1)
	}

	fmt.Println(token)
}
```

- [ ] **Step 6: Build to confirm it compiles**

Run: `cd src && go build ./cmd/certrequest/...`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add src/cmd/certrequest/arguments.go src/cmd/certrequest/arguments_test.go src/cmd/certrequest/main.go
git commit -m "feat(certrequest): mint enrollment tokens against a live CA"
```

---

### Task 9: Makefile build targets

**Files:**
- Modify: `Makefile`

**Interfaces:**
- Consumes: `src/cmd/certclient`, `src/cmd/certrequest` (Tasks 7-8).
- Produces: `make certclient`, `make certrequest` targets; `make build` already picks up both via the existing `$(BINARIES) := $(notdir $(wildcard src/cmd/*))` wildcard, so this task only adds explicit named targets for consistency with `brfs`/`bwfs`/`rwfs`, matching how each of those already has one.

- [ ] **Step 1: Add the two targets**

In `Makefile`, after the existing `rwfs:` target block, add:

```makefile
certrequest: $(BINARY_DIR) ## Build certrequest binary
	@printf "$(BLUE)Building certrequest...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/certrequest ./cmd/certrequest
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/certrequest"

certclient: $(BINARY_DIR) ## Build certclient binary
	@printf "$(BLUE)Building certclient...$(NC) "
	@cd src && CGO_ENABLED=$(CGO_ENABLED) GOOS=$(GOOS) GOARCH=$(GOARCH) \
		$(GO) build $(BUILDFLAGS) $(LDFLAGS) -o ../$(BINARY_DIR)/certclient ./cmd/certclient
	@echo -e "$(GREEN)Built successfully:$(NC)$(BINARY_DIR)/certclient"
```

Also add `certrequest certclient` to the `.PHONY` line near the top:

```makefile
.PHONY: all build clean proto check-deps help brfs bwfs rwfs certrequest certclient test test-e2e lint
```

- [ ] **Step 2: Verify `make build` picks up both binaries**

Run: `cd /home/alex/miniprotector && make build`
Expected: output includes `Built successfully: bin/certrequest` and `Built successfully:
bin/certclient` alongside `brfs`/`bwfs`/`rwfs`.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "chore(build): add certrequest/certclient Makefile targets"
```

---

### Task 10: Documentation

**Files:**
- Create: `docs/components/certrequest.md`
- Create: `docs/components/certclient.md`
- Modify: `README.md`
- Modify: `docs/ARCHITECTURE.md`

**Interfaces:** none — documentation only.

- [ ] **Step 1: Create `docs/components/certrequest.md`**

```markdown
# certrequest

Mints a one-time enrollment token for a node, so it can bootstrap an mTLS identity via
`certclient`. **Control-plane tool** — run on or near the CA host; never deployed onto an agent
host or bundled into an agent Docker image.

## Usage

```
certrequest <hostname> [--san alias]... [--ca-url url] [--root path] [--provisioner name] [--password-file path]
```

```bash
certrequest node-east-01 --san node-east-01.internal --ca-url https://localhost:9000
```

Prints the token to stdout. Relay it to the target node out-of-band (SSH, etc.) as the
`MP_CERT_TOKEN` environment variable for `certclient`.

| Flag | Default | Description |
|------|---------|-------------|
| `--san` | | Additional SAN alias for the token (repeatable) |
| `--ca-url` | read from `--defaults-file` | CA URL, e.g. `https://localhost:9000` |
| `--defaults-file` | `ca/data/config/defaults.json` | Used to default `--ca-url` when it isn't given explicitly |
| `--root` | `ca/data/certs/root_ca.crt` | Path to the CA's root certificate, used to trust the connection to the CA |
| `--provisioner` | `admin@backup.internal` | Provisioner name |
| `--password-file` | `ca/data/secrets/password` | Path to the provisioner password file |

## How it works

Minting a token requires the CA to be reachable: `certrequest` fetches the named provisioner's
encrypted key from the CA over HTTPS (`GET /provisioners`, `GET /provisioners/{kid}/encrypted-key`
— stock step-ca endpoints, no new server-side surface), decrypts it locally with the password, and
signs the token locally. The decrypted key never touches disk. Token validity/SAN authorization is
bounded by the provisioner's own claims (`minTL`/`maxTTL`/`defaultTTL`) configured on the CA.

Anyone able to run `certrequest` with network access to the CA and the provisioner password has
full token-minting authority for any hostname — equivalent to CA-admin privilege. This is why
`certrequest` stays a control-plane-only tool.

## See Also

- [certclient](./certclient.md) — redeems the token this mints
- [ca/ step-ca setup](../../ca/README.md)
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 2: Create `docs/components/certclient.md`**

```markdown
# certclient

Bootstraps or renews this node's mTLS identity from the CA, populating the certs directory that
`bwfs`/`brfs`/`rwfs` read via `common/mtls` (`ca.crt`, `client.crt`, `client.key` under
`MP_CONFIG_PATH/certs`). **Agent tool** — bundled onto every node also running
`bwfs`/`brfs`/`rwfs`.

## Usage

```bash
certclient
MP_CERT_TOKEN=<token> certclient
certclient --token <token>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--token` | | Enrollment token for first-time bootstrap. Least safe of the three sources (visible via `ps`) — prefer `MP_CERT_TOKEN` or the stdin prompt on shared hosts |

Requires `ca_host` set in `local.conf` (the CA's `host:port`).

## Behavior

- **No identity present** (`ca.crt`/`client.crt`/`client.key` missing from the certs dir):
  bootstraps a new one. Gets a token from `--token`, then `MP_CERT_TOKEN`, then an interactive
  stdin prompt, in that order. Trust in the CA is established from the token's embedded root
  fingerprint claim (no separately-distributed root cert needed for this step).
- **Identity already present**: renews it via the CA's mTLS-authenticated renew endpoint —
  authenticated by the existing certificate, no token needed. Reuses the existing private key;
  only `client.crt` is rewritten. Always renews when invoked; there's no expiry check — run it on
  a schedule (cron/systemd timer) if periodic renewal is wanted.

## Building

```bash
make build
```

## See Also

- [certrequest](./certrequest.md) — mints the token this bootstraps from
- [bwfs](./bwfs.md), [brfs](./brfs.md), [rwfs](./rwfs.md) — the services that consume the identity this writes
- [Architecture](../ARCHITECTURE.md)
```

- [ ] **Step 3: Update `README.md`**

In the "Components" section, add after the existing `rwfs` bullet:

```markdown
- **[certrequest](docs/components/certrequest.md)** - Mints one-time enrollment tokens for nodes (control-plane, run on/near the CA)
- **[certclient](docs/components/certclient.md)** - Bootstraps or renews a node's mTLS identity from the CA
```

In the "Documentation" section, no protocol doc is needed (no proto changes), but cross-link is
already covered by the Components section above.

- [ ] **Step 4: Update `docs/ARCHITECTURE.md`**

Add a new section after "Components", before "Backup Process":

```markdown
## Control Plane vs. Agents

|  | Control plane | Agents |
|---|---|---|
| Components | `ca/` (step-ca container), `certrequest` | `bwfs`, `brfs`, `rwfs`, `certclient` |
| Runs where | On/near the CA host | On every backup node |
| Network role | Serves enrollment/renewal/admin (`/1.0/sign`, `/1.0/renew`, `/roots`, `/provisioners`) on `:9000`; has no role in backup traffic | Dial `ca_host:9000` outbound only, for enrollment/renewal; otherwise mesh with each other over gRPC on `:8080` (mTLS) |
| Docker/e2e images | `certrequest` never ships onto an agent host or into an agent image | Agent images bundle `certclient` only |

A node's mTLS identity (`ca.crt`, `client.crt`, `client.key`, consumed by `common/mtls`) is
obtained via `certclient`, using a token minted by `certrequest`. See
[certrequest](components/certrequest.md) and [certclient](components/certclient.md).
```

- [ ] **Step 5: Commit**

```bash
git add docs/components/certrequest.md docs/components/certclient.md README.md docs/ARCHITECTURE.md
git commit -m "docs: document certrequest, certclient, and the control-plane/agent split"
```

---

### Task 11: Full-repo verification

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
Expected: PASS across all packages, including the new `cmd/certclient` and `cmd/certrequest`
packages.

- [ ] **Step 4: Confirm `make build` produces all five binaries**

Run: `cd /home/alex/miniprotector && make build && ls bin/`
Expected: `brfs`, `bwfs`, `rwfs`, `certrequest`, `certclient` all present.

- [ ] **Step 5: Manual smoke test against a real local CA, if Docker is available**

```bash
cd /home/alex/miniprotector/ca
mkdir -p data/secrets
openssl rand -base64 32 > data/secrets/password
docker compose up -d
sleep 3

cd /home/alex/miniprotector
./bin/certrequest smoke-test-node --ca-url https://localhost:9000 > /tmp/smoke-token.txt
TOKEN=$(cat /tmp/smoke-token.txt)

mkdir -p /tmp/smoke-certs
cat > /tmp/smoke-local.conf <<'EOF'
default_port=8080
default_streams=4
logfolder=/tmp
ca_host=localhost:9000
EOF
MP_CONFIG_PATH=/tmp MP_CERT_TOKEN=$TOKEN ./bin/certclient
# (adjust MP_CONFIG_PATH usage to wherever /tmp/smoke-local.conf and /tmp/smoke-certs actually
# resolve to via config.ResolveConfigPath/ResolveCertsDir — the point of this step is confirming
# a real end-to-end token mint + bootstrap succeeds, not the exact paths.)

openssl x509 -in /tmp/smoke-certs/client.crt -noout -subject

cd /home/alex/miniprotector/ca && docker compose down
```

Expected: `certrequest` prints a token, `certclient` reports "Identity bootstrapped", and the
written `client.crt` shows a real, CA-issued certificate. If Docker isn't available in this
environment, note that and skip — this is the one step in the plan that requires a Docker daemon
and network access to pull the `smallstep/step-ca` image.

- [ ] **Step 6: Re-run certclient to confirm renewal works**

Continuing from Step 5's setup (before `docker compose down`):

```bash
MP_CONFIG_PATH=/tmp ./bin/certclient
```

Expected: reports "Identity renewed" (not "bootstrapped") — confirms the existing-identity branch
takes over on a second run without needing `MP_CERT_TOKEN` again.

---

### Task 12: `certrequest` — real step-ca integration test (added post-review)

Added after the final whole-branch review found that the design spec's testing requirement for
`certrequest` ("an integration-style test against a real step-ca test instance ... confirming a
minted token is actually redeemable") was dropped from Task 8's scope, leaving the only coverage
of the real `ca.NewProvisioner`/`ca.Bootstrap`/`ca.NewClient` construction calls as manual testing
— exactly the class of gap that let a real bug through undetected until Task 11's manual Docker
smoke test caught it.

**Files:**
- Create: `src/cmd/certrequest/e2e_test.go`

**Interfaces:**
- Consumes: `ca/docker-compose.yml`/`ca/entrypoint.sh` (Task 1), `ca.NewProvisioner`/`Provisioner.Token`
  (used by `certrequest`, Task 8), `ca.Bootstrap`/`ca.CreateSignRequest`/`(*ca.Client).Sign` (used by
  `certclient`, Tasks 4/7).
- Produces: nothing consumed by other tasks — this is additional test coverage only.

**Design:** A `//go:build e2e` Go test (same tag convention as `src/e2e/*_test.go`, run via
`go test -tags=e2e`) that reuses the exact command sequence already proven to work in Task 11's
manual Docker smoke test, automated:

1. Generate a throwaway provisioner password (`openssl rand` equivalent, or `crypto/rand` +
   base64 in Go) into a `t.TempDir()`-based `ca/data/secrets/password`-equivalent path — **do
   not** touch the real `ca/data/` used by a developer's own running CA; run this against an
   isolated copy of `ca/` (e.g. `os.CopyFS`-style copy of `ca/docker-compose.yml` +
   `ca/entrypoint.sh` into a temp dir, with a fresh `data/` subdirectory) so this test is
   independently repeatable and doesn't collide with a real CA instance.
2. `exec.Command("docker", "compose", "up", "-d")` in that temp copy, poll `https://localhost:<port>/health`
   (or reuse the existing `mtls`-adjacent TLS-skip-verify pattern) until ready, with a timeout.
3. Call `ca.NewProvisioner("admin@backup.internal", "", caURL, password, ca.WithRootFile(rootPath))`
   then `.Token("e2e-test-host")` directly (Go-level call, not shelling out to the built binary —
   this exercises the exact library code path `certrequest`'s `main.go` uses, which is the point).
4. Redeem the token: `ca.Bootstrap(token)`, `ca.CreateSignRequest(token)`, `client.Sign(req)` —
   mirroring `certclient`'s bootstrap function — and assert the returned `*api.SignResponse`
   contains a valid, parseable certificate whose subject/SAN matches `"e2e-test-host"`.
5. `t.Cleanup` runs `docker compose down` in the temp copy unconditionally (even on test failure),
   and removes the temp directory.

- [ ] **Step 1: Write the test** per the design above, in `src/cmd/certrequest/e2e_test.go`,
  `//go:build e2e` tag at the top of the file (matching `src/e2e/e2e_test.go`'s convention).

- [ ] **Step 2: Run it**

Run: `cd src && go test -tags=e2e ./cmd/certrequest/... -v -timeout=120s`
Expected: PASS. Requires a Docker daemon — if unavailable, note that explicitly rather than
skipping silently in a way that looks like a pass.

- [ ] **Step 3: Wire into `make test-e2e`**

Check `Makefile`'s `test-e2e` target (`cd src && go test -tags=e2e -timeout=300s ./e2e/...`) —
widen its package pattern (or add a second invocation) so `./cmd/certrequest/...` is included,
e.g. `./e2e/... ./cmd/certrequest/...`. Run `make test-e2e` to confirm both suites still pass
together.

- [ ] **Step 4: Commit**

```bash
git add src/cmd/certrequest/e2e_test.go Makefile
git commit -m "test(certrequest): add real step-ca integration test for token mint+redeem"
```
