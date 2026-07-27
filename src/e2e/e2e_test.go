//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestE2E_WebUIAvailable is a smoke test against an already-running demo lab
// (`make demo-up`) -- it does not start, wait for, or manage the stack
// itself. It only confirms the `web` service (published at localhost:8091 by
// demo/docker-compose.yml) is up and serving.
func TestE2E_WebUIAvailable(t *testing.T) {
	client := &http.Client{Timeout: 5 * time.Second}

	resp, err := client.Get("http://localhost:8091")
	require.NoError(t, err, "demo web UI unreachable at http://localhost:8091 -- is `make demo-up` running?")
	defer resp.Body.Close()

	require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300,
		"expected 2xx from demo web UI, got %d", resp.StatusCode)
}
