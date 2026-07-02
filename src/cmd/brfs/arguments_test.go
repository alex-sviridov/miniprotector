package main

import (
	"errors"
	"os"
	"testing"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

func testConfig() *config.Config {
	return &config.Config{DefaultPort: 8080, DefaultStreams: 4}
}

func TestParseArguments_HelpFlagDoesNotPanic(t *testing.T) {
	withArgs(t, []string{"brfs", "--help"}, func() {
		require.NotPanics(t, func() {
			_, err := parseArguments(testConfig())
			assert.True(t, errors.Is(err, errHelpRequested), "expected errHelpRequested, got: %v", err)
		})
	})
}

func TestParseArguments_JobIDFlag_ParsesValue(t *testing.T) {
	dir := t.TempDir()
	withArgs(t, []string{"brfs", dir, "--job-id", "custom-job-123"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "custom-job-123", args.JobID)
	})
}

func TestParseArguments_JobIDFlag_DefaultsEmpty(t *testing.T) {
	dir := t.TempDir()
	withArgs(t, []string{"brfs", dir}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Empty(t, args.JobID)
	})
}
