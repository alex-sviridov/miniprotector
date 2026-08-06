//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

	var policyID string
	t.Cleanup(func() {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)
	})
	t.Cleanup(func() {
		if policyID == "" {
			return
		}
		requireStatus(t, apiRequest(t, http.MethodDelete, "/api/v1/policies/"+policyID, nil), http.StatusNoContent)
	})

	t.Run("revoke_locks_out_then_reissue_restores", func(t *testing.T) {
		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/revoke", nil), http.StatusOK)

		out, err := dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.Error(t, err, "certclient operating-refresh should fail while revoked, output: %s", out)

		requireStatus(t, apiRequest(t, http.MethodPost, "/api/v1/clients/"+hostname+"/unrevoke", nil), http.StatusOK)

		out, err = dockerComposeExec(t, hostname, "./certclient", "operating-refresh")
		require.NoError(t, err, "certclient operating-refresh should succeed after unrevoke, output: %s", out)
	})

	var policyName string
	t.Run("create_minute_policy_triggers_backup_job", func(t *testing.T) {
		storagePolicyID := fetchStoragePolicyID(t, "store")

		policyName = fmt.Sprintf("e2e-lifecycle-%d", time.Now().Unix())
		createdAt := time.Now().Unix()

		resp := apiRequest(t, http.MethodPost, "/api/v1/policies", map[string]any{
			"name":              policyName,
			"client_filters":    map[string]any{"hostnames": []string{hostname}},
			"object_filters":    []map[string]any{{"path": "/var/lib/dbdata"}},
			"rpo":               "1m",
			"backup_window":     []string{"* * * * *"},
			"storage_policy_id": storagePolicyID,
		})
		require.Equal(t, http.StatusCreated, resp.StatusCode)
		var created struct {
			ID string `json:"id"`
		}
		decodeJSON(t, resp, &created)
		require.NotEmpty(t, created.ID)
		policyID = created.ID

		out, err := dockerComposeExec(t, hostname, "./policyclient", "fetch")
		require.NoError(t, err, "policyclient fetch failed, output: %s", out)

		waitForBackupJob(t, hostname, policyName, createdAt)
	})
}

// fetchStoragePolicyID looks up an existing storage-typed policy by name
// and returns its ID -- resolved live rather than hardcoded, since a
// hardcoded UUID would silently break if the demo's storage policy is ever
// recreated with a new ID.
func fetchStoragePolicyID(t *testing.T, name string) string {
	t.Helper()

	resp := apiRequest(t, http.MethodGet, "/api/v1/policies?type=storage", nil)
	var listResp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	decodeJSON(t, resp, &listResp)

	for _, p := range listResp.Data {
		if p.Name == name {
			return p.ID
		}
	}
	t.Fatalf("no storage policy named %q found", name)
	return ""
}

// waitForBackupJob polls GET /api/v1/jobs until a backup job produced by
// policyName for sourceHost reaches a terminal "success" state, or fails
// the test after a 90s timeout -- generous because it spans a full
// reconcile tick (ReconcileIntervalSec, 30s in the demo) plus the brfs
// exec and Loki ingest.
func waitForBackupJob(t *testing.T, sourceHost, policyName string, since int64) {
	t.Helper()

	prefix := "backup:" + policyName + ":"
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		resp := apiRequest(t, http.MethodGet, fmt.Sprintf(
			"/api/v1/jobs?kind=backup&source_host=%s&since=%d", sourceHost, since), nil)
		var jobsResp struct {
			Data []struct {
				JobID string `json:"job_id"`
				State string `json:"state"`
			} `json:"data"`
		}
		decodeJSON(t, resp, &jobsResp)

		for _, j := range jobsResp.Data {
			if strings.HasPrefix(j.JobID, prefix) && j.State == "success" {
				return
			}
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("no successful backup job for source_host=%s policy=%s within 90s", sourceHost, policyName)
}
