package main

import (
	"fmt"
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
	JobID      string
	Interval   time.Duration
	Due        func(PolicyState, time.Time) bool
	NextRun    func(PolicyState, time.Time) time.Time
	Background bool
}

// policies returns agent's three embedded policies, their intervals read
// from conf rather than compiled in -- bootstrap-refresh (long-lived
// credential, infrequent), operating-refresh (short-lived credential,
// frequent), and policy-update (fetches this node's applicable backup
// policies from policy-server into a local cache). Each gets a fresh
// per-invocation JobID (also embedded in Args as --job-id) every time this
// function is called -- policiesFunc calls it fresh every reconcile tick,
// the same way backupTasks already does for backup jobs, so an unused
// policy's JobID (one not actually due this tick) is simply discarded.
func policies(conf *config.Config) []Policy {
	now := time.Now()
	bootstrapJobID := policyJobID("bootstrap-refresh", now)
	operatingJobID := policyJobID("operating-refresh", now)
	policyUpdateJobID := policyJobID("policy-update", now)

	return []Policy{
		{ID: "bootstrap-refresh", Binary: "certclient", JobID: bootstrapJobID,
			Args:     []string{"renew", "--job-id", bootstrapJobID},
			Interval: time.Duration(conf.BootstrapCertRefreshIntervalSec) * time.Second},
		{ID: "operating-refresh", Binary: "certclient", JobID: operatingJobID,
			Args:     []string{"operating-refresh", "--job-id", operatingJobID},
			Interval: time.Duration(conf.OperatingCertFetchIntervalSec) * time.Second},
		{ID: "policy-update", Binary: "policyclient", JobID: policyUpdateJobID,
			Args:     []string{"fetch", "--job-id", policyUpdateJobID},
			Interval: time.Duration(conf.PolicyFetchIntervalSec) * time.Second},
	}
}

// policyJobID builds a per-invocation correlation ID for a static policy's
// exec, shaped like backup.go's backupJobID (<id>:<unix-timestamp>) so
// static and dynamic (backup) job-ids follow one convention.
func policyJobID(policyID string, now time.Time) string {
	return fmt.Sprintf("%s:%d", policyID, now.Unix())
}
