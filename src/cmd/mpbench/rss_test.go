package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeakRSS_ReadsOwnProcess(t *testing.T) {
	n, err := peakRSS(os.Getpid())
	require.NoError(t, err)
	assert.Greater(t, n, int64(1<<20), "a Go test process uses more than 1 MiB")
}

func TestPeakRSS_ResetDropsThePeak(t *testing.T) {
	big := make([]byte, 64<<20)
	for i := range big {
		big[i] = 1
	}
	high, err := peakRSS(os.Getpid())
	require.NoError(t, err)
	runtime.KeepAlive(big)
	big = nil
	debug.FreeOSMemory()

	require.NoError(t, resetPeakRSS(os.Getpid()))
	after, err := peakRSS(os.Getpid())
	require.NoError(t, err)
	assert.Less(t, after, high, "the high-water mark restarts from the current RSS")
}

func TestPeakRSS_MissingProcess(t *testing.T) {
	_, err := peakRSS(1 << 30)
	assert.Error(t, err)
}

func TestRunTool_ReportsChildPeakRSS(t *testing.T) {
	rss, err := runTool(context.Background(), filepath.Join(t.TempDir(), "log"), "/bin/true", nil, nil, "")
	require.NoError(t, err)
	assert.Greater(t, rss, int64(0))
}
