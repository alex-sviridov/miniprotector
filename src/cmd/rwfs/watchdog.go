package main

import (
	"context"
	"time"
)

// streamIdleTimeout is the idle window withStallWatchdog uses for
// ResolveRestoreFiles, RestoreFile, and ListFiles streaming calls: a
// stream that's actively producing messages never hits this, no matter
// how long the overall call runs; only an actual stall (no message
// received for this long) does. A var rather than a const purely so
// tests can shrink it instead of waiting out the real 60s -- not a
// user-facing setting; there is no flag for it. See
// docs/superpowers/specs/2026-08-16-rwfs-reliability-performance-design.md.
var streamIdleTimeout = 60 * time.Second

// withStallWatchdog returns a context derived from parent that is
// cancelled if touch isn't called within idle of the last call (or of
// ctx's creation, for the first call) -- an idle timeout, not a
// total-duration timeout, so a stream that's genuinely still producing
// output is never penalized for running long. stop releases the
// watchdog's goroutine and must be called exactly once when the caller is
// done with ctx, whether the underlying call succeeded, failed, or was
// itself cancelled by touch never being called again; it also cancels
// ctx, so a caller doesn't need a separate cancel of its own.
func withStallWatchdog(parent context.Context, idle time.Duration) (ctx context.Context, touch func(), stop func()) {
	ctx, cancel := context.WithCancel(parent)
	timer := time.NewTimer(idle)
	touchCh := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		defer timer.Stop()
		for {
			select {
			case <-timer.C:
				cancel()
				return
			case <-touchCh:
				if !timer.Stop() {
					<-timer.C
				}
				timer.Reset(idle)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	touch = func() {
		select {
		case touchCh <- struct{}{}:
		default: // a touch is already pending; the pending one already resets the timer
		}
	}
	stop = func() {
		close(done)
		cancel()
	}
	return ctx, touch, stop
}
