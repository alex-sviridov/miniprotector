// rwfs_exec_test.go closes the one gap no in-package test can: agent hands
// rwfs an argv it composes itself (restore.go's Args/Stdin), and every test
// on both sides of that boundary uses a fake runner or calls rwfs's Go
// functions directly -- so an argv rwfs's *CLI* rejects (a flag it never
// registered, say) passes every unit test and still fails 100% of the time
// in production. This test builds the real rwfs binary and feeds it the real
// argv, asserting only that it gets past cobra's argument parsing. What
// happens after that (connecting, listing, verifying) is deliberately out of
// scope -- it needs a live bwfs, which src/e2e's Docker tests cover.
package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// buildRwfs builds cmd/rwfs into a temp dir and returns the binary path.
// The test's working directory is its own package dir (src/cmd/agent), so
// rwfs is a sibling at ../rwfs.
func buildRwfs(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available; cannot build the real rwfs binary")
	}
	bin := filepath.Join(t.TempDir(), "rwfs")
	build := exec.Command("go", "build", "-o", bin, "../rwfs")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "building rwfs must succeed: %s", out)
	return bin
}

// rwfsConfigDir writes the minimum local.conf rwfs needs to get as far as
// argument parsing (config is read first, and missing required fields abort
// before any flag is looked at). No certs are written: the run is expected
// to fail at connect time, which is well past the point this test cares
// about.
func rwfsConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	conf := "default_port = 8080\ndefault_streams = 4\nlog_dir = " + filepath.Join(dir, "logs") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "local.conf"), []byte(conf), 0o644))
	return dir
}

func TestRestoreTask_RealRwfsBinaryAcceptsTheArgvAgentProduces(t *testing.T) {
	bin := buildRwfs(t)

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "policies-cache.json")
	writeCachedPoliciesJSON(t, cachePath, []cachedPolicy{
		{
			Name: "web01-emergency", Type: "restore",
			// Nothing listens here; connecting is expected to fail.
			Destinations: []string{"127.0.0.1:65534"},
			Rules: []RestoreRule{
				{Host: "web-01", Path: "/var/www/index.html", Include: true},
				{Path: "/var/www/assets", Include: true},
			},
		},
	})

	tasks, ok := restoreTasks(cachePath, testLogger())
	require.True(t, ok)
	require.Len(t, tasks, 1)
	task := tasks[0]
	require.Equal(t, "rwfs", task.Binary, "this test only makes sense for an rwfs task")

	cmd := exec.Command(bin, task.Args...)
	cmd.Stdin = bytes.NewReader(task.Stdin)
	cmd.Env = append(os.Environ(), config.ConfigPathEnvVar+"="+rwfsConfigDir(t))
	out, err := cmd.CombinedOutput()
	output := string(out)

	// It must fail -- there is no bwfs at that address -- but for the right
	// reason. A cobra-level rejection (unknown flag, unknown command, bad
	// positional count) is the failure mode this test exists to catch.
	require.Error(t, err, "rwfs cannot succeed against an unreachable target; output: %s", output)
	assert.NotContains(t, output, "unknown flag", "rwfs must register every flag agent passes; output: %s", output)
	assert.NotContains(t, output, "unknown command")
	assert.NotContains(t, output, "Arguments error", "argv agent produces must parse cleanly; output: %s", output)
	assert.NotContains(t, output, "Configuration error")

	// Proof it got all the way into runVerify with the argv intact: rwfs
	// stamps every log line with the --job-id it was handed.
	assert.Contains(t, output, "job_id="+task.JobID, "rwfs must adopt agent's --job-id as its correlation id; output: %s", output)
	assert.Contains(t, output, "Verify failed", "the only expected failure is the (unreachable) bwfs connection; output: %s", output)
}
