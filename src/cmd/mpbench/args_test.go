package main

import (
	"errors"
	"flag"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseArgs_Defaults(t *testing.T) {
	a, err := parseArgs([]string{"--bin-dir", "/x"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "/x", a.BinDir)
	assert.Equal(t, 500, a.Files)
	assert.Equal(t, "mixed", a.Profile)
	assert.Equal(t, 0.3, a.DupRatio)
	assert.Equal(t, uint64(1), a.Seed)
	assert.Equal(t, time.Duration(0), a.RTT)
	assert.Equal(t, int64(0), a.Bandwidth)
	assert.Equal(t, 4, a.Streams)
	assert.Equal(t, 0, a.Window)
	assert.Equal(t, 3, a.Runs)
	assert.False(t, a.Keep)
}

func TestParseArgs_AllFlags(t *testing.T) {
	a, err := parseArgs([]string{
		"--bin-dir", "/b", "--files", "10", "--profile", "large", "--dup-ratio", "0.5",
		"--seed", "9", "--rtt", "50ms", "--bandwidth", "100mbit", "--streams", "2",
		"--window", "8", "--brfs-args", "--debug --foo bar", "--rwfs-args", "--retries 1",
		"--runs", "2", "--json", "out.json", "--keep",
	}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, 10, a.Files)
	assert.Equal(t, "large", a.Profile)
	assert.Equal(t, 0.5, a.DupRatio)
	assert.Equal(t, uint64(9), a.Seed)
	assert.Equal(t, 50*time.Millisecond, a.RTT)
	assert.Equal(t, int64(12_500_000), a.Bandwidth)
	assert.Equal(t, 2, a.Streams)
	assert.Equal(t, 8, a.Window)
	assert.Equal(t, []string{"--debug", "--foo", "bar"}, a.BrfsArgs)
	assert.Equal(t, []string{"--retries", "1"}, a.RwfsArgs)
	assert.Equal(t, 2, a.Runs)
	assert.Equal(t, "out.json", a.JSONPath)
	assert.True(t, a.Keep)
}

func TestParseArgs_Rejects(t *testing.T) {
	for name, argv := range map[string][]string{
		"no bin-dir":      {},
		"zero files":      {"--bin-dir", "/x", "--files", "0"},
		"bad profile":     {"--bin-dir", "/x", "--profile", "huge"},
		"dup below 0":     {"--bin-dir", "/x", "--dup-ratio", "-0.1"},
		"dup above 1":     {"--bin-dir", "/x", "--dup-ratio", "1.1"},
		"negative rtt":    {"--bin-dir", "/x", "--rtt", "-1s"},
		"bad bandwidth":   {"--bin-dir", "/x", "--bandwidth", "fast"},
		"zero streams":    {"--bin-dir", "/x", "--streams", "0"},
		"negative window": {"--bin-dir", "/x", "--window", "-1"},
		"zero runs":       {"--bin-dir", "/x", "--runs", "0"},
		"stray argument":  {"--bin-dir", "/x", "extra"},
	} {
		_, err := parseArgs(argv, io.Discard)
		assert.Error(t, err, name)
	}
}

func TestParseArgs_HelpIsErrHelp(t *testing.T) {
	_, err := parseArgs([]string{"-h"}, io.Discard)
	assert.True(t, errors.Is(err, flag.ErrHelp))
}

func TestParseBandwidth(t *testing.T) {
	for in, want := range map[string]int64{
		"":        0,
		"0":       0,
		"8kbit":   1_000,
		"100mbit": 12_500_000,
		"1gbit":   125_000_000,
		"10mbyte": 10_000_000,
		"2KBYTE":  2_000,
		"1.5mbit": 187_500,
	} {
		got, err := parseBandwidth(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, bad := range []string{"fast", "10", "mbit", "-5mbit", "0mbit"} {
		_, err := parseBandwidth(bad)
		assert.Error(t, err, bad)
	}
}

func TestParseArgs_SweepFlags(t *testing.T) {
	a, err := parseArgs([]string{"--bin-dir", "/x", "--sweep-window", "1,4,8", "--sweep-streams", "2, 4", "--sweep-rtt", "10ms,50ms"}, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 4, 8}, a.SweepWindows)
	assert.Equal(t, []int{2, 4}, a.SweepStreams)
	assert.Equal(t, []time.Duration{10 * time.Millisecond, 50 * time.Millisecond}, a.SweepRTTs)
}

func TestParseArgs_SweepRejectsBadLists(t *testing.T) {
	for name, argv := range map[string][]string{
		"non-numeric window": {"--bin-dir", "/x", "--sweep-window", "1,x"},
		"zero streams":       {"--bin-dir", "/x", "--sweep-streams", "0,2"},
		"negative window":    {"--bin-dir", "/x", "--sweep-window", "-1"},
		"bad rtt":            {"--bin-dir", "/x", "--sweep-rtt", "fast"},
		"negative rtt":       {"--bin-dir", "/x", "--sweep-rtt", "-5ms"},
		"empty element":      {"--bin-dir", "/x", "--sweep-window", "1,,2"},
	} {
		_, err := parseArgs(argv, io.Discard)
		assert.Error(t, err, name)
	}
}
