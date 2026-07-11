package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// estimatedNextRun calls isDue directly (rather than re-deriving its
// logic) so this display can never disagree with what the daemon would
// actually do. Returns the zero time.Time for "due now".
func estimatedNextRun(p Policy, s PolicyState, now time.Time) time.Time {
	if isDue(p, s, now) {
		return time.Time{}
	}
	if s.ConsecutiveFailures == 0 {
		if p.NextRun != nil {
			return p.NextRun(s, now)
		}
		if s.LastSuccessAt == nil {
			return time.Time{}
		}
		return s.LastSuccessAt.Add(p.Interval)
	}
	return *s.NextRetryAt
}

func health(s PolicyState) string {
	if s.LastSuccessAt == nil && s.LastAttemptAt == nil {
		return "never run"
	}
	if s.ConsecutiveFailures > 0 {
		return fmt.Sprintf("retrying (%d failures)", s.ConsecutiveFailures)
	}
	return "ok"
}

func formatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Format("2006-01-02 15:04:05")
}

// formatNextRun renders a due-now policy (zero value, or already past) as
// "due now" instead of a stale-looking timestamp.
func formatNextRun(t time.Time, now time.Time) string {
	if t.IsZero() || !t.After(now) {
		return "due now"
	}
	return t.Format("2006-01-02 15:04:05")
}

// renderPolicies reads cachePath and writes a table of every embedded
// policy's reconciliation state to w. It never executes a policy — purely
// a read-only view of what `agent serve` last recorded.
func renderPolicies(w io.Writer, cachePath string, now time.Time, policies []Policy) error {
	cache, err := readCache(cachePath)
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "POLICY\tSTATE\tLAST SUCCESS\tLAST ATTEMPT\tFAILURES\tNEXT RUN")
	for _, p := range policies {
		s := cache[p.ID]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n",
			p.ID,
			health(s),
			formatTime(s.LastSuccessAt),
			formatTime(s.LastAttemptAt),
			s.ConsecutiveFailures,
			formatNextRun(estimatedNextRun(p, s, now), now),
		)
	}
	return tw.Flush()
}
