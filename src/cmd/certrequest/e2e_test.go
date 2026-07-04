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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/smallstep/certificates/ca"
	"github.com/stretchr/testify/require"

	"github.com/alex-sviridov/miniprotector/common/certmint"
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
// It reuses the exact deploy/control-plane/docker-compose.yml (step-ca service
// only) + deploy/control-plane/ca/entrypoint.sh from the repo, copied into a
// t.TempDir() so it never touches a developer's real deploy/control-plane/ca/data/
// state. The copied compose file's host port is rewritten from the fixed
// "9000:9000" to "0:9000" (bind to an OS-assigned ephemeral port) before it's
// written to the temp dir, and the actual assigned port is discovered after
// `docker compose up` via `docker compose port`. This guarantees the test
// can never collide with a real CA a developer might have running locally on
// 9000 — a docker-compose.override.yml alone can't do this, because Compose
// merges list-valued fields like `ports:` by concatenation, not replacement,
// so an override port entry would be added alongside the base file's
// "9000:9000" rather than replacing it, and `docker compose up` would still
// try (and fail) to bind host port 9000.
func TestE2E_TokenMintAndRedeem(t *testing.T) {
	requireDocker(t)

	repoRoot := repoRootDir(t)
	tempDir := t.TempDir()

	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "ca"), 0o755))
	copyComposeFileWithEphemeralPort(t, filepath.Join(repoRoot, "deploy", "control-plane", "docker-compose.yml"), filepath.Join(tempDir, "docker-compose.yml"))
	copyFile(t, filepath.Join(repoRoot, "deploy", "control-plane", "ca", "entrypoint.sh"), filepath.Join(tempDir, "ca", "entrypoint.sh"))
	require.NoError(t, os.Chmod(filepath.Join(tempDir, "ca", "entrypoint.sh"), 0o755))

	// Throwaway provisioner password, unique per run.
	secretsDir := filepath.Join(tempDir, "ca", "data", "secrets")
	require.NoError(t, os.MkdirAll(secretsDir, 0o700))
	password := randomPassword(t)
	require.NoError(t, os.WriteFile(filepath.Join(secretsDir, "password"), []byte(password), 0o600))

	// A unique project name isolates the container/network names from any
	// other compose project (including a real deploy/control-plane stack) that might be
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

	upCmd := compose("up", "-d", "step-ca")
	out, err := upCmd.CombinedOutput()
	require.NoError(t, err, "docker compose up failed: %s", out)

	hostPort := discoverHostPort(t, compose)
	caURL := fmt.Sprintf("https://localhost:%s", hostPort)
	rootPath := filepath.Join(tempDir, "ca", "data", "certs", "root_ca.crt")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	require.NoError(t, waitForCA(ctx, caURL, rootPath), "step-ca never became ready")

	// --- Exercise the exact library calls certrequest's main() uses ---
	token, err := certmint.Mint("e2e-test-host", nil, certmint.Options{
		CAURL:        caURL,
		RootFile:     rootPath,
		Provisioner:  "admin@backup.internal",
		PasswordFile: filepath.Join(secretsDir, "password"),
	})
	require.NoError(t, err, "certmint.Mint")
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

// copyComposeFileWithEphemeralPort copies src to dst, rewriting the literal
// "9000:9000" host:container port mapping to "0:9000" along the way. Binding
// host port 0 tells Docker to publish the container port on any free
// ephemeral host port, so this test's compose stack can never collide with a
// real CA a developer might have running locally on the repo's own default
// port 9000. The actual assigned port is discovered later via
// `docker compose port` (see discoverHostPort).
func copyComposeFileWithEphemeralPort(t *testing.T, src, dst string) {
	t.Helper()
	contents, err := os.ReadFile(src)
	require.NoError(t, err)

	rewritten := strings.Replace(string(contents), `"9000:9000"`, `"0:9000"`, 1)
	require.NotEqual(t, string(contents), rewritten, "expected to find literal \"9000:9000\" port mapping in %s", src)

	require.NoError(t, os.WriteFile(dst, []byte(rewritten), 0o644))
}

// discoverHostPort asks Docker which ephemeral host port it actually
// assigned to the step-ca container's published port 9000 (bound to host
// port 0 by copyComposeFileWithEphemeralPort). `docker compose port` prints
// output like "0.0.0.0:34567"; this parses out just the port number.
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
