//go:build e2e

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/smallstep/certificates/ca"
	"github.com/stretchr/testify/require"
)

// TestE2E_TokenMintAndRedeem exercises the real ca.NewProvisioner/
// Provisioner.Token/ca.Bootstrap/ca.CreateSignRequest/(*ca.Client).Sign
// construction calls against a live, throwaway step-ca instance.
//
// This guards against the class of bug already caught once by hand (see
// docs/protocols and the certclient renew-path fix that added
// ca.WithRootFile to ca.NewClient): unit tests that mock the signer/renewer
// interfaces never exercise the real client-construction code, so a broken
// call to a smallstep library function can sail through `go test ./...`
// undetected.
//
// It reuses the exact ca/docker-compose.yml + ca/entrypoint.sh from the repo,
// copied into a t.TempDir() so it never touches a developer's real ca/data/
// state, and runs on a non-default host port so it can't collide with a real
// CA a developer might have running locally on 9000.
func TestE2E_TokenMintAndRedeem(t *testing.T) {
	requireDocker(t)

	repoRoot := repoRootDir(t)
	tempDir := t.TempDir()

	copyFile(t, filepath.Join(repoRoot, "ca", "docker-compose.yml"), filepath.Join(tempDir, "docker-compose.yml"))
	copyFile(t, filepath.Join(repoRoot, "ca", "entrypoint.sh"), filepath.Join(tempDir, "entrypoint.sh"))
	require.NoError(t, os.Chmod(filepath.Join(tempDir, "entrypoint.sh"), 0o755))

	// Publish on a non-default host port so this never collides with a real
	// CA a developer might have running on the repo's own ca/ directory
	// (which defaults to 9000). docker-compose.override.yml is picked up
	// automatically by `docker compose` alongside docker-compose.yml.
	const hostPort = "9443"
	writeFile(t, filepath.Join(tempDir, "docker-compose.override.yml"), fmt.Sprintf(
		"services:\n  step-ca:\n    ports:\n      - \"%s:9000\"\n", hostPort))

	// Throwaway provisioner password, unique per run.
	secretsDir := filepath.Join(tempDir, "data", "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0o700))
	password := randomPassword(t)
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "password"), []byte(password), 0o600))

	// A unique project name isolates the container/network names from any
	// other compose project (including a real ca/ stack) that might be
	// running concurrently on this host.
	projectName := fmt.Sprintf("certrequest-e2e-%d", time.Now().UnixNano())
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

	upCmd := compose("up", "-d")
	out, err := upCmd.CombinedOutput()
	require.NoError(t, err, "docker compose up failed: %s", out)

	caURL := fmt.Sprintf("https://localhost:%s", hostPort)
	rootPath := filepath.Join(tempDir, "data", "certs", "root_ca.crt")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, waitForCA(ctx, caURL, rootPath), "step-ca never became ready")

	// --- Exercise the exact library calls certrequest's main() uses ---
	provisioner, err := ca.NewProvisioner("admin@backup.internal", "", caURL, []byte(password), ca.WithRootFile(rootPath))
	require.NoError(t, err, "ca.NewProvisioner")

	token, err := provisioner.Token("e2e-test-host")
	require.NoError(t, err, "Provisioner.Token")
	require.NotEmpty(t, token)

	// --- Redeem it, mirroring certclient's bootstrap path ---
	client, err := ca.Bootstrap(token)
	require.NoError(t, err, "ca.Bootstrap")

	req, _, err := ca.CreateSignRequest(token)
	require.NoError(t, err, "ca.CreateSignRequest")

	signResp, err := client.Sign(req)
	require.NoError(t, err, "Client.Sign")
	require.NotNil(t, signResp)

	leaf, err := ca.Certificate(signResp)
	require.NoError(t, err, "ca.Certificate")
	require.Equal(t, "e2e-test-host", leaf.Subject.CommonName)
	require.Contains(t, leaf.DNSNames, "e2e-test-host")
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

// repoRootDir locates the repo root (two levels up from
// src/cmd/certrequest/) the same way src/e2e/e2e_test.go does.
func repoRootDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	// this file lives at <repoRoot>/src/cmd/certrequest/e2e_test.go
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func randomPassword(t *testing.T) string {
	t.Helper()
	buf := make([]byte, 32)
	_, err := rand.Read(buf)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// waitForCA polls step-ca's /health endpoint (skipping TLS verification,
// since we don't yet trust its self-signed root at poll time) until it
// responds 200, or ctx expires. It also waits for the root certificate file
// to be written by `step ca init` inside the container, since that's a
// prerequisite for ca.WithRootFile(rootPath) later.
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
