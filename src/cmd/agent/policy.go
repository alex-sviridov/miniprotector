package main

import (
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
)

// Policy is a single reconcilable unit. By default (Due == nil), it's due
// once more than Interval has elapsed since the last successful run --
// agent's original behavior, unchanged for bootstrap-refresh,
// operating-refresh, and policy-update. A non-nil Due overrides that
// check entirely (see backup.go's backupTasks, whose window+RPO due-check
// doesn't fit a bare interval). NextRun is the equivalent override for
// list-policies' display only (see list.go's estimatedNextRun).
// Background, when true, makes run() execute this policy in a goroutine
// instead of synchronously in the reconcile loop (see reconcile.go).
type Policy struct {
	ID         string
	Binary     string
	Args       []string
	Interval   time.Duration
	Due        func(PolicyState, time.Time) bool
	NextRun    func(PolicyState, time.Time) time.Time
	Background bool
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
