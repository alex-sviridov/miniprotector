package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync/atomic"
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

func TestVectorSupervisor_StartsAndStopsCleanlyOnContextCancel(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-vector.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"), 0o755))

	var spawns int64
	sup := newVectorSupervisor(script, "", testLogger())
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	sup.Start(ctx)

	time.Sleep(100 * time.Millisecond) // let it actually spawn
	require.EqualValues(t, 1, atomic.LoadInt64(&spawns))
	cancel()

	// Wait on the real completion signal rather than guessing a sleep
	// duration -- ctx cancellation must actually tear down the running
	// process (not just stop future respawns), or superviseLoop would
	// stay blocked in cmd.Wait() forever and loopDone would never close.
	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context cancellation")
	}

	assert.EqualValues(t, 1, atomic.LoadInt64(&spawns), "no respawn should happen once ctx is cancelled")
}

func TestVectorSupervisor_RestartsOnUnexpectedExitWithoutHangingForever(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-vector.sh")
	// exits immediately every time -- simulates a persistent crash
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755))

	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Millisecond, 30*time.Millisecond
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	var spawns int64
	sup := newVectorSupervisor(script, "", testLogger())
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	sup.Start(ctx)

	// Wait on the real completion signal (not a fixed sleep) so the
	// assertion below is only evaluated once superviseLoop has fully
	// stopped -- a sleep-based wait here previously raced against the
	// still-running goroutine's calls to backoff(), which reads the same
	// package-level backoffBase/backoffMax vars this test mutates.
	select {
	case <-sup.loopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("supervise loop did not stop after context timeout")
	}

	assert.GreaterOrEqual(t, atomic.LoadInt64(&spawns), int64(2), "a persistently crashing process must be respawned more than once")
}

func TestVectorSupervisor_TriggerRestartDoesNotApplyBackoff(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-vector.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"), 0o755))

	// A large backoff window -- if TriggerRestart incorrectly went through
	// the crash-backoff path, the respawn would not happen within this
	// test's short assertion window.
	origBase, origMax := backoffBase, backoffMax
	backoffBase, backoffMax = 10*time.Second, 10*time.Second
	defer func() { backoffBase, backoffMax = origBase, origMax }()

	var spawns int64
	sup := newVectorSupervisor(script, "", testLogger())
	sup.onSpawnForTest = func() { atomic.AddInt64(&spawns, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sup.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	require.EqualValues(t, 1, atomic.LoadInt64(&spawns))

	sup.TriggerRestart()
	require.Eventually(t, func() bool {
		return atomic.LoadInt64(&spawns) >= 2
	}, time.Second, 20*time.Millisecond, "TriggerRestart must respawn promptly, not wait out the crash-backoff window")
}
