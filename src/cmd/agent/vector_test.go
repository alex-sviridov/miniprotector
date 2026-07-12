package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

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
