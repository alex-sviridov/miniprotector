package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	old := os.Args
	os.Args = args
	defer func() { os.Args = old }()
	fn()
}

func TestParseArguments_RenewJobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"certclient", "renew", "--job-id", "custom-job-123"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "custom-job-123", args.JobID)
	})
}

func TestParseArguments_RenewJobIDFlag_DefaultsEmpty(t *testing.T) {
	withArgs(t, []string{"certclient", "renew"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Empty(t, args.JobID)
	})
}

func TestParseArguments_OperatingRefreshJobIDFlag_ParsesValue(t *testing.T) {
	withArgs(t, []string{"certclient", "operating-refresh", "--job-id", "custom-job-456"}, func() {
		args, err := parseArguments()
		require.NoError(t, err)
		assert.Equal(t, "custom-job-456", args.JobID)
	})
}
