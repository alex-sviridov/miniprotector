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
// for why this is a short, arbitrarily-chosen private-use OID rather than a
// UUID-derived one: crypto/x509's OID parser caps every arc component below
// 2^31, a limit no UUID-derived arc value survives, truncated or not.
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

	emptyAttrsKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	emptyAttrsCSRDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "e2e-issuer-host-emptyattrs"},
	}, emptyAttrsKey)
	require.NoError(t, err)
	emptyAttrsCSR, err := x509.ParseCertificateRequest(emptyAttrsCSRDER)
	require.NoError(t, err)

	emptyAttrsChainPEM, err := mintAndSign("e2e-issuer-host-emptyattrs", nil, map[string]string{}, emptyAttrsCSR, opts, 3600)
	require.NoError(t, err, "mintAndSign with empty attributes")
	emptyAttrsBlock, _ := pem.Decode(emptyAttrsChainPEM)
	require.NotNil(t, emptyAttrsBlock)
	emptyAttrsLeaf, err := x509.ParseCertificate(emptyAttrsBlock.Bytes)
	require.NoError(t, err)
	assert.Nil(t, findExtension(emptyAttrsLeaf, attributeExtensionOID),
		"a certificate minted with an empty (non-nil) attributes map must not carry the attribute extension at all")
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

	// Pre-create the leaf.tpl bind mount's destination directory so Docker
	// doesn't auto-create it (as root, since dockerd runs as root) the first
	// time the container starts. An auto-created directory here would be
	// unremovable by t.TempDir()'s cleanup, which runs as the test's own
	// unprivileged user.
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "ca", "data", "templates"), 0o755))

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
