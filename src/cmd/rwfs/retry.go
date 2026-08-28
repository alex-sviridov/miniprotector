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

const retryBackoffCap = 5 * time.Second

// retryBackoffInitial is the starting backoff between retry attempts,
// doubling up to retryBackoffCap. A var rather than a const purely so
// tests can shrink it instead of waiting out real backoff delays -- not a
// user-facing setting; there is no flag for it. Mirrors watchdog.go's
// streamIdleTimeout, which exists for the same reason.
var retryBackoffInitial = 500 * time.Millisecond

// withRetry runs attempt up to maxRetries times over ctx, retrying only
// while isRetryable(result) is true, backing off with capped doubling
// (retryBackoffInitial, doubling, capped at retryBackoffCap) between
// attempts. Stops immediately -- no further attempts, no backoff wait --
// the moment isRetryable returns false, so a terminal (non-retryable)
// failure surfaces without delay. Also stops immediately, with no log
// line and no wait, the moment ctx is already done after a retryable
// attempt: logging "retrying" and then bailing out via ctx.Done() in the
// select below would otherwise claim a retry that never happens --
// exactly what happens when a sibling worker's failure cancels the whole
// run out from under an in-flight retry loop (restoreFileContent).
//
// logger should already be scoped with per-item fields (path, file_uuid,
// dest_path, etc.) via .With() -- each retried attempt logs "stream
// error, retrying" with "attempt" and "reason" (from the reason func) at
// Warn, the same log line verifyFileWithRetry has always emitted.
//
// maxRetries is expected to be >= 1 (enforced by --retries validation in
// arguments.go); 1 means no retry -- attempt runs once, whatever it
// returns is returned immediately regardless of isRetryable. A value
// less than 1 is clamped to 1 rather than skipping attempt entirely, so
// this helper can never silently fabricate a zero-value result for a
// call it never made.
func withRetry[R any](
	ctx context.Context,
	logger *slog.Logger,
	maxRetries int,
	attempt func(context.Context) R,
	isRetryable func(R) bool,
	reason func(R) string,
) R {
	if maxRetries < 1 {
		maxRetries = 1
	}
	backoff := retryBackoffInitial
	var result R
	for i := 1; i <= maxRetries; i++ {
		result = attempt(ctx)
		if !isRetryable(result) {
			return result
		}
		if i < maxRetries {
			if ctx.Err() != nil {
				return result
			}
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
