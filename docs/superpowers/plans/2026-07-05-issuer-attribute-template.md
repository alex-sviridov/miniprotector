# Issuer Attribute Certificate Template Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a CA-side step-ca template that bakes a client's `attribute` key/value pairs into a real, parseable X.509 extension on every issued operating certificate, proving the round-trip `client-manager attribute set` → `issuer`'s `TemplateData` → an actual certificate extension — the piece `docs/superpowers/specs/2026-07-04-client-manager-phase2-design.md` explicitly deferred.

**Architecture:** A new file, `deploy/control-plane/ca/templates/leaf.tpl`, extends step-ca's built-in default leaf template with one extra `extensions` entry populated from `.Insecure.User` (the unmarshaled `TemplateData` JSON `issuer` already sends on every `Sign` call). `deploy/control-plane/ca/entrypoint.sh` wires this template into the CA's provisioner via `step ca provisioner update` on first boot. `docker-compose.yml` mounts the template file into the `step-ca` container the same way `entrypoint.sh` itself is already mounted. No Go production code changes — this is entirely CA deployment configuration, verified only by the existing Docker-backed e2e test suite in `src/cmd/issuer/e2e_test.go`, since Go templates execute inside step-ca itself, not in this codebase.

**Tech Stack:** Go 1.26, `go.step.sm/crypto`/`smallstep/certificates` (already a dependency, unchanged), step-ca's Go text/template + sprig template functions, Docker Compose (existing `deploy/control-plane` stack).

## Global Constraints

- The custom extension's OID is `1.3.6.1.4.1.61183.1.1` — a short, arbitrarily-chosen, unregistered private-use OID where every arc component is small. **Not** a `2.25.<uuid>` (X.667) arc in any form, truncated or full: `crypto/x509`'s OID parser (`golang.org/x/crypto/cryptobyte`) caps every individual arc component below 2^31, so no UUID-derived arc — 128-bit or truncated — can pass through it; this was confirmed by a real step-ca container rejecting a 60-bit truncated form with HTTP 500 "malformed extension OID field". Do not use any UUID-arc form.
- The extension is non-critical (`"critical": false`) and its value is the raw JSON encoding of the attributes map, base64'd only because the JSON template schema's `value` field is a Go `[]byte`, which `encoding/json` base64-decodes automatically from a JSON string — do not add any other encoding layer.
- No production Go code changes and no new Go dependencies — this plan is deploy-config-only, exercised exclusively through `-tags=e2e` Docker tests.
- Reading or enforcing the extension anywhere in this codebase is explicitly out of scope (see the design spec's Non-goals) — do not add any consumer code.
- Per this repo's `.claude/CLAUDE.md`, feature changes require doc updates before commit: affected `docs/components/*.md` files and `README.md`/`docs/ARCHITECTURE.md` if topology/data-flow changes (it doesn't, here — see Task 2), plus a `CHANGELOG.md` entry before merging to `main`.

---

## Task 1: CA-side attribute template, wired into the compose stack, proven by e2e tests

**Files:**
- Create: `deploy/control-plane/ca/templates/leaf.tpl`
- Modify: `deploy/control-plane/ca/entrypoint.sh`
- Modify: `deploy/control-plane/docker-compose.yml`
- Modify: `src/cmd/issuer/e2e_test.go`

**Interfaces:**
- Consumes: `mintAndSign(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest, opts certmint.Options, ttlSec int) ([]byte, error)` — existing, unchanged, defined in `src/cmd/issuer/mintsign.go`.
- Produces: nothing new for later tasks (Task 2 is docs-only and doesn't call any code from this task). The OID constant `1.3.6.1.4.1.61183.1.1` is the one durable fact Task 2's docs must state accurately.

This task rewrites `src/cmd/issuer/e2e_test.go` in full: three existing tests each duplicate the same ~25 lines of "spin up a throwaway step-ca via docker compose" boilerplate, and this task must add a template-file-copy step to all three anyway (the container now runs `step ca provisioner update ... --x509-template=...` on every boot, so every test that boots `step-ca` needs that file present, not just the new attribute test) — so it's extracted into one shared `startTestCA` helper as part of making this change, rather than duplicating the new step three times.

- [ ] **Step 1: Write the new e2e test file (failing — template file doesn't exist yet)**

Replace the entire contents of `src/cmd/issuer/e2e_test.go` with:

```go
//go:build e2e

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/certmint"
)

// attributeExtensionOID identifies the custom X.509 extension
// deploy/control-plane/ca/templates/leaf.tpl embeds attribute data under.
// See docs/superpowers/specs/2026-07-05-issuer-attribute-template-design.md
// for why this is a short, arbitrarily-chosen private-use OID rather than
// a 2.25.<uuid> (X.667) arc: crypto/x509's OID parser caps every arc
// component below 2^31, which no UUID-derived arc value -- truncated or
// not -- fits under.
var attributeExtensionOID = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 61183, 1, 1}

// TestE2E_MintAndSignEmbedsAttributesAsCertificateExtension proves the final
// hop the phase-2 design deferred: a real step-ca, using the CA-side x509
// template this phase adds, actually embeds TemplateData attributes as a
// real, parseable X.509 extension on the issued certificate -- not just
// accepting the field without rejecting it. A second mintAndSign call with
// nil attributes (mirroring what issuer's self-mint always passes) proves
// the template's `{{ if .Insecure.User }}` guard omits the extension
// entirely rather than emitting an empty one.
func TestE2E_MintAndSignEmbedsAttributesAsCertificateExtension(t *testing.T) {
	opts := startTestCA(t, "issuer-e2e-attrs")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "e2e-issuer-host"},
	}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(csrDER)
	require.NoError(t, err)

	wantAttrs := map[string]string{"role": "prod-db"}
	chainPEM, err := mintAndSign("e2e-issuer-host", nil, wantAttrs, csr, opts, 3600)
	require.NoError(t, err, "mintAndSign")
	require.NotEmpty(t, chainPEM)

	block, _ := pem.Decode(chainPEM)
	require.NotNil(t, block, "expected at least one PEM block in the chain")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.Equal(t, "e2e-issuer-host", leaf.Subject.CommonName)

	ext := findExtension(leaf, attributeExtensionOID)
	require.NotNil(t, ext, "expected certificate to carry the attribute extension %s", attributeExtensionOID)
	var gotAttrs map[string]string
	require.NoError(t, json.Unmarshal(ext.Value, &gotAttrs))
	assert.Equal(t, wantAttrs, gotAttrs, "attribute extension value must round-trip exactly")

	noAttrsKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	noAttrsCSRDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "e2e-issuer-host-noattrs"},
	}, noAttrsKey)
	require.NoError(t, err)
	noAttrsCSR, err := x509.ParseCertificateRequest(noAttrsCSRDER)
	require.NoError(t, err)

	noAttrsChainPEM, err := mintAndSign("e2e-issuer-host-noattrs", nil, nil, noAttrsCSR, opts, 3600)
	require.NoError(t, err, "mintAndSign with nil attributes")
	noAttrsBlock, _ := pem.Decode(noAttrsChainPEM)
	require.NotNil(t, noAttrsBlock)
	noAttrsLeaf, err := x509.ParseCertificate(noAttrsBlock.Bytes)
	require.NoError(t, err)
	assert.Nil(t, findExtension(noAttrsLeaf, attributeExtensionOID),
		"a certificate minted with nil attributes must not carry the attribute extension at all")
}

// TestE2E_MintAndSignEmbedsSANsInCertificate proves the exact-match SAN
// constraint this phase's design turned on: a CSR whose DNSNames were built
// from the hostname plus the same SAN list passed into mintAndSign is
// accepted by a real step-ca, and the resulting leaf certificate's DNSNames
// match exactly.
// certclient's own unit tests (see cmd/certclient) prove its CSR always
// matches whatever issuer's DescribeSANs returns; this test proves that
// when it does, the real CA actually honors it.
func TestE2E_MintAndSignEmbedsSANsInCertificate(t *testing.T) {
	opts := startTestCA(t, "issuer-e2e-sans")

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	hostname := "e2e-sans-host"
	extraSANs := []string{"e2e-sans-host.internal"}
	// certmint.Mint authorizes hostname plus sans (allSANs := append([]string{hostname}, sans...)),
	// and step-ca's OTT provisioner enforces an *exact* match between the token's
	// authorized SANs and the CSR's requested DNSNames -- so the CSR here must carry
	// the same combined set mintAndSign will mint a token for.
	wantSANs := append([]string{hostname}, extraSANs...)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostname},
		DNSNames: wantSANs,
	}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(csrDER)
	require.NoError(t, err)

	chainPEM, err := mintAndSign(hostname, extraSANs, nil, csr, opts, 3600)
	require.NoError(t, err, "mintAndSign")
	require.NotEmpty(t, chainPEM)

	block, _ := pem.Decode(chainPEM)
	require.NotNil(t, block, "expected at least one PEM block in the chain")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, wantSANs, leaf.DNSNames, "issued certificate's SANs must exactly match the CSR's requested DNSNames")
}

// TestE2E_MintSelfIdentityProducesAWorkingServerCertificate proves issuer
// can obtain its own mTLS server identity from nothing but direct CA
// provisioner access -- no enrollment token, no certclient -- against a
// real, throwaway step-ca, and that the resulting certificate's SAN
// actually matches its own hostname (the property that makes real,
// non-loopback TLS hostname verification succeed later).
func TestE2E_MintSelfIdentityProducesAWorkingServerCertificate(t *testing.T) {
	opts := startTestCA(t, "issuer-e2e-selfmint")

	mint := func(hostname string, sans []string, attributes map[string]string, csr *x509.CertificateRequest) ([]byte, error) {
		return mintAndSign(hostname, sans, attributes, csr, opts, 3600)
	}

	certsDir := filepath.Join(t.TempDir(), "issuer-certs")
	require.NoError(t, mintSelfIdentity("e2e-issuer", certsDir, opts.RootFile, mint, 3600))

	chainPEM, err := os.ReadFile(filepath.Join(certsDir, "client.crt"))
	require.NoError(t, err)
	block, _ := pem.Decode(chainPEM)
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "e2e-issuer", leaf.Subject.CommonName)
	assert.Equal(t, []string{"e2e-issuer"}, leaf.DNSNames,
		"issuer's own certificate must carry its hostname as a SAN, not just CommonName, for real (non-loopback) TLS hostname verification to succeed")
	assert.Nil(t, findExtension(leaf, attributeExtensionOID),
		"issuer's self-mint always passes nil attributes and must not carry the attribute extension")
}

// startTestCA spins up a real, throwaway step-ca via docker compose from a
// copy of the actual deploy/control-plane compose file, CA entrypoint
// script, and CA-side attribute template -- so every e2e test in this file
// exercises the exact config real deployments run, not a hand-simplified
// stand-in. Waits for the CA to become ready and returns options for
// calling it. Registers a t.Cleanup to tear the compose project down.
func startTestCA(t *testing.T, projectLabel string) certmint.Options {
	t.Helper()
	requireDocker(t)

	repoRoot := repoRootDir(t)
	tempDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "ca", "templates"), 0o755))
	copyComposeFileWithEphemeralPort(t, filepath.Join(repoRoot, "deploy", "control-plane", "docker-compose.yml"), filepath.Join(tempDir, "docker-compose.yml"))
	copyFile(t, filepath.Join(repoRoot, "deploy", "control-plane", "ca", "entrypoint.sh"), filepath.Join(tempDir, "ca", "entrypoint.sh"))
	require.NoError(t, os.Chmod(filepath.Join(tempDir, "ca", "entrypoint.sh"), 0o755))
	copyFile(t, filepath.Join(repoRoot, "deploy", "control-plane", "ca", "templates", "leaf.tpl"), filepath.Join(tempDir, "ca", "templates", "leaf.tpl"))

	secretsDir := filepath.Join(tempDir, "ca", "data", "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0o700))
	password := randomPassword(t)
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "password"), []byte(password), 0o600))

	projectName := fmt.Sprintf("%s-%d", projectLabel, time.Now().UnixNano())
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

	return certmint.Options{
		CAURL:        caURL,
		RootFile:     rootPath,
		Provisioner:  "admin@backup.internal",
		PasswordFile: filepath.Join(secretsDir, "password"),
	}
}

// findExtension returns the certificate extension matching oid, or nil if
// the certificate doesn't carry one.
func findExtension(cert *x509.Certificate, oid asn1.ObjectIdentifier) *pkix.Extension {
	for i := range cert.Extensions {
		if cert.Extensions[i].Id.Equal(oid) {
			return &cert.Extensions[i]
		}
	}
	return nil
}

// requireDocker skips the test (loudly, with a clear reason) if Docker isn't
// usable in this environment, rather than silently passing.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not found in PATH, skipping e2e test: %v", err)
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon not reachable, skipping e2e test: %v\n%s", err, out)
	}
}

func repoRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	require.NoError(t, err)
	defer in.Close()
	out, err := os.Create(dst)
	require.NoError(t, err)
	defer out.Close()
	_, err = io.Copy(out, in)
	require.NoError(t, err)
}

func copyComposeFileWithEphemeralPort(t *testing.T, src, dst string) {
	t.Helper()
	contents, err := os.ReadFile(src)
	require.NoError(t, err)
	rewritten := strings.Replace(string(contents), `"9000:9000"`, `"0:9000"`, 1)
	require.NotEqual(t, string(contents), rewritten, "expected to find literal \"9000:9000\" port mapping in %s", src)
	require.NoError(t, os.WriteFile(dst, []byte(rewritten), 0o644))
}

func discoverHostPort(t *testing.T, compose func(args ...string) *exec.Cmd) string {
	t.Helper()
	portCmd := compose("port", "step-ca", "9000")
	out, err := portCmd.CombinedOutput()
	require.NoError(t, err, "docker compose port failed: %s", out)
	addr := strings.TrimSpace(string(out))
	idx := strings.LastIndex(addr, ":")
	require.GreaterOrEqual(t, idx, 0, "unexpected `docker compose port` output: %q", addr)
	portStr := addr[idx+1:]
	_, err = strconv.Atoi(portStr)
	require.NoError(t, err, "failed to parse port from `docker compose port` output: %q", addr)
	return portStr
}

func randomPassword(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func waitForCA(ctx context.Context, caURL, rootPath string) error {
	httpClient := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // throwaway test CA, cert not yet trusted at poll time
		},
	}
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s/health to be ready: %w (last error: %v)", caURL, ctx.Err(), lastErr)
		case <-ticker.C:
			if _, err := os.Stat(rootPath); err != nil {
				lastErr = fmt.Errorf("root cert not yet written: %w", err)
				continue
			}
			resp, err := httpClient.Get(caURL + "/health")
			if err != nil {
				lastErr = err
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("unexpected status %d: %s", resp.StatusCode, body)
		}
	}
}
```

- [ ] **Step 2: Confirm the package still compiles and run the e2e tests to see them fail**

Run: `cd src && go vet -tags=e2e ./cmd/issuer/...`
Expected: no output (compiles cleanly) — this only checks the Go code compiles under the `e2e` build tag; it does not require Docker or the template file to exist yet.

Run: `cd src && go test -tags=e2e -timeout=300s ./cmd/issuer/... -run TestE2E_MintAndSignEmbedsAttributesAsCertificateExtension -v`
Expected: FAIL, with an error from `copyFile` inside `startTestCA` — `open .../deploy/control-plane/ca/templates/leaf.tpl: no such file or directory` — because `leaf.tpl` doesn't exist yet. (If Docker isn't available in this environment, this instead prints `--- SKIP` from `requireDocker`; if so, skip straight to Step 3 and rely on Step 4's run to validate.)

- [ ] **Step 3: Create the template and wire it into the CA**

Create `deploy/control-plane/ca/templates/leaf.tpl`:

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

Modify `deploy/control-plane/ca/entrypoint.sh` — it currently reads:

```sh
#!/bin/sh
set -e
if [ ! -f /home/step/config/ca.json ]; then
  step ca init --deployment-type=standalone \
    --name="Enterprise Backup Cluster CA" \
    --dns="ca.backup.internal,localhost,step-ca" \
    --address=":9000" \
    --provisioner="admin@backup.internal" \
    --password-file=/home/step/secrets/password
fi
exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
```

Change it to:

```sh
#!/bin/sh
set -e
if [ ! -f /home/step/config/ca.json ]; then
  step ca init --deployment-type=standalone \
    --name="Enterprise Backup Cluster CA" \
    --dns="ca.backup.internal,localhost,step-ca" \
    --address=":9000" \
    --provisioner="admin@backup.internal" \
    --password-file=/home/step/secrets/password
  step ca provisioner update admin@backup.internal --x509-template=/home/step/templates/leaf.tpl
fi
exec step-ca /home/step/config/ca.json --password-file=/home/step/secrets/password
```

Modify `deploy/control-plane/docker-compose.yml` — the `step-ca` service currently reads:

```yaml
  step-ca:
    image: smallstep/step-ca
    volumes:
      - ./ca/data:/home/step
      - ./ca/entrypoint.sh:/home/step/entrypoint.sh:ro
    ports:
      - "9000:9000"
    entrypoint: ["/home/step/entrypoint.sh"]
    restart: unless-stopped
```

Add a third volume line so it reads:

```yaml
  step-ca:
    image: smallstep/step-ca
    volumes:
      - ./ca/data:/home/step
      - ./ca/entrypoint.sh:/home/step/entrypoint.sh:ro
      - ./ca/templates/leaf.tpl:/home/step/templates/leaf.tpl:ro
    ports:
      - "9000:9000"
    entrypoint: ["/home/step/entrypoint.sh"]
    restart: unless-stopped
```

- [ ] **Step 4: Run the e2e tests and verify they pass**

Run: `cd src && go test -tags=e2e -timeout=300s ./cmd/issuer/... -v`
Expected: `PASS` for all of `TestE2E_MintAndSignEmbedsAttributesAsCertificateExtension`, `TestE2E_MintAndSignEmbedsSANsInCertificate`, and `TestE2E_MintSelfIdentityProducesAWorkingServerCertificate`.

If `TestE2E_MintAndSignEmbedsAttributesAsCertificateExtension` fails at the `docker compose up` step instead of an assertion (i.e. the container crash-loops), run `cd <failing test's tempDir, printed in the failure output> && docker compose -p <project name printed in output> logs step-ca` to see why `step ca provisioner update` failed inside the container — the most likely cause is a flag-name mismatch on this specific step-ca image version, since this plan's flag name was not verified against a running container ahead of time. Check `docker run --rm smallstep/step-ca step ca provisioner update --help` for the exact current flag name and adjust `entrypoint.sh` accordingly.

If any of the three tests are skipped with `docker not found` or `docker daemon not reachable`, Docker isn't usable in this environment — note this in the task's final report rather than treating it as a pass; these tests need to actually run (not just skip) before this task can be considered verified.

- [ ] **Step 5: Run the full non-e2e test suite to confirm nothing else broke**

Run: `cd src && go build ./... && go test ./...`
Expected: builds cleanly, all tests pass (this task changed no non-test, non-e2e-tagged Go code, so this is a regression check on the rest of the `cmd/issuer` package and everything else).

- [ ] **Step 6: Commit**

```bash
git add deploy/control-plane/ca/templates/leaf.tpl deploy/control-plane/ca/entrypoint.sh deploy/control-plane/docker-compose.yml src/cmd/issuer/e2e_test.go
git commit -m "feat(issuer): bake attributes into a real certificate extension via CA-side template

step-ca's default template silently dropped TemplateData; this adds a
custom leaf template (1.3.6.1.4.1.61183.1.1, non-critical, JSON-
encoded) so attribute values issuer already sends actually land in the
issued certificate. Proven via the existing docker-backed e2e suite,
which now also proves the extension is omitted entirely when a client
has no attributes set (self-mint's nil-attributes path)."
```

---

## Task 2: Documentation updates

**Files:**
- Modify: `docs/components/issuer.md`
- Modify: `docs/components/client-manager.md`
- Modify: `docs/SECURITY.md`
- Modify: `CHANGELOG.md`

**Interfaces:**
- Consumes: nothing from Task 1's code (docs reference the OID constant `1.3.6.1.4.1.61183.1.1` as a fact, not a Go symbol).
- Produces: nothing further downstream.

No changes to `README.md`, `docs/ARCHITECTURE.md`, or `deploy/control-plane/README.md`: this change adds no new component, doesn't alter system topology or data flow (attributes already flowed from `client-manager` through `issuer` to the CA; this only changes what the CA does with data already in transit), and `deploy/control-plane/README.md`'s walkthrough never described `step-ca`'s internal provisioner/template configuration to begin with, so there's no existing operator-facing text to correct.

- [ ] **Step 1: Update `docs/components/issuer.md`**

It currently ends its "Behavior" section with this paragraph (verify with `grep -n "Not yet in this phase" docs/components/issuer.md` before editing, since line numbers may have shifted):

```
**Not yet in this phase:** actually baking `attribute` values into a certificate's extensions
requires a custom X.509 template (`options.x509.templateFile` in the CA's `ca.json`) that reads
`.Insecure.User.<field>` — that template is deployment configuration for a CA operator to author,
not something this binary's code prescribes. The e2e test proves a real step-ca accepts a sign
request carrying `TemplateData` without rejecting it and returns a valid, signable certificate; it
does not verify that the data reaches a certificate extension, since that requires the template
above, which this phase does not ship.
```

Replace it with:

```
**Attribute extension:** `attribute` values are baked into the issued certificate as a real X.509
extension (OID `1.3.6.1.4.1.61183.1.1`, non-critical, JSON-encoded, present only when a client
has at least one attribute set), via a custom step-ca leaf template
(`deploy/control-plane/ca/templates/leaf.tpl`) wired into the CA's provisioner by
`deploy/control-plane/ca/entrypoint.sh` on first boot. See
[Design: Issuer Attribute Template](../superpowers/specs/2026-07-05-issuer-attribute-template-design.md)
for why the OID is a short, arbitrarily-chosen private-use OID rather than a standards-compliant
X.667 arc, and why nothing in this codebase yet reads or enforces the extension it embeds.
```

- [ ] **Step 2: Update `docs/components/client-manager.md`**

It currently opens with (verify with `grep -n "intended for baking" docs/components/client-manager.md`):

```
Owns the persistent list of enrolled clients: when they were added, free-form annotations
(`description`), attributes intended for baking into a client's certificate (`attribute`), SAN
aliases (`san`), and a revoked flag.
```

Replace `attributes intended for baking into a client's certificate` with `attributes baked into a client's certificate as a real X.509 extension` so the sentence reads:

```
Owns the persistent list of enrolled clients: when they were added, free-form annotations
(`description`), attributes baked into a client's certificate as a real X.509 extension
(`attribute`), SAN aliases (`san`), and a revoked flag.
```

- [ ] **Step 3: Update `docs/SECURITY.md`**

It currently reads (verify with `grep -n "attribute.*san.*changes propagate the same way" docs/SECURITY.md`):

```
`client-manager revoke <hostname>` sets a flag in `client-manager`'s own SQLite database.
`client-manager` itself has no network interface — it never enforces anything directly. Real
enforcement happens in `issuer`, which shares that same database file: on the revoked node's next
`RequestOperatingCert` call, `issuer` refuses outright, and the node's current operating
certificate simply expires without a replacement, typically within `OperatingCertFetchIntervalSec`
of the check (bounded from above by `OperatingCertTTLSec`, since that's the certificate's own
validity window). `attribute`/`san` changes propagate the same way, on the same schedule, since
every operating-refresh is a fresh `Sign` with a fresh CSR.
```

Add one sentence after that paragraph:

```
`client-manager revoke <hostname>` sets a flag in `client-manager`'s own SQLite database.
`client-manager` itself has no network interface — it never enforces anything directly. Real
enforcement happens in `issuer`, which shares that same database file: on the revoked node's next
`RequestOperatingCert` call, `issuer` refuses outright, and the node's current operating
certificate simply expires without a replacement, typically within `OperatingCertFetchIntervalSec`
of the check (bounded from above by `OperatingCertTTLSec`, since that's the certificate's own
validity window). `attribute`/`san` changes propagate the same way, on the same schedule, since
every operating-refresh is a fresh `Sign` with a fresh CSR.

`attribute` values land in the certificate itself as a real, non-critical X.509 extension (OID
`1.3.6.1.4.1.61183.1.1`, JSON-encoded), not just in the `Sign` request sent to the CA — see
[issuer](components/issuer.md#behavior). Nothing in this codebase yet reads or enforces that
extension; it exists so a future authorization check can, without another round of
certificate-issuance changes.
```

- [ ] **Step 4: Add a `CHANGELOG.md` entry**

Insert a new entry at the top of the changelog list, immediately after the introductory line (verify current top entry with `head -6 CHANGELOG.md` — insert before it, matching the file's existing dated-heading style):

```
## 2026-07-05 — Attributes now land in the certificate, not just the Sign request

`issuer` has passed `attribute` key/value pairs to the CA via `TemplateData` on every `Sign` call
since the operating-certificate work landed, but step-ca's default template ignored the field
entirely — the data reached the wire and was silently dropped. A new CA-side template
(`deploy/control-plane/ca/templates/leaf.tpl`, wired in by `ca/entrypoint.sh`) now embeds those
attributes as a real, non-critical X.509 extension (OID `1.3.6.1.4.1.61183.1.1`, JSON-encoded,
present only when a client has attributes set), closing the gap phase 2's design explicitly
deferred. Nothing yet reads or enforces this extension — that remains separate, later work — but
the round-trip from `client-manager attribute set` to an actual certificate field now provably
works end to end, per a new Docker-backed e2e assertion in `src/cmd/issuer/e2e_test.go`.
```

- [ ] **Step 5: Commit**

```bash
git add docs/components/issuer.md docs/components/client-manager.md docs/SECURITY.md CHANGELOG.md
git commit -m "docs: document the CA-side attribute certificate extension"
```
