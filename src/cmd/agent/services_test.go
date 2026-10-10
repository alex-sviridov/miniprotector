package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunningServiceTasks(t *testing.T) {
	tasks := runningServiceTasks([]config.RunningService{
		{Binary: "listener", Args: []string{"--debug=false"}},
		{Binary: "catalog", Args: []string{"/data"}},
	})
	require.Len(t, tasks, 2)
	assert.Equal(t, "service:listener", tasks[0].ID)
	assert.Equal(t, []string{"--debug=false"}, tasks[0].Args)
	assert.Equal(t, "service:catalog", tasks[1].ID)
	assert.True(t, filepath.Base(tasks[1].Binary) == "catalog", "binary resolved from the bare name: %s", tasks[1].Binary)
}

// markerScript writes its PID line to marker each time it starts, then idles
// until SIGTERM.
func markerScript(t *testing.T, dir, marker string) string {
	t.Helper()
	script := filepath.Join(dir, "svc.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\necho started >> "+marker+"\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"))
	return script
}

func lines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}

func TestSuperviseRunningServices_WaitsForOperatingCertificate(t *testing.T) {
	dir := t.TempDir()
	certs := filepath.Join(dir, "certs")
	require.NoError(t, os.MkdirAll(certs, 0o755))
	marker := filepath.Join(dir, "marker")
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer mgr.StopAll()

	done := make(chan struct{})
	go func() {
		superviseRunningServices(ctx, testLogger(), mgr, certs, []storageTask{{ID: "service:svc", Binary: markerScript(t, dir, marker)}}, 10*time.Millisecond)
		close(done)
	}()

	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, 0, lines(t, marker), "must not start before client.crt exists")

	require.NoError(t, os.WriteFile(filepath.Join(certs, "client.crt"), []byte("x"), 0o600))
	require.Eventually(t, func() bool { return lines(t, marker) == 1 }, 2*time.Second, 10*time.Millisecond)
	<-done
}

func TestSuperviseRunningServices_ReturnsWhenContextCancelledBeforeCertificate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		superviseRunningServices(ctx, testLogger(), newStorageManager(testLogger()), t.TempDir(), nil, 10*time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("did not return after cancel")
	}
}

func TestSuperviseRunningServices_RestartsACrashingService(t *testing.T) {
	dir := t.TempDir()
	certs := filepath.Join(dir, "certs")
	require.NoError(t, os.MkdirAll(certs, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(certs, "client.crt"), []byte("x"), 0o600))
	marker := filepath.Join(dir, "marker")
	script := filepath.Join(dir, "crash.sh")
	require.NoError(t, osWriteExecutable(t, script, "#!/bin/sh\necho started >> "+marker+"\nexit 1\n"))

	mgr := newStorageManager(testLogger())
	mgr.backoff = backoffPolicy{Base: 10 * time.Millisecond, Max: 20 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer mgr.StopAll()

	superviseRunningServices(ctx, testLogger(), mgr, certs, []storageTask{{ID: "service:crash", Binary: script}}, 10*time.Millisecond)
	require.Eventually(t, func() bool { return lines(t, marker) >= 3 }, 3*time.Second, 20*time.Millisecond, "a crashing service must be restarted repeatedly")
}

func TestStorageManager_ReconcileAcceptsNilReconcileState(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	mgr := newStorageManager(testLogger())
	mgr.stabilityWindow = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer mgr.StopAll()

	mgr.reconcile(ctx, nil, []storageTask{{ID: "service:svc", Binary: markerScript(t, dir, marker)}})
	require.Eventually(t, func() bool { return lines(t, marker) == 1 }, 2*time.Second, 10*time.Millisecond)
}

// A crashed listener or control-plane service takes its whole host's gRPC
// surface down, so running services restart within about a second -- unlike
// policy-derived tasks, whose 30s base backoff suits a flapping backup job.
func TestRunningServicesManager_RestartsQuicklyButStaysBounded(t *testing.T) {
	mgr := newRunningServicesManager(testLogger())
	assert.Equal(t, time.Second, mgr.backoff.Base)
	assert.LessOrEqual(t, mgr.backoff.Max, time.Minute)
	assert.Less(t, mgr.backoff.Base, defaultBackoffPolicy.Base)
}
