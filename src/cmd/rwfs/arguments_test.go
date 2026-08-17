package main

import (
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
	return &config.Config{DefaultPort: 8080}
}

func TestParseArguments_ListWithBwfsTargetOnly(t *testing.T) {
	withArgs(t, []string{"rwfs", "list", "localhost:8080"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "list", args.Action)
		assert.Equal(t, "localhost", args.BwfsHost)
		assert.Equal(t, 8080, args.BwfsPort)
		assert.Equal(t, "", args.PathFilter)
	})
}

func TestParseArguments_ListWithServerAndPath(t *testing.T) {
	withArgs(t, []string{"rwfs", "list", "myhost:/home/user", "localhost:8080"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "myhost", args.ServerName)
		assert.Equal(t, "/home/user", args.PathFilter)
		assert.Equal(t, "localhost", args.BwfsHost)
		assert.Equal(t, 8080, args.BwfsPort)
	})
}

func TestParseArguments_MissingBwfsTargetErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "list"}, func() {
		_, err := parseArguments(testConfig())
		assert.Error(t, err)
	})
}

func TestParseArguments_InvalidOutputErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "list", "localhost:8080", "--output", "xml"}, func() {
		_, err := parseArguments(testConfig())
		assert.Error(t, err)
	})
}

func TestParseArguments_ListJobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"rwfs", "list", "localhost:8080", "--job-id", "custom-job-123"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "custom-job-123", args.JobID)
	})
}

func TestParseArguments_ListJobIDFlag_DefaultsEmpty(t *testing.T) {
	withArgs(t, []string{"rwfs", "list", "localhost:8080"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Empty(t, args.JobID, "an omitted --job-id stays empty here; main.go resolves it via jobid.Resolve")
	})
}

func TestParseArguments_VerifyJobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"rwfs", "verify", "localhost:8080", "--rules-stdin", "--job-id", "restore:cart-1:1754870000"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "restore:cart-1:1754870000", args.JobID)
	})
}

func TestParseArguments_VerifyJobIDFlag_DefaultsEmpty(t *testing.T) {
	withArgs(t, []string{"rwfs", "verify", "localhost:8080"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Empty(t, args.JobID)
	})
}

func TestParseArguments_VerifyRulesStdinLeavesServerNameEmpty(t *testing.T) {
	withArgs(t, []string{"rwfs", "verify", "localhost:8080", "--rules-stdin"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.True(t, args.RulesStdin)
		assert.Equal(t, "", args.ServerName, "rules-stdin must not default to the local hostname")
		assert.Equal(t, "", args.PathFilter)
	})
}

func TestParseArguments_VerifyWithoutRulesStdinStillDefaultsServerNameToLocalHostname(t *testing.T) {
	withArgs(t, []string{"rwfs", "verify", "localhost:8080"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.False(t, args.RulesStdin)
		assert.NotEqual(t, "", args.ServerName, "existing behavior must be unchanged when the flag is absent")
	})
}

func TestParseArguments_RestoreWithoutRulesStdinErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080"}, func() {
		_, err := parseArguments(testConfig())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "restore requires --rules-stdin")
	})
}

func TestParseArguments_RestoreWithRulesStdinParsesOverwriteFlag(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin", "--overwrite"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, "restore", args.Action)
		assert.True(t, args.RulesStdin)
		assert.True(t, args.Overwrite)
	})
}

func TestParseArguments_RestoreOverwriteDefaultsFalse(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.False(t, args.Overwrite)
	})
}

func TestParseArguments_RestoreStreamsFlag_DefaultsToFour(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin"}, func() {
		args, err := parseArguments(testConfig())
		require.NoError(t, err)
		assert.Equal(t, 4, args.Streams)
	})
}

func TestParseArguments_RestoreInvalidStreamsErrors(t *testing.T) {
	withArgs(t, []string{"rwfs", "restore", "localhost:8080", "--rules-stdin", "--streams", "0"}, func() {
		_, err := parseArguments(testConfig())
		assert.Error(t, err)
	})
}
