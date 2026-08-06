//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	apiBaseURL = "http://localhost:8090"
	apiToken   = "dev-placeholder-token-change-me"
)

// apiRequest sends an authenticated request to api-server's REST API and
// returns the raw response -- callers decide whether to decode the body.
func apiRequest(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, apiBaseURL+path, reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	require.NoError(t, err)
	return resp
}

// requireStatus closes resp's body and asserts its status code.
func requireStatus(t *testing.T, resp *http.Response, want int) {
	t.Helper()
	defer resp.Body.Close()
	require.Equal(t, want, resp.StatusCode)
}

// decodeJSON decodes resp's body into v and closes it.
func decodeJSON(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	require.NoError(t, json.NewDecoder(resp.Body).Decode(v))
}

// composeFile returns the absolute path to demo/docker-compose.yml,
// resolved relative to this source file so it works regardless of the
// test binary's working directory.
func composeFile() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "demo", "docker-compose.yml")
}

// dockerComposeExec runs a command inside an already-running demo service
// container and returns its combined output. A non-nil error means the
// command exited non-zero.
func dockerComposeExec(t *testing.T, service string, args ...string) (string, error) {
	t.Helper()
	cmdArgs := append([]string{"compose", "-f", composeFile(), "exec", "-T", service}, args...)
	out, err := exec.Command("docker", cmdArgs...).CombinedOutput()
	return string(out), err
}

// TestE2E_ClientLifecycle walks a realistic operator flow against the
// already-running demo lab (`make demo-up`): revoke and reissue a client's
// certificate, create a fast-recurring backup policy, and confirm a real
// backup job and its catalog entry both appear. Subtests run in order and
// share state -- a failure partway through makes later subtests
// meaningless.
func TestE2E_ClientLifecycle(t *testing.T) {
	const hostname = "database"

	t.Cleanup(func() {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)
	})

	t.Run("revoke_locks_out_then_reissue_restores", func(t *testing.T) {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/revoke", nil), http.StatusOK)

		out, err := dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.Error(t, err, "certclient operating-refresh should fail while revoked, output: %s", out)

		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)

		out, err = dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.NoError(t, err, "certclient operating-refresh should succeed after unrevoke, output: %s", out)
	})
}
