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
	assert.Equal(t, []string{"fetch"}, found.Args)
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
