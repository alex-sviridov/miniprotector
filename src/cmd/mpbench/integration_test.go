//go:build integration

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildBinaries(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"brfs", "bwfs", "rwfs"} {
		out, err := exec.Command("go", "build", "-o", filepath.Join(dir, name), "../"+name).CombinedOutput()
		require.NoError(t, err, string(out))
	}
	return dir
}

// One full cycle with the real binaries: cold backup, warm backup, restore,
// then a byte comparison of the restored tree (done inside RunOnce).
func TestIntegration_FullCycle(t *testing.T) {
	bin := buildBinaries(t)
	a := &Args{BinDir: bin, Files: 30, Profile: "mixed", DupRatio: 0.5, Seed: 7, Streams: 2, Window: 4, Runs: 1}

	res, err := RunOnce(context.Background(), a, 1, t.Logf)
	require.NoError(t, err)

	require.Len(t, res.Phases, 3)
	cold, warm, restore := res.Phases[0], res.Phases[1], res.Phases[2]
	assert.Equal(t, "backup-cold", cold.Name)
	assert.Equal(t, "backup-warm", warm.Name)
	assert.Equal(t, "restore", restore.Name)

	for _, p := range res.Phases {
		assert.Positive(t, p.Seconds, p.Name)
		assert.Positive(t, p.WireUp+p.WireDown, p.Name)
		assert.Positive(t, p.ClientRSS, p.Name)
		assert.Positive(t, p.ServerRSS, p.Name)
	}
	assert.Less(t, warm.WireUp*3, cold.WireUp, "warm backup sends hashes only, cold sends data")
	assert.Greater(t, restore.WireDown, restore.WireUp, "restore is mostly server to client")
}

func TestIntegration_LatencySlowsBackupMoreThanLAN(t *testing.T) {
	bin := buildBinaries(t)
	lan := Args{BinDir: bin, Files: 20, Profile: "small", DupRatio: 0, Seed: 3, Streams: 1, Window: 1, Runs: 1}
	wan := lan
	wan.RTT = 20 * time.Millisecond

	l, err := RunOnce(context.Background(), &lan, 1, t.Logf)
	require.NoError(t, err)
	w, err := RunOnce(context.Background(), &wan, 1, t.Logf)
	require.NoError(t, err)

	assert.Greater(t, w.Phases[0].Seconds, l.Phases[0].Seconds*2, "stop-and-wait must feel the RTT")
}
