// checkin.go runs policy-server's background check-in cleanup: on a fixed
// tick, delete every CheckinRecord older than the configured retention
// window. See docs/superpowers/specs/2026-08-03-policy-checkin-tracking-design.md.
package main

import (
	"context"
	"log/slog"
	"time"

	checkinstore "github.com/alex-sviridov/miniprotector/storage/policyserver"
)

// checkinCleanupInterval is how often the cleanup tick fires -- fixed, not
// configurable. Only the retention window (how old a record must be to be
// deleted) is a config value.
const checkinCleanupInterval = time.Minute

// runCheckinCleanup deletes check-in records older than retention every
// interval, until ctx is cancelled. Mirrors watchForReload's ticker-driven
// background-loop shape.
func runCheckinCleanup(ctx context.Context, store *checkinstore.Store, interval, retention time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := store.DeleteOlderThan(time.Now().Add(-retention))
			if err != nil {
				logger.Error("checkin cleanup failed", "error", err)
				continue
			}
			if deleted > 0 {
				logger.Info("checkin cleanup removed stale check-ins", "count", deleted)
			}
		}
	}
}
