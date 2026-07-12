package main

import (
	"testing"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPolicies_IncludesPolicyUpdateWithConfiguredInterval(t *testing.T) {
	conf := &config.Config{PolicyFetchIntervalSec: 1234}
	pols := policies(conf)

	var found *Policy
	for i := range pols {
		if pols[i].ID == "policy-update" {
			found = &pols[i]
		}
	}
	require.NotNil(t, found, "policies() must include a policy-update entry")
	assert.Equal(t, "policyclient", found.Binary)
	assert.Equal(t, "fetch", found.Args[0])
	assert.Contains(t, found.Args, "--job-id")
	assert.Contains(t, found.Args, found.JobID)
	assert.Equal(t, 1234*time.Second, found.Interval)
}

func TestPolicies_StillIncludesExistingCertPolicies(t *testing.T) {
	conf := &config.Config{BootstrapCertRefreshIntervalSec: 86400, OperatingCertFetchIntervalSec: 900}
	pols := policies(conf)

	ids := make([]string, len(pols))
	for i, p := range pols {
		ids[i] = p.ID
	}
	assert.Contains(t, ids, "bootstrap-refresh")
	assert.Contains(t, ids, "operating-refresh")
	assert.Len(t, pols, 3)
}

func TestPolicies_EachStaticPolicyGetsADistinctJobID(t *testing.T) {
	conf := &config.Config{
		BootstrapCertRefreshIntervalSec: 86400,
		OperatingCertFetchIntervalSec:   900,
		PolicyFetchIntervalSec:          900,
	}
	all := policies(conf)
	require.Len(t, all, 3)

	seen := make(map[string]bool)
	for _, p := range all {
		assert.NotEmpty(t, p.JobID, "policy %s must have a JobID", p.ID)
		assert.False(t, seen[p.JobID], "job IDs must be distinct across policies in the same call")
		seen[p.JobID] = true
		assert.Contains(t, p.Args, "--job-id", "policy %s's Args must include --job-id")
		assert.Contains(t, p.Args, p.JobID, "policy %s's Args must carry the same JobID exposed on the struct")
	}
}
