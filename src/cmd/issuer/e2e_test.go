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
	"encoding/base64"
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

// TestE2E_MintAndSignAcceptedByCAWithTemplateData proves that a real, live
// step-ca accepts a Sign request carrying attribute data via TemplateData
// -- without rejecting it -- and returns a valid, signable certificate
// chain. It does NOT prove that the attribute data round-trips into a
// certificate extension: that requires a CA-side custom x509 template,
// which is explicitly out of scope for this phase (see the phase-2 design
// doc). What this test confirms is the narrower, previously-unverified
// fact that the mechanism this design depends on -- a real step-ca signing
// a request that includes TemplateData -- actually works end to end.
func TestE2E_MintAndSignAcceptedByCAWithTemplateData(t *testing.T) {
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

	projectName := fmt.Sprintf("issuer-e2e-%d", time.Now().UnixNano())
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
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "e2e-issuer-host"},
	}, key)
	require.NoError(t, err)
	csr, err := x509.ParseCertificateRequest(csrDER)
	require.NoError(t, err)

	chainPEM, err := mintAndSign("e2e-issuer-host", nil, map[string]string{"role": "prod-db"}, csr, opts, 3600)
	require.NoError(t, err, "mintAndSign")
	require.NotEmpty(t, chainPEM)

	block, _ := pem.Decode(chainPEM)
	require.NotNil(t, block, "expected at least one PEM block in the chain")
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	require.Equal(t, "e2e-issuer-host", leaf.Subject.CommonName)
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
