package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeFakeBootstrapCert writes a self-signed bootstrap.crt/bootstrap.key
// pair with the given CommonName into certsDir. Mirrors
// cmd/certclient/operatingrefresh_test.go's writeTestBootstrapCred exactly
// -- hostnameFromBootstrapCert only ever reads the CommonName back out, so
// a self-signed fixture never needs to chain to a real CA.
func writeFakeBootstrapCert(t *testing.T, certsDir, hostname string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hostname},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	require.NoError(t, os.WriteFile(filepath.Join(certsDir, "bootstrap.crt"), certPEM, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(certsDir, "bootstrap.key"), keyPEM, 0o600))
}

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
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, `"/var/log/mp/*.log"`)
}

func TestRenderVectorConfig_PointsAtLogGatewayEndpoint(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "https://log-gateway.internal:9400")
}

func TestRenderVectorConfig_UsesCertsDirForTLS(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "/var/lib/mp/certs/client.crt")
	assert.Contains(t, got, "/var/lib/mp/certs/client.key")
	assert.Contains(t, got, "/var/lib/mp/certs/ca.crt")
}

func TestRenderVectorConfig_UsesVarDirForDataAndBuffer(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "/var/lib/mp/vector-data")
}

func TestRenderVectorConfig_NeverEnablesTheHTTPAPI(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.NotContains(t, got, "api:", "must never enable Vector's own HTTP API/listener -- agent's own network footprint stays outbound-only")
}

func TestRenderVectorConfig_SetsHostnameLabelFromArgument(t *testing.T) {
	// log-gateway authenticates the push but never inspects or rewrites
	// the body (see docs/SECURITY.md), so Vector itself is the only
	// place the hostname label can come from.
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "node-real-hostname")
	require.NoError(t, err)
	assert.Contains(t, got, `hostname: "node-real-hostname"`)
}

func TestRenderVectorConfig_LiftsJobLifecycleFieldsIntoStructuredMetadata(t *testing.T) {
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "parse_json(.message)")
	assert.Contains(t, got, "structured_metadata:")
	assert.Contains(t, got, `job_id: "{{ job_id }}"`)
	assert.Contains(t, got, `event: "{{ event }}"`)
	assert.Contains(t, got, `status: "{{ status }}"`)
}

func TestRenderVectorConfig_UsesTextCodecSoLineIsJustTheMessage(t *testing.T) {
	// codec: json would serialize the whole Vector event (host, file,
	// source_type, message, plus the binary/job_id/event/status fields
	// add_binary_label attaches) as the stored Loki line -- burying the
	// app's own slog JSON one level deeper inside a "message" string that
	// api-server/web never unwrap. codec: text uses only the event's
	// .message field (the app's original log line) as the stored line,
	// since binary/hostname/job_id/event/status are already carried as
	// labels/structured_metadata above.
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "codec: text")
	assert.NotContains(t, got, "codec: json")
}

func TestRenderVectorConfig_OverridesTimestampFromAppLogTime(t *testing.T) {
	// Without this, Vector's file source stamps .timestamp with its own
	// read time rather than the app's actual log time -- normally near
	// identical, but they diverge (inconsistently across hosts/binaries)
	// after any read lag: agent/Vector restart catch-up, or the disk
	// buffer's when_full: block backpressure. Since api-server and the web
	// UI both sort and display strictly by this timestamp
	// (cmd/api-server/jobs.go, web/src/stores/jobs.js), a stale read-time
	// stamp shows lines out of the order their own embedded "time" field
	// (slog's default TimeKey) implies. Falls back to Vector's read time,
	// same as web/src/utils/logLine.js's parseLogLine, whenever .message
	// isn't JSON or its "time" field is missing/unparseable.
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "is_string(parsed.time)")
	assert.Contains(t, got, `parse_timestamp(parsed.time, "%+")`)
	assert.Contains(t, got, ".timestamp = ts")
}

func TestHostnameFromBootstrapCert_ReadsCommonName(t *testing.T) {
	dir := t.TempDir()
	writeFakeBootstrapCert(t, dir, "node-under-test")

	got, err := hostnameFromBootstrapCert(dir)
	require.NoError(t, err)
	assert.Equal(t, "node-under-test", got)
}

func TestHostnameFromBootstrapCert_MissingCredentialErrors(t *testing.T) {
	dir := t.TempDir() // no bootstrap.crt/key written

	_, err := hostnameFromBootstrapCert(dir)
	assert.Error(t, err)
}

func TestRenderVectorConfig_BufferBlocksInsteadOfDroppingFreshLogs(t *testing.T) {
	// Vector's disk buffer only supports "block" or "drop_newest" -- there
	// is no "drop oldest" mode. drop_newest would discard the freshest,
	// most operationally relevant lines once the buffer fills during an
	// outage; block instead pauses the file source until the buffer
	// drains, so nothing is lost (see docs/superpowers/specs/
	// 2026-08-22-logging-flow-hardening-design.md).
	got, err := renderVectorConfig("/var/log/mp", "/var/lib/mp", "/var/lib/mp/certs", "log-gateway.internal", 9400, "test-node")
	require.NoError(t, err)
	assert.Contains(t, got, "when_full: block")
	assert.NotContains(t, got, "when_full: drop_newest")
}
