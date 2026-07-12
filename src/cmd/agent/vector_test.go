package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveVectorBinary_FindsColocatedBinary writes a fake vector binary
// into a temp directory and confirms resolveVectorBinaryIn finds it --
// testing the pure, directory-parameterized core directly, without needing
// to re-exec the test binary the way
// TestRealExec_ResolvesBinaryColocatedWithOwnExecutable (reconcile_test.go)
// does for the equivalent real-os.Executable()-based path.
func TestResolveVectorBinary_FindsColocatedBinary(t *testing.T) {
	dir := t.TempDir()
	vectorPath := filepath.Join(dir, "vector")
	require.NoError(t, os.WriteFile(vectorPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	got, err := resolveVectorBinaryIn(dir)
	require.NoError(t, err)
	assert.Equal(t, vectorPath, got)
}

func TestResolveVectorBinary_MissingBinaryFailsLoudly(t *testing.T) {
	dir := t.TempDir() // empty -- no vector binary present

	_, err := resolveVectorBinaryIn(dir)
	assert.Error(t, err, "must fail loudly, never fall back to $PATH")
}

func TestRenderVectorConfig_IncludesLogDirGlob(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.Contains(t, got, `"/var/log/mp/*.log"`)
}

func TestRenderVectorConfig_PointsAtLogGatewayEndpoint(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.Contains(t, got, "https://log-gateway.internal:9400")
}

func TestRenderVectorConfig_UsesCertsDirForTLS(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.Contains(t, got, "/var/lib/mp/certs/client.crt")
	assert.Contains(t, got, "/var/lib/mp/certs/client.key")
	assert.Contains(t, got, "/var/lib/mp/certs/ca.crt")
}

func TestRenderVectorConfig_UsesVarDirForDataAndBuffer(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.Contains(t, got, "/var/lib/mp/vector-data")
}

func TestRenderVectorConfig_NeverEnablesTheHTTPAPI(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400)
	require.NoError(t, err)
	assert.NotContains(t, got, "api:", "must never enable Vector's own HTTP API/listener -- agent's own network footprint stays outbound-only")
}
