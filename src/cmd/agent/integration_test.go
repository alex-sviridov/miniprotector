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

// TestRun_BackupTaskFromRealCacheFileExecutesBrfsWithExpectedArgs proves
// the full pipeline end to end within the agent package: a real
// policies-cache.json on disk, read by the real backupTasks(), scheduled
// by the real isDue/run(), resulting in a (fake-executed) brfs invocation
// with the expected path/destination/job-id shape. This is deliberately
// not a Docker-based e2e test -- no existing harness stands up
// policy-server/bwfs together yet (see docs/superpowers/specs/
// 2026-07-10-agent-backup-execution-design.md's Testing section for the
// original proposal and this deviation's rationale) -- everything below
// the process-exec boundary (brfs -> bwfs) is already covered by
// src/e2e's existing Docker-based tests.
func TestRun_BackupTaskFromRealCacheFileExecutesBrfsWithExpectedArgs(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	policiesCachePath := filepath.Join(dir, "policies-cache.json")

	cacheJSON := `[{
		"name": "daily-db-backup",
		"type": "backup",
		"object_filters": [{"path": "/var/lib/postgres"}],
		"rpo": "1h",
		"backup_window": ["* * * * *"],
		"destinations": ["bwfs-east.internal:8080"]
	}]`
	require.NoError(t, os.WriteFile(policiesCachePath, []byte(cacheJSON), 0o644))

	conf := &config.Config{BackupWindowGraceSec: 3600}

	var capturedBinary string
	var capturedArgs []string
	fr := func(ctx context.Context, binary string, args []string) error {
		capturedBinary = binary
		capturedArgs = args
		return nil
	}

	policiesFunc := func() ([]Policy, bool) { return backupTasks(policiesCachePath, testLogger(), conf) }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr, policiesFunc, 2, nil, nil, nil)
	require.NoError(t, err)

	assert.Equal(t, "brfs", capturedBinary)
	require.Len(t, capturedArgs, 5)
	assert.Equal(t, "/var/lib/postgres", capturedArgs[0])
	assert.Equal(t, "--destination", capturedArgs[1])
	assert.Equal(t, "bwfs-east.internal:8080", capturedArgs[2])
	assert.Equal(t, "--job-id", capturedArgs[3])
	assert.Contains(t, capturedArgs[4], "backup:daily-db-backup:var-lib-postgres:")
}

func TestRun_StorageTaskFromRealCacheFileStartsAndPrunesStorageSupervisors(t *testing.T) {
	origWindow := storageStabilityWindow
	storageStabilityWindow = 20 * time.Millisecond
	defer func() { storageStabilityWindow = origWindow }()

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "agent-state.json")
	policiesCachePath := filepath.Join(dir, "policies-cache.json")

	script := filepath.Join(dir, "fake-bwfs.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntrap 'exit 0' TERM\nwhile true; do sleep 0.05; done\n"), 0o755))

	cacheJSON := `[{
		"name": "east-1-storage",
		"type": "storage",
		"port": 9400,
		"config": "{\"backend\": \"filesystem\", \"root\": \"/data/storage\"}"
	}]`
	require.NoError(t, os.WriteFile(policiesCachePath, []byte(cacheJSON), 0o644))

	// Both binary slots point at the same fake script -- this test proves
	// the reconcile-loop wiring (both tasks start, both prune together), not
	// bwfs/catalogsync's real behavior, which is covered by their own
	// packages.
	storageTasksFunc := func() ([]storageTask, bool) { return storageTasks(policiesCachePath, testLogger(), script, script) }
	mgr := newStorageManager(testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, testLogger(), cachePath, 10*time.Millisecond, realExec,
			func() ([]Policy, bool) { return nil, true }, 2, nil, storageTasksFunc, mgr)
	}()

	require.Eventually(t, func() bool {
		cache, err := readCache(cachePath)
		return err == nil &&
			cache["storage:east-1-storage"].LastSuccessAt != nil &&
			cache["storage:east-1-storage:catalogsync"].LastSuccessAt != nil
	}, time.Second, 10*time.Millisecond, "both the bwfs and catalogsync tasks must start and record success")

	// Remove the policy from the cache -- both tasks must be pruned from
	// agent-state.json and both supervisors stopped.
	require.NoError(t, os.WriteFile(policiesCachePath, []byte(`[]`), 0o644))

	require.Eventually(t, func() bool {
		cache, err := readCache(cachePath)
		return err == nil && len(cache) == 0
	}, time.Second, 10*time.Millisecond, "removed storage tasks must be pruned from agent-state.json")

	cancel()
	<-done
}
