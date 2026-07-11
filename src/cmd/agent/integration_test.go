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
		"object_filters": ["/var/lib/postgres"],
		"rpo": "1h",
		"backup_window": ["* * * * *"],
		"destination": "bwfs-east.internal:8080"
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

	policiesFunc := func() ([]Policy, bool) { return backupTasks(policiesCachePath, conf) }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := run(ctx, testLogger(), cachePath, 5*time.Millisecond, fr, policiesFunc, 2)
	require.NoError(t, err)

	assert.Equal(t, "brfs", capturedBinary)
	require.Len(t, capturedArgs, 5)
	assert.Equal(t, "/var/lib/postgres", capturedArgs[0])
	assert.Equal(t, "--destination", capturedArgs[1])
	assert.Equal(t, "bwfs-east.internal:8080", capturedArgs[2])
	assert.Equal(t, "--job-id", capturedArgs[3])
	assert.Contains(t, capturedArgs[4], "backup:daily-db-backup:var-lib-postgres:")
}
