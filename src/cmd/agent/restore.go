// restore.go derives agent's dynamic "restore verification" tasks from
// policies-cache.json -- one task per cached "restore" policy, one-shot:
// due until it succeeds once, never again after. See
// docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// RestoreRule mirrors policyclient's on-disk RestoreRule (cmd/policyclient/
// fetch.go) that agent needs. agent can't import cmd/policyclient directly
// -- Go forbids importing another command's main package -- so this field
// set is duplicated here, the same way backup.go's ObjectFilter already is.
type RestoreRule struct {
	Host      string `json:"host"`
	Path      string `json:"path"`
	Include   bool   `json:"include"`
	DestPath  string `json:"dest_path,omitempty"`
	NotBefore int64  `json:"not_before,omitempty"`
	NotAfter  int64  `json:"not_after,omitempty"`
}

// restoreTaskID is the stable identifier for one restore policy's task in
// agent-state.json -- one task per policy (not per host, unlike backup's
// per-object-filter-path tasks -- a restore policy's rules aren't cleanly
// partitionable by host, since a folder rule can be host-agnostic).
// mode == "restore" gets the restore: prefix (rwfs restore, which creates
// directory structure and writes file content); every other mode (unset
// or "verify") gets the verify: prefix
// (rwfs verify, unchanged behavior, renamed from this ID's original
// restore: prefix now that a second kind of restore-policy task exists).
func restoreTaskID(policyName, mode string) string {
	if mode == "restore" {
		return fmt.Sprintf("restore:%s", policyName)
	}
	return fmt.Sprintf("verify:%s", policyName)
}

// restoreJobID is the --job-id passed to the dispatched rwfs subcommand
// for one run -- includes a timestamp so a retry after failure gets a
// distinct id, mirroring backup.go's backupJobID. Same prefix convention
// as restoreTaskID.
func restoreJobID(policyName, mode string, now time.Time) string {
	if mode == "restore" {
		return fmt.Sprintf("restore:%s:%d", policyName, now.Unix())
	}
	return fmt.Sprintf("verify:%s:%d", policyName, now.Unix())
}

// rulesStdinPayload is the JSON shape piped to `rwfs verify --rules-stdin`
// / `rwfs restore --rules-stdin` -- {"rules": [...]}, matching
// policy-server's RestorePolicy.Rules field name exactly (see
// docs/superpowers/specs/2026-08-10-restore-policy-verification-design.md's
// §4).
type rulesStdinPayload struct {
	Rules []RestoreRule `json:"rules"`
}

// restoreTasks derives one Policy per cached "restore" policy from
// policiesCachePath, valid at the instant it's called -- callers that need
// to notice policies-cache.json changing over time (agent serve's
// reconcile loop) must call this fresh every tick, exactly like
// backupTasks/storageTasks.
//
// ok=false mirrors backupTasks's contract: it means this tick's read of
// policiesCachePath failed, and callers must never treat that as "there
// are zero restore tasks."
//
// A policy whose Destinations is empty (its storage policy has no live
// checkins yet, or storage_policy_id is dangling) contributes no task --
// rather than exec'ing rwfs against an empty target, which would fail
// loudly anyway but with a less useful error than simply not trying.
//
// A policy whose Rules is empty contributes no task either: agent doesn't
// trust the cache blindly. policy-server rejects a rules-less restore
// policy at write time, but a cache file hand-edited or left over from an
// older schema could still carry one, and an empty rule set would select
// zero files and "succeed" -- which this one-shot task would then record
// as permanently done without having done anything. (rwfs rejects that
// payload itself too; see its parseRulesStdin.)
//
// Each skip is logged with the policy name. A disabled policy is skipped
// the same way backup/storage policies already are.
//
// p.Mode == "restore" dispatches `rwfs restore` (creates the resolved
// directory structure and writes the resolved file content -- see
// docs/superpowers/specs/2026-08-17-restore-file-content-design.md),
// with --overwrite appended iff p.Overwrite. Every other mode (unset or
// "verify") dispatches `rwfs verify`, byte-for-byte what this policy type
// has always run.
func restoreTasks(policiesCachePath string, logger *slog.Logger) ([]Policy, bool) {
	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []Policy
	for _, p := range cachedPolicies {
		if p.Type != "restore" {
			continue
		}
		if p.disabled(time.Now()) {
			continue
		}
		taskID := restoreTaskID(p.Name, p.Mode)
		if len(p.Destinations) == 0 {
			logger.Error("restore policy has no resolved destination, skipping", "policy", taskID)
			continue
		}
		if len(p.Rules) == 0 {
			logger.Error("restore policy has no rules, skipping", "policy", taskID)
			continue
		}

		payload, err := json.Marshal(rulesStdinPayload{Rules: p.Rules})
		if err != nil {
			logger.Error("restore policy rules failed to marshal, skipping", "policy", taskID, "error", err)
			continue
		}

		jobID := restoreJobID(p.Name, p.Mode, time.Now())
		args := []string{"verify", p.Destinations[0], "--rules-stdin", "--job-id", jobID}
		if p.Mode == "restore" {
			args = []string{"restore", p.Destinations[0], "--rules-stdin", "--job-id", jobID}
			if p.Overwrite {
				args = append(args, "--overwrite")
			}
		}

		tasks = append(tasks, Policy{
			ID:         taskID,
			Binary:     "rwfs",
			JobID:      jobID,
			Args:       args,
			Stdin:      payload,
			Background: true,
			Due: func(s PolicyState, now time.Time) bool {
				return s.LastSuccessAt == nil
			},
		})
	}
	return tasks, true
}
