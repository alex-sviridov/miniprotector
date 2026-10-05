package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"

	"github.com/alex-sviridov/miniprotector/common/config"
	"github.com/alex-sviridov/miniprotector/retention"
)

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// safeFileName maps a task ID (which contains ':' and '/') to a file name.
func safeFileName(taskID string) string {
	return unsafeFileChars.ReplaceAllString(taskID, "_")
}

// retentionRulesFrom returns the retention rules in effect, in priority
// order. Retention policies don't exist until Part 1 of the retention work
// (see docs/superpowers/specs/2026-10-05-retention-expiry-stamping-design.md),
// so today only the built-in default applies; this is the seam it plugs into.
func retentionRulesFrom(_ []cachedPolicy) []retention.Rule { return nil }

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
