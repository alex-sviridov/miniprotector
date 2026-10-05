package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPhaseResult_ComputesRates(t *testing.T) {
	p := newPhaseResult("backup-cold", 2, 10_000_000, 100, 1500, 500)
	assert.Equal(t, 5.0, p.MBPerSec)
	assert.Equal(t, 50.0, p.FilesPerSec)
	assert.Equal(t, int64(1500), p.WireUp)
	assert.Equal(t, int64(500), p.WireDown)
}

func TestNewPhaseResult_ZeroSecondsDoesNotDivideByZero(t *testing.T) {
	p := newPhaseResult("x", 0, 100, 1, 0, 0)
	assert.Zero(t, p.MBPerSec)
	assert.Zero(t, p.FilesPerSec)
}

func TestMedian(t *testing.T) {
	assert.Equal(t, 3.0, median([]float64{5, 1, 3}))
	assert.Equal(t, 2.5, median([]float64{4, 1, 2, 3}))
	assert.Equal(t, 7.0, median([]float64{7}))
	assert.Zero(t, median(nil))
}

func mkRun(idx int, cold, warm, restore float64) RunResult {
	return RunResult{Index: idx, DatasetFiles: 10, DatasetBytes: 1_000_000, Phases: []PhaseResult{
		newPhaseResult("backup-cold", cold, 1_000_000, 10, 1000, 100),
		newPhaseResult("backup-warm", warm, 1_000_000, 10, 100, 100),
		newPhaseResult("restore", restore, 1_000_000, 10, 50, 1000),
	}}
}

func TestSummarize_MedianMinMaxPerPhaseInRunOrder(t *testing.T) {
	sum := Summarize([]RunResult{mkRun(1, 3, 1, 2), mkRun(2, 1, 1, 4), mkRun(3, 2, 1, 6)})

	require.Len(t, sum, 3)
	assert.Equal(t, "backup-cold", sum[0].Name)
	assert.Equal(t, 3, sum[0].Runs)
	assert.Equal(t, 2.0, sum[0].MedianSeconds)
	assert.Equal(t, 1.0, sum[0].MinSeconds)
	assert.Equal(t, 3.0, sum[0].MaxSeconds)
	assert.Equal(t, 0.5, sum[0].MedianMBPerSec) // 1MB / 2s
	assert.Equal(t, int64(1000), sum[0].MedianWireUp)
	assert.Equal(t, int64(1_000_000), sum[0].PayloadBytes)
	assert.Equal(t, "restore", sum[2].Name)
	assert.Equal(t, 4.0, sum[2].MedianSeconds)
}

func TestSummarize_NoRuns(t *testing.T) {
	assert.Empty(t, Summarize(nil))
}

func TestHumanBytes(t *testing.T) {
	assert.Equal(t, "0 B", humanBytes(0))
	assert.Equal(t, "999 B", humanBytes(999))
	assert.Equal(t, "1.5 kB", humanBytes(1500))
	assert.Equal(t, "2.0 MB", humanBytes(2_000_000))
	assert.Equal(t, "3.5 GB", humanBytes(3_500_000_000))
}

func TestWriteTable_ContainsPhasesAndHeader(t *testing.T) {
	var buf bytes.Buffer
	WriteTable(&buf, "dataset: 10 files", Summarize([]RunResult{mkRun(1, 2, 1, 4)}))
	out := buf.String()
	assert.Contains(t, out, "dataset: 10 files")
	for _, want := range []string{"phase", "backup-cold", "backup-warm", "restore", "MB/s", "wire up", "wire down"} {
		assert.Contains(t, out, want)
	}
}

func variantOf(label string, a Args, runs ...RunResult) VariantResult {
	return VariantResult{Label: label, Args: a, Runs: runs, Summary: Summarize(runs)}
}

func TestWriteComparison_ShowsDeltaVersusFirstVariant(t *testing.T) {
	base := variantOf("window=1", Args{}, mkRun(1, 10, 4, 2))
	fast := variantOf("window=8", Args{}, mkRun(1, 5, 1, 2))

	var buf bytes.Buffer
	WriteComparison(&buf, []VariantResult{base, fast})
	out := buf.String()

	for _, want := range []string{"window=1", "window=8", "backup-cold", "10.00s", "5.00s (-50%)", "1.00s (-75%)", "2.00s (+0%)"} {
		assert.Contains(t, out, want)
	}
}

func TestWriteComparison_SingleVariantPrintsNothing(t *testing.T) {
	var buf bytes.Buffer
	WriteComparison(&buf, []VariantResult{variantOf("run", Args{}, mkRun(1, 1, 1, 1))})
	assert.Empty(t, buf.String())
}

func TestBuildReportAndWriteJSON_RoundTrips(t *testing.T) {
	bin := t.TempDir()
	for _, n := range []string{"brfs", "bwfs", "rwfs"} {
		require.NoError(t, os.WriteFile(filepath.Join(bin, n), []byte("binary "+n), 0o755))
	}
	a := &Args{BinDir: bin, Files: 10, Profile: "mixed", DupRatio: 0.3, Seed: 1, Runs: 2}
	v1 := variantOf("window=1", Args{Files: 10, RTT: 50 * time.Millisecond, Bandwidth: 12_500_000, Streams: 4, Window: 1, Runs: 2, BrfsArgs: []string{"--debug"}},
		mkRun(1, 2, 1, 4), mkRun(2, 3, 1, 5))
	v8 := variantOf("window=8", Args{RTT: 50 * time.Millisecond, Streams: 4, Window: 8, Runs: 2},
		mkRun(1, 1, 1, 4), mkRun(2, 1, 1, 5))

	rep, err := BuildReport(a, time.Unix(1_700_000_000, 0), []VariantResult{v1, v8})
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "out.json")
	require.NoError(t, WriteJSON(path, rep))

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var got Report
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, 10, got.Dataset.Files)
	assert.Equal(t, int64(1_000_000), got.Dataset.Bytes)
	assert.Len(t, got.Binaries, 3)
	assert.Len(t, got.Binaries["brfs"], 64, "sha256 hex")

	require.Len(t, got.Variants, 2)
	assert.Equal(t, "window=1", got.Variants[0].Label)
	assert.Equal(t, 50.0, got.Variants[0].Config.RTTMillis)
	assert.Equal(t, 1, got.Variants[0].Config.Window)
	assert.Equal(t, 8, got.Variants[1].Config.Window)
	assert.Equal(t, []string{"--debug"}, got.Variants[0].Config.BrfsArgs)
	assert.Equal(t, 10, got.Variants[0].Config.Files)
	assert.Len(t, got.Variants[0].Runs, 2)
	assert.Len(t, got.Variants[0].Summary, 3)
}

func TestBuildReport_MissingBinaryIsAnError(t *testing.T) {
	_, err := BuildReport(&Args{BinDir: t.TempDir()}, time.Now(), nil)
	assert.Error(t, err)
}
