// backup.go derives agent's dynamic "backup task" policies from
// policies-cache.json (written by policyclient's policy-update job) --
// one task per (cached policy, object_filters path) pair, due when a
// backup_window cron slot is open and that path's rpo has elapsed. See
// docs/superpowers/specs/2026-07-10-agent-backup-execution-design.md.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/robfig/cron/v3"
)

// ObjectFilter mirrors the subset of policyclient's on-disk ObjectFilter
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type ObjectFilter struct {
	ID      string   `json:"id"`
	Path    string   `json:"path"`
	Include []string `json:"include,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// cachedPolicy mirrors the subset of policyclient's on-disk CachedPolicy
// schema (cmd/policyclient/fetch.go) that agent needs. agent can't import
// cmd/policyclient directly -- Go forbids importing another command's
// main package -- so these fields are duplicated here rather than shared.
type cachedPolicy struct {
	Name          string         `json:"name"`
	Type          string         `json:"type"`
	ObjectFilters []ObjectFilter `json:"object_filters"`
	RPO           string         `json:"rpo"`
	BackupWindow  []string       `json:"backup_window"`
	Destinations  []string       `json:"destinations"`
	// Storage-policy-only fields, zero/empty for a backup policy -- see
	// storage.go's storageTasks, the consumer that reads these.
	Port   int32  `json:"port,omitempty"`
	Config string `json:"config,omitempty"`
	// "restore" policy only, empty/false for every other type.
	Rules     []RestoreRule `json:"rules,omitempty"`
	Mode      string        `json:"mode,omitempty"`
	Overwrite bool          `json:"overwrite,omitempty"`
	// DisabledAt is used by both backup and storage policies -- see backup.go's
	// backupTasks and storage.go's storageTasks, which both skip policies with
	// disabled_at in the past.
	DisabledAt time.Time `json:"disabled_at,omitempty"`
}

// disabled reports whether p's DisabledAt has been set and has passed as of
// now -- the same predicate server.go's isDisabled uses server-side. The
// check lives here, inside backupTasks/storageTasks themselves, rather than
// in isDue or reconcile.go -- a disabled policy must contribute zero tasks
// in the first place, so reconcile.go's existing prune() removes its state
// the same way a deleted policy's already is, with no dedicated
// disabled-handling code of its own (see
// TestRun_DisabledPolicyPrunedViaBackupTasks).
func (p cachedPolicy) disabled(now time.Time) bool {
	return !p.DisabledAt.IsZero() && !p.DisabledAt.After(now)
}

// readCachedPolicies reads policiesCachePath, returning ok=false if the
// file is missing or unparseable -- distinct from a confirmed-good read
// that happens to list zero policies (ok=true, nil slice). Callers that
// prune state derived from this list (see reconcile.go's prune) rely on
// this distinction: a transient read failure must never be mistaken for
// "every policy was removed."
func readCachedPolicies(policiesCachePath string) ([]cachedPolicy, bool) {
	data, err := os.ReadFile(policiesCachePath)
	if err != nil {
		return nil, false
	}
	var policies []cachedPolicy
	if err := json.Unmarshal(data, &policies); err != nil {
		return nil, false
	}
	return policies, true
}

// parseSchedules parses each cron expression independently -- one
// malformed entry is dropped, not treated as invalidating the rest of the
// list, mirroring policy-server's own "skip the bad file, keep the good
// ones" direction (cmd/policy-server/cache.go's Reload).
func parseSchedules(exprs []string) []cron.Schedule {
	var out []cron.Schedule
	for _, expr := range exprs {
		sched, err := cron.ParseStandard(expr)
		if err != nil {
			continue
		}
		out = append(out, sched)
	}
	return out
}

// windowOpen reports whether any schedule fired within the last grace
// window ending at now -- i.e., a trigger occurred and the window hasn't
// closed yet. schedule.Next(t) returns the first activation strictly
// after t, so checking it against now-grace catches any trigger from that
// point forward, up to and including now.
func windowOpen(schedules []cron.Schedule, now time.Time, grace time.Duration) bool {
	threshold := now.Add(-grace)
	for _, s := range schedules {
		if !s.Next(threshold).After(now) {
			return true
		}
	}
	return false
}

// nextWindow returns the soonest upcoming trigger across all schedules,
// strictly after now. Only meaningful when the task is not currently due
// -- see list.go's estimatedNextRun, which checks isDue first.
func nextWindow(schedules []cron.Schedule, now time.Time) time.Time {
	var next time.Time
	for _, s := range schedules {
		t := s.Next(now)
		if next.IsZero() || t.Before(next) {
			next = t
		}
	}
	return next
}

// rpoElapsed reports whether the path's last successful backup is older
// than rpo, or never happened at all.
func rpoElapsed(s PolicyState, now time.Time, rpo time.Duration) bool {
	if s.LastSuccessAt == nil {
		return true
	}
	return now.Sub(*s.LastSuccessAt) > rpo
}

// slug makes path safe to embed in a job-id: strips leading/trailing "/"
// and replaces the rest with "-". Cosmetic only -- job-id is opaque
// metadata to both brfs and bwfs, it never needs to round-trip back to a
// literal path.
func slug(path string) string {
	s := strings.Trim(path, "/")
	s = strings.ReplaceAll(s, "/", "-")
	if s == "" {
		return "root"
	}
	return s
}

// shortID returns id (a UUID) with its dashes stripped, truncated to 8
// hex characters -- git-short-hash-style, just long enough to disambiguate
// in practice without making task/job IDs unreadable. Safe for any input
// length: shorter-than-8 (including empty) is returned unchanged rather
// than panicking on a slice out of range.
func shortID(id string) string {
	stripped := strings.ReplaceAll(id, "-", "")
	if len(stripped) > 8 {
		return stripped[:8]
	}
	return stripped
}

// backupTaskID is the stable identifier for one object filter's
// PolicyState entry in agent-state.json -- stable across ticks, so its
// backoff/success history persists as long as the filter keeps appearing
// in policies-cache.json. filterID's short suffix guarantees uniqueness
// even when two object filters in the same policy share a path (e.g. one
// with include, one with exclude, both scoped to the same root) -- policy
// name and path stay in the string for readability, but the suffix is
// what actually disambiguates.
func backupTaskID(policyName, path, filterID string) string {
	return fmt.Sprintf("backup:%s:%s:%s", policyName, path, shortID(filterID))
}

// backupJobID is the --job-id passed to brfs for one run -- unlike
// backupTaskID, it includes a timestamp so every run gets a distinct ID,
// and it slugs the path so bwfs's job records stay easy to grep.
func backupJobID(policyName, path, filterID string, now time.Time) string {
	return fmt.Sprintf("backup:%s:%s:%s:%d", policyName, slug(path), shortID(filterID), now.Unix())
}

// backupTasks derives one Policy per (cached policy, object_filters path)
// pair from policiesCachePath, valid at the instant it's called. Callers
// that need to notice policies-cache.json changing over time (agent
// serve's reconcile loop) must call this fresh every tick rather than
// caching its result once.
//
// The second return value is ok=false whenever the underlying read
// failed (see readCachedPolicies) -- callers must treat that as "this
// tick's view is untrustworthy," never as "there are zero tasks."
//
// A policy with an unparseable rpo, or with no valid backup_window
// schedule at all, contributes no tasks -- there is no sound due-check
// that could be built for it, so skipping entirely (rather than running
// on a guess) is the fail-safe choice.
//
// A policy whose Destinations is empty (its storage policy has no live
// checkins yet, or storage_policy_id is dangling) contributes no task for
// any of its object filters -- rather than exec'ing brfs with an empty
// --destination, which common.ParseDestination would silently resolve to
// localhost instead of failing loudly. Each skip is logged with the policy
// and would-be job id so the gap is visible without needing to reproduce a
// misdirected backup first. Only Destinations[0] is ever used -- retrying
// the rest of the list on failure is future work.
func backupTasks(policiesCachePath string, logger *slog.Logger, conf *config.Config) ([]Policy, bool) {
	grace := time.Duration(conf.BackupWindowGraceSec) * time.Second

	cachedPolicies, ok := readCachedPolicies(policiesCachePath)
	if !ok {
		return nil, false
	}

	var tasks []Policy
	for _, p := range cachedPolicies {
		// Only type "backup" policies become backup tasks -- a future
		// non-backup type simply contributes zero tasks here, the same
		// fail-safe direction as the unparseable-rpo/no-backup_window
		// skips below: no sound backup task can be built for a policy
		// this loop doesn't understand how to interpret.
		if p.Type != "backup" {
			continue
		}
		if p.disabled(time.Now()) {
			continue
		}
		rpo, err := time.ParseDuration(p.RPO)
		if err != nil {
			continue
		}
		schedules := parseSchedules(p.BackupWindow)
		if len(schedules) == 0 {
			continue
		}

		policyName := p.Name
		var destination string
		if len(p.Destinations) > 0 {
			destination = p.Destinations[0]
		}
		for _, filter := range p.ObjectFilters {
			jobID := backupJobID(policyName, filter.Path, filter.ID, time.Now())
			if destination == "" {
				logger.Error("backup task has no resolved destination, skipping",
					"policy", backupTaskID(policyName, filter.Path, filter.ID),
					"job_id", jobID)
				continue
			}
			args := []string{filter.Path, "--destination", destination, "--job-id", jobID}
			if len(filter.Include) > 0 {
				args = append(args, "--include", strings.Join(filter.Include, ","))
			}
			if len(filter.Exclude) > 0 {
				args = append(args, "--exclude", strings.Join(filter.Exclude, ","))
			}
			tasks = append(tasks, Policy{
				ID:         backupTaskID(policyName, filter.Path, filter.ID),
				Binary:     "brfs",
				JobID:      jobID,
				Args:       args,
				Background: true,
				Due: func(s PolicyState, now time.Time) bool {
					return windowOpen(schedules, now, grace) && rpoElapsed(s, now, rpo)
				},
				NextRun: func(s PolicyState, now time.Time) time.Time {
					return nextWindow(schedules, now)
				},
			})
		}
	}
	return tasks, true
}
