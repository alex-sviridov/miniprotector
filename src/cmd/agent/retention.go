package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/retention"
)

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// safeFileName maps a task ID (which contains ':' and '/') to a file name.
func safeFileName(taskID string) string {
	return unsafeFileChars.ReplaceAllString(taskID, "_")
}

// retentionRulesFrom returns the retention rules in effect for a filesystem
// backup, in evaluation order: cached "retention" policies targeting the
// "filesystem" backup type that aren't disabled, sorted by ascending
// priority (ties by policy name, so the order is deterministic). policy-server
// has already matched each policy's client_filters against this node; what
// remains here is purely which rows apply to a job, which retention.Build
// then narrows by the job's root path. Returns nil when there are none --
// the matrix is then just the built-in default row.
func retentionRulesFrom(cachedPolicies []cachedPolicy) []retention.Rule {
	type ranked struct {
		name     string
		priority int32
		rule     retention.Rule
	}
	now := time.Now()
	var found []ranked
	for _, p := range cachedPolicies {
		if p.Type != "retention" || p.Retention == nil || p.Retention.BackupType != "filesystem" || p.disabled(now) {
			continue
		}
		found = append(found, ranked{
			name:     p.Name,
			priority: p.Retention.Priority,
			rule: retention.Rule{
				Prefix:      p.Retention.Path,
				Include:     p.Retention.Include,
				KeepSeconds: p.Retention.KeepSeconds,
			},
		})
	}
	if len(found) == 0 {
		return nil
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].priority != found[j].priority {
			return found[i].priority < found[j].priority
		}
		return found[i].name < found[j].name
	})
	rules := make([]retention.Rule, len(found))
	for i, f := range found {
		rules[i] = f.rule
	}
	return rules
}

// prepareRetention resolves the retention matrix for one backup job, logs it
// under the job's id, writes it to retentionDir (one file per task,
// overwritten each run) and returns the brfs args that point at it. The
// logged matrix is exactly what brfs applies: agent is the only place that
// resolves rules.
func prepareRetention(logger *slog.Logger, conf *config.Config, retentionDir, taskID, jobID, root string, rules []retention.Rule) ([]string, error) {
	m := retention.Build(rules, root, int64(conf.RetentionDefaultDays)*86400)
	path := filepath.Join(retentionDir, safeFileName(taskID)+".json")
	if err := retention.WriteFile(path, m); err != nil {
		return nil, fmt.Errorf("write retention matrix: %w", err)
	}
	logger.Info("retention matrix resolved", "job_id", jobID, "task", taskID, "rows", m, "event", "retention_matrix")
	return []string{"--retention-file", path}, nil
}
