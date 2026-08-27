// retry.go provides a single generic retry-with-backoff loop, shared by
// verify's per-file verification (verifyFileWithRetry, verify.go) and
// restore's per-file content write (writeRestoreFileWithRetry,
// restorefile.go) -- one implementation so the two commands' retry
// behavior can't drift apart. See
// docs/superpowers/specs/2026-08-27-restore-retry-design.md.
package main

import (
	"context"
	"log/slog"
	"time"
)

const (
	retryBackoffInitial = 500 * time.Millisecond
	retryBackoffCap     = 5 * time.Second
)

// withRetry runs attempt up to maxRetries times over ctx, retrying only
// while isRetryable(result) is true, backing off with capped doubling
// (retryBackoffInitial, doubling, capped at retryBackoffCap) between
// attempts. Stops immediately -- no further attempts, no backoff wait --
// the moment isRetryable returns false, so a terminal (non-retryable)
// failure surfaces without delay.
//
// logger should already be scoped with per-item fields (path, file_uuid,
// dest_path, etc.) via .With() -- each retried attempt logs "stream
// error, retrying" with "attempt" and "reason" (from the reason func) at
// Warn, the same log line verifyFileWithRetry has always emitted.
//
// maxRetries must be >= 1 (enforced by --retries validation in
// arguments.go); 1 means no retry -- attempt runs once, whatever it
// returns is returned immediately regardless of isRetryable.
func withRetry[R any](
	ctx context.Context,
	logger *slog.Logger,
	maxRetries int,
	attempt func(context.Context) R,
	isRetryable func(R) bool,
	reason func(R) string,
) R {
	backoff := retryBackoffInitial
	var result R
	for i := 1; i <= maxRetries; i++ {
		result = attempt(ctx)
		if !isRetryable(result) {
			return result
		}
		if i < maxRetries {
			logger.Warn("stream error, retrying", "attempt", i, "reason", reason(result))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return result
			}
			backoff = min(backoff*2, retryBackoffCap)
		}
	}
	return result
}
