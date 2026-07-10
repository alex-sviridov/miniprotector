package main

import (
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
)

// Policy is a single reconcilable unit: run Binary with Args whenever more
// than Interval has elapsed since the last successful run.
type Policy struct {
	ID       string
	Binary   string
	Args     []string
	Interval time.Duration
}

// policies returns agent's three embedded policies, their intervals read
// from conf rather than compiled in -- bootstrap-refresh (long-lived
// credential, infrequent), operating-refresh (short-lived credential,
// frequent), and policy-update (fetches this node's applicable backup
// policies from policy-server into a local cache; nothing yet acts on that
// cache -- see docs/superpowers/specs/2026-07-10-agent-policy-update-job-design.md).
func policies(conf *config.Config) []Policy {
	return []Policy{
		{ID: "bootstrap-refresh", Binary: "certclient", Args: []string{"renew"},
			Interval: time.Duration(conf.BootstrapCertRefreshIntervalSec) * time.Second},
		{ID: "operating-refresh", Binary: "certclient", Args: []string{"operating-refresh"},
			Interval: time.Duration(conf.OperatingCertFetchIntervalSec) * time.Second},
		{ID: "policy-update", Binary: "policyclient", Args: []string{"fetch"},
			Interval: time.Duration(conf.PolicyFetchIntervalSec) * time.Second},
	}
}
