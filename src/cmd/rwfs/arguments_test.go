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
