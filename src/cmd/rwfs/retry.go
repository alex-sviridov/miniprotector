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

// withRetry runs attempt over ctx until a result allows no further attempt,
// backing off with capped doubling (retryBackoffInitial, doubling, capped
// at retryBackoffCap) between attempts.
//
// maxAttempts(result) says how many attempts in total a result of that
// kind allows, counting the one just made: a success or a final failure
// returns 1, a network error returns the configured --retries, and a
// client hash mismatch returns 2 (see restoreAttempts / verifyAttempts).
// Asking per result, rather than taking one limit and a yes/no predicate,
// lets each kind of failure have its own cap: a hash mismatch gets its one
// retry even under --retries 1, and never more than one under --retries 5.
// When the kind of failure changes between attempts, the latest result's
// cap applies, so the total is bounded by the largest cap.
//
// Stops immediately -- no further attempts, no backoff wait -- once the
// attempt count reaches the cap, so a final failure surfaces without
// delay. Also stops immediately, with no log line and no wait, the moment
// ctx is already done after a retryable attempt: logging "retrying" and
// then bailing out via ctx.Done() in the select below would otherwise
// claim a retry that never happens -- exactly what happens when a sibling
// worker's failure cancels the whole run out from under an in-flight retry
// loop (restoreFileContent).
//
// logger should already be scoped with per-item fields (path, file_uuid,
// dest_path, etc.) via .With() -- each retried attempt logs "transfer
// failed, retrying" with "attempt" and "reason" (from the reason func) at
// Warn. The message is neutral because a hash mismatch is retried too, not
// only stream errors.
//
// attempt always runs at least once, whatever maxAttempts returns, so this
// helper can never fabricate a zero-value result for a call it never made.
func withRetry[R any](
	ctx context.Context,
	logger *slog.Logger,
	attempt func(context.Context) R,
	maxAttempts func(R) int,
	reason func(R) string,
) R {
	backoff := retryBackoffInitial
	for i := 1; ; i++ {
		result := attempt(ctx)
		if i >= maxAttempts(result) || ctx.Err() != nil {
			return result
		}
		logger.Warn("transfer failed, retrying", "attempt", i, "reason", reason(result))
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return result
		}
		backoff = min(backoff*2, retryBackoffCap)
	}
}

// hashMismatchAttempts is how many attempts a client-side BLAKE3 mismatch
// allows: one retry. bwfs verifies every chunk's hash before sending it, so
// a mismatch at the client can only be a fault in transit or in the
// client's memory; a second try usually gets clean bytes. A second
// mismatch suggests something persistent, so it is final rather than
// burning the full --retries budget.
const hashMismatchAttempts = 2
